package main

// genlogo generates the Hawser identity: a coiled heavy-rope mark (a hawser is
// the thick line that moors a ship). The coil reads as a near-solid disc at
// 16px — good favicon behaviour — and shows the rope's twist grooves at large
// sizes. Output is pure-geometry SVG (no fonts, no rasterizer), so the masters
// are portable and hand-editable after generation.
//
// Run: go run ./tools/genlogo  (writes assets/*.svg)
import (
	"fmt"
	"math"
	"os"
	"strings"
)

const (
	size    = 256.0
	cx      = size / 2
	cy      = size / 2
	rOuter  = 104.0
	rInner  = 17.0
	turns   = 3.0
	ropeW   = 27.0 // heavy rope
	samples = 400
)

// spiralPath returns the SVG path `d` for an Archimedean coil from the outer
// end inward. The rope width is applied by the stroke, so one path is the whole
// rope.
func spiralPath() string {
	tmax := turns * 2 * math.Pi
	k := (rOuter - rInner) / tmax
	var b strings.Builder
	for i := 0; i <= samples; i++ {
		t := tmax * float64(i) / float64(samples)
		r := rOuter - k*t
		// Start at the top (-90°) so the rope's loose end sits at 12 o'clock.
		a := t - math.Pi/2
		x := cx + r*math.Cos(a)
		y := cy + r*math.Sin(a)
		if i == 0 {
			fmt.Fprintf(&b, "M%.2f %.2f", x, y)
		} else {
			fmt.Fprintf(&b, "L%.2f %.2f", x, y)
		}
	}
	return b.String()
}

func header() string {
	return fmt.Sprintf(`<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 %d %d" width="%d" height="%d" fill="none">`,
		int(size), int(size), int(size), int(size))
}

// colorMark: rope body in the brand color, twist grooves as a translucent-black
// overlay so the same file re-colors to any brand hue and the grooves follow.
func colorMark(brand string) string {
	sp := spiralPath()
	return header() + "\n" +
		fmt.Sprintf(`  <path d="%s" stroke="%s" stroke-width="%.0f" stroke-linecap="round" stroke-linejoin="round"/>`, sp, brand, ropeW) + "\n" +
		fmt.Sprintf(`  <path d="%s" stroke="#000" stroke-opacity="0.22" stroke-width="%.0f" stroke-linecap="butt" stroke-dasharray="3 13"/>`, sp, ropeW-2) + "\n" +
		"</svg>\n"
}

// monoMark: a solid single-colour silhouette (currentColor), for docs, badges
// and terminals on any background.
func monoMark() string {
	sp := spiralPath()
	return header() + "\n" +
		fmt.Sprintf(`  <path d="%s" stroke="currentColor" stroke-width="%.0f" stroke-linecap="round" stroke-linejoin="round"/>`, sp, ropeW) + "\n" +
		"</svg>\n"
}

// logoLockup: the mark beside a simple wordmark. The wordmark uses a system
// sans via <text>; the mark is the portable part.
func logoLockup(brand string) string {
	sp := spiralPath()
	// A 640x256 canvas: mark on the left, "hawser" to its right.
	return `<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 640 256" width="640" height="256" fill="none">` + "\n" +
		fmt.Sprintf(`  <path d="%s" stroke="%s" stroke-width="%.0f" stroke-linecap="round" stroke-linejoin="round"/>`, sp, brand, ropeW) + "\n" +
		fmt.Sprintf(`  <path d="%s" stroke="#000" stroke-opacity="0.22" stroke-width="%.0f" stroke-linecap="butt" stroke-dasharray="3 13"/>`, sp, ropeW-2) + "\n" +
		fmt.Sprintf(`  <text x="272" y="158" font-family="Segoe UI, system-ui, -apple-system, Roboto, sans-serif" font-size="112" font-weight="700" letter-spacing="-2" fill="%s">hawser</text>`, brand) + "\n" +
		"</svg>\n"
}

func main() {
	const brand = "#2F3B45" // default: deep slate; re-color freely.
	must(os.MkdirAll("assets", 0o755))
	must(os.WriteFile("assets/hawser-mark.svg", []byte(colorMark(brand)), 0o644))
	must(os.WriteFile("assets/hawser-mark-mono.svg", []byte(monoMark()), 0o644))
	must(os.WriteFile("assets/hawser-logo.svg", []byte(logoLockup(brand)), 0o644))
	fmt.Println("wrote assets/hawser-mark.svg, hawser-mark-mono.svg, hawser-logo.svg")
}

func must(err error) {
	if err != nil {
		panic(err)
	}
}
