package doctor

import (
	"strings"
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

// A different context name is not a different engine (#283). When Skrog takes
// the default pipe -- the normal case on a machine without Docker Desktop --
// the stock `default` context points at exactly what the supervisor bound, and
// `docker` reaches Skrog with no context at all.
func TestCheckContextComparesEndpointsNotNames(t *testing.T) {
	const skrogPipe = "npipe:////./pipe/docker_engine"
	const otherPipe = "npipe:////./pipe/dockerDesktopLinuxEngine"

	facts := func(ctxName, active, served string) Facts {
		return Facts{
			Report: report(version.Report{
				Engine:        version.EngineInfo{Installed: true},
				Context:       ctxName,
				ContextSource: "docker config.json",
			}),
			ActiveEndpoint: active,
			ServedEndpoint: served,
		}
	}

	cases := []struct {
		name string
		f    Facts
		want Status
		why  string
	}{
		{
			"default context on the pipe skrog serves", facts("default", skrogPipe, skrogPipe), OK,
			"docker already reaches the Skrog engine; the remedy would change nothing",
		},
		{
			"foreign context on another engine", facts("desktop-linux", otherPipe, skrogPipe), Warn,
			"a different endpoint really is a different engine",
		},
		{
			"no supervisor, so no served endpoint to compare", facts("default", "", ""), Warn,
			"with nothing bound there is no endpoint to match, so the name is all there is",
		},
		{
			"endpoint unknown but supervisor running", facts("default", "", skrogPipe), Warn,
			"an uninspectable context must not be assumed to match",
		},
	}

	c := checkContext()
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := c.Run(tc.f).Status; got != tc.want {
				t.Errorf("status = %v, want %v -- %s", got, tc.want, tc.why)
			}
		})
	}
}

// The OK result has to show the endpoint, because "your context is not skrog
// and that is fine" is only believable with the reason attached.
func TestCheckContextOKNamesTheEndpoint(t *testing.T) {
	const pipe = "npipe:////./pipe/docker_engine"
	r := checkContext().Run(Facts{
		Report: report(version.Report{
			Engine:        version.EngineInfo{Installed: true},
			Context:       "default",
			ContextSource: "docker config.json",
		}),
		ActiveEndpoint: pipe,
		ServedEndpoint: pipe,
	})
	if r.Status != OK {
		t.Fatalf("status = %v, want OK", r.Status)
	}
	if !strings.Contains(strings.Join(r.Detail, "\n"), pipe) {
		t.Errorf("detail does not name the endpoint: %v", r.Detail)
	}
	if r.Remedy != "" {
		t.Errorf("an OK result should not carry a remedy, got %q", r.Remedy)
	}
}
