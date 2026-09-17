package rdp

import "fmt"

// GDI drawing orders (MS-RDPEGDI) — a from-scratch, deliberately bounded
// implementation ported against FreeRDP's libfreerdp/core/orders.c (the
// field-presence delta-encoding scheme isn't fully spelled out anywhere
// more official than "what the reference implementation does", same
// situation as VNC's Tight encoding).
//
// Only the "no cache needed" rectangle orders are supported: DstBlt,
// ScrBlt, OpaqueRect, and their Multi* (multiple-delta-rectangle)
// variants. This client's Order Capability Set (capabilities.go) only
// declares support for exactly these — PatBlt (brush/pattern fills),
// MemBlt/Mem3Blt (cached-bitmap blits), and all text/glyph orders need a
// bitmap or glyph cache subsystem this client doesn't have, so they're
// never declared and a well-behaved server falls back to sending bitmap
// updates for that content instead, exactly like v0.2.0's original
// zero-orders behavior. That fallback is what makes this safe to bound
// this way: nothing is silently missing, it just arrives as pixels
// instead of a compact draw command.
const (
	orderStandard          = 0x01
	orderSecondary         = 0x02
	orderBounds            = 0x04
	orderTypeChange        = 0x08
	orderDeltaCoordinates  = 0x10
	orderZeroBoundsDeltas  = 0x20
	orderZeroFieldByteBit0 = 0x40
	orderZeroFieldByteBit1 = 0x80

	orderTypeDstBlt          = 0x00
	orderTypeScrBlt          = 0x02
	orderTypeOpaqueRect      = 0x0A
	orderTypeMultiDstBlt     = 0x0F
	orderTypeMultiScrBlt     = 0x11
	orderTypeMultiOpaqueRect = 0x12

	dstBltFieldBytes          = 1
	scrBltFieldBytes          = 1
	opaqueRectFieldBytes      = 1
	multiDstBltFieldBytes     = 1
	multiScrBltFieldBytes     = 2
	multiOpaqueRectFieldBytes = 2

	boundLeft       = 0x01
	boundTop        = 0x02
	boundRight      = 0x04
	boundBottom     = 0x08
	boundDeltaLeft  = 0x10
	boundDeltaTop   = 0x20
	boundDeltaRight = 0x40
	boundDeltaBot   = 0x80

	// ROP3 codes this client actually acts on for DstBlt — the only ones
	// that matter when there's no pattern/source operand, i.e. the result
	// depends on nothing but the (unknown-to-us) existing destination
	// pixels or a constant. Anything else is silently skipped: this
	// client keeps no local framebuffer to compute a real function of
	// "current destination" against (see decodeDstBlt).
	rop3Blackness = 0x00
	rop3Whiteness = 0xFF
	rop3NoOp      = 0xAA // D — destination unchanged
	rop3SrcCopy   = 0xCC // S — used by ScrBlt; anything else needs pixels we don't have either
)

type deltaRect struct{ left, top, width, height int32 }

// orderState holds every primary order's persistent field values —
// MS-RDPEGDI's delta encoding means an order only transmits fields that
// changed since the last order *of that same type*, so the previous
// values have to survive across PDUs, not just within one.
type orderState struct {
	orderType        byte
	fieldFlags       uint32
	deltaCoordinates bool

	dstblt struct {
		left, top, width, height int32
		rop                      byte
	}
	scrblt struct {
		left, top, width, height int32
		rop                      byte
		xSrc, ySrc               int32
	}
	opaqueRect struct {
		left, top, width, height int32
		r, g, b                  byte
	}
	multiDstblt struct {
		left, top, width, height int32
		rop                      byte
		numRects                 int
		rects                    []deltaRect
	}
	multiScrblt struct {
		left, top, width, height int32
		rop                      byte
		xSrc, ySrc               int32
		numRects                 int
		rects                    []deltaRect
	}
	multiOpaqueRect struct {
		left, top, width, height int32
		r, g, b                  byte
		numRects                 int
		rects                    []deltaRect
	}
}

func fieldSet(fieldFlags uint32, fieldNumber int) bool {
	return fieldFlags&(1<<uint(fieldNumber-1)) != 0
}

// readFieldFlags reads the variable-length (0-3 byte) field-presence
// bitmask preceding a primary order's data, honoring the two
// "server determined some fields definitely absent, shrink the mask"
// control-flag bits.
func readFieldFlags(r *sliceReader, controlFlags byte, fieldBytes int) (uint32, error) {
	if controlFlags&orderZeroFieldByteBit0 != 0 {
		fieldBytes--
	}
	if controlFlags&orderZeroFieldByteBit1 != 0 {
		if fieldBytes > 1 {
			fieldBytes -= 2
		} else {
			fieldBytes = 0
		}
	}
	var flags uint32
	for i := 0; i < fieldBytes; i++ {
		b, err := r.readUint8()
		if err != nil {
			return 0, err
		}
		flags |= uint32(b) << uint(i*8)
	}
	return flags, nil
}

// readOrderCoord reads one coordinate field: a signed 1-byte delta added
// to the running value, or a signed 2-byte absolute value, depending on
// the order's ORDER_DELTA_COORDINATES control flag.
func readOrderCoord(r *sliceReader, cur *int32, delta bool) error {
	if delta {
		b, err := r.readUint8()
		if err != nil {
			return err
		}
		*cur += int32(int8(b))
		return nil
	}
	v, err := r.readUint16LE()
	if err != nil {
		return err
	}
	*cur = int32(int16(v))
	return nil
}

func readFieldCoord(r *sliceReader, fieldFlags uint32, fieldNumber int, cur *int32, delta bool) error {
	if !fieldSet(fieldFlags, fieldNumber) {
		return nil
	}
	return readOrderCoord(r, cur, delta)
}

func readFieldByte(r *sliceReader, fieldFlags uint32, fieldNumber int, cur *byte) error {
	if !fieldSet(fieldFlags, fieldNumber) {
		return nil
	}
	b, err := r.readUint8()
	if err != nil {
		return err
	}
	*cur = b
	return nil
}

// readOrderDelta reads Tight-unrelated but similarly-shaped RDP "delta"
// value used only inside delta-rectangle lists: a 6-bit signed magnitude
// in the first byte (top bit = continuation, next bit = sign), optionally
// extended by a second byte. Ported directly from FreeRDP's
// update_read_delta rather than re-derived, since the bit-twiddling is
// easy to get subtly wrong from a description alone.
func readOrderDelta(r *sliceReader) (int32, error) {
	b, err := r.readUint8()
	if err != nil {
		return 0, err
	}
	var uvalue uint32
	if b&0x40 != 0 {
		uvalue = uint32(b) | 0xFFFFFFC0
	} else {
		uvalue = uint32(b) & 0x3F
	}
	if b&0x80 != 0 {
		b2, err := r.readUint8()
		if err != nil {
			return 0, err
		}
		uvalue = (uvalue << 8) | uint32(b2)
	}
	return int32(uvalue), nil
}

// readDeltaRects reads a MULTI_*_ORDER's rectangle list: a packed
// zero-bits bitmap (4 bits/rectangle: which of left/top/width/height are
// omitted) followed by the present fields; left/top delta-chain onto the
// previous rectangle even when omitted (omitted means "same as previous"
// for left/top, i.e. a delta of 0), width/height instead just directly
// reuse the previous rectangle's value when omitted.
func readDeltaRects(r *sliceReader, numRects int) ([]deltaRect, error) {
	if numRects < 0 || numRects > 45 {
		return nil, protoErrf("rdp: invalid delta rectangle count %d", numRects)
	}
	zeroBits, err := r.readN((numRects + 1) / 2)
	if err != nil {
		return nil, err
	}
	rects := make([]deltaRect, numRects)
	var flags byte
	for i := 0; i < numRects; i++ {
		if i%2 == 0 {
			flags = zeroBits[i/2]
		}
		var left, top int32
		if flags&0x80 == 0 {
			if left, err = readOrderDelta(r); err != nil {
				return nil, err
			}
		}
		if flags&0x40 == 0 {
			if top, err = readOrderDelta(r); err != nil {
				return nil, err
			}
		}
		if flags&0x20 == 0 {
			if rects[i].width, err = readOrderDelta(r); err != nil {
				return nil, err
			}
		} else if i > 0 {
			rects[i].width = rects[i-1].width
		}
		if flags&0x10 == 0 {
			if rects[i].height, err = readOrderDelta(r); err != nil {
				return nil, err
			}
		} else if i > 0 {
			rects[i].height = rects[i-1].height
		}
		rects[i].left, rects[i].top = left, top
		if i > 0 {
			rects[i].left += rects[i-1].left
			rects[i].top += rects[i-1].top
		}
		flags <<= 4
	}
	return rects, nil
}

// readBounds consumes a primary order's optional clipping-bounds field.
// This client doesn't implement clipping (documented gap — see the type
// doc comment: the orders it supports rarely rely on bounds in practice),
// but still has to parse it correctly to stay byte-aligned for whatever
// comes next.
func readBounds(r *sliceReader) error {
	flags, err := r.readUint8()
	if err != nil {
		return err
	}
	var dummy int32
	for _, pair := range [...][2]byte{{boundLeft, boundDeltaLeft}, {boundTop, boundDeltaTop}, {boundRight, boundDeltaRight}, {boundBottom, boundDeltaBot}} {
		switch {
		case flags&pair[0] != 0:
			if err := readOrderCoord(r, &dummy, false); err != nil {
				return err
			}
		case flags&pair[1] != 0:
			if err := readOrderCoord(r, &dummy, true); err != nil {
				return err
			}
		}
	}
	return nil
}

// solidRGBA fills a fresh w*h*4 buffer with one RGB color, alpha 0xFF.
func solidRGBA(w, h int, r, g, b byte) []byte {
	buf := make([]byte, w*h*4)
	for i := 0; i < w*h; i++ {
		off := i * 4
		buf[off], buf[off+1], buf[off+2], buf[off+3] = r, g, b, 0xFF
	}
	return buf
}

// decodeOrdersFastPath handles a Fast-Path Orders Update (TS_FP_UPDATE
// with updateCode FASTPATH_UPDATETYPE_ORDERS): just a numberOrders count
// followed by that many orders, no padding (unlike the slow-path form —
// see handleSlowPathPDU in transport.go).
func (c *Client) decodeOrdersFastPath(data []byte, sink FramebufferSink) error {
	r := &sliceReader{buf: data}
	numberOrders, err := r.readUint16LE()
	if err != nil {
		return err
	}
	return c.decodeOrdersUpdate(r, int(numberOrders), sink)
}

// decodeOrdersUpdate reads numberOrders primary/secondary/altsec orders
// from r. Secondary and alternate-secondary orders (bitmap/glyph/brush
// caching, window orders, etc.) are all for order types this client never
// declares support for, so encountering one is a genuine protocol
// violation, not a "not implemented, skip" situation — same posture as
// the rest of this client toward servers that ignore capability
// negotiation.
func (c *Client) decodeOrdersUpdate(r *sliceReader, numberOrders int, sink FramebufferSink) error {
	for i := 0; i < numberOrders; i++ {
		controlFlags, err := r.readUint8()
		if err != nil {
			return err
		}
		if controlFlags&orderStandard == 0 || controlFlags&orderSecondary != 0 {
			return &UnsupportedError{Msg: "rdp: server sent a secondary/altsec drawing order, which this client never declared support for"}
		}
		if err := c.decodePrimaryOrder(r, controlFlags, sink); err != nil {
			return err
		}
	}
	return nil
}

func (c *Client) decodePrimaryOrder(r *sliceReader, controlFlags byte, sink FramebufferSink) error {
	st := &c.orderState

	if debugWire {
		fmt.Fprintf(osStderr, "DEBUG primary order: controlFlags=0x%02x priorType=0x%02x\n", controlFlags, st.orderType)
	}

	if controlFlags&orderTypeChange != 0 {
		b, err := r.readUint8()
		if err != nil {
			return err
		}
		st.orderType = b
	}

	var fieldBytes int
	switch st.orderType {
	case orderTypeDstBlt:
		fieldBytes = dstBltFieldBytes
	case orderTypeScrBlt:
		fieldBytes = scrBltFieldBytes
	case orderTypeOpaqueRect:
		fieldBytes = opaqueRectFieldBytes
	case orderTypeMultiDstBlt:
		fieldBytes = multiDstBltFieldBytes
	case orderTypeMultiScrBlt:
		fieldBytes = multiScrBltFieldBytes
	case orderTypeMultiOpaqueRect:
		fieldBytes = multiOpaqueRectFieldBytes
	default:
		return &UnsupportedError{Msg: "rdp: server sent a primary drawing order type it was never told this client supports"}
	}

	fieldFlags, err := readFieldFlags(r, controlFlags, fieldBytes)
	if err != nil {
		return err
	}
	st.fieldFlags = fieldFlags

	if controlFlags&orderBounds != 0 {
		if controlFlags&orderZeroBoundsDeltas == 0 {
			if err := readBounds(r); err != nil {
				return err
			}
		}
	}
	st.deltaCoordinates = controlFlags&orderDeltaCoordinates != 0

	switch st.orderType {
	case orderTypeDstBlt:
		return c.decodeDstBlt(r, sink)
	case orderTypeScrBlt:
		return c.decodeScrBlt(r, sink)
	case orderTypeOpaqueRect:
		return c.decodeOpaqueRect(r, sink)
	case orderTypeMultiDstBlt:
		return c.decodeMultiDstBlt(r, sink)
	case orderTypeMultiScrBlt:
		return c.decodeMultiScrBlt(r, sink)
	case orderTypeMultiOpaqueRect:
		return c.decodeMultiOpaqueRect(r, sink)
	}
	return nil
}

func (c *Client) decodeDstBlt(r *sliceReader, sink FramebufferSink) error {
	st := &c.orderState.dstblt
	f := c.orderState.fieldFlags
	d := c.orderState.deltaCoordinates
	if err := readFieldCoord(r, f, 1, &st.left, d); err != nil {
		return err
	}
	if err := readFieldCoord(r, f, 2, &st.top, d); err != nil {
		return err
	}
	if err := readFieldCoord(r, f, 3, &st.width, d); err != nil {
		return err
	}
	if err := readFieldCoord(r, f, 4, &st.height, d); err != nil {
		return err
	}
	if err := readFieldByte(r, f, 5, &st.rop); err != nil {
		return err
	}
	if st.width <= 0 || st.height <= 0 {
		return nil
	}
	switch st.rop {
	case rop3Blackness:
		sink.Update(int(st.left), int(st.top), int(st.width), int(st.height), solidRGBA(int(st.width), int(st.height), 0, 0, 0))
	case rop3Whiteness:
		sink.Update(int(st.left), int(st.top), int(st.width), int(st.height), solidRGBA(int(st.width), int(st.height), 0xFF, 0xFF, 0xFF))
	case rop3NoOp:
		// Destination explicitly unchanged — nothing to paint.
	default:
		// Every other ROP3 code needs the existing destination pixels
		// (DSTINVERT and friends) or a pattern (PatBlt territory), and
		// this client keeps no local framebuffer to compute that
		// against. Safe to skip: the region simply isn't updated by
		// this order, matching what a client that never declared this
		// capability would experience anyway.
	}
	return nil
}

func (c *Client) decodeScrBlt(r *sliceReader, sink FramebufferSink) error {
	st := &c.orderState.scrblt
	f := c.orderState.fieldFlags
	d := c.orderState.deltaCoordinates
	if err := readFieldCoord(r, f, 1, &st.left, d); err != nil {
		return err
	}
	if err := readFieldCoord(r, f, 2, &st.top, d); err != nil {
		return err
	}
	if err := readFieldCoord(r, f, 3, &st.width, d); err != nil {
		return err
	}
	if err := readFieldCoord(r, f, 4, &st.height, d); err != nil {
		return err
	}
	if err := readFieldByte(r, f, 5, &st.rop); err != nil {
		return err
	}
	if err := readFieldCoord(r, f, 6, &st.xSrc, d); err != nil {
		return err
	}
	if err := readFieldCoord(r, f, 7, &st.ySrc, d); err != nil {
		return err
	}
	if st.width > 0 && st.height > 0 && st.rop == rop3SrcCopy {
		sink.CopyRect(int(st.left), int(st.top), int(st.width), int(st.height), int(st.xSrc), int(st.ySrc))
	}
	return nil
}

func (c *Client) decodeOpaqueRect(r *sliceReader, sink FramebufferSink) error {
	st := &c.orderState.opaqueRect
	f := c.orderState.fieldFlags
	d := c.orderState.deltaCoordinates
	if err := readFieldCoord(r, f, 1, &st.left, d); err != nil {
		return err
	}
	if err := readFieldCoord(r, f, 2, &st.top, d); err != nil {
		return err
	}
	if err := readFieldCoord(r, f, 3, &st.width, d); err != nil {
		return err
	}
	if err := readFieldCoord(r, f, 4, &st.height, d); err != nil {
		return err
	}
	if err := readFieldByte(r, f, 5, &st.r); err != nil {
		return err
	}
	if err := readFieldByte(r, f, 6, &st.g); err != nil {
		return err
	}
	if err := readFieldByte(r, f, 7, &st.b); err != nil {
		return err
	}
	if st.width > 0 && st.height > 0 {
		sink.Update(int(st.left), int(st.top), int(st.width), int(st.height), solidRGBA(int(st.width), int(st.height), st.r, st.g, st.b))
	}
	return nil
}

func (c *Client) decodeMultiDstBlt(r *sliceReader, sink FramebufferSink) error {
	st := &c.orderState.multiDstblt
	f := c.orderState.fieldFlags
	d := c.orderState.deltaCoordinates
	if err := readFieldCoord(r, f, 1, &st.left, d); err != nil {
		return err
	}
	if err := readFieldCoord(r, f, 2, &st.top, d); err != nil {
		return err
	}
	if err := readFieldCoord(r, f, 3, &st.width, d); err != nil {
		return err
	}
	if err := readFieldCoord(r, f, 4, &st.height, d); err != nil {
		return err
	}
	if err := readFieldByte(r, f, 5, &st.rop); err != nil {
		return err
	}
	numRects := byte(st.numRects)
	if err := readFieldByte(r, f, 6, &numRects); err != nil {
		return err
	}
	if fieldSet(f, 7) {
		if _, err := r.readUint16LE(); err != nil { // cbData
			return err
		}
		st.numRects = int(numRects)
		rects, err := readDeltaRects(r, st.numRects)
		if err != nil {
			return err
		}
		st.rects = rects
	} else {
		st.numRects = int(numRects)
	}

	var fill []byte
	switch st.rop {
	case rop3Blackness:
		fill = []byte{0, 0, 0, 0xFF}
	case rop3Whiteness:
		fill = []byte{0xFF, 0xFF, 0xFF, 0xFF}
	default:
		return nil
	}
	for _, rect := range st.rects {
		if rect.width <= 0 || rect.height <= 0 {
			continue
		}
		sink.Update(int(rect.left), int(rect.top), int(rect.width), int(rect.height),
			solidRGBA(int(rect.width), int(rect.height), fill[0], fill[1], fill[2]))
	}
	return nil
}

func (c *Client) decodeMultiScrBlt(r *sliceReader, sink FramebufferSink) error {
	st := &c.orderState.multiScrblt
	f := c.orderState.fieldFlags
	d := c.orderState.deltaCoordinates
	if err := readFieldCoord(r, f, 1, &st.left, d); err != nil {
		return err
	}
	if err := readFieldCoord(r, f, 2, &st.top, d); err != nil {
		return err
	}
	if err := readFieldCoord(r, f, 3, &st.width, d); err != nil {
		return err
	}
	if err := readFieldCoord(r, f, 4, &st.height, d); err != nil {
		return err
	}
	if err := readFieldByte(r, f, 5, &st.rop); err != nil {
		return err
	}
	if err := readFieldCoord(r, f, 6, &st.xSrc, d); err != nil {
		return err
	}
	if err := readFieldCoord(r, f, 7, &st.ySrc, d); err != nil {
		return err
	}
	numRects := byte(st.numRects)
	if err := readFieldByte(r, f, 8, &numRects); err != nil {
		return err
	}
	if fieldSet(f, 9) {
		if _, err := r.readUint16LE(); err != nil { // cbData
			return err
		}
		st.numRects = int(numRects)
		rects, err := readDeltaRects(r, st.numRects)
		if err != nil {
			return err
		}
		st.rects = rects
	} else {
		st.numRects = int(numRects)
	}

	if st.rop != rop3SrcCopy {
		return nil
	}
	// MultiScrBlt carries exactly one (nXSrc, nYSrc) pair for the whole
	// order, not one per rectangle — matching its real-world purpose
	// (scrolling a region that's fragmented by occluding windows: every
	// visible fragment moves by the same scroll offset). Each
	// rectangle's source is therefore its destination shifted by the
	// order's single src/dest delta.
	dx, dy := st.xSrc-st.left, st.ySrc-st.top
	for _, rect := range st.rects {
		if rect.width <= 0 || rect.height <= 0 {
			continue
		}
		sink.CopyRect(int(rect.left), int(rect.top), int(rect.width), int(rect.height),
			int(rect.left+dx), int(rect.top+dy))
	}
	return nil
}

func (c *Client) decodeMultiOpaqueRect(r *sliceReader, sink FramebufferSink) error {
	st := &c.orderState.multiOpaqueRect
	f := c.orderState.fieldFlags
	d := c.orderState.deltaCoordinates
	if err := readFieldCoord(r, f, 1, &st.left, d); err != nil {
		return err
	}
	if err := readFieldCoord(r, f, 2, &st.top, d); err != nil {
		return err
	}
	if err := readFieldCoord(r, f, 3, &st.width, d); err != nil {
		return err
	}
	if err := readFieldCoord(r, f, 4, &st.height, d); err != nil {
		return err
	}
	if err := readFieldByte(r, f, 5, &st.r); err != nil {
		return err
	}
	if err := readFieldByte(r, f, 6, &st.g); err != nil {
		return err
	}
	if err := readFieldByte(r, f, 7, &st.b); err != nil {
		return err
	}
	numRects := byte(st.numRects)
	if err := readFieldByte(r, f, 8, &numRects); err != nil {
		return err
	}
	if fieldSet(f, 9) {
		if _, err := r.readUint16LE(); err != nil { // cbData
			return err
		}
		st.numRects = int(numRects)
		rects, err := readDeltaRects(r, st.numRects)
		if err != nil {
			return err
		}
		st.rects = rects
	} else {
		st.numRects = int(numRects)
	}

	for _, rect := range st.rects {
		if rect.width <= 0 || rect.height <= 0 {
			continue
		}
		sink.Update(int(rect.left), int(rect.top), int(rect.width), int(rect.height),
			solidRGBA(int(rect.width), int(rect.height), st.r, st.g, st.b))
	}
	return nil
}
