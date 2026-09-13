package dockerctx_test

import (
	"context"
	"testing"

	"github.com/wslkit/skrog/internal/dockerctx"
)

// #288: the Manager merges stderr into the command output, and docker writes to
// stderr on success -- most commonly `WARNING: Error loading config file: ...`
// on every invocation when ~/.docker/config.json is malformed. The endpoint is
// compared with == against what the supervisor bound, so a warning prepended to
// the value turns into a spurious "your context is not skrog".
func TestEndpointOfIgnoresWarningsOnStderr(t *testing.T) {
	const want = "npipe:////./pipe/docker_engine"
	cases := []struct {
		name, output string
	}{
		{"clean", want},
		{"warning first", "WARNING: Error loading config file: unexpected end of JSON input\n" + want},
		{"two warnings", "WARNING: one\nWARNING: two\n" + want},
		{"trailing blank line", want + "\n"},
		{"crlf", "WARNING: one\r\n" + want + "\r\n"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newFakeDocker().on("context inspect", tc.output, nil)
			m := &dockerctx.Manager{Runner: f}
			got, err := m.EndpointOf(context.Background(), "default")
			if err != nil {
				t.Fatal(err)
			}
			if got != want {
				t.Errorf("EndpointOf = %q, want %q", got, want)
			}
		})
	}
}
