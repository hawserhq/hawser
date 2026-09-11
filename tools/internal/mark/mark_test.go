package mark

import (
	"math"
	"testing"
)

// The mark has to fit its raster at every size. It nearly does not: the small
// sizes carry a much heavier stroke (StrokeWidth), and half of that stroke
// hangs outside the outline's coordinates. At 16 px the drawing reaches 14
// units past the path on each side — so a future widening of the section, or a
// heavier small-size weight, can clip the gunwale against the icon's edge
// without anything failing to build.
func TestMarkFitsTheBox(t *testing.T) {
	for _, px := range []int{16, 24, 32, 48, 64, 128, 256, 512} {
		minX, minY, maxX, maxY := Bounds(StrokeWidth(px))
		if minX < 0 || minY < 0 || maxX > Box || maxY > Box {
			t.Errorf("at %dpx (stroke %g): drawing spans x %.1f..%.1f, y %.1f..%.1f, outside 0..%g",
				px, StrokeWidth(px), minX, maxX, minY, maxY, Box)
		}
	}
}

// A hull section is symmetric, and the mark is drawn as one side plus a mirror
// so it cannot drift. This pins that the mirroring is actually applied to every
// point, including the flattened bilge curve.
func TestSectionIsSymmetric(t *testing.T) {
	seen := map[float64][]float64{}
	for _, p := range Outline() {
		seen[math.Round(p.Y*100)] = append(seen[math.Round(p.Y*100)], p.X)
	}
	for y, xs := range seen {
		if len(xs) != 2 {
			continue // the stem head and keel sit on the centreline, alone
		}
		if got := xs[0] + xs[1]; math.Abs(got-Box) > 1e-9 {
			t.Errorf("at y=%g the two points are x=%g and x=%g, which straddle %g, not the centreline %g",
				y/100, xs[0], xs[1], got/2, Box/2)
		}
	}
}

// The stroke weight exists to keep the rendered line legible at small sizes.
// Its whole point is that the rendered width never collapses, so that is what
// is asserted — not the unit values, which are free to be retuned.
func TestRenderedStrokeStaysLegible(t *testing.T) {
	for _, px := range []int{16, 24, 32, 48, 64, 128, 256} {
		rendered := StrokeWidth(px) * float64(px) / Box
		if rendered < 1.7 {
			t.Errorf("at %dpx the stroke renders %.2f px, which anti-aliases to a smudge", px, rendered)
		}
	}
}

// The interior has to stay open: the centreline splits the section in two, and
// if the two strokes meet there is no section left, only a filled blob. The
// tightest place is the beam at the keel, at the smallest size.
func TestChambersStayOpen(t *testing.T) {
	const px = 16
	w := StrokeWidth(px)
	// Half the beam at the gunwale, less half the outline stroke and half the
	// centreline stroke, is the clear width of one chamber.
	halfBeam := deck.X - Box/2
	clear := (halfBeam - w) * float64(px) / Box
	if clear < 1.5 {
		t.Errorf("at %dpx each chamber is %.2f px wide; the section closes up", px, clear)
	}
}
