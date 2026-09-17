package rfb

import (
	"fmt"
	"io"
	"os"
	"time"
)

// debugFBWire, gated by RFB_DEBUG_FB, prints one summary line per
// FramebufferUpdate: how many rectangles, which encodings they used, how
// many pixel bytes came over the wire, and how long the whole update took
// to read+decode — the numbers needed to tell "the network is just slow"
// apart from "this client is doing something inefficient" when someone
// reports laggy updates, without having to guess.
var debugFBWire = os.Getenv("RFB_DEBUG_FB") != ""

// handleFramebufferUpdate reads the rectangle count and dispatches each
// rectangle to the right decoder based on its encoding type. The
// msgFramebufferUpdate type byte itself has already been consumed by Run.
func (c *Client) handleFramebufferUpdate(sink FramebufferSink) error {
	start := time.Now()
	var roundTrip time.Duration
	if debugFBWire && !c.lastRequestSent.IsZero() {
		roundTrip = start.Sub(c.lastRequestSent)
	}
	if _, err := readFull(c.r, 1); err != nil { // padding
		return err
	}
	numRects, err := readUint16(c.r)
	if err != nil {
		return err
	}

	// Ask for the next update now, before decoding this one's rectangles
	// — not after, like a naive request/process/request-again loop would.
	// Mirrors TigerVNC's CConnection::framebufferUpdateStart. On a
	// high-latency link (a VNC session run over the open internet rather
	// than a LAN can easily see 100-300ms RTT) waiting until decode is
	// finished to ask for more serializes local decode time behind an
	// extra network round-trip on top of the unavoidable one, and the
	// server can't start capturing its next frame until the request
	// arrives — so every recoverable millisecond here directly shortens
	// the visible lag between doing something on the remote screen and
	// seeing it update.
	if err := c.requestUpdate(true, 0, 0, c.width, c.height); err != nil {
		return err
	}

	encCounts := map[int32]int{}
	pixelArea := 0

	for i := 0; i < int(numRects); i++ {
		x, err := readUint16(c.r)
		if err != nil {
			return err
		}
		y, err := readUint16(c.r)
		if err != nil {
			return err
		}
		w, err := readUint16(c.r)
		if err != nil {
			return err
		}
		h, err := readUint16(c.r)
		if err != nil {
			return err
		}
		rawEnc, err := readUint32(c.r)
		if err != nil {
			return err
		}
		enc := int32(rawEnc)
		encCounts[enc]++
		pixelArea += int(w) * int(h)

		switch enc {
		case EncodingRaw:
			if err := c.decodeRaw(sink, int(x), int(y), int(w), int(h)); err != nil {
				return err
			}
		case EncodingCopyRect:
			if err := c.decodeCopyRect(sink, int(x), int(y), int(w), int(h)); err != nil {
				return err
			}
		case EncodingZRLE:
			if err := c.decodeZRLE(sink, int(x), int(y), int(w), int(h)); err != nil {
				return err
			}
		case EncodingTight:
			if err := c.decodeTight(sink, int(x), int(y), int(w), int(h)); err != nil {
				return err
			}
		case EncodingCursor:
			if err := c.decodeCursor(sink, int(x), int(y), int(w), int(h)); err != nil {
				return err
			}
		case EncodingDesktopSize:
			c.width, c.height = int(w), int(h)
			sink.Resize(int(w), int(h))
		case EncodingExtendedDesktopSize:
			if err := c.decodeExtendedDesktopSize(sink, int(x), int(w), int(h)); err != nil {
				return err
			}
		default:
			return protoErrf("unsupported encoding %d", enc)
		}
	}

	if debugFBWire {
		fmt.Fprintf(os.Stderr, "DEBUG rfb update: rects=%d pixelArea=%d encodings=%v decodeTime=%s roundTrip=%s\n",
			numRects, pixelArea, encodingCounts(encCounts), time.Since(start), roundTrip)
	}

	return nil
}

// encodingCounts renders the per-encoding rectangle tally with names
// instead of raw numeric encoding IDs, for debugFBWire's log line.
func encodingCounts(counts map[int32]int) map[string]int {
	named := make(map[string]int, len(counts))
	for enc, n := range counts {
		named[encodingName(enc)] = n
	}
	return named
}

func encodingName(enc int32) string {
	switch enc {
	case EncodingRaw:
		return "Raw"
	case EncodingCopyRect:
		return "CopyRect"
	case EncodingZRLE:
		return "ZRLE"
	case EncodingTight:
		return "Tight"
	case EncodingCursor:
		return "Cursor"
	case EncodingDesktopSize:
		return "DesktopSize"
	case EncodingExtendedDesktopSize:
		return "ExtendedDesktopSize"
	default:
		return fmt.Sprintf("0x%x", uint32(enc))
	}
}

// fixAlpha overwrites every 4th byte (the unused padding channel in our
// requested pixel format) with 0xFF, turning raw wire pixel data into
// canvas-ready RGBA.
func fixAlpha(buf []byte) {
	for i := 3; i < len(buf); i += 4 {
		buf[i] = 0xFF
	}
}

func (c *Client) decodeRaw(sink FramebufferSink, x, y, w, h int) error {
	buf, err := readFull(c.r, w*h*4)
	if err != nil {
		return err
	}
	fixAlpha(buf)
	sink.Update(x, y, w, h, buf)
	return nil
}

func (c *Client) decodeCopyRect(sink FramebufferSink, x, y, w, h int) error {
	srcX, err := readUint16(c.r)
	if err != nil {
		return err
	}
	srcY, err := readUint16(c.r)
	if err != nil {
		return err
	}
	sink.CopyRect(x, y, w, h, int(srcX), int(srcY))
	return nil
}

func (c *Client) decodeExtendedDesktopSize(sink FramebufferSink, screenCount, w, h int) error {
	// Screen layout data (16 bytes/screen) isn't used by Lupinus v0.1.0
	// (no multi-monitor layout UI yet), but it must still be consumed so
	// the stream doesn't desync.
	if _, err := readFull(c.r, screenCount*16); err != nil {
		return err
	}
	c.width, c.height = w, h
	sink.Resize(w, h)
	return nil
}

func (c *Client) decodeCursor(sink FramebufferSink, hotX, hotY, w, h int) error {
	if w == 0 || h == 0 {
		sink.Cursor(hotX, hotY, 0, 0, nil)
		return nil
	}

	pixels, err := readFull(c.r, w*h*4)
	if err != nil {
		return err
	}
	fixAlpha(pixels)

	rowBytes := (w + 7) / 8
	mask, err := readFull(c.r, rowBytes*h)
	if err != nil {
		return err
	}

	for py := 0; py < h; py++ {
		for px := 0; px < w; px++ {
			maskByte := mask[py*rowBytes+px/8]
			bit := byte(0x80) >> uint(px%8)
			if maskByte&bit == 0 {
				pixels[(py*w+px)*4+3] = 0
			}
		}
	}

	sink.Cursor(hotX, hotY, w, h, pixels)
	return nil
}

// --- ZRLE (RFC 6143 §7.7.4) ---

const zrleTileSize = 64

func (c *Client) decodeZRLE(sink FramebufferSink, x, y, w, h int) error {
	length, err := readUint32(c.r)
	if err != nil {
		return err
	}
	chunk, err := readFull(c.r, int(length))
	if err != nil {
		return err
	}
	c.zrFeeder.feed(chunk)

	zr, err := c.zlibReader()
	if err != nil {
		return err
	}

	tile := make([]byte, zrleTileSize*zrleTileSize*4)

	for ty := 0; ty < h; ty += zrleTileSize {
		tileH := min(zrleTileSize, h-ty)
		for tx := 0; tx < w; tx += zrleTileSize {
			tileW := min(zrleTileSize, w-tx)
			buf := tile[:tileW*tileH*4]
			if err := decodeZRLETile(zr, buf, tileW, tileH); err != nil {
				return err
			}
			sink.Update(x+tx, y+ty, tileW, tileH, buf)
		}
	}
	return nil
}

func decodeZRLETile(r io.Reader, out []byte, w, h int) error {
	subEnc, err := readUint8(r)
	if err != nil {
		return err
	}

	switch {
	case subEnc == 0: // Raw
		cpixels, err := readFull(r, w*h*3)
		if err != nil {
			return err
		}
		for i := 0; i < w*h; i++ {
			copy(out[i*4:i*4+3], cpixels[i*3:i*3+3])
			out[i*4+3] = 0xFF
		}

	case subEnc == 1: // Solid
		cpixel, err := readFull(r, 3)
		if err != nil {
			return err
		}
		for i := 0; i < w*h; i++ {
			copy(out[i*4:i*4+3], cpixel)
			out[i*4+3] = 0xFF
		}

	case subEnc >= 2 && subEnc <= 16: // Packed palette
		paletteSize := int(subEnc)
		palette, err := readPalette(r, paletteSize)
		if err != nil {
			return err
		}
		bits := paletteIndexBits(paletteSize)
		rowBytes := (w*bits + 7) / 8
		for row := 0; row < h; row++ {
			rowData, err := readFull(r, rowBytes)
			if err != nil {
				return err
			}
			for col := 0; col < w; col++ {
				idx := extractPackedIndex(rowData, col, bits)
				if idx >= len(palette) {
					return protoErrf("zrle: palette index %d out of range (size %d)", idx, paletteSize)
				}
				off := (row*w + col) * 4
				copy(out[off:off+3], palette[idx][:])
				out[off+3] = 0xFF
			}
		}

	case subEnc == 128: // Plain RLE
		count := 0
		total := w * h
		for count < total {
			cpixel, err := readFull(r, 3)
			if err != nil {
				return err
			}
			runLen, err := readRLELength(r)
			if err != nil {
				return err
			}
			if count+runLen > total {
				return protoErrf("zrle: plain RLE run overflows tile")
			}
			for i := 0; i < runLen; i++ {
				off := (count + i) * 4
				copy(out[off:off+3], cpixel)
				out[off+3] = 0xFF
			}
			count += runLen
		}

	case subEnc >= 130: // Palette RLE
		paletteSize := int(subEnc) - 128
		palette, err := readPalette(r, paletteSize)
		if err != nil {
			return err
		}
		count := 0
		total := w * h
		for count < total {
			b, err := readUint8(r)
			if err != nil {
				return err
			}
			var idx, runLen int
			if b&0x80 != 0 {
				idx = int(b & 0x7F)
				runLen, err = readRLELength(r)
				if err != nil {
					return err
				}
			} else {
				idx = int(b)
				runLen = 1
			}
			if idx >= len(palette) {
				return protoErrf("zrle: palette RLE index %d out of range (size %d)", idx, paletteSize)
			}
			if count+runLen > total {
				return protoErrf("zrle: palette RLE run overflows tile")
			}
			for i := 0; i < runLen; i++ {
				off := (count + i) * 4
				copy(out[off:off+3], palette[idx][:])
				out[off+3] = 0xFF
			}
			count += runLen
		}

	default:
		return protoErrf("zrle: unsupported tile subencoding %d", subEnc)
	}

	return nil
}

func readPalette(r io.Reader, size int) ([][3]byte, error) {
	raw, err := readFull(r, size*3)
	if err != nil {
		return nil, err
	}
	palette := make([][3]byte, size)
	for i := range palette {
		copy(palette[i][:], raw[i*3:i*3+3])
	}
	return palette, nil
}

func paletteIndexBits(paletteSize int) int {
	switch {
	case paletteSize <= 2:
		return 1
	case paletteSize <= 4:
		return 2
	default:
		return 4
	}
}

// extractPackedIndex reads the col-th packed index (bits-per-index wide,
// MSB-first) from a row of packed pixel data.
func extractPackedIndex(row []byte, col, bits int) int {
	bitOffset := col * bits
	byteIdx := bitOffset / 8
	shift := 8 - bits - (bitOffset % 8)
	mask := byte(1<<uint(bits) - 1)
	return int((row[byteIdx] >> uint(shift)) & mask)
}

// readRLELength decodes a ZRLE/Hextile-style run length: a sequence of
// 0xFF bytes each contributing 255, terminated by a final byte < 255 which
// contributes its own value; the total is 1 + sum of all bytes read.
func readRLELength(r io.Reader) (int, error) {
	total := 1
	for {
		b, err := readUint8(r)
		if err != nil {
			return 0, err
		}
		total += int(b)
		if b != 255 {
			return total, nil
		}
	}
}
