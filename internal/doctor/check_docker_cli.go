package doctor

import (
	"fmt"
	"path/filepath"

	"github.com/wslkit/skrog/internal/dockercli"
	"github.com/wslkit/skrog/internal/version"
)

// checkDockerCLI answers "which docker.exe actually runs, and is it the one you
// think?" — the PATH-shadowing class where a stale Docker Desktop binary
// resolves first while the skrog context is active, so commands work but not
// against Skrog, which looks like a Skrog fault.
func checkDockerCLI() Check {
	c := Check{Name: "docker-cli", Title: "docker CLI on PATH"}
	c.Run = func(f Facts) Result {
		bins := f.Report.Docker
		if len(bins) == 0 {
			r := result(c, Fail, "no docker.exe found on PATH")
			r.Remedy = "install Skrog's bundled docker CLI, or add an existing " +
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
			detail = append(detail, fmt.Sprintf("%s%s (%s)", marker, b.Path, version.OriginLabel(b.Origin)))
		}

		// The shadowing case: the skrog context is selected but a foreign
		// binary wins on PATH.
		//
		// The remedy comes from dockercli.ShadowAdvice, the same place the
		// docker-bundle check and `skrog cli install` get theirs. This check
		// had its own copy, which still said "put Skrog's bin directory
		// earlier on PATH" -- impossible against a machine-PATH entry, and it
		// meant one doctor run could print two contradictory fixes (#287).
		if f.Report.Context == "skrog" && first != nil && first.Origin != version.OriginSkrog {
			r := result(c, Warn, fmt.Sprintf(
				"the skrog context is active but %s (%s) resolves first on PATH",
				first.Path, version.OriginLabel(first.Origin)))
			r.Detail = detail
			if f.CLI.ShadowScope == dockercli.ScopeMachine {
				r.Detail = append(r.Detail, "  the one that runs is on the SYSTEM PATH, which always resolves first")
			}
			r.Remedy = dockercli.ShadowAdvice(
				f.CLI.ShadowScope, filepath.Dir(first.Path), string(first.Origin))
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
