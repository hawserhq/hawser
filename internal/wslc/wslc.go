// Package wslc wraps the `wslc.exe` CLI that ships with WSL 2.9.3+, the way
// internal/wsl wraps `wsl.exe` (#316).
//
// It exists because every wslc session boots a Microsoft-built Moby — dockerd
// on /var/run/docker.sock inside the session VM — with no endpoint reaching it
// from Windows (#317). Skrog can serve that endpoint, but only by way of the
// two operations the shipped CLI exposes: creating a session, and running a
// process in the session VM's root namespace.
//
// Every call goes through Runner so the package is testable without WSL.
package wslc

import (
	"bytes"
	"context"
	"fmt"
	"os/exec"
	"strings"
)

// DefaultExe is where WSL installs the CLI. It is not on PATH by default.
const DefaultExe = `C:\Program Files\WSL\wslc.exe`

// SessionName is the session Skrog owns. Deliberately not the CLI's default
// (`wslc-cli-<user>`): sharing that one would mean Skrog's policy applies to
// whatever the user does with `wslc` by hand, and vice versa (#322).
const SessionName = "skrog"

// Runner executes the CLI. Production uses execRunner; tests substitute a
// fake, which is what keeps this package's tests host-independent.
type Runner interface {
	Run(ctx context.Context, name string, args ...string) ([]byte, error)
}

// Local drives the real wslc.exe.
type Local struct {
	// Exe overrides the CLI path. Empty uses DefaultExe, then PATH.
	Exe string

	// Runner overrides process execution in tests.
	Runner Runner
}

// New returns a Local with defaults.
func New() *Local { return &Local{} }

func (l *Local) exe() string {
	if l.Exe != "" {
		return l.Exe
	}
	return DefaultExe
}

func (l *Local) runner() Runner {
	if l.Runner != nil {
		return l.Runner
	}
	return execRunner{}
}

func (l *Local) run(ctx context.Context, args ...string) (string, error) {
	out, err := l.runner().Run(ctx, l.exe(), args...)
	text := strings.TrimSpace(string(out))
	if err != nil {
		if text != "" {
			return text, fmt.Errorf("wslc %s: %w: %s", strings.Join(args, " "), err, firstLine(text))
		}
		return text, fmt.Errorf("wslc %s: %w", strings.Join(args, " "), err)
	}
	return text, nil
}

// Version reports the CLI version, e.g. "2.9.11.0". A non-nil error means wslc
// is absent or unusable, which is how callers detect "this machine has no
// WSL containers" without a separate probe.
func (l *Local) Version(ctx context.Context) (string, error) {
	out, err := l.run(ctx, "version")
	if err != nil {
		return "", err
	}
	// "wslc 2.9.11.0"
	fields := strings.Fields(out)
	if len(fields) < 2 {
		return "", fmt.Errorf("wslc version: unexpected output %q", out)
	}
	return fields[len(fields)-1], nil
}

// Session is one row of `wslc system session list`.
type Session struct {
	ID          string
	CreatorPID  string
	DisplayName string
}

// Sessions lists the running sessions. An empty slice is not an error: the
// session manager reports no sessions when every VM has idle-terminated,
// which is the normal resting state (#323).
func (l *Local) Sessions(ctx context.Context) ([]Session, error) {
	out, err := l.run(ctx, "system", "session", "list")
	if err != nil {
		return nil, err
	}
	return parseSessions(out), nil
}

// parseSessions reads the CLI's table. Split out so the shape is testable
// against captured output rather than a live session manager.
//
//	ID   Creator PID   Display Name
//	2    26696         wslc-cli-Zoltan
func parseSessions(out string) []Session {
	var sessions []Session
	for i, line := range strings.Split(out, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		// The header is not marked; skip by position and by its first column
		// never being numeric.
		fields := strings.Fields(line)
		if len(fields) < 3 {
			continue
		}
		if i == 0 && !isNumeric(fields[0]) {
			continue
		}
		if !isNumeric(fields[0]) {
			continue
		}
		sessions = append(sessions, Session{
			ID:         fields[0],
			CreatorPID: fields[1],
			// Display names can contain spaces, so take the remainder.
			DisplayName: strings.Join(fields[2:], " "),
		})
	}
	return sessions
}

func isNumeric(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

// HasSession reports whether a session with this display name is running.
func (l *Local) HasSession(ctx context.Context, name string) (bool, error) {
	sessions, err := l.Sessions(ctx)
	if err != nil {
		return false, err
	}
	for _, s := range sessions {
		if s.DisplayName == name {
			return true, nil
		}
	}
	return false, nil
}

// Terminate stops one session's VM. Only ever Skrog's own session: terminating
// the CLI's default would take out whatever the user is running by hand, the
// same rule internal/supervise follows for distros.
func (l *Local) Terminate(ctx context.Context, name string) error {
	_, err := l.run(ctx, "--session", name, "system", "session", "terminate")
	return err
}

// sessionArgs builds the argument list for a root-namespace command.
//
// `--session` is a GLOBAL option and must precede the subcommand; passing it
// after `system session run` is silently wrong (#317).
func sessionArgs(session string, cmd ...string) []string {
	return append([]string{"--session", session, "system", "session", "run"}, cmd...)
}

// RunInSession executes a command in the session VM's ROOT namespace — not in
// a container — and returns its combined output. This is the primitive the
// bootstrap is built on: it is how a relay binary gets started next to
// dockerd's socket.
//
// Starting the session if it does not exist is the CLI's own behaviour, so
// this doubles as "ensure the session".
func (l *Local) RunInSession(ctx context.Context, session string, cmd ...string) (string, error) {
	if session == "" {
		return "", fmt.Errorf("wslc: session name is required")
	}
	if len(cmd) == 0 {
		return "", fmt.Errorf("wslc: command is required")
	}
	return l.run(ctx, sessionArgs(session, cmd...)...)
}

func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return strings.TrimSpace(s[:i])
	}
	return s
}

type execRunner struct{}

// Run always hands the child an explicit stdin, and that is not incidental.
//
// `wslc system session run` relays all three std handles into the guest, and
// the relay setup fails with ERROR_INVALID_HANDLE unless each one is a handle
// it can duplicate. Go's default for a nil Stdin (the NUL device) is not
// enough: measured on wslc 2.9.11.0, `sh -c echo hi` survives it but `curl
// --unix-socket …` does not, so the failure is invisible until the one command
// that matters — talking to the engine socket — is the one that breaks.
//
// An empty reader makes Go create a real pipe and close it immediately, which
// satisfies the relay for every command tried. Stdout and stderr are already
// real (the caller's buffer), so this covers all three.
func (execRunner) Run(ctx context.Context, name string, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Stdin = bytes.NewReader(nil)
	return cmd.CombinedOutput()
}
