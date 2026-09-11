package profile

import (
	"testing"

	"github.com/hawserhq/hawser/internal/hawserfile"
)

func TestValidName(t *testing.T) {
	ok := []string{"work", "home", "corp-vpn", "a.b_c", "Work2"}
	bad := []string{"", ".", "..", ".hidden", "a/b", `a\b`, "a b", "a:b", "-lead"}
	for _, n := range ok {
		if err := ValidName(n); err != nil {
			t.Errorf("ValidName(%q) = %v, want ok", n, err)
		}
	}
	for _, n := range bad {
		if err := ValidName(n); err == nil {
			t.Errorf("ValidName(%q) = ok, want error", n)
		}
	}
}

func TestSaveLoadRoundTripKeepsOnlySettings(t *testing.T) {
	m := &Manager{StateDir: t.TempDir()}
	in := hawserfile.File{
		Distro:        "should-be-dropped",
		EngineVersion: "should-be-dropped",
		IdleTimeout:   "20m",
		Engine:        map[string]string{"dns": "1.1.1.1"},
		Hooks:         map[string]string{"post-start": "x.cmd"},
	}
	if err := m.Save("work", in); err != nil {
		t.Fatal(err)
	}
	got, err := m.Load("work")
	if err != nil {
		t.Fatal(err)
	}
	if got.IdleTimeout != "20m" || got.Engine["dns"] != "1.1.1.1" || got.Hooks["post-start"] != "x.cmd" {
		t.Fatalf("settings not preserved: %+v", got)
	}
	if got.Distro != "" || got.EngineVersion != "" {
		t.Errorf("install-time fields should be dropped: %+v", got)
	}
}

func TestListAndExists(t *testing.T) {
	m := &Manager{StateDir: t.TempDir()}
	if names, _ := m.List(); len(names) != 0 {
		t.Fatalf("empty manager should list nothing, got %v", names)
	}
	for _, n := range []string{"home", "work", "corp"} {
		if err := m.Save(n, hawserfile.File{IdleTimeout: "off"}); err != nil {
			t.Fatal(err)
		}
	}
	names, err := m.List()
	if err != nil {
		t.Fatal(err)
	}
	if len(names) != 3 || names[0] != "corp" || names[1] != "home" || names[2] != "work" {
		t.Fatalf("List not sorted/complete: %v", names)
	}
	if !m.Exists("work") || m.Exists("nope") {
		t.Error("Exists wrong")
	}
}

func TestActiveLifecycle(t *testing.T) {
	m := &Manager{StateDir: t.TempDir()}
	if m.Active() != "" {
		t.Fatal("no active profile initially")
	}
	m.Save("work", hawserfile.File{IdleTimeout: "off"})
	if err := m.SetActive("work"); err != nil {
		t.Fatal(err)
	}
	if m.Active() != "work" {
		t.Fatalf("Active = %q, want work", m.Active())
	}
	// Deleting the active profile clears the marker.
	if err := m.Delete("work"); err != nil {
		t.Fatal(err)
	}
	if m.Active() != "" {
		t.Errorf("deleting the active profile should clear it, got %q", m.Active())
	}
}

func TestLoadAndDeleteMissing(t *testing.T) {
	m := &Manager{StateDir: t.TempDir()}
	if _, err := m.Load("ghost"); err == nil {
		t.Error("loading a missing profile should error")
	}
	if err := m.Delete("ghost"); err == nil {
		t.Error("deleting a missing profile should error")
	}
}
