package doctor

import (
	"testing"

	"github.com/zcsizmadia/hawser/internal/version"
)

func TestCheckEngine(t *testing.T) {
	c := checkEngine()

	if got := c.Run(Facts{Report: report(version.Report{})}).Status; got != Warn {
		t.Errorf("not installed: got %v, want Warn", got)
	}

	installed := Facts{Report: report(version.Report{
		WSL:    "2.7.8",
		Engine: version.EngineInfo{Installed: true, Version: "29.7.2", Distro: "hawser-engine", WSLAtInstall: "2.7.8"},
	})}
	if got := c.Run(installed).Status; got != OK {
		t.Errorf("healthy: got %v, want OK", got)
	}

	skewed := Facts{Report: report(version.Report{
		WSL:    "2.8.0",
		Engine: version.EngineInfo{Installed: true, Version: "29.7.2", Distro: "hawser-engine", WSLAtInstall: "2.7.8"},
	})}
	if got := c.Run(skewed).Status; got != Warn {
		t.Errorf("skew: got %v, want Warn", got)
	}
}
