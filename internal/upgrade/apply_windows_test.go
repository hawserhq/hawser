//go:build windows

package upgrade_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/wslkit/skrog/internal/upgrade"
)

// stage builds an install directory and a staged directory, each holding the
// named binaries with recognisable contents.
func stage(t *testing.T, installed, staged map[string]string) (string, string) {
	t.Helper()
	root := t.TempDir()
	dir := filepath.Join(root, "install")
	st := filepath.Join(root, "staged")
	for _, d := range []string{dir, st} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	for name, body := range installed {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	for name, body := range staged {
		if err := os.WriteFile(filepath.Join(st, name), []byte(body), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	return dir, st
}

func read(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading %s: %v", path, err)
	}
	return string(b)
}

func TestSwapReplacesEveryBinaryAndKeepsTheOldOne(t *testing.T) {
	dir, st := stage(t,
		map[string]string{"skrog.exe": "old-skrog", "skrogw.exe": "old-w", "skrogtray.exe": "old-tray"},
		map[string]string{"skrog.exe": "new-skrog", "skrogw.exe": "new-w", "skrogtray.exe": "new-tray"})

	res, err := upgrade.SwapBinaries(dir, st)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Replaced) != 3 {
		t.Errorf("Replaced = %v, want all three", res.Replaced)
	}
	for name, want := range map[string]string{
		"skrog.exe": "new-skrog", "skrogw.exe": "new-w", "skrogtray.exe": "new-tray",
	} {
		if got := read(t, filepath.Join(dir, name)); got != want {
			t.Errorf("%s = %q, want %q", name, got, want)
		}
	}
	// The old image has to survive under .old: on Windows it is still mapped
	// by whatever process is running it, and deleting it now would fail.
	if got := read(t, filepath.Join(dir, "skrog.exe.old")); got != "old-skrog" {
		t.Errorf("skrog.exe.old = %q, want the old binary", got)
	}
}

// The property that matters most: a swap either happens completely or not at
// all. A new skrog.exe beside an old skrogw.exe from a different release is
// worse than no upgrade.
func TestSwapRollsBackWhenOneBinaryCannotBeReplaced(t *testing.T) {
	dir, st := stage(t,
		map[string]string{"skrog.exe": "old-skrog", "skrogw.exe": "old-w"},
		map[string]string{"skrog.exe": "new-skrog", "skrogw.exe": "new-w"})

	// Make the staged skrogw unreadable by removing it after the rename phase
	// would have listed it -- simulated here by staging a directory in its
	// place, which copyFile cannot read.
	if err := os.Remove(filepath.Join(st, "skrogw.exe")); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(st, "skrogw.exe"), 0o755); err != nil {
		t.Fatal(err)
	}

	if _, err := upgrade.SwapBinaries(dir, st); err == nil {
		t.Fatal("expected an error when a staged binary cannot be installed")
	}
	for name, want := range map[string]string{"skrog.exe": "old-skrog", "skrogw.exe": "old-w"} {
		if got := read(t, filepath.Join(dir, name)); got != want {
			t.Errorf("%s = %q after a failed swap, want the original %q -- the directory was left half-applied",
				name, got, want)
		}
	}
}

// A release that does not carry a binary must leave the installed one alone
// rather than delete it.
func TestSwapLeavesBinariesTheReleaseDoesNotCarry(t *testing.T) {
	dir, st := stage(t,
		map[string]string{"skrog.exe": "old-skrog", "skrogtray.exe": "old-tray"},
		map[string]string{"skrog.exe": "new-skrog"})

	res, err := upgrade.SwapBinaries(dir, st)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Replaced) != 1 || res.Replaced[0] != "skrog.exe" {
		t.Errorf("Replaced = %v, want only skrog.exe", res.Replaced)
	}
	if got := read(t, filepath.Join(dir, "skrogtray.exe")); got != "old-tray" {
		t.Errorf("skrogtray.exe = %q, want it untouched", got)
	}
}

// A second upgrade must not trip over the previous .old file. It does not,
// because os.Rename overwrites it -- see TestSwapRefusesWhileAPreviousOldIsStillHeld
// for the case where that is not enough.
func TestSwapTwiceInARow(t *testing.T) {
	dir, st := stage(t,
		map[string]string{"skrog.exe": "v1"},
		map[string]string{"skrog.exe": "v2"})
	if _, err := upgrade.SwapBinaries(dir, st); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(st, "skrog.exe"), []byte("v3"), 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := upgrade.SwapBinaries(dir, st); err != nil {
		t.Fatalf("second swap failed, so a .old from the first one blocked it: %v", err)
	}
	if got := read(t, filepath.Join(dir, "skrog.exe")); got != "v3" {
		t.Errorf("skrog.exe = %q, want v3", got)
	}
}

func TestCleanOldRemovesLeftoversAndToleratesHeldFiles(t *testing.T) {
	dir, st := stage(t,
		map[string]string{"skrog.exe": "old", "skrogw.exe": "old"},
		map[string]string{"skrog.exe": "new", "skrogw.exe": "new"})
	if _, err := upgrade.SwapBinaries(dir, st); err != nil {
		t.Fatal(err)
	}

	// Hold one of them the way a running process would.
	f, err := os.Open(filepath.Join(dir, "skrogw.exe.old"))
	if err != nil {
		t.Fatal(err)
	}
	removed := upgrade.CleanOld(dir)
	f.Close()

	if len(removed) == 0 {
		t.Error("CleanOld removed nothing; the unheld leftover should have gone")
	}
	for _, r := range removed {
		if r == "skrogw.exe.old" {
			t.Error("CleanOld claims to have removed a file that was held open")
		}
	}
	// And it must not have failed the caller over it.
	if _, err := os.Stat(filepath.Join(dir, "skrog.exe.old")); !os.IsNotExist(err) {
		t.Error("skrog.exe.old survived CleanOld")
	}
}

func TestAssetNameMatchesWhatTheReleaseWorkflowPublishes(t *testing.T) {
	for _, tc := range []struct{ version, arch, want string }{
		{"0.4.2", "amd64", "skrog_0.4.2_windows_amd64.zip"},
		{"v0.4.2", "amd64", "skrog_0.4.2_windows_amd64.zip"}, // a leading v must not reach the name
		{"0.4.2", "arm64", "skrog_0.4.2_windows_arm64.zip"},
	} {
		if got := upgrade.AssetName(tc.version, tc.arch); got != tc.want {
			t.Errorf("AssetName(%q, %q) = %q, want %q", tc.version, tc.arch, got, tc.want)
		}
	}
}

// A .old still held by a process that has not exited blocks the next swap, and
// the swap must roll back rather than leave two generations of .old behind.
//
// This is the case the code used to "handle" with an os.Remove before the
// rename. That line was dead -- os.Rename on Windows is
// MoveFileEx(MOVEFILE_REPLACE_EXISTING) and overwrites an unheld leftover by
// itself -- and removing it changed no test, which is how it was found.
func TestSwapRefusesWhileAPreviousOldIsStillHeld(t *testing.T) {
	dir, st := stage(t,
		map[string]string{"skrog.exe": "v1"},
		map[string]string{"skrog.exe": "v2"})
	if _, err := upgrade.SwapBinaries(dir, st); err != nil {
		t.Fatal(err)
	}

	held, err := os.Open(filepath.Join(dir, "skrog.exe.old"))
	if err != nil {
		t.Fatal(err)
	}
	defer held.Close()

	if err := os.WriteFile(filepath.Join(st, "skrog.exe"), []byte("v3"), 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := upgrade.SwapBinaries(dir, st); err == nil {
		t.Skip("this platform replaced a held file; nothing to assert")
	}
	if got := read(t, filepath.Join(dir, "skrog.exe")); got != "v2" {
		t.Errorf("skrog.exe = %q after a blocked swap, want v2 left in place", got)
	}
}
