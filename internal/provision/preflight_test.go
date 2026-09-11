package provision_test

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"strings"
	"testing"

	"github.com/zcsizmadia/hawser/internal/provision"
	"github.com/zcsizmadia/hawser/internal/wsl"
)

// quietLogger keeps expected-failure tests from filling the test output.
func quietLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

func TestPreflightOKOnHealthyHost(t *testing.T) {
	p := &provision.Provisioner{WSL: healthyWSL(), Logger: quietLogger()}
	r, err := p.Preflight(context.Background(), provision.Options{StateDir: t.TempDir()})
	if err != nil {
		t.Fatalf("Preflight: %v", err)
	}
	if !r.OK {
		t.Errorf("not OK: %+v", r.Problems)
	}
	if len(r.Warnings) != 0 {
		t.Errorf("unexpected warnings: %+v", r.Warnings)
	}
	if r.Status.Version != "2.7.8.0" {
		t.Errorf("Status not reported: %+v", r.Status)
	}
}

func TestPreflightWSLMissingGivesInstructions(t *testing.T) {
	// PLAN §05: print the exact enable-and-reboot instructions rather than
	// attempting elevation.
	w := &fakeWSL{status: wsl.Status{Installed: false}}
	p := &provision.Provisioner{WSL: w, Logger: quietLogger()}

	r, err := p.Preflight(context.Background(), provision.Options{StateDir: t.TempDir()})
	if err != nil {
		t.Fatalf("Preflight: %v", err)
	}
	if r.OK {
		t.Fatal("OK with WSL absent")
	}
	if len(r.Problems) != 1 {
		t.Fatalf("problems = %+v, want exactly 1", r.Problems)
	}
	remedy := r.Problems[0].Remedy
	for _, want := range []string{"wsl --install", "reboot"} {
		if !strings.Contains(remedy, want) {
			t.Errorf("remedy should mention %q, got: %s", want, remedy)
		}
	}
}

func TestPreflightWarnsOnMissingVersionCommand(t *testing.T) {
	// Inbox WSL lacks `wsl --version`, and with it the networking modes several
	// VPN remedies depend on. A warning, not a blocker.
	w := &fakeWSL{status: wsl.Status{Installed: true, DefaultVersion: 2}}
	p := &provision.Provisioner{WSL: w, Logger: quietLogger()}

	r, err := p.Preflight(context.Background(), provision.Options{StateDir: t.TempDir()})
	if err != nil {
		t.Fatalf("Preflight: %v", err)
	}
	if !r.OK {
		t.Errorf("an unknown WSL version should not block install: %+v", r.Problems)
	}
	if len(r.Warnings) == 0 {
		t.Fatal("expected a warning about the unknown WSL version")
	}
	if !strings.Contains(r.Warnings[0].Remedy, "wsl --update") {
		t.Errorf("remedy should suggest wsl --update, got: %s", r.Warnings[0].Remedy)
	}
}

func TestPreflightWarnsOnOldWSL(t *testing.T) {
	w := &fakeWSL{status: wsl.Status{Installed: true, DefaultVersion: 2, Version: "1.2.5"}}
	p := &provision.Provisioner{WSL: w, Logger: quietLogger()}

	r, err := p.Preflight(context.Background(), provision.Options{StateDir: t.TempDir()})
	if err != nil {
		t.Fatalf("Preflight: %v", err)
	}
	if !r.OK {
		t.Errorf("an old WSL should warn, not block: %+v", r.Problems)
	}
	if len(r.Warnings) == 0 {
		t.Error("expected a warning about the old WSL version")
	}
}

func TestPreflightWarnsOnDefaultVersion1(t *testing.T) {
	// Hawser imports with --version 2 regardless, so this only warns.
	w := &fakeWSL{status: wsl.Status{Installed: true, DefaultVersion: 1, Version: "2.7.8.0"}}
	p := &provision.Provisioner{WSL: w, Logger: quietLogger()}

	r, err := p.Preflight(context.Background(), provision.Options{StateDir: t.TempDir()})
	if err != nil {
		t.Fatalf("Preflight: %v", err)
	}
	if !r.OK {
		t.Errorf("default version 1 should not block: %+v", r.Problems)
	}
	found := false
	for _, warn := range r.Warnings {
		if strings.Contains(warn.Summary, "default WSL version is 1") {
			found = true
		}
	}
	if !found {
		t.Errorf("warnings = %+v, want one about the default version", r.Warnings)
	}
}

func TestPreflightDetectsExistingDistroCaseInsensitively(t *testing.T) {
	// WSL distro names are case-insensitive, so a differently-cased match must
	// still be caught or the import would collide.
	w := healthyWSL()
	w.distros = []wsl.Distro{{Name: "Hawser-Engine", State: "Running", Version: 2}}
	p := &provision.Provisioner{WSL: w, Logger: quietLogger()}

	r, err := p.Preflight(context.Background(), provision.Options{StateDir: t.TempDir()})
	if err != nil {
		t.Fatalf("Preflight: %v", err)
	}
	if r.OK {
		t.Error("OK despite a case-differing existing distro")
	}
	if r.ExistingDistro == nil {
		t.Fatal("ExistingDistro not reported")
	}
	if !r.ExistingDistro.Running() {
		t.Errorf("existing distro state not carried: %+v", r.ExistingDistro)
	}
}

func TestPreflightIgnoresUnrelatedDistros(t *testing.T) {
	w := healthyWSL()
	w.distros = []wsl.Distro{
		{Name: "Ubuntu", State: "Running", Version: 2},
		{Name: "docker-desktop", State: "Running", Version: 2},
	}
	p := &provision.Provisioner{WSL: w, Logger: quietLogger()}

	r, err := p.Preflight(context.Background(), provision.Options{StateDir: t.TempDir()})
	if err != nil {
		t.Fatalf("Preflight: %v", err)
	}
	if !r.OK {
		t.Errorf("unrelated distros should not block install: %+v", r.Problems)
	}
}

func TestPreflightPropagatesListError(t *testing.T) {
	w := healthyWSL()
	w.listErr = errors.New("wsl.exe exploded")
	p := &provision.Provisioner{WSL: w, Logger: quietLogger()}

	if _, err := p.Preflight(context.Background(), provision.Options{StateDir: t.TempDir()}); err == nil {
		t.Error("Preflight succeeded despite a list failure")
	}
}

func TestProblemStringIncludesRemedy(t *testing.T) {
	// The remedy is the whole point of the type; it must not be droppable.
	p := provision.Problem{Summary: "something is wrong", Remedy: "do this"}
	s := p.String()
	if !strings.Contains(s, "something is wrong") || !strings.Contains(s, "do this") {
		t.Errorf("Problem.String() = %q", s)
	}
}

func TestExistingDistroPointsAtEngineUpgrade(t *testing.T) {
	// Someone typing `install` on a machine that already has an engine is
	// usually after a newer one. The refusal has to name the command that does
	// that, or the only answers on offer either destroy their data
	// (`uninstall`) or quietly leave them running two engines (`--distro`).
	p := &provision.Provisioner{WSL: bootedWSL(), Logger: quietLogger()}
	r, err := p.Preflight(context.Background(), provision.Options{
		StateDir: t.TempDir(),
		Distro:   provision.DefaultDistro,
	})
	if err != nil {
		t.Fatalf("Preflight: %v", err)
	}
	if r.ExistingDistro == nil {
		t.Fatal("an already-registered distro should be reported")
	}
	var found bool
	for _, pr := range r.Problems {
		if strings.Contains(pr.Remedy, "hawser engine upgrade") {
			found = true
		}
	}
	if !found {
		t.Errorf("the refusal should name `hawser engine upgrade`; problems: %+v", r.Problems)
	}
}
