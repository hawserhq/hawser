package doctor

import (
	"context"
	"fmt"

	"github.com/wslkit/skrog/internal/version"
	"github.com/wslkit/skrog/internal/wslc"
)

// WslcInfo is what doctor could learn about a WSL container session backend
// (#335). Gathered only on an install that uses it, so a distro machine pays
// nothing — not even the cost of finding out whether wslc.exe exists.
type WslcInfo struct {
	// Applicable is true only when the install manifest says this machine's
	// engine is a wslc session. Every field below is meaningless otherwise.
	Applicable bool
	// CLIVersion is what wslc.exe reports; CLIErr is why it could not be asked.
	CLIVersion string
	CLIErr     string
	// Session is the session Skrog would use, empty when none is running.
	// SessionUp distinguishes "none running" from "could not tell".
	Session   string
	SessionUp bool
	// AgentUp is whether Skrog's guest agent is in that session. A session can
	// be up without it: the VM's root filesystem is a tmpfs overlay, so an
	// idle-termination discards the agent and the next connection re-places it.
	AgentUp bool
	// ProbeErr is why the session could not be inspected.
	ProbeErr string
}

// gatherWslc probes the session backend, and only when it is the one in use.
//
// It never creates a session. Doctor must not boot an engine to describe it
// (#82), and on this backend that would mean starting a whole VM — turning a
// diagnostic into an ~820 MB side effect.
func gatherWslc(ctx context.Context, backend string) WslcInfo {
	info := WslcInfo{Applicable: backend == version.BackendWslc}
	if !info.Applicable {
		return info
	}

	l := wslc.New()
	ver, err := l.Version(ctx)
	if err != nil {
		info.CLIErr = err.Error()
		return info
	}
	info.CLIVersion = ver

	sessions, err := l.Sessions(ctx)
	if err != nil {
		info.ProbeErr = err.Error()
		return info
	}
	if len(sessions) == 0 {
		return info
	}

	want := wslc.DefaultSessionName()
	info.Session = sessions[0].DisplayName
	for _, s := range sessions {
		if s.DisplayName == wslc.SessionName || s.DisplayName == want {
			info.Session = s.DisplayName
			break
		}
	}
	info.SessionUp = true

	if up, err := l.AgentRunning(ctx, info.Session); err != nil {
		info.ProbeErr = err.Error()
	} else {
		info.AgentUp = up
	}
	return info
}

// checkWslc reports the state of the WSL container session this install serves.
//
// Skipped entirely on the distro backend rather than passing, because a check
// that says "ok" about a backend the machine does not use is noise in a report
// people read top to bottom.
//
// The one genuine failure is wslc.exe being unusable: nothing works without it.
// Everything else is reported and not judged — a terminated session is the
// normal resting state, and a missing agent is re-placed on the next
// connection, so warning about either would cry wolf on a healthy machine.
func checkWslc() Check {
	c := Check{Name: "wslc-session", Title: "WSL container session"}
	c.Run = func(f Facts) Result {
		w := f.Wslc
		if !w.Applicable {
			return result(c, Skip, "this install uses the engine distro backend")
		}
		if w.CLIErr != "" {
			r := result(c, Fail, "wslc.exe is not usable: "+w.CLIErr)
			r.Remedy = "`wsl --update` installs it; WSL 2.9.3 or newer is required."
			return r
		}

		var detail []string
		detail = append(detail, "  wslc              "+w.CLIVersion)
		switch {
		case w.ProbeErr != "":
			detail = append(detail, "  session           could not be inspected: "+w.ProbeErr)
		case !w.SessionUp:
			detail = append(detail, "  session           none running (one starts on the next docker command)")
		default:
			detail = append(detail, "  session           "+w.Session)
			if w.AgentUp {
				detail = append(detail, "  guest agent       running")
			} else {
				detail = append(detail, "  guest agent       not running (re-placed on the next connection)")
			}
		}

		r := result(c, OK, fmt.Sprintf("engine backend wslc, session %s",
			orNone(w.Session)))
		r.Detail = detail
		return r
	}
	return c
}

func orNone(s string) string {
	if s == "" {
		return "(none running)"
	}
	return s
}
