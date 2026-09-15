package wslc

import (
	"context"
	"errors"
	"strings"
	"testing"
)

// fakeRunner records the last invocation and replays a canned result, so the
// argument order — which is load-bearing for `--session` — is assertable
// without a live session manager.
type fakeRunner struct {
	out  string
	err  error
	name string
	args []string
}

func (f *fakeRunner) Run(_ context.Context, name string, args ...string) ([]byte, error) {
	f.name = name
	f.args = args
	return []byte(f.out), f.err
}

func local(f *fakeRunner) *Local { return &Local{Exe: "wslc.exe", Runner: f} }

func TestVersionParsesTheCLIBanner(t *testing.T) {
	f := &fakeRunner{out: "wslc 2.9.11.0\n"}
	got, err := local(f).Version(context.Background())
	if err != nil {
		t.Fatalf("Version: %v", err)
	}
	if got != "2.9.11.0" {
		t.Errorf("version = %q, want 2.9.11.0", got)
	}
}

func TestVersionRejectsOutputItDoesNotUnderstand(t *testing.T) {
	f := &fakeRunner{out: "???"}
	if _, err := local(f).Version(context.Background()); err == nil {
		t.Fatal("want an error for unparseable version output, got nil")
	}
}

// A missing wslc.exe is the ordinary case on a machine without WSL containers,
// and it must surface as an error rather than an empty version that reads like
// success.
func TestVersionSurfacesALaunchFailure(t *testing.T) {
	f := &fakeRunner{err: errors.New("exec: file not found")}
	if _, err := local(f).Version(context.Background()); err == nil {
		t.Fatal("want an error when the CLI cannot be launched")
	}
}

func TestParseSessionsReadsTheRealTable(t *testing.T) {
	// Captured verbatim from wslc 2.9.11.0.
	const out = `ID   Creator PID   Display Name
2    26696         wslc-cli-Zoltan
7    31004         skrog`

	got := parseSessions(out)
	if len(got) != 2 {
		t.Fatalf("parsed %d sessions, want 2: %+v", len(got), got)
	}
	if got[0].ID != "2" || got[0].CreatorPID != "26696" || got[0].DisplayName != "wslc-cli-Zoltan" {
		t.Errorf("first row = %+v", got[0])
	}
	if got[1].DisplayName != "skrog" {
		t.Errorf("second row display name = %q, want skrog", got[1].DisplayName)
	}
}

// No sessions is the resting state once every VM has idle-terminated, so the
// header-only table must parse as "none", not as an error or a phantom row.
func TestParseSessionsHandlesAnEmptyTable(t *testing.T) {
	const out = `ID   Creator PID   Display Name`
	if got := parseSessions(out); len(got) != 0 {
		t.Errorf("parsed %d sessions from a header-only table, want 0: %+v", len(got), got)
	}
}

func TestParseSessionsKeepsSpacesInDisplayNames(t *testing.T) {
	const out = `ID   Creator PID   Display Name
3    100           my session name`
	got := parseSessions(out)
	if len(got) != 1 || got[0].DisplayName != "my session name" {
		t.Errorf("got %+v, want one row named %q", got, "my session name")
	}
}

// --session is a GLOBAL option: the CLI ignores it after the subcommand, which
// silently runs against the wrong session (#317). This is the one ordering
// mistake that would be invisible in manual testing.
func TestSessionArgsPutSessionBeforeTheSubcommand(t *testing.T) {
	args := sessionArgs("skrog", "/bin/true")
	want := []string{"--session", "skrog", "system", "session", "run", "/bin/true"}
	if strings.Join(args, " ") != strings.Join(want, " ") {
		t.Errorf("args = %v, want %v", args, want)
	}
	if args[0] != "--session" {
		t.Errorf("--session must come first, got %q", args[0])
	}
}

func TestRunInSessionPassesTheCommandThrough(t *testing.T) {
	f := &fakeRunner{out: "hello"}
	got, err := local(f).RunInSession(context.Background(), "skrog", "echo", "hello")
	if err != nil {
		t.Fatalf("RunInSession: %v", err)
	}
	if got != "hello" {
		t.Errorf("output = %q, want hello", got)
	}
	want := "--session skrog system session run echo hello"
	if strings.Join(f.args, " ") != want {
		t.Errorf("args = %q, want %q", strings.Join(f.args, " "), want)
	}
}

func TestRunInSessionRequiresSessionAndCommand(t *testing.T) {
	f := &fakeRunner{}
	if _, err := local(f).RunInSession(context.Background(), "", "true"); err == nil {
		t.Error("want an error for an empty session name")
	}
	if _, err := local(f).RunInSession(context.Background(), "skrog"); err == nil {
		t.Error("want an error for an empty command")
	}
}

func TestHasSessionMatchesOnDisplayName(t *testing.T) {
	f := &fakeRunner{out: "ID   Creator PID   Display Name\n2    26696         wslc-cli-Zoltan"}
	l := local(f)

	if ok, err := l.HasSession(context.Background(), "skrog"); err != nil || ok {
		t.Errorf("HasSession(skrog) = %v, %v; want false, nil", ok, err)
	}
	if ok, err := l.HasSession(context.Background(), "wslc-cli-Zoltan"); err != nil || !ok {
		t.Errorf("HasSession(wslc-cli-Zoltan) = %v, %v; want true, nil", ok, err)
	}
}

// Terminate must name the session explicitly; a bare terminate would hit the
// CLI's default session and take out whatever the user is running by hand.
func TestTerminateNamesTheSession(t *testing.T) {
	f := &fakeRunner{}
	if err := local(f).Terminate(context.Background(), "skrog"); err != nil {
		t.Fatalf("Terminate: %v", err)
	}
	want := "--session skrog system session terminate"
	if strings.Join(f.args, " ") != want {
		t.Errorf("args = %q, want %q", strings.Join(f.args, " "), want)
	}
}

// The CLI reports failures on stdout as often as through the exit code, so the
// message has to reach the caller rather than being swallowed.
func TestErrorsCarryTheCLIMessage(t *testing.T) {
	f := &fakeRunner{out: "ERROR_INVALID_HANDLE\nsecond line", err: errors.New("exit status 1")}
	_, err := local(f).RunInSession(context.Background(), "skrog", "true")
	if err == nil {
		t.Fatal("want an error")
	}
	if !strings.Contains(err.Error(), "ERROR_INVALID_HANDLE") {
		t.Errorf("error %q does not carry the CLI message", err)
	}
}

// agentPattern is AgentPath written out with its first character bracketed, so
// pgrep/pkill cannot match the shell running them. The two are separate
// constants because Go cannot slice a const, so nothing but this test stops
// them drifting apart — and if they do, pkill starts SIGTERMing its own shell
// and pgrep starts reporting a dead agent as alive.
func TestAgentPatternMatchesAgentPath(t *testing.T) {
	want := "[" + AgentPath[0:1] + "]" + AgentPath[1:]
	if agentPattern != want {
		t.Errorf("agentPattern = %q, want %q (derived from AgentPath %q)", agentPattern, want, AgentPath)
	}
	// And the whole point: the pattern must not contain the literal path, or
	// it would match the command line that carries it.
	if strings.Contains(agentPattern, AgentPath) {
		t.Errorf("agentPattern %q contains AgentPath verbatim; it would self-match", agentPattern)
	}
}
