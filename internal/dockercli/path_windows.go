//go:build windows

package dockercli

import (
	"fmt"
	"strings"
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows/registry"
)

// envKeyPath is the per-user environment key; a var so tests can redirect it.
var envKeyPath = `Environment`

// AddToUserPath prepends dir to the current user's PATH (HKCU\Environment), so a
// new shell finds Skrog's docker.exe first. It is a careful read-modify-write:
// it never truncates (unlike `setx`, which caps at 1024 chars), it de-duplicates,
// and it preserves the value's REG_EXPAND_SZ type so entries like %USERPROFILE%
// keep expanding. Returns whether a change was made (already-present is a no-op).
//
// User scope means no elevation, and the change is reversible (RemoveFromUserPath,
// which uninstall calls). A running shell keeps its old PATH until it restarts —
// the broadcast below tells Explorer-launched processes to reload, but existing
// consoles are unaffected by design.
func AddToUserPath(dir string) (bool, error) {
	k, err := registry.OpenKey(registry.CURRENT_USER, envKeyPath, registry.QUERY_VALUE|registry.SET_VALUE)
	if err != nil {
		return false, fmt.Errorf("opening the user Environment key: %w", err)
	}
	defer k.Close()

	cur, valType, err := k.GetStringValue("Path")
	if err == registry.ErrNotExist {
		cur, valType = "", registry.EXPAND_SZ
	} else if err != nil {
		return false, fmt.Errorf("reading user PATH: %w", err)
	}

	entries := splitPath(cur)
	if containsPath(entries, dir) {
		return false, nil
	}
	// Prepend so Skrog's docker wins over a lingering Docker Desktop on PATH.
	updated := strings.Join(append([]string{dir}, entries...), ";")

	// Preserve REG_EXPAND_SZ when that was the original type, so %VAR% entries
	// already in PATH keep expanding; default to expandable for a fresh value.
	if valType == registry.EXPAND_SZ || valType == 0 {
		err = k.SetExpandStringValue("Path", updated)
	} else {
		err = k.SetStringValue("Path", updated)
	}
	if err != nil {
		return false, fmt.Errorf("writing user PATH: %w", err)
	}
	broadcastEnvChange()
	return true, nil
}

// RemoveFromUserPath removes dir from the user's PATH if present. Returns whether
// a change was made. Removing an absent entry is success.
func RemoveFromUserPath(dir string) (bool, error) {
	k, err := registry.OpenKey(registry.CURRENT_USER, envKeyPath, registry.QUERY_VALUE|registry.SET_VALUE)
	if err != nil {
		return false, fmt.Errorf("opening the user Environment key: %w", err)
	}
	defer k.Close()

	cur, valType, err := k.GetStringValue("Path")
	if err == registry.ErrNotExist {
		return false, nil
	} else if err != nil {
		return false, fmt.Errorf("reading user PATH: %w", err)
	}

	entries := splitPath(cur)
	kept := make([]string, 0, len(entries))
	removed := false
	for _, e := range entries {
		if samePath(e, dir) {
			removed = true
			continue
		}
		kept = append(kept, e)
	}
	if !removed {
		return false, nil
	}

	updated := strings.Join(kept, ";")
	if valType == registry.EXPAND_SZ || valType == 0 {
		err = k.SetExpandStringValue("Path", updated)
	} else {
		err = k.SetStringValue("Path", updated)
	}
	if err != nil {
		return false, fmt.Errorf("writing user PATH: %w", err)
	}
	broadcastEnvChange()
	return true, nil
}

// UserPathContains reports whether dir is already on the user PATH.
func UserPathContains(dir string) (bool, error) {
	k, err := registry.OpenKey(registry.CURRENT_USER, envKeyPath, registry.QUERY_VALUE)
	if err != nil {
		return false, err
	}
	defer k.Close()
	cur, _, err := k.GetStringValue("Path")
	if err == registry.ErrNotExist {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return containsPath(splitPath(cur), dir), nil
}

func splitPath(v string) []string {
	var out []string
	for _, e := range strings.Split(v, ";") {
		if strings.TrimSpace(e) != "" {
			out = append(out, e)
		}
	}
	return out
}

func containsPath(entries []string, dir string) bool {
	for _, e := range entries {
		if samePath(e, dir) {
			return true
		}
	}
	return false
}

// samePath compares two PATH entries case-insensitively with trailing
// separators normalized, which is how Windows treats them.
func samePath(a, b string) bool {
	norm := func(s string) string {
		return strings.ToLower(strings.TrimRight(strings.TrimSpace(s), `\/`))
	}
	return norm(a) == norm(b)
}

// broadcastEnvChange tells top-level windows the environment changed, so a new
// process launched from Explorer picks up the PATH without a logoff. Best
// effort: failure just means the change lands on next logon.
func broadcastEnvChange() {
	const (
		HWND_BROADCAST   = 0xFFFF
		WM_SETTINGCHANGE = 0x001A
		SMTO_ABORTIFHUNG = 0x0002
	)
	user32 := syscall.NewLazyDLL("user32.dll")
	proc := user32.NewProc("SendMessageTimeoutW")
	env, _ := syscall.UTF16PtrFromString("Environment")
	var out uintptr
	proc.Call(uintptr(HWND_BROADCAST), uintptr(WM_SETTINGCHANGE), 0,
		uintptr(unsafe.Pointer(env)), uintptr(SMTO_ABORTIFHUNG), 5000, uintptr(unsafe.Pointer(&out)))
}
