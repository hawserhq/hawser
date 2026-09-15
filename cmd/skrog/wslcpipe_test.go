//go:build windows

package main

import (
	"strings"
	"testing"

	"github.com/wslkit/skrog/internal/pipeproxy"
)

// The wslc backend must not compete for \\.\pipe\docker_engine (#335).
//
// It used to call pipeproxy.SelectPipeName, which prefers the default pipe and
// falls back only when something else already holds it. That made the two
// backends race for plain `docker`: whichever bridge started first owned it,
// and on a machine where Docker Desktop held the default they would instead
// both land on the fallback pipe and the second would fail to listen at all.
func TestWslcDefaultsToItsOwnPipe(t *testing.T) {
	name, reason := selectWslcPipe("")

	if name != pipeproxy.WslcPipeName {
		t.Errorf("selectWslcPipe(\"\") = %q, want %q", name, pipeproxy.WslcPipeName)
	}
	if name == pipeproxy.DefaultPipeName {
		t.Error("the wslc backend took the default pipe; it must coexist with the distro backend")
	}
	if name == pipeproxy.FallbackPipeName {
		t.Error("the wslc backend took the distro backend's fallback pipe")
	}
	if reason == "" {
		t.Error("the choice must come with a reason; it decides whether DOCKER_HOST is needed")
	}
}

// An explicit --pipe still wins, including the default one: on a machine with
// no distro install, serving the wslc session on \\.\pipe\docker_engine is a
// reasonable thing to ask for.
func TestWslcHonoursAnExplicitPipe(t *testing.T) {
	name, reason := selectWslcPipe(`\\.\pipe\custom_thing`)
	if name != `\\.\pipe\custom_thing` {
		t.Errorf("explicit pipe not honoured: got %q", name)
	}
	if !strings.Contains(reason, "explicit") {
		t.Errorf("reason = %q, want it to say the caller asked", reason)
	}
}

// The three names are distinct, which is what keeps the backends apart.
func TestPipeNamesAreDistinct(t *testing.T) {
	names := map[string]string{
		"default":  pipeproxy.DefaultPipeName,
		"fallback": pipeproxy.FallbackPipeName,
		"wslc":     pipeproxy.WslcPipeName,
	}
	seen := map[string]string{}
	for label, n := range names {
		if prev, dup := seen[n]; dup {
			t.Errorf("%s and %s are both %q", prev, label, n)
		}
		seen[n] = label
	}
}

// DockerHostFor must render the wslc pipe into something the CLI accepts,
// since the banner prints it for the user to paste.
func TestWslcPipeRendersAsADockerHost(t *testing.T) {
	got := pipeproxy.DockerHostFor(pipeproxy.WslcPipeName)
	const want = "npipe:////./pipe/skrog_wslc"
	if got != want {
		t.Errorf("DockerHostFor(%q) = %q, want %q", pipeproxy.WslcPipeName, got, want)
	}
}
