package rdp

import (
	"reflect"
	"testing"
)

type recordedUpdate struct {
	x, y, w, h int
	rgba       []byte
}
type recordedCopy struct{ dstX, dstY, w, h, srcX, srcY int }

type recordingSink struct {
	updates []recordedUpdate
	copies  []recordedCopy
}

func (s *recordingSink) Init(int, int, string) {}
func (s *recordingSink) Update(x, y, w, h int, rgba []byte) {
	cp := make([]byte, len(rgba))
	copy(cp, rgba)
	s.updates = append(s.updates, recordedUpdate{x, y, w, h, cp})
}
func (s *recordingSink) CopyRect(dstX, dstY, w, h, srcX, srcY int) {
	s.copies = append(s.copies, recordedCopy{dstX, dstY, w, h, srcX, srcY})
}
func (s *recordingSink) Resize(int, int)                   {}
func (s *recordingSink) Cursor(int, int, int, int, []byte) {}
func (s *recordingSink) CutText(string)                    {}

func solid(w, h int, r, g, b byte) []byte {
	return solidRGBA(w, h, r, g, b)
}

// TestDecodeOpaqueRect covers the single-rectangle solid-fill order: all
// four coordinate fields plus the three individual color-byte fields
// present, absolute (non-delta) coordinates.
func TestDecodeOpaqueRect(t *testing.T) {
	order := []byte{
		0x09,       // controlFlags: ORDER_STANDARD | ORDER_TYPE_CHANGE
		0x0A,       // orderType: OPAQUE_RECT
		0x7F,       // fieldFlags: fields 1-7 present (1 byte, OPAQUE_RECT_ORDER_FIELD_BYTES=1)
		0x0A, 0x00, // left = 10
		0x14, 0x00, // top = 20
		0x1E, 0x00, // width = 30
		0x28, 0x00, // height = 40
		0xAA, 0xBB, 0xCC, // r, g, b
	}
	c := &Client{}
	sink := &recordingSink{}
	r := &sliceReader{buf: order}
	if err := c.decodeOrdersUpdate(r, 1, sink); err != nil {
		t.Fatalf("decodeOrdersUpdate: %v", err)
	}
	if r.pos != len(order) {
		t.Errorf("consumed %d of %d bytes", r.pos, len(order))
	}
	want := []recordedUpdate{{10, 20, 30, 40, solid(30, 40, 0xAA, 0xBB, 0xCC)}}
	if !reflect.DeepEqual(sink.updates, want) {
		t.Errorf("updates = %+v, want %+v", sink.updates, want)
	}
}

// TestDecodeDstBltBlackness covers the simplest DstBlt case (no source or
// pattern operand needed) and confirms the persistent per-order-type
// state actually persists: a second order of the same type with
// ORDER_TYPE_CHANGE unset and only the rop field present must reuse the
// coordinates from the first.
func TestDecodeDstBltBlackness(t *testing.T) {
	orders := []byte{
		// Order 1: full DstBlt, BLACKNESS.
		0x09,       // controlFlags: STANDARD | TYPE_CHANGE
		0x00,       // orderType: DSTBLT
		0x1F,       // fieldFlags: fields 1-5 present
		0x64, 0x00, // left = 100
		0x64, 0x00, // top = 100
		0x32, 0x00, // width = 50
		0x32, 0x00, // height = 50
		0x00, // rop = BLACKNESS
		// Order 2: same type (no TYPE_CHANGE), only rop field present ->
		// coordinates reused from order 1, rop switches to WHITENESS.
		0x01, // controlFlags: STANDARD only
		0x10, // fieldFlags: field 5 only
		0xFF, // rop = WHITENESS
	}
	c := &Client{}
	sink := &recordingSink{}
	r := &sliceReader{buf: orders}
	if err := c.decodeOrdersUpdate(r, 2, sink); err != nil {
		t.Fatalf("decodeOrdersUpdate: %v", err)
	}
	want := []recordedUpdate{
		{100, 100, 50, 50, solid(50, 50, 0, 0, 0)},
		{100, 100, 50, 50, solid(50, 50, 0xFF, 0xFF, 0xFF)}, // reused coords, new rop
	}
	if !reflect.DeepEqual(sink.updates, want) {
		t.Errorf("updates = %+v, want %+v", sink.updates, want)
	}
}

// TestDecodeScrBltSrcCopy confirms ScrBlt(SRCCOPY) turns into a plain
// CopyRect call, the same primitive VNC's CopyRect encoding uses.
func TestDecodeScrBltSrcCopy(t *testing.T) {
	order := []byte{
		0x09,       // controlFlags: STANDARD | TYPE_CHANGE
		0x02,       // orderType: SCRBLT
		0x7F,       // fieldFlags: fields 1-7 present
		0x00, 0x00, // left = 0
		0x0A, 0x00, // top = 10
		0x40, 0x00, // width = 64
		0x20, 0x00, // height = 32
		0xCC,       // rop = SRCCOPY
		0x00, 0x00, // xSrc = 0
		0x32, 0x00, // ySrc = 50
	}
	c := &Client{}
	sink := &recordingSink{}
	r := &sliceReader{buf: order}
	if err := c.decodeOrdersUpdate(r, 1, sink); err != nil {
		t.Fatalf("decodeOrdersUpdate: %v", err)
	}
	want := []recordedCopy{{0, 10, 64, 32, 0, 50}}
	if !reflect.DeepEqual(sink.copies, want) {
		t.Errorf("copies = %+v, want %+v", sink.copies, want)
	}
	if len(sink.updates) != 0 {
		t.Errorf("expected no Update calls, got %+v", sink.updates)
	}
}

// TestDecodeMultiDstBltDeltaRects exercises the delta-rectangle list
// decoder (readDeltaRects/readOrderDelta) end to end: two rectangles, the
// second reusing width/height from the first and expressed as a small
// positive delta on left (top unchanged).
func TestDecodeMultiDstBltDeltaRects(t *testing.T) {
	order := []byte{
		0x09,       // controlFlags: STANDARD | TYPE_CHANGE
		0x0F,       // orderType: MULTI_DSTBLT
		0x7F,       // fieldFlags: fields 1-7 present (MULTI_DSTBLT_ORDER_FIELD_BYTES=1)
		0x00, 0x00, // left (bounding box, unused for painting) = 0
		0x00, 0x00, // top = 0
		0x64, 0x00, // width = 100
		0x64, 0x00, // height = 100
		0x00,       // rop = BLACKNESS
		0x02,       // numRectangles = 2
		0x00, 0x00, // cbData (unchecked)
		0x03,                   // zeroBits: rect0 nibble=0x0 (all present), rect1 nibble=0x3 (width+height omitted)
		0x05, 0x05, 0x0A, 0x0A, // rect0: left=5, top=5, width=10, height=10
		0x03, 0x00, // rect1: left delta=+3, top delta=+0 (width/height reused)
	}
	c := &Client{}
	sink := &recordingSink{}
	r := &sliceReader{buf: order}
	if err := c.decodeOrdersUpdate(r, 1, sink); err != nil {
		t.Fatalf("decodeOrdersUpdate: %v", err)
	}
	want := []recordedUpdate{
		{5, 5, 10, 10, solid(10, 10, 0, 0, 0)},
		{8, 5, 10, 10, solid(10, 10, 0, 0, 0)}, // left = 5+3, top = 5+0, width/height reused
	}
	if !reflect.DeepEqual(sink.updates, want) {
		t.Errorf("updates = %+v, want %+v", sink.updates, want)
	}
}

// TestDecodePrimaryOrderRejectsUndeclaredType confirms a server that
// ignores capability negotiation and sends an order type this client
// never advertised support for (e.g. PATBLT) is treated as a protocol
// error rather than silently mis-parsed.
func TestDecodePrimaryOrderRejectsUndeclaredType(t *testing.T) {
	order := []byte{0x09, 0x01} // controlFlags: STANDARD|TYPE_CHANGE, orderType: PATBLT (not declared)
	c := &Client{}
	sink := &recordingSink{}
	r := &sliceReader{buf: order}
	err := c.decodeOrdersUpdate(r, 1, sink)
	if err == nil {
		t.Fatal("expected an error for an undeclared order type, got nil")
	}
}
