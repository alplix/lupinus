package wsbridge

import "testing"

func solid(w, h int, r, g, b byte) []byte {
	buf := make([]byte, w*h*4)
	for i := 0; i < w*h; i++ {
		buf[i*4], buf[i*4+1], buf[i*4+2], buf[i*4+3] = r, g, b, 0xFF
	}
	return buf
}

func TestDiffAndStore(t *testing.T) {
	sess := &Session{}
	sess.resetShadow(10, 8)

	// Nothing is on the canvas yet, so the first update is sent whole.
	rect := solid(4, 3, 10, 20, 30)
	x, y, w, h, ok := sess.diffAndStore(2, 1, 4, 3, rect)
	if !ok || x != 2 || y != 1 || w != 4 || h != 3 {
		t.Fatalf("first update = (%d,%d %dx%d ok=%v), want whole rect (2,1 4x3)", x, y, w, h, ok)
	}

	// Resending identical pixels must be suppressed.
	if _, _, _, _, ok := sess.diffAndStore(2, 1, 4, 3, rect); ok {
		t.Fatal("identical update was not suppressed")
	}

	// Changing one interior pixel shrinks the update to just that pixel.
	changed := solid(4, 3, 10, 20, 30)
	changed[(1*4+2)*4] = 99 // row 1, col 2 of the rect -> canvas (4, 2)
	x, y, w, h, ok = sess.diffAndStore(2, 1, 4, 3, changed)
	if !ok || x != 4 || y != 2 || w != 1 || h != 1 {
		t.Fatalf("one-pixel change = (%d,%d %dx%d ok=%v), want (4,2 1x1)", x, y, w, h, ok)
	}
	if got := sess.fb[(2*10+4)*4]; got != 99 {
		t.Errorf("shadow not updated: got %d, want 99", got)
	}

	// Two separated changes -> bounding box covering both.
	changed2 := solid(4, 3, 10, 20, 30)
	changed2[(0*4+0)*4] = 1 // canvas (2,1)
	changed2[(2*4+3)*4] = 2 // canvas (5,3)
	x, y, w, h, ok = sess.diffAndStore(2, 1, 4, 3, changed2)
	// Pixel (4,2) also reverted to 10, so it is part of the diff too.
	if !ok || x != 2 || y != 1 || w != 4 || h != 3 {
		t.Fatalf("multi-change = (%d,%d %dx%d ok=%v), want (2,1 4x3)", x, y, w, h, ok)
	}

	// Out-of-bounds rectangles are passed through untouched.
	x, y, w, h, ok = sess.diffAndStore(8, 6, 4, 4, solid(4, 4, 1, 1, 1))
	if !ok || x != 8 || y != 6 || w != 4 || h != 4 {
		t.Fatalf("out-of-bounds rect altered: (%d,%d %dx%d ok=%v)", x, y, w, h, ok)
	}
}

func TestCopyShadowOverlap(t *testing.T) {
	sess := &Session{}
	sess.resetShadow(4, 4)
	// Rows 0..3 hold values 1..4 in the R channel of column 0.
	for r := 0; r < 4; r++ {
		sess.fb[(r*4)*4] = byte(r + 1)
	}
	// Scroll rows 0..2 down by one (overlapping copy): expect 1,1,2,3.
	sess.copyShadow(0, 1, 1, 3, 0, 0)
	got := []byte{sess.fb[0], sess.fb[4*4], sess.fb[8*4], sess.fb[12*4]}
	if want := []byte{1, 1, 2, 3}; string(got) != string(want) {
		t.Errorf("scroll down = %v, want %v", got, want)
	}
}
