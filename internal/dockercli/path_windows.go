//go:build windows

package dockercli

import (
	"fmt"
	"path/filepath"
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

// samePath reports whether two PATH entries name the same directory.
//
// One side is normally a raw registry entry and the other a directory derived
// from exec.LookPath, and those are spelled differently often enough that a
// naive comparison is wrong on a stock Windows machine (#286).
func samePath(a, b string) bool {
	na, nb := normPathEntry(a), normPathEntry(b)
	return na != "" && na == nb
}

// normPathEntry renders a PATH entry the way exec.LookPath would hand it back.
//
// Three differences, each of which produced a silent miss:
//
//   - REG_EXPAND_SZ values come out of registry.GetStringValue *unexpanded*, so
//     a stock `%SystemRoot%\system32` never matched `C:\Windows\system32`, and
//     the caller concluded the directory was on neither PATH. Expanding a
//     literal path is a no-op, so both sides can go through it.
//   - filepath.SplitList and exec.LookPath strip surrounding quotes, so an
//     entry stored as "C:\Program Files\X" never matched the unquoted
//     directory of the binary found in it.
//   - LookPath returns filepath.Join(dir, name), which Cleans: `C:/tools/bin`,
//     `C:\tools\\bin` and `C:\tools\.\bin` all arrive spelled `C:\tools\bin`.
func normPathEntry(s string) string {
	s = strings.TrimSpace(s)
	if len(s) >= 2 && strings.HasPrefix(s, `"`) && strings.HasSuffix(s, `"`) {
		s = strings.TrimSpace(s[1 : len(s)-1])
	}
	if strings.Contains(s, "%") {
		if expanded, err := registry.ExpandString(s); err == nil {
			s = expanded
		}
	}
	if s == "" {
		return ""
	}
	return strings.ToLower(strings.TrimRight(filepath.Clean(s), `\/`))
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

// The system-wide environment key. Both the root and the path are vars so a
// test can point them at a scratch key under HKCU: HKLM is not writable without
// elevation, so redirecting the path alone -- as this used to -- achieved
// nothing and left the machine half of PathScopeOf with no coverage (#286).
var (
	machineEnvRoot    = registry.LOCAL_MACHINE
	machineEnvKeyPath = `SYSTEM\CurrentControlSet\Control\Session Manager\Environment`
)

// Scope says which PATH a directory is on.
//
// It matters because Windows composes a process PATH as machine-then-user, so
// *every* machine entry resolves before *every* user entry. Skrog writes only
// the user PATH -- deliberately, since that needs no elevation -- and therefore
// cannot out-order a machine entry at all. Advice that says "make sure Skrog's
// bin directory precedes it" is impossible in that case, and telling a user to
// do an impossible thing is worse than telling them nothing (#282).
type Scope int

const (
	// ScopeUnknown means neither PATH could be read, or the directory is on
	// neither -- a session-only PATH entry, most likely.
	ScopeUnknown Scope = iota
	// ScopeUser is the per-user PATH, which Skrog can edit without elevation.
	ScopeUser
	// ScopeMachine is the system PATH, which always resolves first and needs
	// elevation to change.
	ScopeMachine
)

// PathScopeOf reports which PATH dir is on. Machine wins when it is on both,
// because that is the one that decides resolution order.
//
// Errors are folded into ScopeUnknown on purpose: this exists to make a message
// more accurate, and a message is never worth failing a command for.
func PathScopeOf(dir string) Scope {
	if onMachinePath(dir) {
		return ScopeMachine
	}
	if ok, err := UserPathContains(dir); err == nil && ok {
		return ScopeUser
	}
	return ScopeUnknown
}

func onMachinePath(dir string) bool {
	k, err := registry.OpenKey(machineEnvRoot, machineEnvKeyPath, registry.QUERY_VALUE)
	if err != nil {
		return false
	}
	defer k.Close()
	cur, _, err := k.GetStringValue("Path")
	if err != nil {
		return false
	}
	return containsPath(splitPath(cur), dir)
}
