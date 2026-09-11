package doctor

import (
	"testing"

	"github.com/wslkit/skrog/internal/version"
)

func TestCheckContext(t *testing.T) {
	installed := func(ctxName, src string) Facts {
		return Facts{Report: report(version.Report{
			Engine:        version.EngineInfo{Installed: true},
			Context:       ctxName,
			ContextSource: src,
		})}
	}

	cases := []struct {
		name string
		f    Facts
		want Status
	}{
		{"no engine", Facts{Report: report(version.Report{})}, Skip},
		{"skrog context", installed("skrog", "docker config.json"), OK},
		{"foreign context", installed("desktop-linux", "docker config.json"), Warn},
		{"docker_host override", installed("", "DOCKER_HOST=tcp://x"), Warn},
	}
	c := checkContext()
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := c.Run(tc.f).Status; got != tc.want {
				t.Fatalf("status = %v, want %v", got, tc.want)
			}
		})
	}
}
