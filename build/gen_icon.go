//go:build ignore

// Command gen_icon procedurally draws the Lupinus mark — a lupinus flower
// spike, a crescent moon and a small star accent — using only the Go
// standard library (image/draw/png, plus a hand-rolled ICO and ICNS
// encoder), and writes every icon format the app's packaging needs.
//
// The design mirrors build/appicon.svg (the hand-authored master vector);
// keep the two in sync if you tweak the mark.
//
// Run with: go run build/gen_icon.go
package main

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"os"
	"path/filepath"
)

// --- palette ---

var (
	colGround     = mustColor(0x15, 0x10, 0x24) // Deep Violet
	colStem       = mustColor(0x24, 0x16, 0x3D) // Nebula Purple
	colSpikeLow   = mustColor(0x7C, 0x5C, 0xFF) // Lupinus Violet
	colSpikeHigh  = mustColor(0xF2, 0x9B, 0xCB) // Sakura Pink
	colMoonAndBud = mustColor(0xED, 0xE9, 0xFF) // Moonlight
	colStar       = mustColor(0xF7, 0xC4, 0xDE) // Soft Sakura
)

func mustColor(r, g, b uint8) color.RGBA {
	return color.RGBA{R: r, G: g, B: b, A: 255}
}

// --- design, expressed as fractions of the canvas size so it scales
// cleanly to every requested pixel size. Coordinates match
// build/appicon.svg's 256x256 layout divided by 256. ---

const groundRadius = 48.0 / 256.0

type rectSpec struct{ x0, y0, x1, y1, radius float64 }

var ground = rectSpec{8.0 / 256, 8.0 / 256, 248.0 / 256, 248.0 / 256, groundRadius}
var stem = rectSpec{98.0 / 256, 205.0 / 256, 110.0 / 256, 235.0 / 256, 6.0 / 256}

type circleSpec struct {
	cx, cy, r float64
	col       color.RGBA
}

var moon = circleSpec{186.0 / 256, 64.0 / 256, 34.0 / 256, colMoonAndBud}
var moonCut = circleSpec{200.0 / 256, 56.0 / 256, 30.0 / 256, colGround}

// flower spike: overlapping blooms tapering from violet at the base to
// sakura pink at the tip, topped with a small moonlit bud.
var spike = []circleSpec{
	{104.0 / 256, 205.0 / 256, 34.0 / 256, colSpikeLow},
	{104.0 / 256, 178.0 / 256, 30.0 / 256, colSpikeLow},
	{104.0 / 256, 152.0 / 256, 26.0 / 256, colSpikeLow},
	{104.0 / 256, 128.0 / 256, 22.0 / 256, colSpikeHigh},
	{104.0 / 256, 106.0 / 256, 18.0 / 256, colSpikeHigh},
	{104.0 / 256, 86.0 / 256, 14.0 / 256, colSpikeHigh},
	{104.0 / 256, 70.0 / 256, 8.0 / 256, colMoonAndBud},
}

type point struct{ x, y float64 }

// star accent: a small 4-point sparkle, coordinates from appicon.svg's
// polygon divided by 256.
var star = []point{
	{140.0 / 256, 18.0 / 256},
	{146.0 / 256, 30.0 / 256},
	{158.0 / 256, 36.0 / 256},
	{146.0 / 256, 42.0 / 256},
	{140.0 / 256, 54.0 / 256},
	{134.0 / 256, 42.0 / 256},
	{122.0 / 256, 36.0 / 256},
	{134.0 / 256, 30.0 / 256},
}

// --- rasterizer: draw at supersample x the target size onto an opaque-or-
// transparent RGBA canvas (every fill is fully opaque or fully transparent,
// which is already valid Go alpha-premultiplied color.RGBA data), then box
// -downsample. Averaging premultiplied samples is what gives correct
// anti-aliased edges — including the transparent rounded-corner ground —
// without a real AA rasterizer. ---

const supersample = 4

func drawIcon(size int) *image.RGBA {
	hi := size * supersample
	canvas := image.NewRGBA(image.Rect(0, 0, hi, hi))
	S := float64(hi)

	fillRoundedRect(canvas, ground, S, colGround)
	fillCircle(canvas, moon, S)
	fillCircle(canvas, moonCut, S)
	fillPolygon(canvas, star, S, colStar)
	fillRoundedRect(canvas, stem, S, colStem)
	for _, c := range spike {
		fillCircle(canvas, c, S)
	}

	return downsample(canvas, size, supersample)
}

func fillRoundedRect(img *image.RGBA, r rectSpec, S float64, col color.RGBA) {
	x0, y0, x1, y1, rad := r.x0*S, r.y0*S, r.x1*S, r.y1*S, r.radius*S
	minX, maxX := int(x0), int(x1)+1
	minY, maxY := int(y0), int(y1)+1
	for y := minY; y <= maxY; y++ {
		for x := minX; x <= maxX; x++ {
			fx, fy := float64(x)+0.5, float64(y)+0.5
			if insideRoundedRect(fx, fy, x0, y0, x1, y1, rad) {
				setOpaque(img, x, y, col)
			}
		}
	}
}

func insideRoundedRect(px, py, x0, y0, x1, y1, rad float64) bool {
	if px < x0 || px > x1 || py < y0 || py > y1 {
		return false
	}
	// Clamp to the "core" rect shrunk by the corner radius; outside that
	// core on both axes we're in corner territory and need the circular
	// distance test.
	cx := clampf(px, x0+rad, x1-rad)
	cy := clampf(py, y0+rad, y1-rad)
	dx, dy := px-cx, py-cy
	return dx*dx+dy*dy <= rad*rad
}

func clampf(v, lo, hi float64) float64 {
	if lo > hi { // radius larger than half the rect's extent
		return (lo + hi) / 2
	}
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}

func fillCircle(img *image.RGBA, c circleSpec, S float64) {
	cx, cy, r := c.cx*S, c.cy*S, c.r*S
	minX, maxX := int(cx-r)-1, int(cx+r)+1
	minY, maxY := int(cy-r)-1, int(cy+r)+1
	r2 := r * r
	for y := minY; y <= maxY; y++ {
		for x := minX; x <= maxX; x++ {
			fx, fy := float64(x)+0.5, float64(y)+0.5
			dx, dy := fx-cx, fy-cy
			if dx*dx+dy*dy <= r2 {
				setOpaque(img, x, y, c.col)
			}
		}
	}
}

// fillPolygon rasterizes a simple (non-self-intersecting) polygon given in
// fractional coordinates, using an even-odd point-in-polygon test.
func fillPolygon(img *image.RGBA, pts []point, S float64, col color.RGBA) {
	if len(pts) < 3 {
		return
	}
	minX, minY, maxX, maxY := pts[0].x, pts[0].y, pts[0].x, pts[0].y
	for _, p := range pts {
		minX, maxX = minf(minX, p.x), maxf(maxX, p.x)
		minY, maxY = minf(minY, p.y), maxf(maxY, p.y)
	}
	px0, py0, px1, py1 := int(minX*S)-1, int(minY*S)-1, int(maxX*S)+1, int(maxY*S)+1
	for y := py0; y <= py1; y++ {
		for x := px0; x <= px1; x++ {
			fx, fy := (float64(x)+0.5)/S, (float64(y)+0.5)/S
			if pointInPolygon(fx, fy, pts) {
				setOpaque(img, x, y, col)
			}
		}
	}
}

func pointInPolygon(x, y float64, pts []point) bool {
	inside := false
	n := len(pts)
	for i, j := 0, n-1; i < n; j, i = i, i+1 {
		pi, pj := pts[i], pts[j]
		if (pi.y > y) != (pj.y > y) {
			xInt := pj.x + (y-pj.y)/(pi.y-pj.y)*(pi.x-pj.x)
			if x < xInt {
				inside = !inside
			}
		}
	}
	return inside
}

func minf(a, b float64) float64 {
	if a < b {
		return a
	}
	return b
}
func maxf(a, b float64) float64 {
	if a > b {
		return a
	}
	return b
}

func setOpaque(img *image.RGBA, x, y int, col color.RGBA) {
	b := img.Bounds()
	if x < b.Min.X || x >= b.Max.X || y < b.Min.Y || y >= b.Max.Y {
		return
	}
	img.SetRGBA(x, y, col)
}

// downsample box-filters a supersample x supersample block of
// alpha-premultiplied pixels into one output pixel per block. Averaging
// premultiplied components directly is what produces correct edge
// blending (including partial transparency at the rounded-rect corners).
func downsample(hi *image.RGBA, size, factor int) *image.RGBA {
	out := image.NewRGBA(image.Rect(0, 0, size, size))
	for oy := 0; oy < size; oy++ {
		for ox := 0; ox < size; ox++ {
			var rs, gs, bs, as uint32
			for dy := 0; dy < factor; dy++ {
				for dx := 0; dx < factor; dx++ {
					c := hi.RGBAAt(ox*factor+dx, oy*factor+dy)
					rs += uint32(c.R)
					gs += uint32(c.G)
					bs += uint32(c.B)
					as += uint32(c.A)
				}
			}
			n := uint32(factor * factor)
			out.SetRGBA(ox, oy, color.RGBA{
				R: uint8(rs / n),
				G: uint8(gs / n),
				B: uint8(bs / n),
				A: uint8(as / n),
			})
		}
	}
	return out
}

func encodePNG(img *image.RGBA) []byte {
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		panic(err)
	}
	return buf.Bytes()
}

// --- ICO: a multi-resolution Windows icon, PNG-compressed per entry
// (supported since Vista, required for the 256px entry since classic BMP
// entries top out at 255px). ---

func encodeICO(sizes []int, pngBySize map[int][]byte) []byte {
	buf := new(bytes.Buffer)
	binary.Write(buf, binary.LittleEndian, uint16(0)) // reserved
	binary.Write(buf, binary.LittleEndian, uint16(1)) // type: icon
	binary.Write(buf, binary.LittleEndian, uint16(len(sizes)))

	headerLen := 6 + 16*len(sizes)
	offset := uint32(headerLen)

	type entry struct {
		size int
		data []byte
	}
	entries := make([]entry, 0, len(sizes))
	for _, s := range sizes {
		entries = append(entries, entry{s, pngBySize[s]})
	}

	for _, e := range entries {
		dim := byte(e.size)
		if e.size >= 256 {
			dim = 0 // 0 means 256 in ICO's single-byte dimension field
		}
		row := make([]byte, 16)
		row[0] = dim                                // width
		row[1] = dim                                // height
		row[2] = 0                                  // color count (0 = no palette / >=8bpp)
		row[3] = 0                                  // reserved
		binary.LittleEndian.PutUint16(row[4:6], 1)  // color planes
		binary.LittleEndian.PutUint16(row[6:8], 32) // bits per pixel
		binary.LittleEndian.PutUint32(row[8:12], uint32(len(e.data)))
		binary.LittleEndian.PutUint32(row[12:16], offset)
		buf.Write(row)
		offset += uint32(len(e.data))
	}
	for _, e := range entries {
		buf.Write(e.data)
	}
	return buf.Bytes()
}

// --- ICNS: Apple's chunked icon container. Modern entry types hold a
// plain PNG payload, so this needs nothing beyond image/png — no cgo, no
// external tool. Format: 4-byte magic "icns", 4-byte big-endian total
// file length, then a sequence of TLV chunks (4-byte type, 4-byte
// big-endian chunk length INCLUDING the 8-byte header, then the payload). ---

type icnsEntry struct {
	osType string
	data   []byte
}

func encodeICNS(entries []icnsEntry) []byte {
	body := new(bytes.Buffer)
	for _, e := range entries {
		if len(e.osType) != 4 {
			panic("icns: osType must be 4 bytes: " + e.osType)
		}
		body.WriteString(e.osType)
		binary.Write(body, binary.BigEndian, uint32(8+len(e.data)))
		body.Write(e.data)
	}

	out := new(bytes.Buffer)
	out.WriteString("icns")
	binary.Write(out, binary.BigEndian, uint32(8+body.Len()))
	out.Write(body.Bytes())
	return out.Bytes()
}

// verifyICNS is a best-effort structural self-check (this repo has no
// macOS box to run `iconutil`/`sips` against): re-parses the chunk list,
// confirms the total length matches, and confirms every chunk's payload
// starts with a PNG magic number.
func verifyICNS(b []byte) error {
	if len(b) < 8 || string(b[0:4]) != "icns" {
		return fmt.Errorf("missing icns magic")
	}
	total := binary.BigEndian.Uint32(b[4:8])
	if int(total) != len(b) {
		return fmt.Errorf("total length mismatch: header says %d, file is %d bytes", total, len(b))
	}
	pngMagic := []byte{0x89, 'P', 'N', 'G', '\r', '\n', 0x1a, '\n'}
	off := 8
	for off < len(b) {
		if off+8 > len(b) {
			return fmt.Errorf("truncated chunk header at offset %d", off)
		}
		osType := string(b[off : off+4])
		length := binary.BigEndian.Uint32(b[off+4 : off+8])
		if off+int(length) > len(b) {
			return fmt.Errorf("chunk %q length %d overruns file", osType, length)
		}
		payload := b[off+8 : off+int(length)]
		if len(payload) < 8 || !bytes.Equal(payload[:8], pngMagic) {
			return fmt.Errorf("chunk %q payload is not PNG data", osType)
		}
		off += int(length)
	}
	if off != len(b) {
		return fmt.Errorf("trailing bytes after last chunk")
	}
	return nil
}

func main() {
	root := "build"

	sizes := []int{16, 24, 32, 48, 64, 128, 256}
	pngBySize := make(map[int][]byte, len(sizes))
	for _, s := range sizes {
		pngBySize[s] = encodePNG(drawIcon(s))
	}

	must := func(err error) {
		if err != nil {
			panic(err)
		}
	}
	writeFile := func(path string, data []byte) {
		must(os.MkdirAll(filepath.Dir(path), 0o755))
		must(os.WriteFile(path, data, 0o644))
		fmt.Printf("wrote %s (%d bytes)\n", path, len(data))
	}

	// Source assets referenced by Wails tooling and main.go's systray embed.
	writeFile(filepath.Join(root, "appicon.png"), pngBySize[256])
	writeFile(filepath.Join(root, "icon.png"), pngBySize[256])

	// Windows: multi-resolution ICO.
	icoSizes := []int{16, 24, 32, 48, 64, 128, 256}
	writeFile(filepath.Join(root, "windows", "icon.ico"), encodeICO(icoSizes, pngBySize))

	// Linux: hicolor-theme-style loose PNGs + a .desktop launcher.
	for _, s := range sizes {
		writeFile(filepath.Join(root, "linux", fmt.Sprintf("icon_%d.png", s)), pngBySize[s])
	}
	writeFile(filepath.Join(root, "linux", "lupinus.desktop"), []byte(desktopFile))

	// macOS: ICNS built purely in Go from the same PNGs (icp*/ic0*/ic1*
	// are just PNG payloads in a chunked container — no cgo needed). We
	// reuse the 32/64/256px renders as the "@2x" variants of 16/32/128 —
	// they're pixel-identical to what iconutil would produce from a real
	// @2x source at those exact dimensions.
	icns := encodeICNS([]icnsEntry{
		{"icp4", pngBySize[16]},  // 16x16
		{"icp5", pngBySize[32]},  // 32x32
		{"icp6", pngBySize[64]},  // 64x64
		{"ic07", pngBySize[128]}, // 128x128
		{"ic08", pngBySize[256]}, // 256x256
		{"ic11", pngBySize[32]},  // 16x16@2x
		{"ic12", pngBySize[64]},  // 32x32@2x
		{"ic13", pngBySize[256]}, // 128x128@2x
	})
	must(verifyICNS(icns))
	writeFile(filepath.Join(root, "darwin", "icon.icns"), icns)

	fmt.Println("done")
}

const desktopFile = `[Desktop Entry]
Type=Application
Name=Lupinus
Comment=Native VNC Client
Exec=lupinus
Icon=lupinus
Terminal=false
Categories=Network;RemoteAccess;
StartupWMClass=lupinus
`
