// Package mark holds the Skrog mark's geometry, so that the SVG masters
// (tools/genlogo) and the rasters (tools/genicons) are the same drawing rather
// than two drawings that have to be kept in agreement by hand. The earlier
// coil mark duplicated its constants across both tools; they drifted.
//
// The mark is a hull's body plan — the section a naval architect draws at each
// frame, seen from ahead: a cambered sheer across the top, slab sides, the
// round of the bilge, and a shallow V down to the keel, with the centreline
// through it. Skrog is Norwegian for hull.
//
// Two properties of that choice are worth recording, because they are what
// earlier attempts lacked:
//
//   - It is a *section*, not a view. It has no tapering end, so it cannot be
//     misread as any of the things a pointed oval gets misread as.
//   - It is 200x172 in a 256 box — near enough square to fill an icon with no
//     tilt and no surrounding disc.
package mark

import (
	"fmt"
	"math"
	"strings"
)

// Box is the design grid. Every coordinate below is in these units; renderers
// scale by target/Box.
const Box = 256.0

// Pt is a point in the design box.
type Pt struct{ X, Y float64 }

// The section, starboard side. The port side is the mirror about x = Box/2, so
// only half the hull is written down and symmetry cannot drift.
//
//	stemHead ── deck            the cambered sheer, peak on the centreline
//	              │             slab side, falling almost plumb
//	           turn             where the side starts to roll
//	              ╰─ bilge      the round of the bilge, one cubic
//	                  ╰─ keel   shallow V to the centreline
var (
	stemHead = Pt{128, 52}                               // sheer camber peak, on the centreline
	deck     = Pt{236, 70}                               // gunwale
	turn     = Pt{228, 158}                              // foot of the slab side
	bilge    = [3]Pt{{228, 188}, {218, 202}, {194, 206}} // cubic: two controls, one end
	keel     = Pt{128, 214}                              // on the centreline
)

// bilgeSegs is how finely the bilge cubic is flattened for the rasterizer. The
// curve spans ~60 units, so 24 segments put the chords well under a pixel even
// at 512.
const bilgeSegs = 24

func mirror(p Pt) Pt { return Pt{Box - p.X, p.Y} }

// Outline returns the closed section as a flattened polyline, starting and
// ending at the stem head. The rasterizer strokes it; it is never filled.
func Outline() []Pt {
	pts := []Pt{stemHead, deck, turn}
	pts = append(pts, flattenCubic(turn, bilge[0], bilge[1], bilge[2], bilgeSegs)...)
	pts = append(pts, keel)

	// Port side: the same points mirrored, walked back up to the stem head.
	port := []Pt{mirror(bilge[2])}
	port = append(port, flattenCubic(mirror(bilge[2]), mirror(bilge[1]), mirror(bilge[0]), mirror(turn), bilgeSegs)...)
	port = append(port, mirror(deck), stemHead)
	return append(pts, port...)
}

// Centreline is the keel line, stem head to keel, drawn full height — the one
// interior line the mark carries.
func Centreline() []Pt { return []Pt{stemHead, keel} }

// flattenCubic samples a cubic Bezier, excluding p0 (the caller already has it)
// and including p3.
func flattenCubic(p0, c1, c2, p3 Pt, n int) []Pt {
	out := make([]Pt, 0, n)
	for i := 1; i <= n; i++ {
		t := float64(i) / float64(n)
		u := 1 - t
		out = append(out, Pt{
			X: u*u*u*p0.X + 3*u*u*t*c1.X + 3*u*t*t*c2.X + t*t*t*p3.X,
			Y: u*u*u*p0.Y + 3*u*u*t*c1.Y + 3*u*t*t*c2.Y + t*t*t*p3.Y,
		})
	}
	return out
}

// OutlinePathD is the SVG `d` for the section, with the bilge left as a real
// cubic rather than the flattened form — an SVG master should be editable by
// hand, and 50 line segments is not.
func OutlinePathD() string {
	var b strings.Builder
	fmt.Fprintf(&b, "M%s L%s L%s C%s %s %s L%s",
		p(stemHead), p(deck), p(turn), p(bilge[0]), p(bilge[1]), p(bilge[2]), p(keel))
	fmt.Fprintf(&b, " L%s C%s %s %s L%s Z",
		p(mirror(bilge[2])), p(mirror(bilge[1])), p(mirror(bilge[0])), p(mirror(turn)), p(mirror(deck)))
	return b.String()
}

// CentrelinePathD is the SVG `d` for the keel line.
func CentrelinePathD() string {
	return fmt.Sprintf("M%s L%s", p(stemHead), p(keel))
}

func p(q Pt) string { return fmt.Sprintf("%g %g", q.X, q.Y) }

// StrokeWidth is the stroke weight, in design units, for the mark rendered at
// px pixels.
//
// The mark is an outline, so what has to stay legible is the stroke's *rendered*
// width, not the shape's area — and one weight cannot serve the whole range.
// The 15 units that read correctly on a 512 px tile are 0.9 px in a 16 px tray
// icon, which anti-aliases to a grey smudge. That is exactly how the project's
// first outline mark failed.
//
// Small sizes therefore get a heavier stroke: the ordinary optical correction,
// chosen here so the rendered weight never drops below 1.7 px.
//
//	16 px -> 1.75 px    32 px -> 2.25 px    64 px -> 3.75 px
//	24 px -> 1.97 px    48 px -> 3.00 px   256 px -> 15.0 px
func StrokeWidth(px int) float64 {
	switch {
	case px <= 16:
		return 28
	case px <= 24:
		return 21
	case px <= 32:
		return 18
	case px <= 48:
		return 16
	default:
		return SVGStroke
	}
}

// SVGStroke is the weight the SVG masters carry. One file is displayed at many
// sizes, so it cannot be corrected per size the way the rasters are; 15 units
// is 1.6 px in the docs-site header at 28 px and 7 px in a README at 120 px,
// which covers where the SVGs are actually used.
const SVGStroke = 15.0

// Bounds is the mark's extent including the stroke at weight w — what a
// renderer needs to confirm the drawing fits its raster.
func Bounds(w float64) (minX, minY, maxX, maxY float64) {
	minX, minY = math.MaxFloat64, math.MaxFloat64
	maxX, maxY = -math.MaxFloat64, -math.MaxFloat64
	for _, q := range Outline() {
		minX, minY = math.Min(minX, q.X), math.Min(minY, q.Y)
		maxX, maxY = math.Max(maxX, q.X), math.Max(maxY, q.Y)
	}
	h := w / 2
	return minX - h, minY - h, maxX + h, maxY + h
}
