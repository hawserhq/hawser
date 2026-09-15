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
	"io"
	"os"
	"os/exec"
	"strings"
)

// DefaultExe is where WSL installs the CLI. It is not on PATH by default.
const DefaultExe = `C:\Program Files\WSL\wslc.exe`

// SessionName is the session Skrog would prefer to own. Keeping Skrog's
// containers out of the CLI's default session is what stops Skrog's policy
// from applying to whatever the user does with `wslc` by hand, and vice versa
// (#322).
//
// It is aspirational for now, because the shipped CLI cannot create a
// persistent named session. Measured on 2.9.11.0:
//
//   - `--session <name>` on any subcommand requires the session to already
//     exist; it never creates one (WSLC_E_SESSION_NOT_FOUND).
//   - `system session enter --name <name> <storage>` is the only creator, and
//     its own help says the session is "non-persistent" and "deleted when the
//     shell exits" — so holding one open means holding a shell process open
//     for the lifetime of the engine.
//   - The CLI's default session, by contrast, outlives its creator: verified
//     with the creator PID gone and the session still listed, serving
//     commands, and cold-booting its VM on demand.
//
// So a dedicated session needs either the SDK's WslcCreateSession or the
// internal COM path, neither of which is a dependency worth taking for a
// spike. Until then ResolveSession falls back to the default session and the
// policy caveat in #322 stands.
const SessionName = "skrog"

// DefaultSessionName is the session the wslc CLI creates for the current user
// when no --session is given.
func DefaultSessionName() string {
	user := os.Getenv("USERNAME")
	if user == "" {
		user = os.Getenv("USER")
	}
	return "wslc-cli-" + user
}

// ResolveSession picks the session to work in: Skrog's own if it exists,
// otherwise the CLI default — creating that default when nothing is running,
// so starting the bridge needs no manual setup step (#335).
//
// It still cannot create a NAMED session; see SessionName for why.
func (l *Local) ResolveSession(ctx context.Context) (string, error) {
	sessions, err := l.Sessions(ctx)
	if err != nil {
		return "", err
	}
	if len(sessions) == 0 {
		// Not an error, and telling the user to run `wslc run --rm hello-world`
		// first was pure friction (#335). The CLI creates the default session
		// on demand, so Skrog can simply ask for it.
		//
		// It has to be asked for in the right way, though: `system session run`
		// with NO --session creates it, and WITH --session requires it to
		// already exist ("Session not found"). Everything downstream passes
		// --session, so one bare call has to go first. Found by running this
		// against a machine with every session terminated.
		if err := l.createDefaultSession(ctx); err != nil {
			return "", err
		}
		return DefaultSessionName(), nil
	}
	fallback := DefaultSessionName()
	var haveFallback bool
	for _, s := range sessions {
		if s.DisplayName == SessionName {
			return SessionName, nil
		}
		if s.DisplayName == fallback {
			haveFallback = true
		}
	}
	if haveFallback {
		return fallback, nil
	}
	// Some other session exists; using it is better than failing, and the
	// caller logs which one it picked.
	return sessions[0].DisplayName, nil
}

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

// RunInSessionStdin is RunInSession with data piped to the command's stdin.
//
// The relay is byte-exact and handles multi-megabyte payloads: verified by
// streaming the 2.6 MB agent binary in and comparing sha256 on both sides.
// That is what lets the bootstrap place a binary and a secret in the root
// namespace without a holder container or a virtiofs share — and the secret
// never touches a Windows folder the user's other processes can read (#320).
func (l *Local) RunInSessionStdin(ctx context.Context, session string, stdin io.Reader, cmd ...string) (string, error) {
	if session == "" {
		return "", fmt.Errorf("wslc: session name is required")
	}
	if len(cmd) == 0 {
		return "", fmt.Errorf("wslc: command is required")
	}
	r, ok := l.runner().(StdinRunner)
	if !ok {
		return "", fmt.Errorf("wslc: runner does not support stdin")
	}
	args := sessionArgs(session, cmd...)
	out, err := r.RunStdin(ctx, stdin, l.exe(), args...)
	text := strings.TrimSpace(string(out))
	if err != nil {
		if text != "" {
			return text, fmt.Errorf("wslc %s: %w: %s", strings.Join(args, " "), err, firstLine(text))
		}
		return text, fmt.Errorf("wslc %s: %w", strings.Join(args, " "), err)
	}
	return text, nil
}

// StdinRunner is the optional half of Runner for commands that need input.
// Kept separate so a test fake can implement only what it exercises.
type StdinRunner interface {
	RunStdin(ctx context.Context, stdin io.Reader, name string, args ...string) ([]byte, error)
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
	return execRunner{}.RunStdin(ctx, bytes.NewReader(nil), name, args...)
}

// RunStdin is the same, with a caller-supplied stdin. A nil reader still gets
// an empty one rather than being left unset, for the reason above.
func (execRunner) RunStdin(ctx context.Context, stdin io.Reader, name string, args ...string) ([]byte, error) {
	if stdin == nil {
		stdin = bytes.NewReader(nil)
	}
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Stdin = stdin
	return cmd.CombinedOutput()
}

// createDefaultSession brings the CLI's default session into existence.
//
// `wslc system session run <cmd>` with no --session creates it and runs there;
// the same command WITH --session fails when it does not yet exist. So this is
// the one call that must omit the flag, and it exists so a user never has to
// run `wslc run --rm hello-world` by hand before starting the bridge (#335).
//
// Booting the session VM takes a few seconds, which is why the caller should
// not be holding a short timeout when this runs.
func (l *Local) createDefaultSession(ctx context.Context) error {
	if _, err := l.run(ctx, "system", "session", "run", "true"); err != nil {
		return fmt.Errorf("wslc: could not start a container session: %w", err)
	}
	return nil
}
