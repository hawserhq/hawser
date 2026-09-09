package main

// genicons rasterizes the coiled-rope mark (the same Archimedean spiral as
// tools/genlogo) into the raster assets the SVG masters cannot be: a multi-size
// Windows .ico for the executables, PNGs for the docs favicon and the GitHub
// social preview, and the tray status icons (the mark tinted green/grey/red).
//
// Pure Go, no external rasterizer: each pixel's coverage is its anti-aliased
// distance to the spiral stroke — the technique tools/genico used for the dot,
// generalized to a path.
//
// Run: go run ./tools/genicons
import (
	"bytes"
	"encoding/binary"
	"fmt"
	"go/format"
	"image"
	"image/color"
	"image/draw"
	"image/png"
	"math"
	"os"
	"path/filepath"
)

// brand is the slate rope color, matching the SVG masters.
var brand = color.RGBA{0x2F, 0x3B, 0x45, 0xFF}

// Geometry mirrors tools/genlogo, in a 256-unit design box.
const (
	box     = 256.0
	cx      = box / 2
	cy      = box / 2
	rOuter  = 104.0
	rInner  = 17.0
	turns   = 3.0
	ropeW   = 27.0
	samples = 600
)

type pt struct{ x, y float64 }

// spiral returns the coil polyline scaled to a size-by-size raster.
func spiral(size float64) []pt {
	s := size / box
	tmax := turns * 2 * math.Pi
	k := (rOuter - rInner) / tmax
	pts := make([]pt, 0, samples+1)
	for i := 0; i <= samples; i++ {
		t := tmax * float64(i) / float64(samples)
		r := rOuter - k*t
		a := t - math.Pi/2
		pts = append(pts, pt{(cx + r*math.Cos(a)) * s, (cy + r*math.Sin(a)) * s})
	}
	return pts
}

// distSeg is the distance from p to segment a-b.
func distSeg(px, py float64, a, b pt) float64 {
	dx, dy := b.x-a.x, b.y-a.y
	l2 := dx*dx + dy*dy
	if l2 == 0 {
		return math.Hypot(px-a.x, py-a.y)
	}
	t := ((px-a.x)*dx + (py-a.y)*dy) / l2
	t = math.Max(0, math.Min(1, t))
	return math.Hypot(px-(a.x+t*dx), py-(a.y+t*dy))
}

// raster draws the coil at size px in c on a transparent ground, anti-aliased.
func raster(size int, c color.RGBA) *image.RGBA {
	img := image.NewRGBA(image.Rect(0, 0, size, size))
	pts := spiral(float64(size))
	half := ropeW / 2 * float64(size) / box
	for y := 0; y < size; y++ {
		for x := 0; x < size; x++ {
			px, py := float64(x)+0.5, float64(y)+0.5
			min := math.MaxFloat64
			for i := 1; i < len(pts); i++ {
				if d := distSeg(px, py, pts[i-1], pts[i]); d < min {
					min = d
					if min <= half-1 {
						break // fully inside; no closer edge matters
					}
				}
			}
			cov := half - min + 0.5 // 1px anti-alias band
			if cov <= 0 {
				continue
			}
			if cov > 1 {
				cov = 1
			}
			a := uint8(cov * 255)
			img.SetRGBA(x, y, color.RGBA{
				R: uint8(uint16(c.R) * uint16(a) / 255),
				G: uint8(uint16(c.G) * uint16(a) / 255),
				B: uint8(uint16(c.B) * uint16(a) / 255),
				A: a,
			})
		}
	}
	return img
}

// writeSocial renders a 1280x640 GitHub social-preview card: the mark centered
// on the off-white brand ground. Uploaded via the repo's settings, not embedded.
func writeSocial(path string) {
	const w, h, mark = 1280, 640, 440
	dst := image.NewRGBA(image.Rect(0, 0, w, h))
	draw.Draw(dst, dst.Bounds(), &image.Uniform{color.RGBA{0xF7, 0xF6, 0xF3, 0xFF}}, image.Point{}, draw.Src)
	m := raster(mark, brand)
	ox, oy := (w-mark)/2, (h-mark)/2
	draw.Draw(dst, image.Rect(ox, oy, ox+mark, oy+mark), m, image.Point{}, draw.Over)
	writePNG(path, dst)
}

func writePNG(path string, img image.Image) {
	f, err := os.Create(path)
	must(err)
	defer f.Close()
	must(png.Encode(f, img))
	fmt.Println("wrote", path)
}

func main() {
	must(os.MkdirAll("assets/icons", 0o755))

	// PNGs for docs / previews, plus a 32px favicon.
	writePNG("assets/icons/hawser-256.png", raster(256, brand))
	writePNG("assets/icons/hawser-512.png", raster(512, brand))
	writePNG("assets/icons/favicon-32.png", raster(32, brand))
	writeSocial("assets/icons/social-preview.png")

	// Windows .ico with the sizes Explorer, the taskbar and the tray use.
	writeICO("assets/icons/hawser.ico", []int{16, 24, 32, 48, 64, 128, 256}, brand)

	// Tray status icons: the mark tinted to the engine state (#76: compose
	// status onto the mark, replacing the old plain dot).
	writeTrayIcons("cmd/hawsertray/icons_windows.go")

	fmt.Println("done")
}

// dibICO packs a 16x16 image as a classic 32bpp DIB icon — the format the tray
// loads most reliably at small sizes (PNG-in-ICO can misrender at 16px). The
// alpha channel carries the anti-aliased edges; the AND mask is left zero.
func dibICO(img *image.RGBA) []byte {
	const n = 16
	var dib bytes.Buffer
	binary.Write(&dib, binary.LittleEndian, uint32(40)) // header size
	binary.Write(&dib, binary.LittleEndian, int32(n))   // width
	binary.Write(&dib, binary.LittleEndian, int32(n*2)) // height (XOR+AND)
	binary.Write(&dib, binary.LittleEndian, uint16(1))  // planes
	binary.Write(&dib, binary.LittleEndian, uint16(32)) // bpp
	binary.Write(&dib, binary.LittleEndian, uint32(0))  // compression
	binary.Write(&dib, binary.LittleEndian, uint32(0))  // image size
	binary.Write(&dib, binary.LittleEndian, [4]int32{}) // ppm + colors
	for y := n - 1; y >= 0; y-- {
		for x := 0; x < n; x++ {
			px := img.RGBAAt(x, y)
			dib.Write([]byte{px.B, px.G, px.R, px.A})
		}
	}
	dib.Write(make([]byte, 4*n)) // AND mask, padded to 4 bytes per row

	var out bytes.Buffer
	binary.Write(&out, binary.LittleEndian, uint16(0))
	binary.Write(&out, binary.LittleEndian, uint16(1))
	binary.Write(&out, binary.LittleEndian, uint16(1))
	out.Write([]byte{n, n, 0, 0})
	binary.Write(&out, binary.LittleEndian, uint16(1))
	binary.Write(&out, binary.LittleEndian, uint16(32))
	binary.Write(&out, binary.LittleEndian, uint32(dib.Len()))
	binary.Write(&out, binary.LittleEndian, uint32(22))
	out.Write(dib.Bytes())
	return out.Bytes()
}

// writeTrayIcons regenerates the tray's green/grey/red status icons as the coil
// mark tinted to each state, emitted as a Go source file of byte slices.
func writeTrayIcons(path string) {
	icons := []struct {
		name string
		c    color.RGBA
	}{
		{"iconGreen", color.RGBA{0x2E, 0xA0, 0x43, 0xFF}}, // running
		{"iconGrey", color.RGBA{0x8C, 0x94, 0x9E, 0xFF}},  // idle / stopped
		{"iconRed", color.RGBA{0xDA, 0x36, 0x33, 0xFF}},   // down / error
	}
	var b bytes.Buffer
	fmt.Fprint(&b, "//go:build windows\n\npackage main\n\n"+
		"// Generated 16x16 coil status icons (green/grey/red). The tray shows the\n"+
		"// Hawser mark in the engine's state color. Regenerate with `go run ./tools/genicons`.\n\n")
	for _, e := range icons {
		data := dibICO(raster(16, e.c))
		fmt.Fprintf(&b, "var %s = []byte{", e.name)
		for i, by := range data {
			if i%16 == 0 {
				b.WriteString("\n\t")
			}
			fmt.Fprintf(&b, "0x%02x, ", by)
		}
		b.WriteString("\n}\n\n")
	}
	src, err := format.Source(b.Bytes())
	must(err)
	must(os.WriteFile(path, src, 0o644))
	fmt.Println("wrote", path)
}

// writeICO packs several PNG-encoded sizes into one .ico. PNG-in-ICO is valid
// since Vista and keeps large sizes small; Explorer picks the size it needs.
func writeICO(path string, sizes []int, c color.RGBA) {
	type entry struct {
		size int
		png  []byte
	}
	var entries []entry
	for _, s := range sizes {
		var buf bytes.Buffer
		must(png.Encode(&buf, raster(s, c)))
		entries = append(entries, entry{s, buf.Bytes()})
	}

	var out bytes.Buffer
	binary.Write(&out, binary.LittleEndian, uint16(0))            // reserved
	binary.Write(&out, binary.LittleEndian, uint16(1))            // type: icon
	binary.Write(&out, binary.LittleEndian, uint16(len(entries))) // count
	offset := 6 + 16*len(entries)                                 // header + directory
	for _, e := range entries {
		dim := byte(e.size)
		if e.size >= 256 {
			dim = 0 // 0 means 256 in the ICO directory
		}
		out.WriteByte(dim)                                          // width
		out.WriteByte(dim)                                          // height
		out.WriteByte(0)                                            // palette
		out.WriteByte(0)                                            // reserved
		binary.Write(&out, binary.LittleEndian, uint16(1))          // planes
		binary.Write(&out, binary.LittleEndian, uint16(32))         // bpp
		binary.Write(&out, binary.LittleEndian, uint32(len(e.png))) // bytes
		binary.Write(&out, binary.LittleEndian, uint32(offset))     // offset
		offset += len(e.png)
	}
	for _, e := range entries {
		out.Write(e.png)
	}
	must(os.WriteFile(path, out.Bytes(), 0o644))
	fmt.Println("wrote", path, "("+filepath.Base(path)+",", len(sizes), "sizes)")
}

func must(err error) {
	if err != nil {
		panic(err)
	}
}
