package main

// genlogo generates the Skrog identity: the hull's body plan, drawn as a naval
// architect draws it — an outline with the centreline through it, no fill.
// Skrog is Norwegian for hull, so the mark is now a picture of the name; the
// coil it replaces was a picture of the project's former one (Hawser, the
// mooring line).
//
// The geometry lives in tools/internal/mark so this tool and tools/genicons
// draw the same thing. Output is pure-geometry SVG — no fonts, no rasterizer —
// so the masters stay portable and hand-editable after generation.
//
// Run: go run ./tools/genlogo  (writes assets/*.svg)
import (
	"fmt"
	"os"

	"github.com/wslkit/skrog/tools/internal/mark"
)

// drawing is the mark's two paths at a given colour, indented for embedding.
func drawing(stroke string) string {
	return fmt.Sprintf(
		`  <g stroke="%s" stroke-width="%g" stroke-linejoin="round" stroke-linecap="round">`+"\n"+
			`    <path d="%s"/>`+"\n"+
			`    <path d="%s"/>`+"\n"+
			`  </g>`,
		stroke, mark.SVGStroke, mark.OutlinePathD(), mark.CentrelinePathD())
}

// markSVG is the square mark. fill="none" on the root is what makes this an
// outline drawing rather than a silhouette: every path here is stroke-only.
func markSVG(stroke string) string {
	return fmt.Sprintf(
		`<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 %d %d" width="%d" height="%d" fill="none">`+"\n%s\n</svg>\n",
		int(mark.Box), int(mark.Box), int(mark.Box), int(mark.Box), drawing(stroke))
}

// logoLockup is the mark beside the wordmark. The wordmark uses a system sans
// via <text>; the mark is the portable part.
func logoLockup(brand string) string {
	return `<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 640 256" width="640" height="256" fill="none">` + "\n" +
		drawing(brand) + "\n" +
		fmt.Sprintf(`  <text x="272" y="158" font-family="Segoe UI, system-ui, -apple-system, Roboto, sans-serif" font-size="112" font-weight="700" letter-spacing="-2" fill="%s">skrog</text>`, brand) + "\n" +
		"</svg>\n"
}

func main() {
	const (
		brand     = "#2F3B45" // default: deep slate, for light grounds.
		brandDark = "#CBD5DF" // light slate, for dark grounds (README dark mode).
	)
	must(os.MkdirAll("assets", 0o755))
	must(os.WriteFile("assets/skrog-mark.svg", []byte(markSVG(brand)), 0o644))
	must(os.WriteFile("assets/skrog-mark-ondark.svg", []byte(markSVG(brandDark)), 0o644))
	must(os.WriteFile("assets/skrog-mark-mono.svg", []byte(markSVG("currentColor")), 0o644))
	must(os.WriteFile("assets/skrog-logo.svg", []byte(logoLockup(brand)), 0o644))
	fmt.Println("wrote assets/skrog-mark.svg, skrog-mark-ondark.svg, skrog-mark-mono.svg, skrog-logo.svg")
}

func must(err error) {
	if err != nil {
		panic(err)
	}
}
