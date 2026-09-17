package rdp

import (
	"context"
	"crypto/tls"
	"fmt"
	"net"
	"sync"
	"time"
)

// FramebufferSink receives decoded framebuffer events from a Client, from
// the single goroutine running Client.Run. Deliberately the same method
// shapes as rfb.FramebufferSink (minus CopyRect, unused in RDP v0.2.0) so
// that wsbridge.Session — which already implements all of them — works
// with either protocol client with zero changes; see the project plan.
type FramebufferSink interface {
	Init(width, height int, name string)
	Update(x, y, w, h int, rgba []byte)
	Resize(width, height int)
	Cursor(hotspotX, hotspotY, w, h int, rgba []byte)
	CutText(text string)
}

// Client is a single RDP session: TPKT/X.224 transport, TLS security,
// MCS/GCC session setup, then the live Fast-Path PDU loop.
type Client struct {
	conn net.Conn // raw TCP conn, pre-TLS
	tc   net.Conn // active conn: tc == conn until TLS upgrade, then the *tls.Conn

	username, password, domain string

	// Negotiated during MCS/GCC setup (gcc.go/mcs.go).
	serverWidth, serverHeight int
	mcsUserID                 uint16
	ioChannelID               uint16
	cliprdrChannelID          uint16 // 0 if the server refused clipboard redirection
	shareID                   uint32

	bitsPerPixel int // negotiated with the server's Bitmap capability, see capabilities.go

	// Clipboard state (cliprdr.go). Guarded by clipMu since SendClientCutText
	// is called from wsbridge's input goroutine while the Run() goroutine
	// concurrently handles incoming CB_FORMAT_DATA_REQUEST for the same text.
	clipMu   sync.Mutex
	clipText string

	// Virtual channel chunk reassembly (MS-RDPBCGR 2.2.6.1). Only one
	// non-I/O channel exists (cliprdr), so a single buffer suffices.
	vcBuf []byte
}

// Dial connects to addr (host:port) and runs the full pre-session setup:
// TPKT/X.224 + TLS negotiation, MCS/GCC, Client Info, licensing, and the
// capability/finalization exchange. The returned Client is ready for Run.
func Dial(ctx context.Context, addr string, opts DialOptions) (*Client, error) {
	dialer := net.Dialer{}
	if opts.DialTimeoutMS > 0 {
		dialer.Timeout = time.Duration(opts.DialTimeoutMS) * time.Millisecond
	}
	conn, err := dialer.DialContext(ctx, "tcp", addr)
	if err != nil {
		return nil, fmt.Errorf("rdp: dial %s: %w", addr, err)
	}

	c := &Client{
		conn:     conn,
		tc:       conn,
		username: opts.Username,
		password: opts.Password,
		domain:   opts.Domain,
	}

	// Bound the whole handshake (not just the initial TCP connect) so a
	// server that accepts the connection but then never replies — e.g.
	// because this client sent something malformed it silently ignored —
	// fails fast instead of hanging forever. Cleared once the session is
	// live, since Run's Fast-Path loop needs to block indefinitely.
	handshakeDeadline := 15 * time.Second
	if opts.DialTimeoutMS > 0 {
		handshakeDeadline = time.Duration(opts.DialTimeoutMS) * time.Millisecond
	}
	conn.SetDeadline(time.Now().Add(handshakeDeadline))
	defer conn.SetDeadline(time.Time{})

	if err := c.negotiateSecurity(); err != nil {
		conn.Close()
		return nil, err
	}
	if err := c.mcsConnect(); err != nil {
		conn.Close()
		return nil, err
	}
	if err := c.sendClientInfo(); err != nil {
		conn.Close()
		return nil, err
	}
	if err := c.handleLicensing(); err != nil {
		conn.Close()
		return nil, err
	}
	if err := c.capabilityExchange(); err != nil {
		conn.Close()
		return nil, err
	}
	if err := c.finalize(); err != nil {
		conn.Close()
		return nil, err
	}

	return c, nil
}

func (c *Client) Close() error {
	return c.conn.Close()
}

// Run reads and dispatches Fast-Path Update PDUs until the connection
// closes or ctx is cancelled. It blocks; call it from its own goroutine.
// See fastpath.go for the framing and bitmap.go/rle.go for how Bitmap
// Updates become the RGBA rectangles sink.Update expects.
func (c *Client) Run(ctx context.Context, sink FramebufferSink) error {
	sink.Init(c.serverWidth, c.serverHeight, "")

	done := make(chan struct{})
	defer close(done)
	go func() {
		select {
		case <-ctx.Done():
			c.conn.Close()
		case <-done:
		}
	}()

	for {
		if err := c.readFastPathUpdate(sink); err != nil {
			return err
		}
	}
}

// negotiateSecurity performs the X.224 Connection Request/Confirm exchange
// and, once the server confirms PROTOCOL_SSL, upgrades the connection to
// TLS. See transport.go.
func (c *Client) negotiateSecurity() error {
	if err := writeTPKT(c.tc, buildConnectionRequest(c.username)); err != nil {
		return err
	}
	payload, err := readTPKT(c.tc)
	if err != nil {
		return err
	}
	if _, err := parseConnectionConfirm(payload); err != nil {
		return err
	}

	// RDP servers use self-signed certificates by default; there's no
	// cert pinning/TOFU yet (documented roadmap gap, same honesty as the
	// VNC client's "no TLS in v0.1.0" note, inverted here).
	tlsConn := tls.Client(c.conn, &tls.Config{InsecureSkipVerify: true}) //nolint:gosec
	if err := tlsConn.HandshakeContext(context.Background()); err != nil {
		return fmt.Errorf("rdp: TLS handshake: %w", err)
	}
	c.tc = tlsConn
	return nil
}
