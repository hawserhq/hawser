//go:build windows

package dockercli

import (
	"os"
	"path/filepath"
	"testing"

	"golang.org/x/sys/windows/registry"
)

// These run in CI. The existing scope_live_test.go reads the machine's real
// registry and is tagged `livepath`, so it never runs there -- and it passed on
// the author's machine only because both PATH hives happened to hold literal
// REG_SZ values, which is not what a stock Windows install looks like (#286).

// useScratchMachineEnv points the machine-PATH reader at a disposable HKCU key.
// HKLM needs elevation, so without this redirect the machine half of
// PathScopeOf cannot be tested at all.
func useScratchMachineEnv(t *testing.T) {
	t.Helper()
	origRoot, origPath := machineEnvRoot, machineEnvKeyPath
	machineEnvRoot = registry.CURRENT_USER
	machineEnvKeyPath = `Software\SkrogTest\MachineEnvironment`
	k, _, err := registry.CreateKey(registry.CURRENT_USER, machineEnvKeyPath, registry.ALL_ACCESS)
	if err != nil {
		t.Fatalf("creating scratch machine env key: %v", err)
	}
	k.Close()
	t.Cleanup(func() {
		registry.DeleteKey(registry.CURRENT_USER, machineEnvKeyPath)
		registry.DeleteKey(registry.CURRENT_USER, `Software\SkrogTest`)
		machineEnvRoot, machineEnvKeyPath = origRoot, origPath
	})
}

func setMachinePath(t *testing.T, v string, expand bool) {
	t.Helper()
	k, err := registry.OpenKey(machineEnvRoot, machineEnvKeyPath, registry.SET_VALUE)
	if err != nil {
		t.Fatal(err)
	}
	defer k.Close()
	if expand {
		err = k.SetExpandStringValue("Path", v)
	} else {
		err = k.SetStringValue("Path", v)
	}
	if err != nil {
		t.Fatal(err)
	}
}

// The defect #286 was filed for: a stock machine PATH is REG_EXPAND_SZ full of
// %SystemRoot%-style entries, and exec.LookPath hands back the expanded form.
func TestPathScopeMatchesExpandableEntries(t *testing.T) {
	useScratchEnv(t)
	useScratchMachineEnv(t)

	sysRoot := os.Getenv("SystemRoot")
	if sysRoot == "" {
		t.Skip("SystemRoot not set")
	}
	local := os.Getenv("LOCALAPPDATA")
	if local == "" {
		t.Skip("LOCALAPPDATA not set")
	}

	// Shaped like a real one: expandable entries, REG_EXPAND_SZ.
	setMachinePath(t, `%SystemRoot%\system32;%SystemRoot%;C:\literal\machine`, true)
	setPath(t, `%LOCALAPPDATA%\Programs\Thing;C:\literal\user`, true)

	cases := []struct {
		name, dir string
		want      Scope
	}{
		{"machine entry stored as %SystemRoot%", filepath.Join(sysRoot, "system32"), ScopeMachine},
		{"machine entry stored literally", `C:\literal\machine`, ScopeMachine},
		{"user entry stored as %LOCALAPPDATA%", filepath.Join(local, `Programs\Thing`), ScopeUser},
		{"user entry stored literally", `C:\literal\user`, ScopeUser},
		{"on neither", `C:\definitely\not\on\path`, ScopeUnknown},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := PathScopeOf(tc.dir); got != tc.want {
				t.Errorf("PathScopeOf(%q) = %v, want %v", tc.dir, got, tc.want)
			}
		})
	}
}

// Machine wins when a directory is on both, because that is the one that
// decides resolution order.
func TestPathScopeMachineWinsOverUser(t *testing.T) {
	useScratchEnv(t)
	useScratchMachineEnv(t)
	setMachinePath(t, `C:\both`, false)
	setPath(t, `C:\both`, false)
	if got := PathScopeOf(`C:\both`); got != ScopeMachine {
		t.Errorf("PathScopeOf = %v, want ScopeMachine — machine resolves first", got)
	}
}

// The other two spellings that silently missed. exec.LookPath strips quotes and
// Cleans, so the registry side has to be normalised the same way.
func TestNormPathEntryMatchesWhatLookPathReturns(t *testing.T) {
	cases := []struct {
		name, entry, lookPathDir string
		wantSame                 bool
	}{
		{"quoted entry", `"C:\Program Files\Quoted Tool\bin"`, `C:\Program Files\Quoted Tool\bin`, true},
		{"forward slashes", `C:/tools/bin`, `C:\tools\bin`, true},
		{"doubled separator", `C:\tools\\bin`, `C:\tools\bin`, true},
		{"dot segment", `C:\tools\.\bin`, `C:\tools\bin`, true},
		{"trailing separator", `C:\tools\bin\`, `C:\tools\bin`, true},
		{"case", `c:\TOOLS\Bin`, `C:\tools\bin`, true},
		{"genuinely different", `C:\tools\bin`, `C:\tools\other`, false},
		{"empty never matches", ``, ``, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := samePath(tc.entry, tc.lookPathDir); got != tc.wantSame {
				t.Errorf("samePath(%q, %q) = %v, want %v", tc.entry, tc.lookPathDir, got, tc.wantSame)
			}
		})
	}
}

// AddToUserPath de-duplicates through the same comparison, so it could add a
// second copy of a directory already present under a %VAR% spelling.
func TestAddToUserPathDeduplicatesExpandableEntries(t *testing.T) {
	useScratchEnv(t)
	local := os.Getenv("LOCALAPPDATA")
	if local == "" {
		t.Skip("LOCALAPPDATA not set")
	}
	setPath(t, `%LOCALAPPDATA%\Programs\Thing`, true)

	added, err := AddToUserPath(filepath.Join(local, `Programs\Thing`))
	if err != nil {
		t.Fatal(err)
	}
	if added {
		t.Error("AddToUserPath added a duplicate of an entry already present as %LOCALAPPDATA%\\Programs\\Thing")
	}
}
