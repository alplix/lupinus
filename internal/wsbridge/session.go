package wsbridge

import (
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
	id    string
	input InputSink

	mu   sync.Mutex
	conn *websocket.Conn

	attachedCh chan struct{}
	attachOnce sync.Once
	closedCh   chan struct{}
	closeOnce  sync.Once
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
	switch data[0] {
	case frameInPointer:
		if len(data) < 6 {
			return
		}
		x := int(binary.BigEndian.Uint16(data[1:3]))
		y := int(binary.BigEndian.Uint16(data[3:5]))
		mask := data[5]
		_ = sess.input.SendPointerEvent(x, y, mask)
	case frameInKey:
		if len(data) < 6 {
			return
		}
		keysym := binary.BigEndian.Uint32(data[1:5])
		down := data[5] != 0
		_ = sess.input.SendKeyEvent(keysym, down)
	case frameInClipboard:
		if len(data) < 5 {
			return
		}
		n := binary.BigEndian.Uint32(data[1:5])
		if uint32(len(data)) < 5+n {
			return
		}
		_ = sess.input.SendClientCutText(string(data[5 : 5+n]))
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
	buf := make([]byte, 5)
	buf[0] = frameInit
	binary.BigEndian.PutUint16(buf[1:3], uint16(width))
	binary.BigEndian.PutUint16(buf[3:5], uint16(height))
	sess.write(buf)
}

func (sess *Session) Update(x, y, w, h int, rgba []byte) {
	buf := make([]byte, 9+len(rgba))
	buf[0] = frameUpdate
	binary.BigEndian.PutUint16(buf[1:3], uint16(x))
	binary.BigEndian.PutUint16(buf[3:5], uint16(y))
	binary.BigEndian.PutUint16(buf[5:7], uint16(w))
	binary.BigEndian.PutUint16(buf[7:9], uint16(h))
	copy(buf[9:], rgba)
	sess.write(buf)
}

func (sess *Session) CopyRect(dstX, dstY, w, h, srcX, srcY int) {
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

func (sess *Session) Resize(width, height int) {
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
