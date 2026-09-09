package doctor

import (
	"testing"

	"github.com/zcsizmadia/hawser/internal/runner"
	"github.com/zcsizmadia/hawser/internal/version"
)

func TestCheckRunner(t *testing.T) {
	healthy := runner.Facts{
		AutoLogonConfigured: true, AutoLogonUser: "r", CurrentUser: "r",
		AutostartRegistered: true, SupervisorRunning: true, Engine: "running",
	}
	cases := []struct {
		name   string
		mutate func(*runner.Facts)
		want   Status
	}{
		{"not a runner (no auto-logon) skips", func(f *runner.Facts) { f.AutoLogonConfigured = false }, Skip},
		{"all in place", func(*runner.Facts) {}, OK},
		{"plaintext password is a warning", func(f *runner.Facts) { f.PlaintextPassword = true }, Warn},
		{"missing autostart fails", func(f *runner.Facts) { f.AutostartRegistered = false }, Fail},
		{"supervisor down fails", func(f *runner.Facts) { f.SupervisorRunning = false }, Fail},
		{"warning plus failure is a failure", func(f *runner.Facts) {
			f.PlaintextPassword = true
			f.Engine = "stopped"
		}, Fail},
	}
	c := checkRunner()
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := healthy
			tc.mutate(&f)
			r := c.Run(Facts{Runner: f})
			if r.Status != tc.want {
				t.Fatalf("status = %v, want %v (%s)", r.Status, tc.want, r.Summary)
			}
			if (tc.want == Warn || tc.want == Fail) && r.Remedy == "" {
				t.Error("a non-OK runner result must carry a remedy")
			}
		})
	}
}

func TestRunnerEngineState(t *testing.T) {
	if got := runnerEngineState(Facts{}); got != "not-installed" {
		t.Errorf("nil report = %q", got)
	}
	installed := func(reachable, idle bool) Facts {
		f := Facts{EngineReachable: reachable, EngineIdle: idle}
		f.Report = report(version.Report{Engine: version.EngineInfo{Installed: true}})
		return f
	}
	if got := runnerEngineState(installed(true, false)); got != "running" {
		t.Errorf("reachable = %q", got)
	}
	if got := runnerEngineState(installed(false, true)); got != "idle" {
		t.Errorf("idle = %q", got)
	}
	if got := runnerEngineState(installed(false, false)); got != "stopped" {
		t.Errorf("down = %q", got)
	}
}
