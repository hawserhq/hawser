package doctor

import "fmt"

// checkContext verifies docker is actually pointed at the Hawser engine. A
// machine with Docker Desktop's context still selected, or a stray
// DOCKER_HOST, talks to something other than Hawser while looking like it
// should work — a common source of "hawser is broken" reports that are really
// "docker is aimed elsewhere."
func checkContext() Check {
	c := Check{Name: "context", Title: "docker context"}
	c.Run = func(f Facts) Result {
		if !f.Report.Engine.Installed {
			return result(c, Skip, "no engine installed; context not yet relevant")
		}

		ctx, src := f.Report.Context, f.Report.ContextSource

		// DOCKER_HOST overrides context entirely: version reports empty context.
		if ctx == "" {
			r := result(c, Warn, "DOCKER_HOST overrides the docker context")
			r.Detail = []string{"  source: " + src}
			r.Remedy = "unset DOCKER_HOST to use the hawser context, or point it at " +
				"the Hawser pipe (npipe:////./pipe/docker_engine) if you set it deliberately."
			return r
		}

		if ctx != "hawser" {
			r := result(c, Warn, fmt.Sprintf("the active docker context is %q, not hawser", ctx))
			r.Detail = []string{"  source: " + src}
			r.Remedy = "run `docker context use hawser` so docker talks to the Hawser engine."
			return r
		}

		return result(c, OK, fmt.Sprintf("docker context is hawser (%s)", src))
	}
	return c
}
