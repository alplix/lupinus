package rfb

import (
	"bufio"
	"compress/zlib"
	"context"
	"fmt"
	"io"
	"net"
	"os"
	"sync"
	"sync/atomic"
	"time"
)

// FramebufferSink receives decoded framebuffer events from a Client. All
// methods are called from the single goroutine running Client.Run, in
// order, so implementations don't need their own locking for ordering
// (though they will typically hand the data off to another goroutine, e.g.
// a WebSocket writer, and should copy any byte slices they intend to keep
// past the call since Client reuses buffers).
type FramebufferSink interface {
	// Init is called once, right after the handshake, with the initial
	// desktop size and name.
	Init(width, height int, name string)
	// Update delivers a decoded rectangle of RGBA pixels (4 bytes/pixel,
	// alpha always 0xFF), row-major, tightly packed (stride == w*4).
	Update(x, y, w, h int, rgba []byte)
	// CopyRect asks the sink to copy an on-screen region to another
	// position, without new pixel data being sent over the wire.
	CopyRect(dstX, dstY, w, h, srcX, srcY int)
	// Resize is called when the server changes the desktop size
	// (DesktopSize/ExtendedDesktopSize pseudo-encodings).
	Resize(width, height int)
	// Cursor delivers a new cursor image (RGBA, premultiplied alpha) and
	// its hotspot, for local cursor rendering.
	Cursor(hotspotX, hotspotY, w, h int, rgba []byte)
	// CutText delivers clipboard text pushed by the server.
	CutText(text string)
}

// farLinkRTT is the TCP connect time above which a server is assumed to be
// across a slow-ish link (see Dial).
const farLinkRTT = 80 * time.Millisecond

// DialOptions configures a Client connection.
type DialOptions struct {
	// Password for VNC Authentication or Apple Remote Desktop (secARD).
	// Leave empty if the server offers (and Lupinus should accept) the
	// None security type.
	Password string
	// Username for Apple Remote Desktop authentication (see ard.go) —
	// macOS's Screen Sharing server requires this whenever it's set to
	// log in as a real user account rather than accept a shared VNC
	// password. Ignored for every other security type: plain VNC
	// Authentication has no concept of a username.
	Username string
	// Quality is one of "balanced" (default), "quality" or "bandwidth" —
	// see qualityPresetLevels in types.go for exactly what each one asks
	// the server for. An empty/unrecognized value behaves like "balanced".
	Quality string
	// DialTimeout bounds the initial TCP connect. Zero means no timeout.
	DialTimeout time.Duration
}

// Client is a single RFB protocol session.
type Client struct {
	conn net.Conn
	r    *bufio.Reader
	w    io.Writer

	width, height int
	name          string
	quality       string // see DialOptions.Quality

	zr       io.ReadCloser
	zrFeeder *chunkFeeder

	// Tight encoding (tight.go) uses up to 4 independent long-lived zlib
	// streams, selected per-rectangle by the server and individually
	// reset on the server's instruction — same continuous-stream
	// requirement as ZRLE's single stream above, just four of them.
	tightZlib [4]tightZStream

	writeMu sync.Mutex

	// lastRequestSent, only maintained when debugFBWire (encodings.go) is
	// on, lets the debug log show real request-to-response round-trip
	// time — the number that actually tells "the network/server is slow"
	// apart from "this client is slow to decode".
	lastRequestSent time.Time

	// bytesIn counts every byte read off the wire (per-update size for the
	// adaptive logic in encodings.go, and statsLoop's throughput line);
	// nUpdates and nPointer only feed statsLoop.
	bytesIn, readWait, nUpdates, nPointer atomic.Int64

	// Pixel format state (see pixfmt.go / noteUpdate). level is the format
	// the decoders currently expect, sentLevel the last one requested from
	// the server, wantLevel the one adaptation would like next.
	level, sentLevel, wantLevel int
	// lastFast: the previous update was small/quick, so it's worth
	// pipelining the next request. cooldown counts updates to skip before
	// judging a pixel-format change again.
	lastFast bool
	cooldown int
	// Smoothed link throughput (bytes/s) and observed full-screen frame size
	// (bytes) at each pixel level, from which noteUpdate picks the level;
	// plus the bookkeeping that stops it flapping between two levels.
	tput       float64
	levelFrame [len(pixelLevels)]float64
	upBlocked  [len(pixelLevels)]time.Time
	upBackoff  [len(pixelLevels)]time.Duration
	lastUpAt   time.Time
	lastUpFrom int

	// Reused per-tile ZRLE working buffers (see decodeZRLE).
	zrleTile, zrleScratch []byte
}

// countingConn tallies every byte read off the wire and the time spent
// blocked waiting for it — the part of an update's duration that is the link
// and the server, as opposed to local decoding.
type countingConn struct {
	net.Conn
	n    *atomic.Int64
	wait *atomic.Int64 // nanoseconds spent inside Read
}

func (c countingConn) Read(p []byte) (int, error) {
	t := time.Now()
	n, err := c.Conn.Read(p)
	c.wait.Add(int64(time.Since(t)))
	c.n.Add(int64(n))
	return n, err
}

// statsLoop prints one line per second (debugFBWire only): wire bytes
// received, framebuffer updates handled and pointer events sent. Tells
// "the link or server can't deliver pixels fast enough" (low KB/s, few
// updates) apart from "this client is flooding the server with input"
// (huge pointer/s) without needing a packet capture.
func (c *Client) statsLoop(done <-chan struct{}) {
	t := time.NewTicker(time.Second)
	defer t.Stop()
	var lastB, lastU, lastP int64
	for {
		select {
		case <-done:
			return
		case <-t.C:
			b, u, p := c.bytesIn.Load(), c.nUpdates.Load(), c.nPointer.Load()
			fmt.Fprintf(os.Stderr, "DEBUG rfb stats: %d KB/s in, %d updates/s, %d pointer events/s\n", (b-lastB)/1024, u-lastU, p-lastP)
			lastB, lastU, lastP = b, u, p
		}
	}
}

// Dial connects to addr (host:port) and performs the full RFB handshake:
// version negotiation, security (None or VNC Authentication), ClientInit/
// ServerInit, and the initial SetPixelFormat/SetEncodings. The returned
// Client is ready for Run.
func Dial(ctx context.Context, addr string, opts DialOptions) (*Client, error) {
	dialer := net.Dialer{Timeout: opts.DialTimeout}
	dialStart := time.Now()
	conn, err := dialer.DialContext(ctx, "tcp", addr)
	if err != nil {
		return nil, fmt.Errorf("rfb: dial %s: %w", addr, err)
	}
	// TCP connect time is one round trip. A far-away server is very likely
	// also a bandwidth-limited one, so start at 16-bit colour rather than
	// pushing a full-fidelity first frame down the link; the tuner (see
	// noteUpdate) moves back up after that frame if the link turns out fast.
	startLevel := debugStartLevel
	if startLevel == 0 && time.Since(dialStart) >= farLinkRTT && frameTarget(opts.Quality) != 0 {
		startLevel = 1
	}

	c := &Client{
		conn:      conn,
		w:         conn,
		zrFeeder:  &chunkFeeder{},
		quality:   opts.Quality,
		level:     startLevel,
		sentLevel: startLevel,
		wantLevel: startLevel,
		lastFast:  false, // the first update is the full screen: never pipeline it
	}
	c.r = bufio.NewReaderSize(countingConn{Conn: conn, n: &c.bytesIn, wait: &c.readWait}, 64*1024)

	if err := c.handshake(opts.Username, opts.Password); err != nil {
		conn.Close()
		return nil, err
	}

	return c, nil
}

// Close terminates the underlying TCP connection.
func (c *Client) Close() error {
	return c.conn.Close()
}

// Run reads and dispatches server messages until the connection closes or
// ctx is cancelled. It blocks; call it from its own goroutine.
func (c *Client) Run(ctx context.Context, rawSink FramebufferSink) error {
	sink := &timedSink{FramebufferSink: rawSink}
	sink.Init(c.width, c.height, c.name)

	done := make(chan struct{})
	defer close(done)
	go func() {
		select {
		case <-ctx.Done():
			c.conn.Close()
		case <-done:
		}
	}()
	if debugFBWire {
		go c.statsLoop(done)
	}

	// Kick things off with a non-incremental request for the whole
	// screen, then keep asking for incremental updates as each one is
	// fully processed.
	if err := c.requestUpdate(false, 0, 0, c.width, c.height); err != nil {
		return err
	}

	for {
		msgType, err := c.r.ReadByte()
		if err != nil {
			return err
		}

		switch msgType {
		case msgFramebufferUpdate:
			if err := c.handleFramebufferUpdate(sink); err != nil {
				return err
			}
		case msgSetColourMapEntries:
			if err := c.skipSetColourMapEntries(); err != nil {
				return err
			}
		case msgBell:
			// No-op: Lupinus doesn't surface the server bell yet.
		case msgServerCutText:
			text, err := c.readServerCutText()
			if err != nil {
				return err
			}
			sink.CutText(text)
		default:
			return protoErrf("unknown server message type %d", msgType)
		}
	}
}

// chunkFeeder is an io.Reader that serves bytes from whatever slice was
// last handed to feed, reporting io.EOF once that slice is exhausted. It
// backs the single long-lived zlib.Reader used for ZRLE decoding across the
// whole connection lifetime (RFC 6143 §7.7.4 requires one continuous zlib
// stream). Callers must only ask it to decode exactly as many bytes as the
// protocol guarantees are present in the fed chunk — see decodeZRLE.
type chunkFeeder struct {
	buf []byte
}

func (f *chunkFeeder) feed(b []byte) { f.buf = b }

func (f *chunkFeeder) Read(p []byte) (int, error) {
	if len(f.buf) == 0 {
		return 0, io.EOF
	}
	n := copy(p, f.buf)
	f.buf = f.buf[n:]
	return n, nil
}

// zlibReader returns the connection's single long-lived ZRLE zlib decoder,
// creating it on first use. Callers must feed at least one chunk into
// c.zrFeeder before the first call, since zlib.NewReader reads the 2-byte
// stream header immediately.
func (c *Client) zlibReader() (io.Reader, error) {
	if c.zr == nil {
		zr, err := zlib.NewReader(c.zrFeeder)
		if err != nil {
			return nil, err
		}
		c.zr = zr
	}
	return c.zr, nil
}
