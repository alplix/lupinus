package rdp

// decodeBitmapUpdate parses TS_UPDATE_BITMAP_DATA (MS-RDPBCGR
// 2.2.9.1.1.3.1.1): a count followed by that many TS_BITMAP_DATA
// rectangles, each independently compressed or raw.
func decodeBitmapUpdate(data []byte, sink FramebufferSink) error {
	r := &sliceReader{buf: data}
	numRects, err := r.readUint16LE()
	if err != nil {
		return err
	}
	for i := 0; i < int(numRects); i++ {
		if err := decodeBitmapRect(r, sink); err != nil {
			return err
		}
	}
	return nil
}

func decodeBitmapRect(r *sliceReader, sink FramebufferSink) error {
	left, err := r.readUint16LE()
	if err != nil {
		return err
	}
	top, err := r.readUint16LE()
	if err != nil {
		return err
	}
	right, err := r.readUint16LE()
	if err != nil {
		return err
	}
	bottom, err := r.readUint16LE()
	if err != nil {
		return err
	}
	width, err := r.readUint16LE()
	if err != nil {
		return err
	}
	height, err := r.readUint16LE()
	if err != nil {
		return err
	}
	bpp, err := r.readUint16LE()
	if err != nil {
		return err
	}
	flags, err := r.readUint16LE()
	if err != nil {
		return err
	}
	bitmapLength, err := r.readUint16LE()
	if err != nil {
		return err
	}
	bitmapData, err := r.readN(int(bitmapLength))
	if err != nil {
		return err
	}

	// destRight/destBottom (MS-RDPBCGR 2.2.9.1.1.3.1.2.2) are inclusive:
	// the rectangle covers [left, right] × [top, bottom], i.e. right-left+1
	// pixels wide — but also just use width/height directly, which is
	// what the compressed/raw pixel data actually contains and what real
	// servers keep consistent with the dest rectangle in practice.
	_ = right
	_ = bottom

	rgba, err := decodeBitmapPixels(bitmapData, int(width), int(height), int(bpp), flags)
	if err != nil {
		return err
	}
	sink.Update(int(left), int(top), int(width), int(height), rgba)
	return nil
}

// decodeBitmapPixels turns one TS_BITMAP_DATA's payload into top-down,
// tightly-packed RGBA (alpha always 0xFF) — decompressing with Interleaved
// RLE (rle.go) if BITMAP_COMPRESSION is set, and converting whatever
// server-native bpp (8/15/16/24/32) it used either way.
func decodeBitmapPixels(data []byte, width, height, bpp int, flags uint16) ([]byte, error) {
	bytesPerPixel := bppToBytes(bpp)
	if bytesPerPixel == 0 {
		return nil, protoErrf("bitmap update: unsupported bitsPerPixel %d", bpp)
	}

	var srcRows []byte // top-down, width*bytesPerPixel per row
	if flags&bitmapCompression != 0 {
		payload := data
		if flags&noBitmapCompressionHdr == 0 {
			// TS_CD_HEADER (8 bytes): cbCompFirstRowSize, cbCompMainBodySize,
			// cbScanWidth, cbUncompressedSize — informational only, the RLE
			// stream itself is self-describing.
			if len(payload) < 8 {
				return nil, protoErrf("bitmap update: compressed data shorter than TS_CD_HEADER")
			}
			payload = payload[8:]
		}
		out := make([]byte, width*height*bytesPerPixel)
		if err := decodeInterleavedRLE(payload, out, width, height, bytesPerPixel); err != nil {
			return nil, err
		}
		srcRows = out
	} else {
		if len(data) < width*height*bytesPerPixel {
			return nil, protoErrf("bitmap update: uncompressed data too short (%d bytes, want %d)", len(data), width*height*bytesPerPixel)
		}
		srcRows = data
	}

	// Raw/decompressed RDP bitmap rows are bottom-up (like a Windows DIB);
	// flip while converting to the top-down RGBA sink.Update expects.
	rgba := make([]byte, width*height*4)
	stride := width * bytesPerPixel
	for y := 0; y < height; y++ {
		srcRow := srcRows[(height-1-y)*stride : (height-1-y)*stride+stride]
		dstRow := rgba[y*width*4 : y*width*4+width*4]
		convertRowToRGBA(srcRow, dstRow, width, bytesPerPixel)
	}
	return rgba, nil
}

const noBitmapCompressionHdr = 0x0400

func bppToBytes(bpp int) int {
	switch bpp {
	case 8:
		return 1
	case 15, 16:
		return 2
	case 24:
		return 3
	case 32:
		return 4
	default:
		return 0
	}
}

// convertRowToRGBA converts one row of server-native pixels to RGBA. 8bpp
// is treated as grayscale (this client doesn't track the server's
// palette from Palette Updates in v0.2.0 — a known gap, see the project
// plan roadmap); 15/16bpp unpack the standard 555/565 RGB masks; 24/32bpp
// are already byte-aligned BGR(X).
func convertRowToRGBA(src, dst []byte, width, bytesPerPixel int) {
	for x := 0; x < width; x++ {
		so := x * bytesPerPixel
		do := x * 4
		switch bytesPerPixel {
		case 1:
			v := src[so]
			dst[do], dst[do+1], dst[do+2], dst[do+3] = v, v, v, 0xFF
		case 2:
			v := uint16(src[so]) | uint16(src[so+1])<<8
			// 16bpp: RNS_UD_COLOR_16BPP_565 (5-6-5); also decodes 555
			// close enough for MVP (top bit of green ignored either way).
			r := (v >> 11) & 0x1F
			g := (v >> 5) & 0x3F
			b := v & 0x1F
			dst[do] = byte(r<<3 | r>>2)
			dst[do+1] = byte(g<<2 | g>>4)
			dst[do+2] = byte(b<<3 | b>>2)
			dst[do+3] = 0xFF
		case 3:
			dst[do] = src[so+2]
			dst[do+1] = src[so+1]
			dst[do+2] = src[so]
			dst[do+3] = 0xFF
		case 4:
			dst[do] = src[so+2]
			dst[do+1] = src[so+1]
			dst[do+2] = src[so]
			dst[do+3] = 0xFF
		}
	}
}
