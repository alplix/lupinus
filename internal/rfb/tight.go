package rfb

import (
	"bytes"
	"compress/zlib"
	"image"
	"image/jpeg"
	"io"
	"os"
)

var debugTightWire = os.Getenv("RFB_DEBUG_TIGHT") != ""

// Tight encoding (a widely-deployed RFB extension — TigerVNC/TightVNC/
// RealVNC all support it, and it's often preferred over ZRLE for
// real-world workloads) — decoded from scratch against TigerVNC's own
// TightDecoder.cxx/TightConstants.h (fetched for reference while writing
// this, not guessed from memory) rather than a general spec document,
// since the exact bit-packing of the compression-control byte isn't
// written down anywhere more official than "what the reference
// implementations do".
//
// This client's full-fidelity pixel format (pixelLevels[0]: 32bpp/24-depth/
// true-colour, byte-aligned RGB — see pixfmt.go) is exactly Tight's "is888"
// fast path, so unlike TigerVNC's decoder this implementation doesn't need
// the generic 8/16bpp branches: truecolor pixel and palette entries alike
// are always the 3-byte TPIXEL form. That's why Tight is only advertised —
// and only ever decoded — at pixel level 0; the reduced-depth levels drop it
// from SetEncodings (see Client.encodingsMessage).
const (
	tightExplicitFilter = 0x04
	tightFill           = 0x08
	tightJpeg           = 0x09
	tightMaxSubencoding = 0x09

	tightFilterCopy     = 0x00
	tightFilterPalette  = 0x01
	tightFilterGradient = 0x02

	// Below this many bytes the server sends pixel data raw rather than
	// paying zlib's per-chunk overhead for a negligible payload.
	tightMinToCompress = 12
)

// tightZStream is one of a connection's 4 independent, persistent zlib
// decompressors (see Client.tightZlib) — the same chunkFeeder-backed
// long-lived-Reader technique decodeZRLE uses for its single stream,
// generalized to 4 with individual reset control.
type tightZStream struct {
	feeder chunkFeeder
	zr     io.ReadCloser
}

// reset discards the stream's state (dictionary/history), matching the
// server telling us "the next chunk on this stream starts a fresh zlib
// stream, don't try to continue the old one".
func (s *tightZStream) reset() { s.zr = nil }

func (s *tightZStream) read(chunk []byte, out []byte) error {
	s.feeder.feed(chunk)
	if s.zr == nil {
		zr, err := zlib.NewReader(&s.feeder)
		if err != nil {
			return err
		}
		s.zr = zr
	}
	_, err := io.ReadFull(s.zr, out)
	return err
}

func (c *Client) decodeTight(sink FramebufferSink, x, y, w, h int) error {
	compCtl, err := readUint8(c.r)
	if err != nil {
		return err
	}

	for i := 0; i < 4; i++ {
		if compCtl&(1<<uint(i)) != 0 {
			c.tightZlib[i].reset()
		}
	}
	sub := compCtl >> 4
	if sub > tightMaxSubencoding {
		return protoErrf("tight: unsupported subencoding 0x%x", sub)
	}
	if debugTightWire {
		println("tight: rect", x, y, w, h, "compCtl", int(compCtl), "sub", int(sub))
	}

	if sub == tightFill {
		rgb, err := readFull(c.r, 3)
		if err != nil {
			return err
		}
		buf := make([]byte, w*h*4)
		fillRGBA(buf, rgb)
		sink.Update(x, y, w, h, buf)
		return nil
	}

	if sub == tightJpeg {
		length, err := readTightCompactLength(c.r)
		if err != nil {
			return err
		}
		data, err := readFull(c.r, length)
		if err != nil {
			return err
		}
		img, err := jpeg.Decode(bytes.NewReader(data))
		if err != nil {
			return protoErrf("tight: JPEG decode: %v", err)
		}
		buf := make([]byte, w*h*4)
		rgbaFromImage(buf, img, w, h)
		sink.Update(x, y, w, h, buf)
		return nil
	}

	// "Basic" (zlib) compression, optionally with a Copy/Palette/Gradient
	// filter applied before compression — sub's low 2 bits select which
	// of the 4 persistent zlib streams this rectangle's data belongs to.
	streamID := int(sub & 0x03)
	palSize := 0
	var palette [][3]byte
	useGradient := false

	if sub&tightExplicitFilter != 0 {
		filterID, err := readUint8(c.r)
		if err != nil {
			return err
		}
		if debugTightWire {
			println("tight:   filterID", int(filterID))
		}
		switch filterID {
		case tightFilterPalette:
			n, err := readUint8(c.r)
			if err != nil {
				return err
			}
			palSize = int(n) + 1
			palette, err = readPalette(c.r, palSize)
			if err != nil {
				return err
			}
			if debugTightWire {
				println("tight:   palSize", palSize)
			}
		case tightFilterGradient:
			useGradient = true
		case tightFilterCopy:
			// no-op: the default, uncompressed-filter pixel layout
		default:
			return protoErrf("tight: invalid filter id %d", filterID)
		}
	}

	var rowSize int
	switch {
	case palSize != 0 && palSize <= 2:
		rowSize = (w + 7) / 8
	case palSize != 0:
		rowSize = w
	default:
		rowSize = w * 3
	}
	dataSize := h * rowSize

	var data []byte
	if dataSize < tightMinToCompress {
		data, err = readFull(c.r, dataSize)
		if err != nil {
			return err
		}
	} else {
		length, err := readTightCompactLength(c.r)
		if err != nil {
			return err
		}
		chunk, err := readFull(c.r, length)
		if err != nil {
			return err
		}
		data = make([]byte, dataSize)
		if err := c.tightZlib[streamID].read(chunk, data); err != nil {
			return err
		}
	}

	buf := make([]byte, w*h*4)
	switch {
	case palSize != 0:
		decodeTightPalette(data, palette, buf, w, h)
	case useGradient:
		decodeTightGradient(data, buf, w, h)
	default:
		for i := 0; i < w*h; i++ {
			copy(buf[i*4:i*4+3], data[i*3:i*3+3])
			buf[i*4+3] = 0xFF
		}
	}
	sink.Update(x, y, w, h, buf)
	return nil
}

// readTightCompactLength reads Tight's variable-length "compact length"
// (1-3 bytes: 7 data bits per byte, continuation via the top bit, except
// the 3rd byte which — uniquely — contributes all 8 bits, for a 22-bit
// maximum value).
func readTightCompactLength(r io.Reader) (int, error) {
	b0, err := readUint8(r)
	if err != nil {
		return 0, err
	}
	result := int(b0 & 0x7F)
	if b0&0x80 == 0 {
		return result, nil
	}
	b1, err := readUint8(r)
	if err != nil {
		return 0, err
	}
	result |= int(b1&0x7F) << 7
	if b1&0x80 == 0 {
		return result, nil
	}
	b2, err := readUint8(r)
	if err != nil {
		return 0, err
	}
	result |= int(b2) << 14
	return result, nil
}

func fillRGBA(buf []byte, rgb []byte) {
	for i := 0; i < len(buf); i += 4 {
		copy(buf[i:i+3], rgb)
		buf[i+3] = 0xFF
	}
}

func rgbaFromImage(out []byte, img image.Image, w, h int) {
	b := img.Bounds()
	for py := 0; py < h; py++ {
		for px := 0; px < w; px++ {
			r, g, bl, _ := img.At(b.Min.X+px, b.Min.Y+py).RGBA()
			off := (py*w + px) * 4
			out[off] = byte(r >> 8)
			out[off+1] = byte(g >> 8)
			out[off+2] = byte(bl >> 8)
			out[off+3] = 0xFF
		}
	}
}

// decodeTightPalette expands 1-bit (palSize<=2) or 1-byte (palSize<=256)
// palette indices into RGBA, matching TigerVNC's FilterPalette (the
// <=2-color case packs indices MSB-first within each byte).
func decodeTightPalette(in []byte, palette [][3]byte, out []byte, w, h int) {
	if len(palette) <= 2 {
		rowBytes := (w + 7) / 8
		for y := 0; y < h; y++ {
			row := in[y*rowBytes : y*rowBytes+rowBytes]
			for x := 0; x < w; x++ {
				bit := 7 - uint(x%8)
				idx := (row[x/8] >> bit) & 1
				off := (y*w + x) * 4
				copy(out[off:off+3], palette[idx][:])
				out[off+3] = 0xFF
			}
		}
		return
	}
	for i := 0; i < w*h; i++ {
		idx := in[i]
		off := i * 4
		copy(out[off:off+3], palette[idx][:])
		out[off+3] = 0xFF
	}
}

// decodeTightGradient reconstructs RGB pixel data compressed with Tight's
// gradient filter — a MED-like per-channel predictor (TigerVNC's
// FilterGradient24: predict from left+above-left+above, clamped to
// [0,255], then add the transmitted residual with natural uint8
// wraparound, matching the encoder's arithmetic exactly).
func decodeTightGradient(in []byte, out []byte, w, h int) {
	prevRow := make([]byte, w*3)
	thisRow := make([]byte, w*3)
	var pix [3]byte

	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			if x == 0 {
				for c := 0; c < 3; c++ {
					pix[c] = in[y*w*3+c] + prevRow[c]
					thisRow[c] = pix[c]
				}
			} else {
				var est [3]byte
				for c := 0; c < 3; c++ {
					e := int(prevRow[x*3+c]) + int(pix[c]) - int(prevRow[(x-1)*3+c])
					if e > 0xFF {
						e = 0xFF
					} else if e < 0 {
						e = 0
					}
					est[c] = byte(e)
				}
				for c := 0; c < 3; c++ {
					pix[c] = in[(y*w+x)*3+c] + est[c]
					thisRow[x*3+c] = pix[c]
				}
			}
			off := (y*w + x) * 4
			out[off] = pix[0]
			out[off+1] = pix[1]
			out[off+2] = pix[2]
			out[off+3] = 0xFF
		}
		copy(prevRow, thisRow)
	}
}
