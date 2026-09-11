//go:build windows

package autostart

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/sys/windows/registry"
)

// useScratchKey points the package at a disposable key so tests never touch
// the user's real Run key.
func useScratchKey(t *testing.T) {
	t.Helper()
	orig := runKeyPath
	runKeyPath = `Software\SkrogTest\Run`

	// The scratch key must exist for OpenKey(SET_VALUE) to succeed.
	k, _, err := registry.CreateKey(registry.CURRENT_USER, runKeyPath, registry.ALL_ACCESS)
	if err != nil {
		t.Fatalf("creating scratch key: %v", err)
	}
	k.Close()

	t.Cleanup(func() {
		registry.DeleteKey(registry.CURRENT_USER, runKeyPath)
		registry.DeleteKey(registry.CURRENT_USER, `Software\SkrogTest`)
		runKeyPath = orig
	})
}

// fakeInstall lays out skrog.exe + skrogw.exe in a temp dir.
func fakeInstall(t *testing.T, withLauncher bool) string {
	t.Helper()
	dir := t.TempDir()
	exe := filepath.Join(dir, "skrog.exe")
	if err := os.WriteFile(exe, []byte("stub"), 0o755); err != nil {
		t.Fatal(err)
	}
	if withLauncher {
		if err := os.WriteFile(filepath.Join(dir, "skrogw.exe"), []byte("stub"), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	return exe
}

func TestEnableStatusDisableRoundTrip(t *testing.T) {
	useScratchKey(t)
	exe := fakeInstall(t, true)

	if err := Enable(exe); err != nil {
		t.Fatalf("Enable: %v", err)
	}

	enabled, cmd, err := Status()
	if err != nil {
		t.Fatalf("Status: %v", err)
	}
	if !enabled {
		t.Fatal("not enabled after Enable")
	}
	if !strings.Contains(cmd, "skrogw.exe") {
		t.Errorf("registered command %q must run the windowless launcher, not the console binary", cmd)
	}
	if !strings.HasPrefix(cmd, `"`) {
		t.Errorf("command %q is unquoted; a space in the install path would break it", cmd)
	}

	if err := Disable(); err != nil {
		t.Fatalf("Disable: %v", err)
	}
	if enabled, _, _ := Status(); enabled {
		t.Error("still enabled after Disable")
	}
}

func TestEnableRefusesWithoutLauncher(t *testing.T) {
	useScratchKey(t)
	exe := fakeInstall(t, false)

	err := Enable(exe)
	if err == nil {
		t.Fatal("Enable succeeded without skrogw.exe; a console binary at logon flashes a window")
	}
	if !strings.Contains(err.Error(), "skrogw.exe") {
		t.Errorf("error should name the missing launcher: %v", err)
	}
	if enabled, _, _ := Status(); enabled {
		t.Error("a refused Enable must not leave a Run entry behind")
	}
}

func TestDisableWhenAbsentIsSuccess(t *testing.T) {
	useScratchKey(t)
	// The user asked for a state, not an action.
	if err := Disable(); err != nil {
		t.Errorf("Disable with no entry = %v, want nil", err)
	}
}

func TestDisableIfOwnedProtectsOtherInstalls(t *testing.T) {
	// The Run entry is one-per-user while installs are per-state-dir, so
	// uninstalling a secondary install (the e2e suite beside a real one) must
	// not delete the primary's entry.
	useScratchKey(t)
	primary := fakeInstall(t, true)
	if err := Enable(primary); err != nil {
		t.Fatal(err)
	}

	secondaryDir := t.TempDir() // different install, no entry of its own
	removed, err := DisableIfOwned(secondaryDir)
	if err != nil {
		t.Fatalf("DisableIfOwned: %v", err)
	}
	if removed {
		t.Fatal("uninstalling a secondary install removed the primary's autostart entry")
	}
	if enabled, _, _ := Status(); !enabled {
		t.Fatal("primary's entry is gone")
	}

	// The owner, however, must remove it.
	removed, err = DisableIfOwned(filepath.Dir(primary))
	if err != nil {
		t.Fatalf("DisableIfOwned(owner): %v", err)
	}
	if !removed {
		t.Fatal("the owning install could not remove its own entry")
	}
	if enabled, _, _ := Status(); enabled {
		t.Fatal("entry still present after the owner removed it")
	}
}
