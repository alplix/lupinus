package rdp

// Interleaved RLE decompression (MS-RDPBCGR 3.1.9.1 / [MS-RDPEGDI] and
// friends) — the mandatory baseline bitmap codec every RDP server
// supports. This is a direct, line-for-line port of FreeRDP's
// RLEDECOMPRESS algorithm (libfreerdp/codec/include/bitmap.h, Apache-2.0),
// unified over one Go implementation parameterized by bytesPerPixel (1, 2
// or 3 — the only widths Interleaved RLE is defined for; 32bpp bitmaps use
// a different codec, out of scope, see the project plan) instead of C's
// macro-generated per-width specializations.

const (
	regularBGRun         = 0x00
	megaMegaBGRun        = 0xF0
	regularFGRun         = 0x01
	megaMegaFGRun        = 0xF1
	liteSetFGFGRun       = 0x0C
	megaMegaSetFGRun     = 0xF6
	liteDitheredRun      = 0x0E
	megaMegaDitheredRun  = 0xF8
	regularColorRun      = 0x03
	megaMegaColorRun     = 0xF3
	regularFGBGImage     = 0x02
	megaMegaFGBGImage    = 0xF2
	liteSetFGFGBGImage   = 0x0D
	megaMegaSetFGBGImage = 0xF7
	regularColorImage    = 0x04
	megaMegaColorImage   = 0xF4
	specialFGBG1         = 0xF9
	specialFGBG2         = 0xFA
	specialWhite         = 0xFD
	specialBlack         = 0xFE

	maskRegularRunLength = 0x1F
	maskLiteRunLength    = 0x0F
	maskSpecialFGBG1     = 0x03
	maskSpecialFGBG2     = 0x05
)

func extractCodeID(b byte) uint32 {
	if b&0xC0 != 0xC0 {
		return uint32(b >> 5)
	}
	if b&0xF0 == 0xF0 {
		return uint32(b)
	}
	return uint32(b >> 4)
}

// extractRunLength returns the run length for code, plus how many bytes
// of the order header (including the leading code byte) it consumed. A
// return of (0, 0) means the source was exhausted mid-header.
func extractRunLength(code uint32, src []byte) (runLength, advance int) {
	if len(src) == 0 {
		return 0, 0
	}
	switch code {
	case regularFGBGImage:
		rl := int(src[0] & maskRegularRunLength)
		if rl == 0 {
			if len(src) < 2 {
				return 0, 0
			}
			return int(src[1]) + 1, 2
		}
		return rl * 8, 1
	case liteSetFGFGBGImage:
		rl := int(src[0] & maskLiteRunLength)
		if rl == 0 {
			if len(src) < 2 {
				return 0, 0
			}
			return int(src[1]) + 1, 2
		}
		return rl * 8, 1
	case regularBGRun, regularFGRun, regularColorRun, regularColorImage:
		rl := int(src[0] & maskRegularRunLength)
		if rl == 0 {
			if len(src) < 2 {
				return 0, 0
			}
			return int(src[1]) + 32, 2
		}
		return rl, 1
	case liteSetFGFGRun, liteDitheredRun:
		rl := int(src[0] & maskLiteRunLength)
		if rl == 0 {
			if len(src) < 2 {
				return 0, 0
			}
			return int(src[1]) + 16, 2
		}
		return rl, 1
	case megaMegaBGRun, megaMegaFGRun, megaMegaSetFGRun, megaMegaDitheredRun,
		megaMegaColorRun, megaMegaFGBGImage, megaMegaSetFGBGImage, megaMegaColorImage:
		if len(src) < 3 {
			return 0, 0
		}
		return int(src[1]) | int(src[2])<<8, 3
	default:
		return 0, 0
	}
}

func whitePixel(bpp int) uint32 {
	switch bpp {
	case 1:
		return 0xFF
	case 2:
		return 0xFFFF
	default:
		return 0xFFFFFF
	}
}

func readPixel(buf []byte, bpp int) uint32 {
	switch bpp {
	case 1:
		return uint32(buf[0])
	case 2:
		return uint32(buf[0]) | uint32(buf[1])<<8
	default: // 3
		return uint32(buf[0]) | uint32(buf[1])<<8 | uint32(buf[2])<<16
	}
}

func writePixel(buf []byte, v uint32, bpp int) {
	buf[0] = byte(v)
	if bpp >= 2 {
		buf[1] = byte(v >> 8)
	}
	if bpp >= 3 {
		buf[2] = byte(v >> 16)
	}
}

// decodeInterleavedRLE decompresses src into out (width*height*bpp bytes,
// row-major in decode order — top row first as the stream describes it;
// bitmap.go treats that as the bottom-up DIB convention and flips rows
// when converting to the caller's top-down RGBA buffer).
func decodeInterleavedRLE(src []byte, out []byte, width, height, bpp int) error {
	rowDelta := width * bpp
	if rowDelta == 0 || len(out) < rowDelta*height {
		return protoErrf("interleaved RLE: invalid output buffer (%dx%d @ %d bytes/px)", width, height, bpp)
	}

	destPos := 0
	destEnd := rowDelta * height
	srcPos := 0
	fgPel := whitePixel(bpp)
	insertFgPel := false
	firstLine := true

	// helpers bound to this call's out/bpp/rowDelta
	readAbove := func(pos int) uint32 { return readPixel(out[pos-rowDelta:], bpp) }
	write := func(v uint32) { writePixel(out[destPos:], v, bpp); destPos += bpp }
	ensure := func(n int) bool { return destPos+n*bpp <= destEnd }

	writeFGBGImage := func(bitmask byte, cBits int, useAbove bool) bool {
		if !ensure(cBits) {
			return false
		}
		mask := byte(0x01)
		for i := 0; i < cBits; i++ {
			var data uint32
			if useAbove {
				xor := readAbove(destPos)
				if bitmask&mask != 0 {
					data = xor ^ fgPel
				} else {
					data = xor
				}
			} else {
				if bitmask&mask != 0 {
					data = fgPel
				} else {
					data = 0
				}
			}
			write(data)
			mask <<= 1
		}
		return true
	}

	for srcPos < len(src) {
		if firstLine && destPos >= rowDelta {
			firstLine = false
			insertFgPel = false
		}

		code := extractCodeID(src[srcPos])

		if code == regularBGRun || code == megaMegaBGRun {
			runLength, advance := extractRunLength(code, src[srcPos:])
			if advance == 0 {
				return protoErrf("interleaved RLE: truncated BG run header")
			}
			srcPos += advance

			if firstLine {
				if insertFgPel {
					if !ensure(1) {
						return protoErrf("interleaved RLE: output overflow (BG run, first line)")
					}
					write(fgPel)
					runLength--
				}
				if !ensure(runLength) {
					return protoErrf("interleaved RLE: output overflow (BG run, first line)")
				}
				for i := 0; i < runLength; i++ {
					write(0)
				}
			} else {
				if insertFgPel {
					v := readAbove(destPos) ^ fgPel
					if !ensure(1) {
						return protoErrf("interleaved RLE: output overflow (BG run)")
					}
					write(v)
					runLength--
				}
				if !ensure(runLength) {
					return protoErrf("interleaved RLE: output overflow (BG run)")
				}
				for i := 0; i < runLength; i++ {
					write(readAbove(destPos))
				}
			}
			insertFgPel = true
			continue
		}

		insertFgPel = false

		switch code {
		case regularFGRun, megaMegaFGRun, liteSetFGFGRun, megaMegaSetFGRun:
			runLength, advance := extractRunLength(code, src[srcPos:])
			if advance == 0 {
				return protoErrf("interleaved RLE: truncated FG run header")
			}
			srcPos += advance
			if code == liteSetFGFGRun || code == megaMegaSetFGRun {
				if srcPos+bpp > len(src) {
					return protoErrf("interleaved RLE: truncated FG pixel")
				}
				fgPel = readPixel(src[srcPos:], bpp)
				srcPos += bpp
			}
			if !ensure(runLength) {
				return protoErrf("interleaved RLE: output overflow (FG run)")
			}
			if firstLine {
				for i := 0; i < runLength; i++ {
					write(fgPel)
				}
			} else {
				for i := 0; i < runLength; i++ {
					write(readAbove(destPos) ^ fgPel)
				}
			}

		case liteDitheredRun, megaMegaDitheredRun:
			runLength, advance := extractRunLength(code, src[srcPos:])
			if advance == 0 {
				return protoErrf("interleaved RLE: truncated dithered run header")
			}
			srcPos += advance
			if srcPos+2*bpp > len(src) {
				return protoErrf("interleaved RLE: truncated dithered pixels")
			}
			pixelA := readPixel(src[srcPos:], bpp)
			srcPos += bpp
			pixelB := readPixel(src[srcPos:], bpp)
			srcPos += bpp
			if !ensure(runLength * 2) {
				return protoErrf("interleaved RLE: output overflow (dithered run)")
			}
			for i := 0; i < runLength; i++ {
				write(pixelA)
				write(pixelB)
			}

		case regularColorRun, megaMegaColorRun:
			runLength, advance := extractRunLength(code, src[srcPos:])
			if advance == 0 {
				return protoErrf("interleaved RLE: truncated color run header")
			}
			srcPos += advance
			if srcPos+bpp > len(src) {
				return protoErrf("interleaved RLE: truncated color run pixel")
			}
			pixelA := readPixel(src[srcPos:], bpp)
			srcPos += bpp
			if !ensure(runLength) {
				return protoErrf("interleaved RLE: output overflow (color run)")
			}
			for i := 0; i < runLength; i++ {
				write(pixelA)
			}

		case regularFGBGImage, megaMegaFGBGImage, liteSetFGFGBGImage, megaMegaSetFGBGImage:
			runLength, advance := extractRunLength(code, src[srcPos:])
			if advance == 0 {
				return protoErrf("interleaved RLE: truncated FGBG image header")
			}
			srcPos += advance
			if code == liteSetFGFGBGImage || code == megaMegaSetFGBGImage {
				if srcPos+bpp > len(src) {
					return protoErrf("interleaved RLE: truncated FGBG fg pixel")
				}
				fgPel = readPixel(src[srcPos:], bpp)
				srcPos += bpp
			}
			for runLength > 8 {
				if srcPos+1 > len(src) {
					return protoErrf("interleaved RLE: truncated FGBG bitmask")
				}
				bitmask := src[srcPos]
				srcPos++
				if !writeFGBGImage(bitmask, 8, !firstLine) {
					return protoErrf("interleaved RLE: output overflow (FGBG image)")
				}
				runLength -= 8
			}
			if runLength > 0 {
				if srcPos+1 > len(src) {
					return protoErrf("interleaved RLE: truncated FGBG bitmask")
				}
				bitmask := src[srcPos]
				srcPos++
				if !writeFGBGImage(bitmask, runLength, !firstLine) {
					return protoErrf("interleaved RLE: output overflow (FGBG image)")
				}
			}

		case regularColorImage, megaMegaColorImage:
			runLength, advance := extractRunLength(code, src[srcPos:])
			if advance == 0 {
				return protoErrf("interleaved RLE: truncated color image header")
			}
			srcPos += advance
			if !ensure(runLength) || srcPos+runLength*bpp > len(src) {
				return protoErrf("interleaved RLE: color image overflow")
			}
			for i := 0; i < runLength; i++ {
				write(readPixel(src[srcPos:], bpp))
				srcPos += bpp
			}

		case specialFGBG1:
			srcPos++
			if !writeFGBGImage(maskSpecialFGBG1, 8, !firstLine) {
				return protoErrf("interleaved RLE: output overflow (special FGBG1)")
			}

		case specialFGBG2:
			srcPos++
			if !writeFGBGImage(maskSpecialFGBG2, 8, !firstLine) {
				return protoErrf("interleaved RLE: output overflow (special FGBG2)")
			}

		case specialWhite:
			srcPos++
			if !ensure(1) {
				return protoErrf("interleaved RLE: output overflow (white)")
			}
			write(whitePixel(bpp))

		case specialBlack:
			srcPos++
			if !ensure(1) {
				return protoErrf("interleaved RLE: output overflow (black)")
			}
			write(0)

		default:
			return protoErrf("interleaved RLE: invalid code 0x%02x at offset %d", code, srcPos)
		}
	}

	return nil
}
