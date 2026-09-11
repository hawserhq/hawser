package doctor

import "github.com/hawserhq/hawser/internal/supervise"

// checkSupervisor cross-checks two independent signals of engine health: the
// single-instance lock (a supervisor is running) and whether the engine
// actually answers. Their disagreement is exactly the class the old named-mutex
// bug produced — `hawser status` reporting a running supervisor as stopped —
// which is why doctor derives the verdict from both rather than trusting either.
func checkSupervisor() Check {
	c := Check{Name: "supervisor", Title: "supervisor / engine agreement"}
	c.Run = func(f Facts) Result {
		if !f.Report.Engine.Installed {
			return result(c, Skip, "no engine installed")
		}

		held, reachable, idle := f.SupervisorHeld, f.EngineReachable, f.EngineIdle
		desiredStopped := f.Desired == string(supervise.DesiredStopped)

		if desiredStopped {
			if held {
				return result(c, OK, "supervisor running; engine stopped by request (`hawser start` to resume)")
			}
			return result(c, OK, "engine stopped by request; no supervisor running")
		}

		switch {
		case held && reachable:
			return result(c, OK, "supervisor running and engine reachable")
		case held && idle:
			return result(c, OK, "supervisor running; engine idle-stopped (a docker command wakes it)")
		case held && !reachable:
			r := result(c, Fail, "supervisor is running but the engine does not answer")
			r.Remedy = "run `hawser restart`; if it recurs, check the supervisor log in the state dir."
			return r
		case !held && reachable:
			r := result(c, Warn, "the engine answers but no supervisor lock is held")
			r.Detail = []string{"an engine reachable with no supervisor tracking it is orphaned or externally started"}
			r.Remedy = "run `hawser start` so the supervisor manages it (health checks, idle stop)."
			return r
		default: // !held && !reachable
			r := result(c, Warn, "no supervisor running and the engine is down")
			r.Remedy = "run `hawser start` (or `hawser autostart enable` to start it at logon)."
			return r
		}
	}
	return c
}
