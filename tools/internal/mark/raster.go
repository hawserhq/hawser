package mark

import (
	"image"
	"image/color"
	"math"
)

// Raster draws the mark at size px in c on a transparent ground, anti-aliased.
//
// There is no external rasterizer and no fill rule: the drawing is two stroked
// polylines, so a pixel's coverage is just its distance to the nearest segment.
// Round caps and round joins fall out of that for free — a point is inked when
// it lies within half a stroke of any segment, whatever the segment's angle.
//
// Each size is drawn from the geometry rather than downsampled from one large
// render, because the stroke weight is corrected per size (see StrokeWidth); a
// downsample would carry the large size's weight down with it and fade out.
func Raster(size int, c color.RGBA) *image.RGBA {
	img := image.NewRGBA(image.Rect(0, 0, size, size))
	s := float64(size) / Box
	lines := [][]Pt{scalePts(Outline(), s), scalePts(Centreline(), s)}
	half := StrokeWidth(size) / 2 * s

	for y := 0; y < size; y++ {
		for x := 0; x < size; x++ {
			px, py := float64(x)+0.5, float64(y)+0.5
			min := math.MaxFloat64
		search:
			for _, pts := range lines {
				for i := 1; i < len(pts); i++ {
					if d := distSeg(px, py, pts[i-1], pts[i]); d < min {
						min = d
						if min <= half-1 {
							break search // fully inside; no closer edge matters
						}
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

func scalePts(src []Pt, s float64) []Pt {
	out := make([]Pt, len(src))
	for i, q := range src {
		out[i] = Pt{q.X * s, q.Y * s}
	}
	return out
}

// distSeg is the distance from (px,py) to segment a-b.
func distSeg(px, py float64, a, b Pt) float64 {
	dx, dy := b.X-a.X, b.Y-a.Y
	l2 := dx*dx + dy*dy
	if l2 == 0 {
		return math.Hypot(px-a.X, py-a.Y)
	}
	t := ((px-a.X)*dx + (py-a.Y)*dy) / l2
	t = math.Max(0, math.Min(1, t))
	return math.Hypot(px-(a.X+t*dx), py-(a.Y+t*dy))
}
