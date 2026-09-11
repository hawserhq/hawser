package doctor

import (
	"testing"

	"github.com/wslkit/skrog/internal/supervise"
	"github.com/wslkit/skrog/internal/version"
)

func TestCheckSupervisor(t *testing.T) {
	installed := version.Report{Engine: version.EngineInfo{Installed: true}}
	base := func(mut func(*Facts)) Facts {
		f := Facts{Report: report(installed), Desired: string(supervise.DesiredRunning)}
		mut(&f)
		return f
	}

	cases := []struct {
		name string
		f    Facts
		want Status
	}{
		{"not installed", Facts{Report: report(version.Report{})}, Skip},
		{"held and reachable", base(func(f *Facts) { f.SupervisorHeld = true; f.EngineReachable = true }), OK},
		{"held and idle", base(func(f *Facts) { f.SupervisorHeld = true; f.EngineIdle = true }), OK},
		{"held but unreachable", base(func(f *Facts) { f.SupervisorHeld = true }), Fail},
		{"reachable but not held", base(func(f *Facts) { f.EngineReachable = true }), Warn},
		{"neither", base(func(f *Facts) {}), Warn},
		{"stopped by request, held", base(func(f *Facts) { f.Desired = string(supervise.DesiredStopped); f.SupervisorHeld = true }), OK},
		{"stopped by request, not held", base(func(f *Facts) { f.Desired = string(supervise.DesiredStopped) }), OK},
	}
	c := checkSupervisor()
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := c.Run(tc.f).Status; got != tc.want {
				t.Fatalf("status = %v, want %v", got, tc.want)
			}
		})
	}
}
