package rfb

// pixelLevel is one of the pixel formats this client can ask the server for,
// plus what the decoders need to turn that wire format into canvas-ready
// RGBA. Level 0 is always the full-fidelity 32bpp format; higher levels trade
// colour depth for fewer bytes on the wire — the one lever that helps when
// a server (macOS Screen Sharing, notably) only ever sends lossless ZRLE
// and a slow link, not the client, is what makes each frame take seconds.
type pixelLevel struct {
	pf PixelFormat
	// bpp is bytes per pixel in Raw rectangles; cpixel is bytes per ZRLE
	// "compressed pixel" (RFC 6143 §7.7.5 — 3 rather than 4 for our 32bpp
	// depth-24 format, otherwise the same as bpp).
	bpp, cpixel int
	// lut maps a wire pixel value to an RGBA word (R in the low byte, alpha
	// 0xFF in the high byte — i.e. the bytes land in canvas RGBA order when
	// stored little-endian). Nil at level 0, which is decoded directly.
	lut []uint32
}

// word converts the wire pixel at the start of b to an RGBA word. Works for
// both Raw pixels and ZRLE CPIXELs, since level 0 only ever reads the first
// three bytes of either.
func (l *pixelLevel) word(b []byte) uint32 {
	switch l.cpixel {
	case 3:
		return uint32(b[0]) | uint32(b[1])<<8 | uint32(b[2])<<16 | 0xFF000000
	case 2:
		return l.lut[uint16(b[0])|uint16(b[1])<<8]
	default:
		return l.lut[b[0]]
	}
}

var pixelLevels = [...]pixelLevel{
	// 32bpp, depth 24, little-endian, R at byte 0 / G at byte 1 / B at byte
	// 2 on the wire. That byte order is exactly RGB (plus an unused 4th byte
	// overwritten with alpha=255), so decoded Raw pixel data can be handed to
	// an HTML canvas ImageData buffer with no channel shuffling.
	{
		pf: PixelFormat{
			BitsPerPixel: 32, Depth: 24, TrueColor: 1,
			RedMax: 255, GreenMax: 255, BlueMax: 255,
			RedShift: 0, GreenShift: 8, BlueShift: 16,
		},
		bpp: 4, cpixel: 3,
	},
	// RGB565: half the bytes, visibly fine for a desktop.
	{
		pf: PixelFormat{
			BitsPerPixel: 16, Depth: 16, TrueColor: 1,
			RedMax: 31, GreenMax: 63, BlueMax: 31,
			RedShift: 11, GreenShift: 5, BlueShift: 0,
		},
		bpp: 2, cpixel: 2,
		lut: buildLUT(16, 31, 63, 31, 11, 5, 0),
	},
	// RGB332: a quarter of the bytes; banded gradients, but interactive.
	{
		pf: PixelFormat{
			BitsPerPixel: 8, Depth: 8, TrueColor: 1,
			RedMax: 7, GreenMax: 7, BlueMax: 3,
			RedShift: 5, GreenShift: 2, BlueShift: 0,
		},
		bpp: 1, cpixel: 1,
		lut: buildLUT(8, 7, 7, 3, 5, 2, 0),
	},
}

func buildLUT(bits int, rMax, gMax, bMax uint32, rShift, gShift, bShift uint) []uint32 {
	lut := make([]uint32, 1<<bits)
	for v := range lut {
		r := (uint32(v) >> rShift & rMax) * 255 / rMax
		g := (uint32(v) >> gShift & gMax) * 255 / gMax
		b := (uint32(v) >> bShift & bMax) * 255 / bMax
		lut[v] = r | g<<8 | b<<16 | 0xFF000000
	}
	return lut
}

// pixelMessage builds the SetPixelFormat message asking for level l.
func pixelMessage(l *pixelLevel) []byte {
	pf := l.pf
	buf := make([]byte, 20)
	buf[0] = msgSetPixelFormat
	buf[4] = pf.BitsPerPixel
	buf[5] = pf.Depth
	buf[6] = pf.BigEndian
	buf[7] = pf.TrueColor
	buf[8] = byte(pf.RedMax >> 8)
	buf[9] = byte(pf.RedMax)
	buf[10] = byte(pf.GreenMax >> 8)
	buf[11] = byte(pf.GreenMax)
	buf[12] = byte(pf.BlueMax >> 8)
	buf[13] = byte(pf.BlueMax)
	buf[14] = pf.RedShift
	buf[15] = pf.GreenShift
	buf[16] = pf.BlueShift
	return buf
}
