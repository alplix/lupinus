package rfb

import (
	"bytes"
	"testing"
)

func TestReadRLELength(t *testing.T) {
	cases := []struct {
		in   []byte
		want int
	}{
		{[]byte{5}, 6},
		{[]byte{0}, 1},
		{[]byte{255, 10}, 266},
		{[]byte{255, 255, 0}, 511},
	}
	for _, c := range cases {
		got, err := readRLELength(bytes.NewReader(c.in), make([]byte, 1))
		if err != nil {
			t.Fatalf("readRLELength(%v): %v", c.in, err)
		}
		if got != c.want {
			t.Errorf("readRLELength(%v) = %d, want %d", c.in, got, c.want)
		}
	}
}

func TestExtractPackedIndex(t *testing.T) {
	// bits=4: 0xAB = 1010 1011 -> col0=0xA, col1=0xB
	row4 := []byte{0xAB}
	if got := extractPackedIndex(row4, 0, 4); got != 0xA {
		t.Errorf("bits=4 col0 = %d, want %d", got, 0xA)
	}
	if got := extractPackedIndex(row4, 1, 4); got != 0xB {
		t.Errorf("bits=4 col1 = %d, want %d", got, 0xB)
	}

	// bits=1: 0b10110000 -> col0=1, col1=0, col2=1
	row1 := []byte{0b10110000}
	want1 := []int{1, 0, 1}
	for i, w := range want1 {
		if got := extractPackedIndex(row1, i, 1); got != w {
			t.Errorf("bits=1 col%d = %d, want %d", i, got, w)
		}
	}
}

func rgbaAt(buf []byte, i int) (r, g, b, a byte) {
	return buf[i*4], buf[i*4+1], buf[i*4+2], buf[i*4+3]
}

func TestDecodeZRLETileRaw(t *testing.T) {
	// subencoding 0 (Raw), 2x1 tile, CPIXELs (R,G,B) for each pixel.
	in := []byte{0, 10, 20, 30, 40, 50, 60}
	out := make([]byte, 2*1*4)
	if err := decodeZRLETile(bytes.NewReader(in), &pixelLevels[0], make([]byte, zrleScratchSize), out, 2, 1); err != nil {
		t.Fatalf("decodeZRLETile: %v", err)
	}
	if r, g, b, a := rgbaAt(out, 0); r != 10 || g != 20 || b != 30 || a != 0xFF {
		t.Errorf("pixel0 = %d,%d,%d,%d", r, g, b, a)
	}
	if r, g, b, a := rgbaAt(out, 1); r != 40 || g != 50 || b != 60 || a != 0xFF {
		t.Errorf("pixel1 = %d,%d,%d,%d", r, g, b, a)
	}
}

func TestDecodeZRLETileSolid(t *testing.T) {
	// subencoding 1 (Solid), 2x2 tile, single CPIXEL.
	in := []byte{1, 7, 8, 9}
	out := make([]byte, 2*2*4)
	if err := decodeZRLETile(bytes.NewReader(in), &pixelLevels[0], make([]byte, zrleScratchSize), out, 2, 2); err != nil {
		t.Fatalf("decodeZRLETile: %v", err)
	}
	for i := 0; i < 4; i++ {
		if r, g, b, a := rgbaAt(out, i); r != 7 || g != 8 || b != 9 || a != 0xFF {
			t.Errorf("pixel%d = %d,%d,%d,%d", i, r, g, b, a)
		}
	}
}

func TestDecodeZRLETilePackedPalette(t *testing.T) {
	// subencoding 2 (packed palette, size 2), 4x1 tile, 1 bit/index.
	// palette[0]=(1,1,1), palette[1]=(2,2,2); indices 1,0,1,0 packed MSB-first -> 0b1010_0000 = 0xA0.
	in := []byte{2, 1, 1, 1, 2, 2, 2, 0xA0}
	out := make([]byte, 4*1*4)
	if err := decodeZRLETile(bytes.NewReader(in), &pixelLevels[0], make([]byte, zrleScratchSize), out, 4, 1); err != nil {
		t.Fatalf("decodeZRLETile: %v", err)
	}
	want := [][3]byte{{2, 2, 2}, {1, 1, 1}, {2, 2, 2}, {1, 1, 1}}
	for i, w := range want {
		if r, g, b, a := rgbaAt(out, i); r != w[0] || g != w[1] || b != w[2] || a != 0xFF {
			t.Errorf("pixel%d = %d,%d,%d,%d, want %v", i, r, g, b, a, w)
		}
	}
}

func TestDecodeZRLETilePlainRLE(t *testing.T) {
	// subencoding 128 (Plain RLE), 3 pixels total, one run of length 3
	// (encoded as 1 + runlength-byte(2)).
	in := []byte{128, 5, 6, 7, 2}
	out := make([]byte, 3*4)
	if err := decodeZRLETile(bytes.NewReader(in), &pixelLevels[0], make([]byte, zrleScratchSize), out, 3, 1); err != nil {
		t.Fatalf("decodeZRLETile: %v", err)
	}
	for i := 0; i < 3; i++ {
		if r, g, b, a := rgbaAt(out, i); r != 5 || g != 6 || b != 7 || a != 0xFF {
			t.Errorf("pixel%d = %d,%d,%d,%d", i, r, g, b, a)
		}
	}
}

func TestDecodeZRLETilePaletteRLE(t *testing.T) {
	// subencoding 130 (palette RLE, size 2), 3 pixels total: one run
	// entry (top bit set -> index 0) with run length 3.
	in := []byte{130, 1, 1, 1, 2, 2, 2, 0x80, 2}
	out := make([]byte, 3*4)
	if err := decodeZRLETile(bytes.NewReader(in), &pixelLevels[0], make([]byte, zrleScratchSize), out, 3, 1); err != nil {
		t.Fatalf("decodeZRLETile: %v", err)
	}
	for i := 0; i < 3; i++ {
		if r, g, b, a := rgbaAt(out, i); r != 1 || g != 1 || b != 1 || a != 0xFF {
			t.Errorf("pixel%d = %d,%d,%d,%d", i, r, g, b, a)
		}
	}
}

// The reduced pixel levels must decode a ZRLE tile to the right RGBA:
// expected values come from expanding each channel to 8 bits by hand.
func TestDecodeZRLETileReducedLevels(t *testing.T) {
	scratch := make([]byte, zrleScratchSize)

	// Level 1 (RGB565, little-endian CPIXEL): solid tile of 0xF800 = pure red.
	out := make([]byte, 2*1*4)
	if err := decodeZRLETile(bytes.NewReader([]byte{1, 0x00, 0xF8}), &pixelLevels[1], scratch, out, 2, 1); err != nil {
		t.Fatalf("level 1 solid: %v", err)
	}
	if want := []byte{255, 0, 0, 255, 255, 0, 0, 255}; !bytes.Equal(out, want) {
		t.Errorf("level 1 solid = %v, want %v", out, want)
	}

	// Level 1 raw: 0x07E0 = pure green, 0x001F = pure blue.
	if err := decodeZRLETile(bytes.NewReader([]byte{0, 0xE0, 0x07, 0x1F, 0x00}), &pixelLevels[1], scratch, out, 2, 1); err != nil {
		t.Fatalf("level 1 raw: %v", err)
	}
	if want := []byte{0, 255, 0, 255, 0, 0, 255, 255}; !bytes.Equal(out, want) {
		t.Errorf("level 1 raw = %v, want %v", out, want)
	}

	// Level 2 (RGB332, 1-byte CPIXEL) palette of {0xE0 red, 0x03 blue}, indices 0,1.
	if err := decodeZRLETile(bytes.NewReader([]byte{2, 0xE0, 0x03, 0b01000000}), &pixelLevels[2], scratch, out, 2, 1); err != nil {
		t.Fatalf("level 2 palette: %v", err)
	}
	if want := []byte{255, 0, 0, 255, 0, 0, 255, 255}; !bytes.Equal(out, want) {
		t.Errorf("level 2 palette = %v, want %v", out, want)
	}
}
