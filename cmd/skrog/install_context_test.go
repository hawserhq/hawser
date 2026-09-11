package main

import (
	"context"
	"strings"
	"testing"

	"github.com/wslkit/skrog/internal/dockerctx"
	"github.com/wslkit/skrog/internal/provision"
)

// fakeDocker answers `docker context inspect` from the test, so the ownership
// rule can be exercised without a docker CLI on the machine.
type fakeDocker struct {
	endpoint string
	err      error
}

func (f fakeDocker) Run(ctx context.Context, name string, args ...string) ([]byte, error) {
	if f.err != nil {
		return nil, f.err
	}
	return []byte(f.endpoint + "\n"), nil
}

func managerAt(endpoint string, err error) *dockerctx.Manager {
	return &dockerctx.Manager{Runner: fakeDocker{endpoint: endpoint, err: err}}
}

func TestContextIsOursWhenItStillPointsAtUs(t *testing.T) {
	m := &provision.Manifest{DockerContextHost: "npipe:////./pipe/docker_engine"}
	ours, why := contextIsOurs(context.Background(),
		managerAt("npipe:////./pipe/docker_engine", nil), m)
	if !ours {
		t.Errorf("ours = false (%s), want true", why)
	}
}

func TestContextIsNotOursWhenAnotherInstallTookIt(t *testing.T) {
	// The bug: uninstalling the second install removed the first one's
	// context, and `docker --context skrog` then failed for an install that
	// was still running perfectly.
	m := &provision.Manifest{DockerContextHost: "npipe:////./pipe/skrog_engine"}
	ours, why := contextIsOurs(context.Background(),
		managerAt("npipe:////./pipe/docker_engine", nil), m)
	if ours {
		t.Fatal("ours = true; a context pointing at another install must not be removed")
	}
	for _, want := range []string{"docker_engine", "skrog_engine", "another install"} {
		if !strings.Contains(why, want) {
			t.Errorf("reason %q should mention %q", why, want)
		}
	}
}

func TestContextIsOursOnAnInstallThatPredatesTheField(t *testing.T) {
	// Those machines were installed when one install was the only
	// possibility, so the context almost certainly is theirs — and leaving a
	// context pointed at a pipe nobody serves breaks every later docker
	// command, which is the worse of the two failures.
	ours, _ := contextIsOurs(context.Background(),
		managerAt("npipe:////./pipe/docker_engine", nil), &provision.Manifest{})
	if !ours {
		t.Error("ours = false for a manifest with no recorded endpoint")
	}
}

func TestContextIsOursWhenThereIsNoManifest(t *testing.T) {
	ours, _ := contextIsOurs(context.Background(),
		managerAt("npipe:////./pipe/docker_engine", nil), nil)
	if !ours {
		t.Error("ours = false with no manifest at all")
	}
}

func TestContextIsOursWhenTheEndpointCannotBeRead(t *testing.T) {
	// No docker CLI, or no context to inspect. Remove handles both, and
	// guessing "not ours" would leave a dangling context behind instead.
	m := &provision.Manifest{DockerContextHost: "npipe:////./pipe/skrog_engine"}
	ours, _ := contextIsOurs(context.Background(),
		managerAt("", &dockerctx.ErrNoDockerCLI{}), m)
	if !ours {
		t.Error("ours = false when the endpoint could not be read")
	}
}

func TestUninstallRestoresRatherThanRemovesWhenItTookTheContextOver(t *testing.T) {
	// The scenario the bug report describes, at the level the decision is
	// made: this install recorded that it took the context from another, so
	// the correct cleanup is to hand it back, not to delete it.
	m := &provision.Manifest{
		DockerContextHost:     "npipe:////./pipe/skrog_engine",
		DockerContextPrevious: "npipe:////./pipe/docker_engine",
	}
	ours, _ := contextIsOurs(context.Background(),
		managerAt("npipe:////./pipe/skrog_engine", nil), m)
	if !ours {
		t.Fatal("ours = false; the context does still point at this install")
	}
	if m.DockerContextPrevious == "" {
		t.Fatal("no previous endpoint recorded, so uninstall would delete instead of restore")
	}
}

func TestAnInstallThatCreatedTheContextRecordsNoPrevious(t *testing.T) {
	// The single-install machine, which is almost everyone: nothing to hand
	// back, so uninstall removes the context and leaves nothing dangling.
	m := &provision.Manifest{DockerContextHost: "npipe:////./pipe/docker_engine"}
	if m.DockerContextPrevious != "" {
		t.Error("a fresh install should record no previous endpoint")
	}
	ours, _ := contextIsOurs(context.Background(),
		managerAt("npipe:////./pipe/docker_engine", nil), m)
	if !ours {
		t.Error("ours = false for a context this install created and still owns")
	}
}
