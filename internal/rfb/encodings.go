package rfb

import (
	"encoding/binary"
	"fmt"
	"io"
	"os"
	"strconv"
	"time"
)

// debugFBWire, gated by RFB_DEBUG_FB, prints one summary line per
// FramebufferUpdate: how many rectangles, which encodings they used, how
// many pixel bytes came over the wire, and how long the whole update took
// to read+decode — the numbers needed to tell "the network is just slow"
// apart from "this client is doing something inefficient" when someone
// reports laggy updates, without having to guess.
var debugFBWire = os.Getenv("RFB_DEBUG_FB") != ""

// Test/diagnosis knobs, only ever read from the environment: RFB_DEBUG_ZRLE
// stops advertising Tight so a server that would pick it (TigerVNC) falls
// back to ZRLE like macOS Screen Sharing does; RFB_DEBUG_LEVEL=N starts the
// session at pixel level N (see pixfmt.go) instead of 0.
var (
	debugForceZRLE  = os.Getenv("RFB_DEBUG_ZRLE") != ""
	debugStartLevel = func() int {
		n, err := strconv.Atoi(os.Getenv("RFB_DEBUG_LEVEL"))
		if err != nil || n < 0 || n >= len(pixelLevels) {
			return 0
		}
		return n
	}()
)

// timedSink wraps the real sink to measure how long the update spent inside
// it. That time is downstream backpressure (the WebSocket write blocking on
// a slow renderer), not network transfer, so the adaptive logic in
// noteUpdate has to subtract it before deciding the link is slow.
type timedSink struct {
	FramebufferSink
	spent time.Duration
}

func (s *timedSink) Update(x, y, w, h int, rgba []byte) {
	t := time.Now()
	s.FramebufferSink.Update(x, y, w, h, rgba)
	s.spent += time.Since(t)
}

// handleFramebufferUpdate reads the rectangle count and dispatches each
// rectangle to the right decoder based on its encoding type. The
// msgFramebufferUpdate type byte itself has already been consumed by Run.
func (c *Client) handleFramebufferUpdate(sink *timedSink) error {
	start := time.Now()
	startBytes, startWait := c.bytesIn.Load(), c.readWait.Load()
	sink.spent = 0
	c.nUpdates.Add(1)
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
	// (mirrors TigerVNC's CConnection::framebufferUpdateStart) — but only
	// while updates are small and quick. On a high-latency link that
	// overlaps the request's trip with local decode, which is a real win for
	// the many small updates of normal desktop use. When updates are big
	// (a server like macOS Screen Sharing sends the whole screen for the
	// smallest change), pipelining is actively harmful on a bandwidth-bound
	// link: the next full frame starts streaming behind this one, so the
	// echo of a keystroke sits in the pipe behind up to a whole extra frame
	// of data. Then we wait until this one is done — see the end of this
	// function.
	pipelined := c.lastFast
	if pipelined {
		if err := c.sendNextRequest(); err != nil {
			return err
		}
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
			if c.level != 0 {
				return protoErrf("tight rectangle at reduced pixel level %d", c.level)
			}
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

	total := time.Since(start)
	wireBytes := c.bytesIn.Load() - startBytes
	waited := time.Duration(c.readWait.Load() - startWait)
	c.noteUpdate(total-sink.spent, waited, wireBytes, pixelArea, encCounts[EncodingTight] > 0)

	if !pipelined {
		if err := c.sendNextRequest(); err != nil {
			return err
		}
	}
	// Everything the server sends from here on uses whatever pixel format
	// we've most recently asked for: sendNextRequest only ever changes it
	// while no update request is outstanding, so this update was the last
	// one encoded in the old format.
	c.level = c.sentLevel

	if debugFBWire {
		fmt.Fprintf(os.Stderr, "DEBUG rfb update: rects=%d pixelArea=%d encodings=%v level=%d wire=%dKB total=%s sink=%s roundTrip=%s pipelined=%v\n",
			numRects, pixelArea, encodingCounts(encCounts), c.level, wireBytes/1024, total, sink.spent, roundTrip, pipelined)
	}

	return nil
}

// Adaptive behavior. Two independent mechanisms, both automatic:
//
// Pipelining: an update is "big" when it moved at least bigUpdateBytes and
// its network+decode time (excluding time blocked in the sink) reached
// bigUpdateTime; after a big update the next request waits until the update
// is fully read instead of being sent at its start.
//
// Pixel level (colour depth): colour depth is the one thing that shrinks the
// output of a server that only ever sends lossless data (macOS Screen
// Sharing sends the whole screen for the smallest change), so the client
// measures the link's throughput and the size of a full-screen frame, and
// picks the best-looking level whose predicted full-frame time is within
// frameTarget — moving down when the link can't keep up and back up when it
// can. No setting is involved; the "quality" preset just pins level 0.
const (
	bigUpdateBytes = 200 << 10
	bigUpdateTime  = 250 * time.Millisecond

	// Throughput is sampled from updates at least this large (smaller ones
	// are dominated by latency, not bandwidth); a full-screen frame size is
	// sampled from updates covering at least a quarter of the screen.
	tputSampleBytes = 128 << 10
	frameSampleMin  = 64 << 10

	// After asking for a new pixel format, wait this many updates before
	// judging again: the first one or two still reflect the old format.
	adaptCooldown = 3
)

// Unvisited pixel levels are estimated from a visited one, deliberately
// pessimistically in both directions since real ratios depend heavily on the
// content (smooth gradients shrink far more at 8-bit than 24->16 suggests):
// dropping a level is assumed to save only levelShrink of the bytes, and
// climbing one to cost levelGrow times as many.
const (
	levelShrink = 0.6
	levelGrow   = 4.0
)

// frameTarget is how long a full-screen frame may take on the wire before
// the pixel level is reduced. Zero disables adaptation (the "quality"
// preset).
func frameTarget(preset string) time.Duration {
	switch preset {
	case "quality":
		return 0
	case "bandwidth":
		return 200 * time.Millisecond
	default:
		return 400 * time.Millisecond
	}
}

func ewma(prev, sample float64) float64 {
	if prev == 0 {
		return sample
	}
	return 0.7*prev + 0.3*sample
}

// noteUpdate feeds one finished update into the adaptive logic. busy is its
// whole duration excluding time blocked in the sink (network + decode);
// waited is the part of that spent blocked reading the wire — the link and
// the server, which colour depth can affect, unlike local decode time.
func (c *Client) noteUpdate(busy, waited time.Duration, wireBytes int64, pixelArea int, usedTight bool) {
	big := wireBytes >= bigUpdateBytes && busy >= bigUpdateTime
	c.lastFast = !big

	// Tight carries JPEG and its own palettes, and only decodes at level 0
	// anyway: nothing for the colour-depth tuner to do.
	if usedTight {
		return
	}
	if wireBytes >= tputSampleBytes && waited >= 20*time.Millisecond {
		c.tput = ewma(c.tput, float64(wireBytes)/waited.Seconds())
	}
	if screen := c.width * c.height; screen > 0 && pixelArea*4 >= screen && wireBytes >= frameSampleMin {
		c.levelFrame[c.level] = ewma(c.levelFrame[c.level], float64(wireBytes)*float64(screen)/float64(pixelArea))
	}

	if c.cooldown > 0 {
		c.cooldown--
		return
	}
	target := frameTarget(c.quality)
	if target == 0 || c.tput == 0 || c.wantLevel != c.level {
		return
	}
	now := time.Now()
	best := len(pixelLevels) - 1
	for l := range pixelLevels {
		limit := target
		if l < c.level {
			// Moving up to better quality needs headroom so it doesn't
			// flap, and is held off after a recent failed attempt.
			if now.Before(c.upBlocked[l]) {
				continue
			}
			limit = target * 6 / 10
		}
		if p, ok := c.predictFrame(l); ok && p <= limit {
			best = l
			break
		}
	}
	if best == c.level {
		return
	}
	if best < c.level {
		c.lastUpAt, c.lastUpFrom = now, c.level
	} else if c.level < c.lastUpFrom && now.Sub(c.lastUpAt) < 90*time.Second {
		// Upgraded to this level and had to come straight back down: don't
		// try it again for a while, backing off further each time.
		b := c.upBackoff[c.level]*2 + 30*time.Second
		c.upBackoff[c.level] = min(b, 10*time.Minute)
		c.upBlocked[c.level] = now.Add(c.upBackoff[c.level])
	}
	c.wantLevel = best
	c.cooldown = adaptCooldown
}

// predictFrame estimates how long a full-screen frame takes on the wire at
// pixel level l, from the measured throughput and the observed full-frame
// size — at l itself when it has been visited, otherwise scaled from the
// nearest visited level. ok is false before any frame has been observed.
func (c *Client) predictFrame(l int) (time.Duration, bool) {
	bytes := c.levelFrame[l]
	if bytes == 0 {
		// Nearest visited level, preferring the current one on ties.
		src := -1
		for o := range pixelLevels {
			if c.levelFrame[o] == 0 {
				continue
			}
			if dist, best := abs(o-l), abs(src-l); src < 0 || dist < best || (dist == best && o == c.level) {
				src = o
			}
		}
		if src >= 0 {
			bytes = c.levelFrame[src]
			for i := src; i < l; i++ {
				bytes *= levelShrink
			}
			for i := src; i > l; i-- {
				bytes *= levelGrow
			}
		}
	}
	if bytes == 0 {
		return 0, false
	}
	return time.Duration(bytes / c.tput * float64(time.Second)), true
}

// sendNextRequest asks for the next incremental update, first applying any
// pending pixel-format downgrade. It must only be called when no update
// request is outstanding (the server can't be mid-way through composing an
// update in the old format), which the two call sites in
// handleFramebufferUpdate — and the invariant of never having more than one
// request in flight — guarantee.
func (c *Client) sendNextRequest() error {
	if c.wantLevel != c.sentLevel {
		msg := append(pixelMessage(&pixelLevels[c.wantLevel]), c.encodingsMessage(c.wantLevel)...)
		c.writeMu.Lock()
		_, err := c.w.Write(msg)
		c.writeMu.Unlock()
		if err != nil {
			return err
		}
		c.sentLevel = c.wantLevel
	}
	return c.requestUpdate(true, 0, 0, c.width, c.height)
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

func (c *Client) decodeRaw(sink FramebufferSink, x, y, w, h int) error {
	lv := &pixelLevels[c.level]
	buf, err := readFull(c.r, w*h*lv.bpp)
	if err != nil {
		return err
	}
	sink.Update(x, y, w, h, toRGBA(lv, buf, w*h))
	return nil
}

// toRGBA converts n wire pixels in level lv's format to canvas RGBA. At
// level 0 that's the input buffer itself with the unused 4th byte of each
// pixel overwritten with alpha; at the reduced levels it's a fresh buffer.
func toRGBA(lv *pixelLevel, wire []byte, n int) []byte {
	if lv.bpp == 4 {
		for i := 3; i < len(wire); i += 4 {
			wire[i] = 0xFF
		}
		return wire
	}
	out := make([]byte, n*4)
	for i := 0; i < n; i++ {
		binary.LittleEndian.PutUint32(out[i*4:], lv.word(wire[i*lv.bpp:]))
	}
	return out
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

	lv := &pixelLevels[c.level]
	wire, err := readFull(c.r, w*h*lv.bpp)
	if err != nil {
		return err
	}
	pixels := toRGBA(lv, wire, w*h)

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

const (
	zrleTileSize = 64
	// zrleScratchSize fits the largest single read a tile decode ever makes:
	// a Raw tile of 64x64 CPIXELs at 3 bytes each.
	zrleScratchSize = zrleTileSize * zrleTileSize * 4
)

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

	if c.zrleTile == nil {
		c.zrleTile = make([]byte, zrleTileSize*zrleTileSize*4)
		c.zrleScratch = make([]byte, zrleScratchSize)
	}
	lv := &pixelLevels[c.level]

	for ty := 0; ty < h; ty += zrleTileSize {
		tileH := min(zrleTileSize, h-ty)
		for tx := 0; tx < w; tx += zrleTileSize {
			tileW := min(zrleTileSize, w-tx)
			buf := c.zrleTile[:tileW*tileH*4]
			if err := decodeZRLETile(zr, lv, c.zrleScratch, buf, tileW, tileH); err != nil {
				return err
			}
			sink.Update(x+tx, y+ty, tileW, tileH, buf)
		}
	}
	return nil
}

// decodeZRLETile decodes one ≤64x64 tile into out (RGBA, w*h*4 bytes).
// scratch is caller-owned working space of at least zrleScratchSize bytes:
// this runs once per tile and per palette/run read, so reading into a reused
// buffer instead of allocating one per read matters at 5-megapixel frames.
func decodeZRLETile(r io.Reader, lv *pixelLevel, scratch, out []byte, w, h int) error {
	if _, err := io.ReadFull(r, scratch[:1]); err != nil {
		return err
	}
	subEnc := scratch[0]
	cp := lv.cpixel
	total := w * h

	switch {
	case subEnc == 0: // Raw
		buf := scratch[:total*cp]
		if _, err := io.ReadFull(r, buf); err != nil {
			return err
		}
		for i := 0; i < total; i++ {
			binary.LittleEndian.PutUint32(out[i*4:], lv.word(buf[i*cp:]))
		}

	case subEnc == 1: // Solid
		if _, err := io.ReadFull(r, scratch[:cp]); err != nil {
			return err
		}
		word := lv.word(scratch)
		for i := 0; i < total; i++ {
			binary.LittleEndian.PutUint32(out[i*4:], word)
		}

	case subEnc >= 2 && subEnc <= 16: // Packed palette
		paletteSize := int(subEnc)
		var palette [16]uint32
		if err := readPaletteWords(r, lv, scratch, palette[:paletteSize]); err != nil {
			return err
		}
		bits := paletteIndexBits(paletteSize)
		rowBytes := (w*bits + 7) / 8
		for row := 0; row < h; row++ {
			rowData := scratch[:rowBytes]
			if _, err := io.ReadFull(r, rowData); err != nil {
				return err
			}
			for col := 0; col < w; col++ {
				idx := extractPackedIndex(rowData, col, bits)
				if idx >= paletteSize {
					return protoErrf("zrle: palette index %d out of range (size %d)", idx, paletteSize)
				}
				binary.LittleEndian.PutUint32(out[(row*w+col)*4:], palette[idx])
			}
		}

	case subEnc == 128: // Plain RLE
		count := 0
		for count < total {
			if _, err := io.ReadFull(r, scratch[:cp]); err != nil {
				return err
			}
			word := lv.word(scratch)
			runLen, err := readRLELength(r, scratch)
			if err != nil {
				return err
			}
			if count+runLen > total {
				return protoErrf("zrle: plain RLE run overflows tile")
			}
			for i := 0; i < runLen; i++ {
				binary.LittleEndian.PutUint32(out[(count+i)*4:], word)
			}
			count += runLen
		}

	case subEnc >= 130: // Palette RLE
		paletteSize := int(subEnc) - 128
		var palette [128]uint32
		if err := readPaletteWords(r, lv, scratch, palette[:paletteSize]); err != nil {
			return err
		}
		count := 0
		for count < total {
			if _, err := io.ReadFull(r, scratch[:1]); err != nil {
				return err
			}
			b := scratch[0]
			var idx, runLen int
			if b&0x80 != 0 {
				idx = int(b & 0x7F)
				var err error
				runLen, err = readRLELength(r, scratch)
				if err != nil {
					return err
				}
			} else {
				idx = int(b)
				runLen = 1
			}
			if idx >= paletteSize {
				return protoErrf("zrle: palette RLE index %d out of range (size %d)", idx, paletteSize)
			}
			if count+runLen > total {
				return protoErrf("zrle: palette RLE run overflows tile")
			}
			word := palette[idx]
			for i := 0; i < runLen; i++ {
				binary.LittleEndian.PutUint32(out[(count+i)*4:], word)
			}
			count += runLen
		}

	default:
		return protoErrf("zrle: unsupported tile subencoding %d", subEnc)
	}

	return nil
}

// readPaletteWords reads len(palette) CPIXELs and converts each to an RGBA word.
func readPaletteWords(r io.Reader, lv *pixelLevel, scratch []byte, palette []uint32) error {
	cp := lv.cpixel
	raw := scratch[:len(palette)*cp]
	if _, err := io.ReadFull(r, raw); err != nil {
		return err
	}
	for i := range palette {
		palette[i] = lv.word(raw[i*cp:])
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
func readRLELength(r io.Reader, scratch []byte) (int, error) {
	total := 1
	for {
		if _, err := io.ReadFull(r, scratch[:1]); err != nil {
			return 0, err
		}
		b := scratch[0]
		total += int(b)
		if b != 255 {
			return total, nil
		}
	}
}

func abs(n int) int {
	if n < 0 {
		return -n
	}
	return n
}
