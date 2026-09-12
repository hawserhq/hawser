package runner

import (
	"strings"
	"testing"
	"time"
)

func powerFinding(t *testing.T, p PowerFacts) Finding {
	t.Helper()
	for _, f := range Evaluate(Facts{Engine: "running", Power: p}) {
		if f.Name == "power" {
			return f
		}
	}
	t.Fatal("Evaluate emitted no power finding")
	return Finding{}
}

// A runner that suspends mid-job fails it in a way that looks like a Skrog
// fault, and `runner check` is what an operator runs to satisfy themselves the
// host is set up — so it has to say something about sleep (#268).
func TestPowerFinding(t *testing.T) {
	for _, tc := range []struct {
		name     string
		power    PowerFacts
		want     Status
		contains string
	}{
		{
			name:     "never sleeps",
			power:    PowerFacts{Known: true},
			want:     OK,
			contains: "does not sleep or hibernate",
		},
		{
			name:     "sleeps on AC",
			power:    PowerFacts{Known: true, StandbyAfter: time.Hour},
			want:     Warn,
			contains: "sleeps after 1h0m0s",
		},
		{
			name:     "hibernates on AC",
			power:    PowerFacts{Known: true, HibernateAfter: 30 * time.Minute},
			want:     Warn,
			contains: "hibernates after 30m0s",
		},
		{
			name:     "both, and both are named so the operator knows what to change",
			power:    PowerFacts{Known: true, StandbyAfter: time.Hour, HibernateAfter: 2 * time.Hour},
			want:     Warn,
			contains: "sleeps after 1h0m0s, hibernates after 2h0m0s",
		},
		{
			// A probe that did not happen must never read as one that passed —
			// the failure mode this whole finding exists to correct.
			name:     "unreadable",
			power:    PowerFacts{Reason: "powercfg not found"},
			want:     Warn,
			contains: "unknown",
		},
		{
			name:     "on battery, with AC timeouts off",
			power:    PowerFacts{Known: true, OnBattery: true},
			want:     OK,
			contains: "currently on battery",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := powerFinding(t, tc.power)
			if f.Status != tc.want {
				t.Errorf("status = %s, want %s (%s)", f.Status, tc.want, f.Summary)
			}
			if !strings.Contains(f.Summary, tc.contains) {
				t.Errorf("summary %q does not mention %q", f.Summary, tc.contains)
			}
			if tc.want == Warn && f.Remedy == "" {
				t.Error("a warning with no remedy is a warning nobody can act on")
			}
		})
	}
}

// Warn, never Fail: plenty of runners are desktops that will never sleep, and
// a check that fails a healthy host is one people learn to ignore.
func TestSleepingMachineIsStillReady(t *testing.T) {
	findings := Evaluate(Facts{
		AutoLogonConfigured: true, AutostartRegistered: true, SupervisorRunning: true,
		Engine: "running",
		Power:  PowerFacts{Known: true, StandbyAfter: time.Hour},
	})
	if !Ready(findings) {
		t.Error("a machine that sleeps is reported as not ready; this finding must warn, not fail")
	}
}

// A caller that did not probe gets no power finding, rather than a fabricated
// "unknown". ReadPower never returns the zero value — it sets Known or Reason
// — so the empty case means "this caller did not ask", which is a different
// claim from "the probe failed". Both real callers probe.
func TestNoPowerFactsMeansNoPowerFinding(t *testing.T) {
	for _, f := range Evaluate(Facts{Engine: "running"}) {
		if f.Name == "power" {
			t.Errorf("a caller that supplied no power facts got a %s finding: %s", f.Status, f.Summary)
		}
	}
}

// The guarantee the branch above depends on.
func TestReadPowerNeverReturnsTheZeroValue(t *testing.T) {
	if p := ReadPower(); p == (PowerFacts{}) {
		t.Error("ReadPower returned the zero value, which Evaluate reads as 'not probed' and stays silent about")
	}
}
