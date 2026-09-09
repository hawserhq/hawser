package engineconfig

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

func TestApplyDefaultsAddsOnlyAbsentKeys(t *testing.T) {
	f := newFakeDistro()
	f.files[DefaultDaemonPath] = `{"log-driver":"json-file"}`
	m := &Manager{WSL: f, Distro: "d"}

	added, err := m.ApplyDefaults(context.Background())
	if err != nil {
		t.Fatalf("ApplyDefaults: %v", err)
	}
	if len(added) != 1 || added[0] != "userland-proxy" {
		t.Fatalf("added = %v, want [userland-proxy]", added)
	}

	var cfg map[string]any
	if err := json.Unmarshal([]byte(f.files[DefaultDaemonPath]), &cfg); err != nil {
		t.Fatalf("daemon.json not JSON after defaults: %v", err)
	}
	if cfg["userland-proxy"] != false {
		t.Errorf("userland-proxy = %v, want false", cfg["userland-proxy"])
	}
	if cfg["log-driver"] != "json-file" {
		t.Errorf("existing key lost: log-driver = %v", cfg["log-driver"])
	}
	// The result was validated before the live file was replaced.
	if strings.Join(f.ops, ",") != "read,write-candidate,validate,backup,write-live" {
		t.Errorf("ops = %v", f.ops)
	}
}

func TestApplyDefaultsRespectsUserChoice(t *testing.T) {
	f := newFakeDistro()
	// The user turned the proxy on deliberately; a default must not undo that.
	f.files[DefaultDaemonPath] = `{"userland-proxy": true}`
	m := &Manager{WSL: f, Distro: "d"}

	added, err := m.ApplyDefaults(context.Background())
	if err != nil {
		t.Fatalf("ApplyDefaults: %v", err)
	}
	if len(added) != 0 {
		t.Fatalf("added = %v, want nothing", added)
	}
	if len(f.ops) != 1 || f.ops[0] != "read" {
		t.Errorf("a no-op default still touched the distro: ops = %v", f.ops)
	}
	if f.files[DefaultDaemonPath] != `{"userland-proxy": true}` {
		t.Errorf("daemon.json rewritten: %s", f.files[DefaultDaemonPath])
	}
}

func TestApplyDefaultsOnEmptyFile(t *testing.T) {
	f := newFakeDistro()
	m := &Manager{WSL: f, Distro: "d"}
	if _, err := m.ApplyDefaults(context.Background()); err != nil {
		t.Fatalf("ApplyDefaults on missing daemon.json: %v", err)
	}
	if !strings.Contains(f.files[DefaultDaemonPath], `"userland-proxy": false`) {
		t.Errorf("daemon.json = %s", f.files[DefaultDaemonPath])
	}
}

func TestApplyDefaultsRefusedByValidate(t *testing.T) {
	f := newFakeDistro()
	f.validateOK = false
	f.files[DefaultDaemonPath] = `{}`
	m := &Manager{WSL: f, Distro: "d"}
	if _, err := m.ApplyDefaults(context.Background()); err == nil {
		t.Fatal("expected the dockerd --validate refusal to surface")
	}
	if f.files[DefaultDaemonPath] != `{}` {
		t.Errorf("live daemon.json replaced despite failed validation: %s", f.files[DefaultDaemonPath])
	}
}

func TestBoolKeyRoundTrip(t *testing.T) {
	f := newFakeDistro()
	m := &Manager{WSL: f, Distro: "d"}
	if _, err := m.Set(context.Background(), "userland-proxy", "true"); err != nil {
		t.Fatalf("Set: %v", err)
	}
	got, err := m.Get(context.Background(), "userland-proxy")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got != "true" {
		t.Errorf("Get = %q, want true", got)
	}
	if _, err := m.Set(context.Background(), "userland-proxy", "maybe"); err == nil {
		t.Error("Set of a non-boolean succeeded")
	}
}
