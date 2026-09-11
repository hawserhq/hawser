package dockerctx_test

import (
	"context"
	"strings"
	"testing"

	"github.com/hawserhq/hawser/internal/dockerctx"
)

// The remote-engine contexts (#138) reuse fakeDocker from dockerctx_test.go.

func TestCreateTLSCreatesWithAllMaterial(t *testing.T) {
	f := newFakeDocker().
		on("context ls", "default\nhawser", nil).
		on("context create", "", nil)
	m := &dockerctx.Manager{Runner: f}

	err := m.CreateTLS(context.Background(), "hawser-desk", "Hawser remote engine desk",
		"tcp://desktop:2376", `C:\r\ca.pem`, `C:\r\cert.pem`, `C:\r\key.pem`)
	if err != nil {
		t.Fatal(err)
	}
	c := f.callWith("context create hawser-desk")
	if c == nil {
		t.Fatalf("expected `context create hawser-desk`, calls: %v", f.calls)
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
		on("context ls", "default\nhawser-desk", nil).
		on("context update", "", nil)
	m := &dockerctx.Manager{Runner: f}
	if err := m.CreateTLS(context.Background(), "hawser-desk", "d", "tcp://h:2376", "a", "b", "c"); err != nil {
		t.Fatal(err)
	}
	if !f.called("context update hawser-desk") || f.called("context create") {
		t.Errorf("existing context must be updated in place, calls: %v", f.calls)
	}
}

func TestRemoveNamedRestoresWhenCurrent(t *testing.T) {
	// Docker refuses to remove the context in use, so RemoveNamed must switch
	// away first — to the local hawser context, not "default".
	f := newFakeDocker().
		on("context ls", "default\nhawser\nhawser-desk", nil).
		on("context show", "hawser-desk", nil).
		on("context use", "", nil).
		on("context rm", "", nil)
	m := &dockerctx.Manager{Runner: f}
	if err := m.RemoveNamed(context.Background(), "hawser-desk", "hawser"); err != nil {
		t.Fatal(err)
	}
	if !f.called("context use hawser") {
		t.Errorf("should switch to hawser before removing, calls: %v", f.calls)
	}
	if !f.called("context rm hawser-desk") {
		t.Errorf("should remove the context, calls: %v", f.calls)
	}
}

func TestRemoveNamedMissingIsSuccess(t *testing.T) {
	f := newFakeDocker().on("context ls", "default\nhawser", nil)
	m := &dockerctx.Manager{Runner: f}
	if err := m.RemoveNamed(context.Background(), "hawser-gone", "hawser"); err != nil {
		t.Fatalf("removing an absent context should succeed: %v", err)
	}
	if f.called("context rm") {
		t.Error("nothing to remove, rm must not be called")
	}
}

func TestExistsNamed(t *testing.T) {
	f := newFakeDocker().on("context ls", "default\nhawser-a\nhawser-b", nil)
	m := &dockerctx.Manager{Runner: f}
	if ok, _ := m.ExistsNamed(context.Background(), "hawser-b"); !ok {
		t.Error("hawser-b should exist")
	}
	if ok, _ := m.ExistsNamed(context.Background(), "hawser-c"); ok {
		t.Error("hawser-c should not exist")
	}
}
