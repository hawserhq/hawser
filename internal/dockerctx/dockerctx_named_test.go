package dockerctx_test

import (
	"context"
	"strings"
	"testing"

	"github.com/wslkit/skrog/internal/dockerctx"
)

// The remote-engine contexts (#138) reuse fakeDocker from dockerctx_test.go.

func TestCreateTLSCreatesWithAllMaterial(t *testing.T) {
	f := newFakeDocker().
		on("context ls", "default\nskrog", nil).
		on("context create", "", nil)
	m := &dockerctx.Manager{Runner: f}

	err := m.CreateTLS(context.Background(), "skrog-desk", "Skrog remote engine desk",
		"tcp://desktop:2376", `C:\r\ca.pem`, `C:\r\cert.pem`, `C:\r\key.pem`)
	if err != nil {
		t.Fatal(err)
	}
	c := f.callWith("context create skrog-desk")
	if c == nil {
		t.Fatalf("expected `context create skrog-desk`, calls: %v", f.calls)
	}
	joined := strings.Join(c, " ")
	// One --docker spec carrying host + the three certificate paths.
	for _, want := range []string{"host=tcp://desktop:2376", `ca=C:\r\ca.pem`, `cert=C:\r\cert.pem`, `key=C:\r\key.pem`} {
		if !strings.Contains(joined, want) {
			t.Errorf("create args missing %q: %s", want, joined)
		}
	}
	if f.called("context update") {
		t.Error("a new context must be created, not updated")
	}
}

func TestCreateTLSUpdatesExisting(t *testing.T) {
	f := newFakeDocker().
		on("context ls", "default\nskrog-desk", nil).
		on("context update", "", nil)
	m := &dockerctx.Manager{Runner: f}
	if err := m.CreateTLS(context.Background(), "skrog-desk", "d", "tcp://h:2376", "a", "b", "c"); err != nil {
		t.Fatal(err)
	}
	if !f.called("context update skrog-desk") || f.called("context create") {
		t.Errorf("existing context must be updated in place, calls: %v", f.calls)
	}
}

func TestRemoveNamedRestoresWhenCurrent(t *testing.T) {
	// Docker refuses to remove the context in use, so RemoveNamed must switch
	// away first — to the local skrog context, not "default".
	f := newFakeDocker().
		on("context ls", "default\nskrog\nskrog-desk", nil).
		on("context show", "skrog-desk", nil).
		on("context use", "", nil).
		on("context rm", "", nil)
	m := &dockerctx.Manager{Runner: f}
	if err := m.RemoveNamed(context.Background(), "skrog-desk", "skrog"); err != nil {
		t.Fatal(err)
	}
	if !f.called("context use skrog") {
		t.Errorf("should switch to skrog before removing, calls: %v", f.calls)
	}
	if !f.called("context rm skrog-desk") {
		t.Errorf("should remove the context, calls: %v", f.calls)
	}
}

func TestRemoveNamedMissingIsSuccess(t *testing.T) {
	f := newFakeDocker().on("context ls", "default\nskrog", nil)
	m := &dockerctx.Manager{Runner: f}
	if err := m.RemoveNamed(context.Background(), "skrog-gone", "skrog"); err != nil {
		t.Fatalf("removing an absent context should succeed: %v", err)
	}
	if f.called("context rm") {
		t.Error("nothing to remove, rm must not be called")
	}
}

func TestExistsNamed(t *testing.T) {
	f := newFakeDocker().on("context ls", "default\nskrog-a\nskrog-b", nil)
	m := &dockerctx.Manager{Runner: f}
	if ok, _ := m.ExistsNamed(context.Background(), "skrog-b"); !ok {
		t.Error("skrog-b should exist")
	}
	if ok, _ := m.ExistsNamed(context.Background(), "skrog-c"); ok {
		t.Error("skrog-c should not exist")
	}
}
