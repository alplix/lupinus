package rdp

import (
	"fmt"
	"io"
)

// Fast-Path Update codes (MS-RDPBCGR 2.2.9.1.2.1, updateHeader low nibble).
const (
	fpUpdateOrders      = 0x0
	fpUpdateBitmap      = 0x1
	fpUpdatePalette     = 0x2
	fpUpdateSynchronize = 0x3
	fpUpdateSurfCmds    = 0x4
	fpUpdatePtrNull     = 0x5
	fpUpdatePtrDefault  = 0x6
	fpUpdatePtrPosition = 0x8
	fpUpdateColor       = 0x9
	fpUpdateCached      = 0xA
	fpUpdatePointer     = 0xB
)

// readFastPathUpdate reads one Fast-Path Update PDU (MS-RDPBCGR 2.2.9.1.2)
// — the framing server graphics traffic actually uses, distinct from the
// TPKT/X.224/MCS-wrapped "slow path" used during setup. Confirmed
// byte-for-byte against xrdp's xrdp_sec_send_fastpath (libxrdp/xrdp_sec.c,
// "no crypt" branch, the path this client always takes since Enhanced/TLS
// security means encryptionMethods is always 0): one header byte (action
// in the low 2 bits, secFlags in the high 2 — always 0/0 for an
// unencrypted session), then a length using the same short/long PER-style
// form used elsewhere (xrdp always emits the long form in practice, but
// this reads both correctly).
func (c *Client) readFastPathUpdate(sink FramebufferSink) error {
	header, err := readUint8(c.tc)
	if err != nil {
		return err
	}
	if header == tpktVersion {
		// Occasional slow-path traffic (e.g. Set Error Info, Save
		// Session Info) can interleave with the Fast-Path graphics
		// stream even after activation. This client doesn't act on
		// any of it in v0.2.0, but must still parse the TPKT/X.224
		// framing to consume exactly the right number of bytes and
		// stay in sync for the next PDU, whichever path it uses.
		return c.handleSlowPathPDU(sink)
	}
	if header&0x03 != fastPathOutputAction {
		return protoErrf("expected Fast-Path output PDU, got action %d (byte 0x%02x)", header&0x03, header)
	}

	first, err := readUint8(c.tc)
	if err != nil {
		return err
	}
	var length int
	if first&0x80 != 0 {
		second, err := readUint8(c.tc)
		if err != nil {
			return err
		}
		length = int(first&0x7F)<<8 | int(second)
	} else {
		length = int(first)
	}
	// length includes the header byte(s) already read; the two forms
	// consumed 2 or 3 bytes so far.
	headerBytes := 2
	if first&0x80 != 0 {
		headerBytes = 3
	}
	remaining := length - headerBytes
	if remaining < 0 {
		return protoErrf("Fast-Path Update PDU: length %d smaller than its own header", length)
	}
	body, err := readFull(c.tc, remaining)
	if err != nil {
		return err
	}
	if debugWire {
		fmt.Fprintf(osStderr, "DEBUG readFastPathUpdate: header=0x%02x length=%d body=% x\n", header, length, body)
	}

	r := &sliceReader{buf: body}
	for r.pos < len(r.buf) {
		if err := c.decodeFastPathUpdate(r, sink); err != nil {
			return err
		}
	}
	return nil
}

// decodeFastPathUpdate reads one TS_FP_UPDATE structure from r and
// dispatches it. Fragmentation (an update split across multiple TS_FP_
// UPDATE entries) isn't implemented — every server actually observed
// sends single-fragment updates in practice, and a fragmented update is
// rejected with a clear error rather than silently corrupting the frame.
func (c *Client) decodeFastPathUpdate(r *sliceReader, sink FramebufferSink) error {
	updateHeader, err := r.readUint8()
	if err != nil {
		return err
	}
	updateCode := updateHeader & 0x0F
	fragmentation := (updateHeader >> 4) & 0x03
	compressed := updateHeader&0x80 != 0

	if fragmentation != 0 {
		return &UnsupportedError{Msg: "fragmented Fast-Path updates aren't implemented"}
	}
	if compressed {
		// compressionFlags byte present; this client doesn't use
		// Fast-Path-level (as opposed to per-bitmap) compression.
		if _, err := r.readUint8(); err != nil {
			return err
		}
	}

	size, err := r.readUint16LE()
	if err != nil {
		return err
	}
	data, err := r.readN(int(size))
	if err != nil {
		return err
	}

	switch updateCode {
	case fpUpdateBitmap:
		return decodeBitmapUpdate(data, sink)
	case fpUpdateOrders:
		return c.decodeOrdersFastPath(data, sink)
	default:
		// Palette, synchronize, pointer shapes/position, etc: not
		// implemented in v0.2.0 (see the project plan) — safe to skip
		// since we already consumed exactly `size` bytes.
		return nil
	}
}

// sliceReader is a tiny bounds-checked cursor over an in-memory byte
// slice, used for parsing the body of a Fast-Path Update PDU (which may
// contain several TS_FP_UPDATE entries back to back).
type sliceReader struct {
	buf []byte
	pos int
}

func (r *sliceReader) readUint8() (uint8, error) {
	if r.pos >= len(r.buf) {
		return 0, io.ErrUnexpectedEOF
	}
	b := r.buf[r.pos]
	r.pos++
	return b, nil
}

func (r *sliceReader) readUint16LE() (uint16, error) {
	if r.pos+2 > len(r.buf) {
		return 0, io.ErrUnexpectedEOF
	}
	v := uint16(r.buf[r.pos]) | uint16(r.buf[r.pos+1])<<8
	r.pos += 2
	return v, nil
}

func (r *sliceReader) readN(n int) ([]byte, error) {
	if n < 0 || r.pos+n > len(r.buf) {
		return nil, io.ErrUnexpectedEOF
	}
	b := r.buf[r.pos : r.pos+n]
	r.pos += n
	return b, nil
}
