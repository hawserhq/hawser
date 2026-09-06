package doctor

import (
	"fmt"
	"strings"
)

// checkCredentialHelper verifies that every docker credential helper the CLI
// config references actually resolves on PATH. This is not hypothetical: a
// config pointing at docker-credential-desktop (or -wincred) that is not on
// PATH made `hawser migrate` fail live — the docker CLI shells out to the
// helper for `docker login`, pulls from private registries, and the save/load
// migrate uses, and a missing helper aborts all of them.
func checkCredentialHelper() Check {
	c := Check{Name: "credential-helper", Title: "docker credential helpers"}
	c.Run = func(f Facts) Result {
		if len(f.CredHelpers) == 0 {
			return result(c, OK, "no credential helper configured")
		}

		var missing []CredHelper
		detail := make([]string, 0, len(f.CredHelpers))
		for _, h := range f.CredHelpers {
			if h.Resolved {
				detail = append(detail, fmt.Sprintf("  %s -> %s (%s)", h.Binary, h.Path, h.Source))
			} else {
				detail = append(detail, fmt.Sprintf("  %s NOT FOUND (%s)", h.Binary, h.Source))
				missing = append(missing, h)
			}
		}

		if len(missing) > 0 {
			names := make([]string, len(missing))
			for i, h := range missing {
				names[i] = h.Binary
			}
			r := result(c, Fail, fmt.Sprintf("credential helper not on PATH: %s",
				strings.Join(names, ", ")))
			r.Detail = detail
			r.Remedy = "install the helper (Docker Desktop ships docker-credential-desktop; " +
				"docker-credential-wincred ships with the docker CLI) and put it on PATH, " +
				"or remove the reference from ~/.docker/config.json. docker login, private " +
				"pulls, and `hawser migrate` fail until the helper resolves."
			return r
		}

		r := result(c, OK, fmt.Sprintf("%d credential helper(s) resolve on PATH", len(f.CredHelpers)))
		r.Detail = detail
		return r
	}
	return c
}
