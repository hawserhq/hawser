package dockerctx_test

import (
	"context"
	"strings"
	"testing"

	"github.com/wslkit/skrog/internal/dockerctx"
)

// The wslc backend gets its own context so the two backends coexist (#335).
// Before this, both called Ensure and whichever bridge started last repointed
// the single `skrog` context at itself — silently changing which engine a
// user's `docker` command reached.
func TestEnsureNamedCreatesASecondContext(t *testing.T) {
	f := newFakeDocker().
		on("context ls", "default\nskrog", nil).
		on("context create", "", nil)
	m := &dockerctx.Manager{Runner: f}

	err := m.EnsureNamed(context.Background(), dockerctx.WslcName,
		"Skrog via a WSL container session (experimental)",
		"npipe:////./pipe/skrog_wslc")
	if err != nil {
		t.Fatal(err)
	}

	c := f.callWith("context create " + dockerctx.WslcName)
	if c == nil {
		t.Fatalf("expected `context create %s`, calls: %v", dockerctx.WslcName, f.calls)
	}
	if joined := strings.Join(c, " "); !strings.Contains(joined, "host=npipe:////./pipe/skrog_wslc") {
		t.Errorf("create args do not carry the wslc pipe: %s", joined)
	}
	// The distro context already exists in the listing above and must be left
	// exactly as it was.
	if f.called("context update " + dockerctx.Name) {
		t.Error("the distro context was updated; the two backends must not share one")
	}
}

func TestEnsureNamedUpdatesItsOwnContextOnly(t *testing.T) {
	f := newFakeDocker().
		on("context ls", "default\nskrog\nskrog-wslc", nil).
		on("context inspect", "npipe:////./pipe/stale", nil).
		on("context update", "", nil)
	m := &dockerctx.Manager{Runner: f}

	if err := m.EnsureNamed(context.Background(), dockerctx.WslcName, "d",
		"npipe:////./pipe/skrog_wslc"); err != nil {
		t.Fatal(err)
	}
	c := f.callWith("context update " + dockerctx.WslcName)
	if c == nil {
		t.Fatalf("expected `context update %s`, calls: %v", dockerctx.WslcName, f.calls)
	}
	if f.called("context create") {
		t.Error("an existing context must be updated, not recreated")
	}
}

// An endpoint that already matches must not be rewritten: Ensure runs on every
// bridge start, and a no-op start should not touch the user's docker config.
func TestEnsureNamedIsANoOpWhenAlreadyCorrect(t *testing.T) {
	f := newFakeDocker().
		on("context ls", "default\nskrog-wslc", nil).
		on("context inspect", "npipe:////./pipe/skrog_wslc", nil)
	m := &dockerctx.Manager{Runner: f}

	if err := m.EnsureNamed(context.Background(), dockerctx.WslcName, "d",
		"npipe:////./pipe/skrog_wslc"); err != nil {
		t.Fatal(err)
	}
	if f.called("context update") || f.called("context create") {
		t.Errorf("a correct context was rewritten; calls: %v", f.calls)
	}
}

// Ensure is EnsureNamed with the distro identity, and that must not drift:
// every existing install depends on this exact name and description.
func TestEnsureStillTargetsTheDistroContext(t *testing.T) {
	f := newFakeDocker().
		on("context ls", "default", nil).
		on("context create", "", nil)
	m := &dockerctx.Manager{Runner: f}

	if err := m.Ensure(context.Background(), "npipe:////./pipe/docker_engine"); err != nil {
		t.Fatal(err)
	}
	c := f.callWith("context create " + dockerctx.Name)
	if c == nil {
		t.Fatalf("Ensure no longer creates %q; calls: %v", dockerctx.Name, f.calls)
	}
	if joined := strings.Join(c, " "); !strings.Contains(joined, "Skrog engine (WSL2)") {
		t.Errorf("the distro context description changed: %s", joined)
	}
}

// The two names must stay distinct, which is the whole point.
func TestBackendContextNamesDiffer(t *testing.T) {
	if dockerctx.Name == dockerctx.WslcName {
		t.Fatalf("both backends would write context %q", dockerctx.Name)
	}
}

func TestEnsureNamedRejectsAnEmptyName(t *testing.T) {
	m := &dockerctx.Manager{Runner: newFakeDocker()}
	if err := m.EnsureNamed(context.Background(), "", "d", "npipe://x"); err == nil {
		t.Fatal("want an error for an empty context name")
	}
}
