package wsbridge

import (
	"bytes"
	"context"
	"encoding/binary"
	"sync"

	"github.com/coder/websocket"
)

// Frame type tags for the binary protocol spoken between wsbridge and
// frontend/src/viewer.js. Keep FRAME_* in that file in sync with these.
const (
	frameInit     = 0x01 // -> uint16 width, uint16 height
	frameUpdate   = 0x02 // -> uint16 x,y,w,h + w*h*4 bytes RGBA
	frameCopyRect = 0x03 // -> uint16 dstX,dstY,w,h,srcX,srcY
	frameResize   = 0x04 // -> uint16 width, height
	frameCursor   = 0x05 // -> uint16 hotX,hotY,w,h + w*h*4 bytes RGBA
	frameCutText  = 0x06 // -> uint32 length + utf8 bytes

	frameInPointer   = 0x10 // <- uint16 x,y + uint8 buttonMask
	frameInKey       = 0x11 // <- uint32 keysym + uint8 down
	frameInClipboard = 0x12 // <- uint32 length + utf8 bytes
)

// Session bridges one live RFB connection to one browser-side canvas. It
// implements rfb.FramebufferSink (Init/Update/CopyRect/Resize/Cursor/
// CutText below) so it can be handed directly to rfb.Client.Run.
type Session struct {
	id string

	mu    sync.Mutex // guards input and conn
	input InputSink
	conn  *websocket.Conn

	attachedCh chan struct{}
	attachOnce sync.Once
	closedCh   chan struct{}
	closeOnce  sync.Once

	// Shadow copy of what the browser canvas currently shows, so Update can
	// forward only pixels that actually changed. Some servers (macOS Screen
	// Sharing above all) answer the smallest change — a typed character, a
	// blinking cursor — with the entire screen, and pushing 20MB of RGBA for
	// a 2880x1800 frame through the WebSocket into the webview costs ~300ms
	// by itself (the loopback WebSocket into Chromium tops out around
	// 70MB/s), so diffing here turns those frames into a handful of tiles.
	// Only touched from the goroutine driving the sink (Init/Update/
	// CopyRect/Resize), like the rest of the FramebufferSink methods.
	fbW, fbH int
	fb       []byte
}

// SetInput repoints outgoing pointer/keyboard/clipboard input at a new
// live connection — used after auto-reconnect redials the server and gets
// a new *rfb.Client/*rdp.Client, so input captured by the still-attached
// browser tab reaches the new connection instead of the dead one.
func (sess *Session) SetInput(input InputSink) {
	sess.mu.Lock()
	sess.input = input
	sess.mu.Unlock()
}

func (sess *Session) attach(ctx context.Context, conn *websocket.Conn) {
	sess.mu.Lock()
	sess.conn = conn
	sess.mu.Unlock()
	sess.attachOnce.Do(func() { close(sess.attachedCh) })

	defer conn.CloseNow()
	for {
		msgType, data, err := conn.Read(ctx)
		if err != nil {
			return
		}
		if msgType != websocket.MessageBinary || len(data) == 0 {
			continue
		}
		sess.handleInput(data)
	}
}

func (sess *Session) handleInput(data []byte) {
	sess.mu.Lock()
	input := sess.input
	sess.mu.Unlock()

	switch data[0] {
	case frameInPointer:
		if len(data) < 6 {
			return
		}
		x := int(binary.BigEndian.Uint16(data[1:3]))
		y := int(binary.BigEndian.Uint16(data[3:5]))
		mask := data[5]
		_ = input.SendPointerEvent(x, y, mask)
	case frameInKey:
		if len(data) < 6 {
			return
		}
		keysym := binary.BigEndian.Uint32(data[1:5])
		down := data[5] != 0
		_ = input.SendKeyEvent(keysym, down)
	case frameInClipboard:
		if len(data) < 5 {
			return
		}
		n := binary.BigEndian.Uint32(data[1:5])
		if uint32(len(data)) < 5+n {
			return
		}
		_ = input.SendClientCutText(string(data[5 : 5+n]))
	}
}

func (sess *Session) close() {
	sess.closeOnce.Do(func() { close(sess.closedCh) })
	sess.mu.Lock()
	conn := sess.conn
	sess.mu.Unlock()
	if conn != nil {
		conn.Close(websocket.StatusNormalClosure, "session closed")
	}
}

// write blocks until the frontend has attached (or the session closes),
// then sends one binary frame. Framebuffer updates are deltas, not full
// refreshes, so they must never be silently dropped — this deliberately
// applies backpressure to the RFB read loop rather than skipping frames.
func (sess *Session) write(data []byte) {
	select {
	case <-sess.attachedCh:
	case <-sess.closedCh:
		return
	}

	sess.mu.Lock()
	conn := sess.conn
	sess.mu.Unlock()
	if conn == nil {
		return
	}
	_ = conn.Write(context.Background(), websocket.MessageBinary, data)
}

// --- rfb.FramebufferSink ---

func (sess *Session) Init(width, height int, name string) {
	sess.resetShadow(width, height)
	buf := make([]byte, 5)
	buf[0] = frameInit
	binary.BigEndian.PutUint16(buf[1:3], uint16(width))
	binary.BigEndian.PutUint16(buf[3:5], uint16(height))
	sess.write(buf)
}

func (sess *Session) Update(x, y, w, h int, rgba []byte) {
	nx, ny, nw, nh, ok := sess.diffAndStore(x, y, w, h, rgba)
	if !ok {
		return // identical to what the canvas already shows
	}
	if nx != x || ny != y || nw != w || nh != h {
		// Send only the changed sub-rectangle, repacked to a tight stride.
		sub := make([]byte, nw*nh*4)
		for row := 0; row < nh; row++ {
			src := ((ny-y+row)*w + (nx - x)) * 4
			copy(sub[row*nw*4:], rgba[src:src+nw*4])
		}
		x, y, w, h, rgba = nx, ny, nw, nh, sub
	}
	buf := make([]byte, 9+len(rgba))
	buf[0] = frameUpdate
	binary.BigEndian.PutUint16(buf[1:3], uint16(x))
	binary.BigEndian.PutUint16(buf[3:5], uint16(y))
	binary.BigEndian.PutUint16(buf[5:7], uint16(w))
	binary.BigEndian.PutUint16(buf[7:9], uint16(h))
	copy(buf[9:], rgba)
	sess.write(buf)
}

func (sess *Session) resetShadow(w, h int) {
	sess.fbW, sess.fbH = w, h
	// Zeroed, i.e. alpha 0: no real pixel (alpha is always 0xFF) can compare
	// equal, so the first update of every region is always sent.
	sess.fb = make([]byte, w*h*4)
}

// diffAndStore compares the incoming rectangle against the shadow copy,
// records it there, and returns the smallest sub-rectangle that actually
// differs. ok is false when nothing differs. Rectangles that don't fit the
// tracked framebuffer are passed through unchanged (and untracked).
func (sess *Session) diffAndStore(x, y, w, h int, rgba []byte) (nx, ny, nw, nh int, ok bool) {
	if sess.fb == nil || x < 0 || y < 0 || w <= 0 || h <= 0 ||
		x+w > sess.fbW || y+h > sess.fbH || len(rgba) < w*h*4 {
		return x, y, w, h, true
	}
	stride := w * 4
	minX, maxX, minY, maxY := w, -1, h, -1
	for row := 0; row < h; row++ {
		src := rgba[row*stride : (row+1)*stride]
		dst := sess.fb[((y+row)*sess.fbW+x)*4:][:stride]
		if bytes.Equal(src, dst) {
			continue
		}
		first := 0
		for binary.LittleEndian.Uint32(src[first*4:]) == binary.LittleEndian.Uint32(dst[first*4:]) {
			first++
		}
		last := w - 1
		for binary.LittleEndian.Uint32(src[last*4:]) == binary.LittleEndian.Uint32(dst[last*4:]) {
			last--
		}
		minX, maxX = min(minX, first), max(maxX, last)
		minY, maxY = min(minY, row), max(maxY, row)
	}
	if maxX < 0 {
		return 0, 0, 0, 0, false
	}
	nw, nh = maxX-minX+1, maxY-minY+1
	for row := minY; row <= maxY; row++ {
		copy(sess.fb[((y+row)*sess.fbW+x+minX)*4:], rgba[(row*w+minX)*4:(row*w+minX+nw)*4])
	}
	return x + minX, y + minY, nw, nh, true
}

func (sess *Session) CopyRect(dstX, dstY, w, h, srcX, srcY int) {
	sess.copyShadow(dstX, dstY, w, h, srcX, srcY)
	buf := make([]byte, 13)
	buf[0] = frameCopyRect
	binary.BigEndian.PutUint16(buf[1:3], uint16(dstX))
	binary.BigEndian.PutUint16(buf[3:5], uint16(dstY))
	binary.BigEndian.PutUint16(buf[5:7], uint16(w))
	binary.BigEndian.PutUint16(buf[7:9], uint16(h))
	binary.BigEndian.PutUint16(buf[9:11], uint16(srcX))
	binary.BigEndian.PutUint16(buf[11:13], uint16(srcY))
	sess.write(buf)
}

// copyShadow mirrors a CopyRect into the shadow framebuffer (rows copied in
// whichever direction avoids overwriting source rows before they're read).
func (sess *Session) copyShadow(dstX, dstY, w, h, srcX, srcY int) {
	if sess.fb == nil || w <= 0 || h <= 0 ||
		dstX < 0 || dstY < 0 || srcX < 0 || srcY < 0 ||
		dstX+w > sess.fbW || srcX+w > sess.fbW || dstY+h > sess.fbH || srcY+h > sess.fbH {
		return
	}
	row := func(r int) {
		copy(sess.fb[((dstY+r)*sess.fbW+dstX)*4:][:w*4], sess.fb[((srcY+r)*sess.fbW+srcX)*4:][:w*4])
	}
	if dstY > srcY {
		for r := h - 1; r >= 0; r-- {
			row(r)
		}
	} else {
		for r := 0; r < h; r++ {
			row(r)
		}
	}
}

func (sess *Session) Resize(width, height int) {
	sess.resetShadow(width, height)
	buf := make([]byte, 5)
	buf[0] = frameResize
	binary.BigEndian.PutUint16(buf[1:3], uint16(width))
	binary.BigEndian.PutUint16(buf[3:5], uint16(height))
	sess.write(buf)
}

func (sess *Session) Cursor(hotX, hotY, w, h int, rgba []byte) {
	buf := make([]byte, 9+len(rgba))
	buf[0] = frameCursor
	binary.BigEndian.PutUint16(buf[1:3], uint16(hotX))
	binary.BigEndian.PutUint16(buf[3:5], uint16(hotY))
	binary.BigEndian.PutUint16(buf[5:7], uint16(w))
	binary.BigEndian.PutUint16(buf[7:9], uint16(h))
	copy(buf[9:], rgba)
	sess.write(buf)
}

func (sess *Session) CutText(text string) {
	body := []byte(text)
	buf := make([]byte, 5+len(body))
	buf[0] = frameCutText
	binary.BigEndian.PutUint32(buf[1:5], uint32(len(body)))
	copy(buf[5:], body)
	sess.write(buf)
}
