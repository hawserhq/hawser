//go:build windows

package supervise

import (
	"fmt"
	"os"
	"path/filepath"
	"time"

	"golang.org/x/sys/windows"
)

// Lock is a held single-instance claim. Release with Close; the OS also
// releases it if the process dies, which is the property a PID file cannot
// offer without staleness heuristics.
//
// The claim is an exclusively-opened DELETE_ON_CLOSE file in the state dir,
// not a named mutex (#71). A mutex conflates two lifetimes — the OBJECT lives
// while any handle exists, but the LOCK is thread-affine ownership — and both
// bit us: a status probe's lingering handle made a fresh Acquire read
// "already running", and testing ownership instead made a second Acquire on
// the same OS thread recursively succeed. File sharing has neither problem:
// exclusion is per-handle (share-none rejects any second open with access,
// regardless of thread or process), probes open with zero access, which the
// kernel exempts from sharing checks entirely (they can neither block a real
// Acquire nor keep the claim alive), and process death closes the handle,
// which deletes the file.
type Lock struct {
	handle windows.Handle
}

// lockPath is the claim file inside the (normalized) state dir — per-install
// by construction: two Skrog installs with different state dirs (the e2e
// suite next to a real install) must not exclude each other. Normalization
// (#71) makes `--state-dir C:/x`, `C:\x`, and relative spellings agree.
func lockPath(stateDir string) string {
	norm := stateDir
	if abs, err := filepath.Abs(stateDir); err == nil {
		norm = abs
	}
	return filepath.Join(filepath.Clean(norm), "supervisor.lock")
}

// ErrAlreadyRunning reports a second supervisor for the same install.
type ErrAlreadyRunning struct{ StateDir string }

func (e *ErrAlreadyRunning) Error() string {
	return fmt.Sprintf("a supervisor for %s is already running "+
		"(two would fight over the pipe); use `skrog status` to see it", e.StateDir)
}

// Acquire claims the single-instance lock, failing fast if held elsewhere —
// including elsewhere in this same process.
func Acquire(stateDir string) (*Lock, error) {
	path := lockPath(stateDir)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, fmt.Errorf("creating state dir for the supervisor lock: %w", err)
	}
	name, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return nil, err
	}
	// Bounded retry, for one specific window: when the previous holder closes
	// while a zero-access probe handle is still open, the file sits in
	// delete-pending until that probe handle closes (microseconds), and new
	// opens fail ACCESS_DENIED/DELETE_PENDING in the meantime. That is "just
	// released", not "held" — retry through it. A genuine ACL denial also
	// lands here and surfaces after the deadline with the real error.
	deadline := time.Now().Add(2 * time.Second)
	for {
		h, err := windows.CreateFile(name,
			windows.GENERIC_READ|windows.GENERIC_WRITE,
			0, // share nothing: this open IS the lock
			nil,
			windows.OPEN_ALWAYS, // a leftover file from a hard power loss is reclaimed, not fatal
			windows.FILE_ATTRIBUTE_NORMAL|windows.FILE_FLAG_DELETE_ON_CLOSE,
			0)
		switch err {
		case nil:
			return &Lock{handle: h}, nil
		case windows.ERROR_SHARING_VIOLATION:
			return nil, &ErrAlreadyRunning{StateDir: stateDir}
		case windows.ERROR_ACCESS_DENIED, windows.ERROR_DELETE_PENDING:
			if time.Now().After(deadline) {
				return nil, fmt.Errorf("acquiring supervisor lock: %w", err)
			}
			time.Sleep(time.Millisecond)
		default:
			return nil, fmt.Errorf("acquiring supervisor lock: %w", err)
		}
	}
}

// Held reports whether some process holds the lock, without taking it. Used by
// `skrog status` and by `skrog start` to decide whether to spawn a
// supervisor.
//
// The probe opens with zero access, which bypasses sharing checks: it can
// never block a real Acquire, never keeps the claim alive, and a held lock
// still answers it. Advisory by design — Acquire is the arbiter, and a
// supervisor spawned against a stale answer simply exits through Acquire's
// already-running path.
func Held(stateDir string) bool {
	name, err := windows.UTF16PtrFromString(lockPath(stateDir))
	if err != nil {
		return true // unqueryable reads as held: never spawn a duplicate
	}
	h, err := windows.CreateFile(name,
		0, // query only: exempt from sharing, so this cannot interfere
		windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE,
		nil,
		windows.OPEN_EXISTING,
		windows.FILE_ATTRIBUTE_NORMAL,
		0)
	if err != nil {
		// Not found (or a pending delete) means nobody holds it.
		return false
	}
	windows.CloseHandle(h)
	return true
}

// Close releases the claim; DELETE_ON_CLOSE removes the file with the handle.
func (l *Lock) Close() error {
	if l.handle != 0 {
		windows.CloseHandle(l.handle)
		l.handle = 0
	}
	return nil
}
