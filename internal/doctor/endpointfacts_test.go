//go:build windows

package doctor

import (
	"context"
	"errors"
	"testing"

	"github.com/wslkit/skrog/internal/supervise"
	"github.com/wslkit/skrog/internal/version"
)

// #289: this wiring lived inline in Gather with no coverage at all. Deleting
// the context lookup reverted #283 in full and the whole suite stayed green.
func TestEndpointFacts(t *testing.T) {
	const (
		pipe   = `\\.\pipe\docker_engine`
		served = "npipe:////./pipe/docker_engine"
		other  = "npipe:////./pipe/dockerDesktopLinuxEngine"
	)

	record := func(p string) func(string) (supervise.Endpoint, bool) {
		return func(string) (supervise.Endpoint, bool) {
			return supervise.Endpoint{Pipe: p}, true
		}
	}
	noRecord := func(string) (supervise.Endpoint, bool) { return supervise.Endpoint{}, false }
	returns := func(v string) func(context.Context, string) (string, error) {
		return func(context.Context, string) (string, error) { return v, nil }
	}
	fails := func(context.Context, string) (string, error) {
		return "", errors.New("no docker CLI")
	}

	cases := []struct {
		name          string
		held          bool
		activeContext string
		read          func(string) (supervise.Endpoint, bool)
		lookup        func(context.Context, string) (string, error)
		wantServed    string
		wantActive    string
	}{
		{
			name: "supervisor running, context on the same pipe",
			held: true, activeContext: "default", read: record(pipe), lookup: returns(served),
			wantServed: served, wantActive: served,
		},
		{
			name: "supervisor running, context aimed elsewhere",
			held: true, activeContext: "desktop-linux", read: record(pipe), lookup: returns(other),
			wantServed: served, wantActive: other,
		},
		{
			// Nothing is bound, so there is no endpoint to compare against and
			// checkContext must fall back to the name.
			name: "no supervisor",
			held: false, activeContext: "default", read: record(pipe), lookup: returns(served),
			wantServed: "", wantActive: "",
		},
		{
			// The write may have failed (#288). Held is true but there is
			// nothing to report.
			name: "supervisor running, no record",
			held: true, activeContext: "default", read: noRecord, lookup: returns(served),
			wantServed: "", wantActive: "",
		},
		{
			// A context that cannot be inspected must not read as a match:
			// equality is what suppresses the warning.
			name: "lookup fails",
			held: true, activeContext: "default", read: record(pipe), lookup: fails,
			wantServed: served, wantActive: "",
		},
		{
			// DOCKER_HOST overrides the context, which version reports as empty.
			name: "no active context",
			held: true, activeContext: "", read: record(pipe), lookup: returns(served),
			wantServed: served, wantActive: "",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			gotServed, gotActive := endpointFacts(context.Background(), `C:\state`,
				tc.held, tc.activeContext, tc.read, tc.lookup)
			if gotServed != tc.wantServed {
				t.Errorf("served = %q, want %q", gotServed, tc.wantServed)
			}
			if gotActive != tc.wantActive {
				t.Errorf("active = %q, want %q", gotActive, tc.wantActive)
			}
		})
	}
}

// The pair has to survive into the check that uses it, or the wiring is
// decorative. This is the end of the #283 chain: record -> Facts -> verdict.
func TestEndpointFactsReachCheckContext(t *testing.T) {
	served, active := endpointFacts(context.Background(), `C:\state`, true, "default",
		func(string) (supervise.Endpoint, bool) {
			return supervise.Endpoint{Pipe: `\\.\pipe\docker_engine`}, true
		},
		func(context.Context, string) (string, error) {
			return "npipe:////./pipe/docker_engine", nil
		})

	f := Facts{
		Report: report(version.Report{
			Engine:        version.EngineInfo{Installed: true},
			Context:       "default",
			ContextSource: "implicit default",
		}),
		ServedEndpoint: served,
		ActiveEndpoint: active,
	}
	if got := checkContext().Run(f).Status; got != OK {
		t.Errorf("checkContext = %v, want ok — the gathered endpoints match, so "+
			"docker is reaching the Skrog engine under a different context name", got)
	}
}
