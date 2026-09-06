package doctor

import (
	"fmt"

	"github.com/zcsizmadia/hawser/internal/version"
)

// checkDockerCLI answers "which docker.exe actually runs, and is it the one you
// think?" — the PATH-shadowing class where a stale Docker Desktop binary
// resolves first while the hawser context is active, so commands work but not
// against Hawser, which looks like a Hawser fault.
func checkDockerCLI() Check {
	c := Check{Name: "docker-cli", Title: "docker CLI on PATH"}
	c.Run = func(f Facts) Result {
		bins := f.Report.Docker
		if len(bins) == 0 {
			r := result(c, Fail, "no docker.exe found on PATH")
			r.Remedy = "install Hawser's bundled docker CLI, or add an existing " +
				"docker.exe to PATH."
			return r
		}

		first := firstDocker(bins)
		detail := make([]string, 0, len(bins))
		for _, b := range bins {
			marker := "  "
			if b.First {
				marker = "* "
			}
			detail = append(detail, fmt.Sprintf("%s%s (%s)", marker, b.Path, b.Origin))
		}

		// The shadowing case: the hawser context is selected but a foreign
		// binary wins on PATH.
		if f.Report.Context == "hawser" && first != nil && first.Origin != version.OriginHawser {
			r := result(c, Warn, fmt.Sprintf(
				"the hawser context is active but %s (%s) resolves first on PATH",
				first.Path, first.Origin))
			r.Detail = detail
			r.Remedy = "put Hawser's bin directory earlier on PATH, or invoke docker " +
				"by its full path, so the binary you run matches the context."
			return r
		}

		summary := "1 docker.exe on PATH"
		if len(bins) > 1 {
			summary = fmt.Sprintf("%d docker.exe on PATH; the first one runs", len(bins))
		}
		r := result(c, OK, summary)
		if len(bins) > 1 {
			r.Detail = detail
		}
		return r
	}
	return c
}

func firstDocker(bins []version.Binary) *version.Binary {
	for i := range bins {
		if bins[i].First {
			return &bins[i]
		}
	}
	return nil
}
