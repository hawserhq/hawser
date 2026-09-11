package supervise

import (
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// A supervisor restart is requested through a file, for the same reason the
// desired state is (#202).
//
// Windows has no SIGTERM to send, and the single-instance claim is a
// DELETE_ON_CLOSE handle rather than a PID file — deliberately, because the
// OS releasing it on process death is the property that makes it
// trustworthy — so there is no pid to signal even if there were a signal.
// Killing the process by name would also be wrong: it would look like a crash
// to the watchdog, which would restart it on the crash path with backoff
// instead of letting the CLI bring up a clean one.
//
// So: the CLI leaves a note, the supervisor reads it, deletes it, and exits
// zero. Exiting zero is what tells the watchdog this was asked for rather
// than a crash, so it does not race the CLI to respawn.

func restartPath(stateDir string) string {
	return filepath.Join(stateDir, "restart-request")
}

// RequestRestart asks a running supervisor to exit cleanly. It is a no-op
// against an install with no supervisor running — ClearRestart at startup
// means a note left for a supervisor that was already gone cannot make the
// next one exit on sight.
func RequestRestart(stateDir string) error {
	if err := os.MkdirAll(stateDir, 0o755); err != nil {
		return fmt.Errorf("creating state dir: %w", err)
	}
	stamp := time.Now().UTC().Format(time.RFC3339Nano) + "\n"
	tmp := restartPath(stateDir) + ".tmp"
	if err := os.WriteFile(tmp, []byte(stamp), 0o644); err != nil {
		return fmt.Errorf("writing restart request: %w", err)
	}
	if err := os.Rename(tmp, restartPath(stateDir)); err != nil {
		return fmt.Errorf("committing restart request: %w", err)
	}
	return nil
}

// RestartRequested reports whether a restart has been asked for.
func RestartRequested(stateDir string) bool {
	_, err := os.Stat(restartPath(stateDir))
	return err == nil
}

// ClearRestart removes any outstanding request. The supervisor calls this
// twice: once at startup, so a note left behind by a crash cannot shut down a
// healthy new process, and once when it acts on one.
func ClearRestart(stateDir string) error {
	err := os.Remove(restartPath(stateDir))
	if os.IsNotExist(err) {
		return nil
	}
	return err
}
