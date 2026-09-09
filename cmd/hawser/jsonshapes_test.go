package main

import (
	"encoding/json"
	"testing"

	"github.com/zcsizmadia/hawser/internal/hawserfile"
	"github.com/zcsizmadia/hawser/internal/snapshot"
)

// These tests pin the --json contract (#137, docs/cli-json.md): the keys tools
// depend on, and the deliberate null-vs-empty choices. A rename or removal here
// is a breaking change for every consumer, so it must be a conscious one.

// roundTrip marshals v and decodes it generically, the way a consumer sees it.
func roundTrip(t *testing.T, v any) map[string]any {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatalf("not a JSON object: %s", b)
	}
	return m
}

func requireKeys(t *testing.T, m map[string]any, keys ...string) {
	t.Helper()
	for _, k := range keys {
		if _, ok := m[k]; !ok {
			t.Errorf("missing key %q in %v", k, m)
		}
	}
}

func TestStatusShape(t *testing.T) {
	m := roundTrip(t, statusJSON{StateDir: `C:\x`, Supervisor: "running", Engine: "idle", Desired: "running"})
	requireKeys(t, m, "installed", "stateDir", "supervisor", "engine", "desired", "gpu")
	// Empty optionals are omitted, not emitted as "".
	for _, k := range []string{"distro", "profile"} {
		if _, ok := m[k]; ok {
			t.Errorf("%q should be omitted when empty", k)
		}
	}
	gpu, ok := m["gpu"].(map[string]any)
	if !ok {
		t.Fatalf("gpu should be an object: %v", m["gpu"])
	}
	requireKeys(t, gpu, "enabled", "probed", "visible", "specInstalled")
}

func TestCLIStatusShape(t *testing.T) {
	m := roundTrip(t, cliStatusJSON{Arch: "amd64", BinDir: `C:\b`, Tools: []cliToolJSON{}})
	requireKeys(t, m, "arch", "binDir", "onPath", "tools")
	if tools, ok := m["tools"].([]any); !ok || tools == nil {
		t.Errorf("tools must be an array even when empty, got %v", m["tools"])
	}
	tool := roundTrip(t, cliToolJSON{Name: "docker", Role: "cli"})
	requireKeys(t, tool, "name", "version", "role", "path", "installed", "available")
}

func TestConfigListNullVsEmptyEngine(t *testing.T) {
	// No engine installed: engine is null — a different answer from "installed,
	// nothing set" ({}), and consumers branch on it.
	m := roundTrip(t, configListJSON{Settings: map[string]string{"gpu": "off"}})
	requireKeys(t, m, "settings", "engine")
	if m["engine"] != nil {
		t.Errorf("engine should be null with no engine, got %v", m["engine"])
	}
	m = roundTrip(t, configListJSON{Settings: map[string]string{}, Engine: map[string]string{}})
	if eng, ok := m["engine"].(map[string]any); !ok || eng == nil {
		t.Errorf("engine should be {} when installed with nothing set, got %v", m["engine"])
	}
}

func TestProfileListShape(t *testing.T) {
	m := roundTrip(t, profileListJSON{Profiles: []profileEntryJSON{}})
	requireKeys(t, m, "profiles")
	if _, ok := m["active"]; ok {
		t.Error("active should be omitted when no profile is active")
	}
	if p, ok := m["profiles"].([]any); !ok || p == nil {
		t.Errorf("profiles must be an array even when empty, got %v", m["profiles"])
	}
	e := roundTrip(t, profileEntryJSON{Name: "work", Active: true})
	requireKeys(t, e, "name", "active")
}

func TestSnapshotResultShapes(t *testing.T) {
	requireKeys(t, roundTrip(t, snapshotRestoredJSON{Restored: "x"}), "restored")
	requireKeys(t, roundTrip(t, snapshotDeletedJSON{Deleted: "x"}), "deleted")
	// The list path emits snapshot.Meta values; pin its keys here too.
	requireKeys(t, roundTrip(t, snapshot.Meta{Name: "x"}), "name", "created", "distro", "sha256", "sizeBytes")
	// An empty list is [] — the list code builds a non-nil slice for exactly this.
	b, _ := json.Marshal([]snapshot.Meta{})
	if string(b) != "[]" {
		t.Errorf("empty snapshot list = %s, want []", b)
	}
}

// TestHawserfileJSONMirrorsYAML: `profile show --json` and `config export --json`
// must emit the same document a hawser.yaml holds, so the JSON keys are the
// kebab-case YAML keys, not Go field names.
func TestHawserfileJSONMirrorsYAML(t *testing.T) {
	on := true
	m := roundTrip(t, hawserfile.File{
		Distro: "d", DataDir: "x", EngineVersion: "1", IdleTimeout: "off", Autostart: &on,
		Engine: map[string]string{"a": "b"}, Hooks: map[string]string{"post-start": "s"},
		Integrations: []string{"Ubuntu"},
	})
	requireKeys(t, m, "distro", "data-dir", "engine-version", "idle-timeout", "autostart", "engine", "hooks", "integrations")
	for _, bad := range []string{"Distro", "DataDir", "EngineVersion"} {
		if _, ok := m[bad]; ok {
			t.Errorf("Go field name %q leaked into JSON", bad)
		}
	}
}
