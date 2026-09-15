package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/wslkit/skrog/internal/provision"
	"github.com/wslkit/skrog/internal/version"
)

// writeManifest drops a manifest where the provisioner will find it.
func writeManifest(t *testing.T, m provision.Manifest) provision.Options {
	t.Helper()
	dir := t.TempDir()
	opts := provision.Options{StateDir: dir}
	p := &provision.Provisioner{}
	if err := p.SaveManifest(opts, &m); err != nil {
		t.Fatalf("SaveManifest: %v", err)
	}
	return opts
}

// A manifest from before #335 has no backend field at all, and must keep
// resolving to the distro it names. This is every existing install.
func TestResolveEngineTargetDefaultsToDistro(t *testing.T) {
	opts := writeManifest(t, provision.Manifest{Distro: "skrog-engine"})
	got, ok := resolveEngineTarget(&provision.Provisioner{}, opts)
	if !ok {
		t.Fatal("an install with a distro was not found")
	}
	if got.Backend != provision.BackendDistro {
		t.Errorf("backend = %q, want %q", got.Backend, provision.BackendDistro)
	}
	if got.Distro != "skrog-engine" {
		t.Errorf("distro = %q", got.Distro)
	}
	if got.isWslc() {
		t.Error("a distro install reported itself as wslc")
	}
}

func TestResolveEngineTargetReadsWslc(t *testing.T) {
	opts := writeManifest(t, provision.Manifest{Backend: provision.BackendWslc})
	got, ok := resolveEngineTarget(&provision.Provisioner{}, opts)
	if !ok {
		t.Fatal("a wslc install was not found")
	}
	if !got.isWslc() {
		t.Errorf("backend = %q, want wslc", got.Backend)
	}
	if got.Distro != "" {
		t.Errorf("a wslc install named distro %q; it has none", got.Distro)
	}
}

// `supervise --distro X` with no manifest worked before any of this and must
// keep working, or driving Skrog by hand breaks.
func TestResolveEngineTargetFallsBackToTheFlag(t *testing.T) {
	opts := provision.Options{StateDir: t.TempDir(), Distro: "hand-rolled"}
	got, ok := resolveEngineTarget(&provision.Provisioner{}, opts)
	if !ok {
		t.Fatal("an explicit --distro was not honoured")
	}
	if got.Backend != provision.BackendDistro || got.Distro != "hand-rolled" {
		t.Errorf("got %+v", got)
	}
}

func TestResolveEngineTargetReportsNoInstall(t *testing.T) {
	opts := provision.Options{StateDir: t.TempDir()}
	if _, ok := resolveEngineTarget(&provision.Provisioner{}, opts); ok {
		t.Error("an empty state dir reported an install")
	}
}

// The manifest a distro install writes must not gain a field, or every
// existing install's file changes shape on the next write.
func TestDistroManifestStaysByteCompatible(t *testing.T) {
	dir := t.TempDir()
	opts := provision.Options{StateDir: dir}
	p := &provision.Provisioner{}
	if err := p.SaveManifest(opts, &provision.Manifest{Distro: "skrog-engine"}); err != nil {
		t.Fatal(err)
	}
	// Find it without depending on the private path helper.
	var found string
	_ = filepath.WalkDir(dir, func(path string, d os.DirEntry, err error) error {
		if err == nil && !d.IsDir() && filepath.Ext(path) == ".json" {
			found = path
		}
		return nil
	})
	if found == "" {
		t.Fatal("no manifest written")
	}
	b, err := os.ReadFile(found)
	if err != nil {
		t.Fatal(err)
	}
	var raw map[string]any
	if err := json.Unmarshal(b, &raw); err != nil {
		t.Fatal(err)
	}
	if _, present := raw["backend"]; present {
		t.Errorf("a distro manifest now carries a backend field: %s", b)
	}
}

// internal/version duplicates the constant rather than importing provision.
// If they ever disagree, `skrog version` silently prints the distro line for a
// wslc install.
func TestVersionBackendConstantMatchesProvision(t *testing.T) {
	if version.BackendWslc != provision.BackendWslc {
		t.Errorf("version.BackendWslc = %q, provision.BackendWslc = %q",
			version.BackendWslc, provision.BackendWslc)
	}
}
