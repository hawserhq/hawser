package doctor

import (
	"fmt"

	"github.com/zcsizmadia/hawser/internal/runner"
)

// checkRunner folds `hawser runner check` (#150) into doctor, gated on the
// runner fingerprint: auto-logon configured. On an ordinary laptop it skips and
// points at the standalone command; on a runner it rolls the findings up into
// one result so a dead build agent is diagnosed with the rest of the machine.
func checkRunner() Check {
	c := Check{Name: "runner", Title: "unattended runner setup"}
	c.Run = func(f Facts) Result {
		if !f.Runner.AutoLogonConfigured {
			return result(c, Skip, "no auto-logon configured; not an unattended runner (`hawser runner check` evaluates anyway)")
		}
		findings := runner.Evaluate(f.Runner)
		worst := OK
		var detail []string
		remedy := ""
		for _, fd := range findings {
			switch fd.Status {
			case runner.Fail:
				worst = Fail
			case runner.Warn:
				if worst < Warn {
					worst = Warn
				}
			default:
				continue
			}
			detail = append(detail, fmt.Sprintf("  %-18s %s", fd.Name, fd.Summary))
			if remedy == "" {
				remedy = fd.Remedy
			}
		}
		switch worst {
		case Fail:
			r := result(c, Fail, "this runner will not bring the engine back after a reboot")
			r.Detail, r.Remedy = detail, remedy
			return r
		case Warn:
			r := result(c, Warn, "runner setup works but has a warning")
			r.Detail, r.Remedy = detail, remedy
			return r
		}
		return result(c, OK, "auto-logon, autostart, supervisor and engine are all in place")
	}
	return c
}

// runnerEngineState maps the gathered engine facts to the runner package's
// vocabulary.
func runnerEngineState(f Facts) string {
	switch {
	case f.Report == nil || !f.Report.Engine.Installed:
		return "not-installed"
	case f.EngineReachable:
		return "running"
	case f.EngineIdle:
		return "idle"
	default:
		return "stopped"
	}
}
