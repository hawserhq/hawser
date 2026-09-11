package doctor

import (
	"fmt"
	"strings"
	"time"

	"github.com/hawserhq/hawser/internal/remote"
)

// certWarnWindow is how far ahead an expiring remote client certificate is
// flagged: two weeks is enough to get a new one issued without a fire drill.
const certWarnWindow = 14 * 24 * time.Hour

// checkContext verifies docker is actually pointed at a Hawser engine — the
// local one, or a registered remote (#138). A machine with Docker Desktop's
// context still selected, or a stray DOCKER_HOST, talks to something other than
// Hawser while looking like it should work — a common source of "hawser is
// broken" reports that are really "docker is aimed elsewhere."
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

		if ctx == "hawser" {
			return result(c, OK, fmt.Sprintf("docker context is hawser (%s)", src))
		}

		// A hawser-<name> context is a remote engine. Known and healthy is OK;
		// the failure that matters is a client certificate about to (or already)
		// stop working, which otherwise surfaces as an opaque TLS error.
		if name, ok := strings.CutPrefix(ctx, remote.ContextPrefix); ok {
			for _, rem := range f.Remotes {
				if rem.Name != name {
					continue
				}
				switch {
				case !rem.CertNotAfter.IsZero() && time.Now().After(rem.CertNotAfter):
					r := result(c, Fail, fmt.Sprintf("remote %q client certificate expired on %s",
						name, rem.CertNotAfter.Format("2006-01-02")))
					r.Detail = []string{"  host: " + rem.Host}
					r.Remedy = "on the server run `hawser serve cert` for a fresh client certificate, " +
						"copy it over, then re-run `hawser remote add " + name + " ...`; " +
						"or `hawser remote use local` to go back to the local engine."
					return r
				case !rem.CertNotAfter.IsZero() && time.Until(rem.CertNotAfter) < certWarnWindow:
					r := result(c, Warn, fmt.Sprintf("remote %q client certificate expires %s",
						name, rem.CertNotAfter.Format("2006-01-02")))
					r.Detail = []string{"  host: " + rem.Host}
					r.Remedy = "renew before it lapses: `hawser serve cert` on the server, then " +
						"`hawser remote add " + name + " ...` with the new client certificate."
					return r
				}
				return result(c, OK, fmt.Sprintf("docker context is remote:%s (%s)", name, rem.Host))
			}
			r := result(c, Warn, fmt.Sprintf("context %q looks like a Hawser remote, but none is registered", ctx))
			r.Detail = []string{"  source: " + src}
			r.Remedy = "register it with `hawser remote add " + name + " --host ... --certs ...`, " +
				"or `hawser remote use local`."
			return r
		}

		r := result(c, Warn, fmt.Sprintf("the active docker context is %q, not hawser", ctx))
		r.Detail = []string{"  source: " + src}
		r.Remedy = "run `hawser remote use local` (or `docker context use hawser`) so docker " +
			"talks to the Hawser engine."
		return r
	}
	return c
}
