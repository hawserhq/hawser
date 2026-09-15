package doctor

import (
	"strings"
	"testing"
)

func runWslcCheck(t *testing.T, w WslcInfo) Result {
	t.Helper()
	return checkWslc().Run(Facts{Wslc: w})
}

// A distro install must not see this check at all. A report people read top to
// bottom should not carry an "ok" about a backend the machine does not use.
func TestWslcCheckSkipsOnTheDistroBackend(t *testing.T) {
	r := runWslcCheck(t, WslcInfo{Applicable: false})
	if r.Status != Skip {
		t.Errorf("status = %v, want Skip", r.Status)
	}
}

// The one genuine failure: without a usable CLI nothing on this backend works,
// and the remedy is the command that fixes it.
func TestWslcCheckFailsWhenTheCLIIsUnusable(t *testing.T) {
	r := runWslcCheck(t, WslcInfo{Applicable: true, CLIErr: "exec: wslc.exe not found"})
	if r.Status != Fail {
		t.Errorf("status = %v, want Fail", r.Status)
	}
	if !strings.Contains(r.Remedy, "wsl --update") {
		t.Errorf("remedy does not name the fix: %q", r.Remedy)
	}
}

// A terminated session is the normal resting state, not a problem: the next
// docker command starts one. Warning here would cry wolf on a healthy machine.
func TestWslcCheckPassesWithNoSessionRunning(t *testing.T) {
	r := runWslcCheck(t, WslcInfo{Applicable: true, CLIVersion: "2.9.11.0"})
	if r.Status != OK {
		t.Errorf("status = %v, want OK — no session is the resting state", r.Status)
	}
	if !strings.Contains(strings.Join(r.Detail, "\n"), "none running") {
		t.Errorf("detail does not say the session is absent: %v", r.Detail)
	}
}

// Likewise a missing agent: the VM's root is a tmpfs overlay, so an
// idle-termination discards it and the next connection re-places it.
func TestWslcCheckPassesWithNoAgent(t *testing.T) {
	r := runWslcCheck(t, WslcInfo{
		Applicable: true, CLIVersion: "2.9.11.0",
		Session: "wslc-cli-me", SessionUp: true, AgentUp: false,
	})
	if r.Status != OK {
		t.Errorf("status = %v, want OK", r.Status)
	}
	joined := strings.Join(r.Detail, "\n")
	if !strings.Contains(joined, "re-placed") {
		t.Errorf("detail does not explain that the agent returns: %v", r.Detail)
	}
}

func TestWslcCheckReportsAHealthySession(t *testing.T) {
	r := runWslcCheck(t, WslcInfo{
		Applicable: true, CLIVersion: "2.9.11.0",
		Session: "wslc-cli-me", SessionUp: true, AgentUp: true,
	})
	if r.Status != OK {
		t.Errorf("status = %v, want OK", r.Status)
	}
	joined := strings.Join(r.Detail, "\n")
	for _, want := range []string{"2.9.11.0", "wslc-cli-me", "running"} {
		if !strings.Contains(joined, want) {
			t.Errorf("detail is missing %q: %v", want, r.Detail)
		}
	}
	if !strings.Contains(r.Summary, "wslc-cli-me") {
		t.Errorf("summary does not name the session: %q", r.Summary)
	}
}

// A probe that failed must say so rather than being reported as "no session":
// "could not tell" and "there is none" call for different actions.
func TestWslcCheckSurfacesAProbeError(t *testing.T) {
	r := runWslcCheck(t, WslcInfo{
		Applicable: true, CLIVersion: "2.9.11.0", ProbeErr: "access denied",
	})
	if !strings.Contains(strings.Join(r.Detail, "\n"), "access denied") {
		t.Errorf("the probe error is not reported: %v", r.Detail)
	}
}

// gatherWslc must do nothing at all on a distro install -- not even look for
// wslc.exe. This is what keeps doctor's cost unchanged for everyone else.
func TestGatherWslcIsInertOnTheDistroBackend(t *testing.T) {
	got := gatherWslc(t.Context(), "distro")
	if got.Applicable {
		t.Error("gatherWslc reported applicable for a distro install")
	}
	if got.CLIVersion != "" || got.CLIErr != "" || got.Session != "" {
		t.Errorf("gatherWslc probed on a distro install: %+v", got)
	}
}
