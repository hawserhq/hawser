//go:build windows

package dockercli

import (
	"testing"

	"golang.org/x/sys/windows/registry"
)

// useScratchEnv points the PATH helpers at a disposable key so tests never
// touch the user's real Environment.
func useScratchEnv(t *testing.T) {
	t.Helper()
	orig := envKeyPath
	envKeyPath = `Software\SkrogTest\Environment`
	k, _, err := registry.CreateKey(registry.CURRENT_USER, envKeyPath, registry.ALL_ACCESS)
	if err != nil {
		t.Fatalf("creating scratch env key: %v", err)
	}
	k.Close()
	t.Cleanup(func() {
		registry.DeleteKey(registry.CURRENT_USER, envKeyPath)
		registry.DeleteKey(registry.CURRENT_USER, `Software\SkrogTest`)
		envKeyPath = orig
	})
}

func setPath(t *testing.T, v string, expand bool) {
	t.Helper()
	k, err := registry.OpenKey(registry.CURRENT_USER, envKeyPath, registry.SET_VALUE)
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

func getPath(t *testing.T) (string, uint32) {
	t.Helper()
	k, err := registry.OpenKey(registry.CURRENT_USER, envKeyPath, registry.QUERY_VALUE)
	if err != nil {
		t.Fatal(err)
	}
	defer k.Close()
	v, typ, err := k.GetStringValue("Path")
	if err != nil {
		t.Fatal(err)
	}
	return v, typ
}

func TestAddPrependsAndIsIdempotent(t *testing.T) {
	useScratchEnv(t)
	setPath(t, `C:\existing;%USERPROFILE%\bin`, true)

	dir := `C:\Users\me\AppData\Local\Skrog\bin`
	added, err := AddToUserPath(dir)
	if err != nil || !added {
		t.Fatalf("AddToUserPath = (%v,%v), want (true,nil)", added, err)
	}
	got, typ := getPath(t)
	if got != dir+`;C:\existing;%USERPROFILE%\bin` {
		t.Errorf("prepend wrong: %q", got)
	}
	// REG_EXPAND_SZ must be preserved so %USERPROFILE% keeps expanding.
	if typ != registry.EXPAND_SZ {
		t.Errorf("PATH type = %d, want REG_EXPAND_SZ(%d)", typ, registry.EXPAND_SZ)
	}
	// Second add is a no-op.
	added, err = AddToUserPath(dir)
	if err != nil || added {
		t.Fatalf("second AddToUserPath = (%v,%v), want (false,nil)", added, err)
	}
	if now, _ := getPath(t); now != got {
		t.Errorf("idempotent add changed PATH: %q -> %q", got, now)
	}
}

func TestAddIsCaseAndSlashInsensitive(t *testing.T) {
	useScratchEnv(t)
	setPath(t, `C:\Skrog\Bin`, false)
	// Same dir, different case + trailing slash: must be recognized as present.
	added, err := AddToUserPath(`c:\skrog\bin\`)
	if err != nil {
		t.Fatal(err)
	}
	if added {
		t.Error("a case/slash variant of an existing entry should not be added again")
	}
}

func TestRemoveOnlyOurEntry(t *testing.T) {
	useScratchEnv(t)
	dir := `C:\Skrog\bin`
	setPath(t, `C:\keep;`+dir+`;C:\also-keep`, false)

	removed, err := RemoveFromUserPath(dir)
	if err != nil || !removed {
		t.Fatalf("RemoveFromUserPath = (%v,%v), want (true,nil)", removed, err)
	}
	if got, _ := getPath(t); got != `C:\keep;C:\also-keep` {
		t.Errorf("remove left PATH = %q", got)
	}
	// Removing again is a no-op success.
	if removed, err := RemoveFromUserPath(dir); err != nil || removed {
		t.Fatalf("second remove = (%v,%v), want (false,nil)", removed, err)
	}
}

func TestContains(t *testing.T) {
	useScratchEnv(t)
	setPath(t, `C:\a;C:\b`, false)
	if ok, _ := UserPathContains(`C:\b`); !ok {
		t.Error("should find C:\\b")
	}
	if ok, _ := UserPathContains(`C:\c`); ok {
		t.Error("should not find C:\\c")
	}
}
