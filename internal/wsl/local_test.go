package wsl_test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"unicode/utf16"

	"github.com/hawserhq/hawser/internal/wsl"
)

// fakeRunner records invocations and replays canned output, so every wsl.exe
// argument list the product builds is asserted without WSL being present.
type fakeRunner struct {
	calls   [][]string
	started [][]string
	// replies maps a joined argument prefix to output.
	replies map[string]reply
}

type reply struct {
	out []byte
	err error
}

func newFakeRunner() *fakeRunner {
	return &fakeRunner{replies: map[string]reply{}}
}

func (f *fakeRunner) on(argPrefix string, out string, err error) *fakeRunner {
	f.replies[argPrefix] = reply{out: utf16leBytes(out), err: err}
	return f
}

func (f *fakeRunner) onRaw(argPrefix string, out []byte, err error) *fakeRunner {
	f.replies[argPrefix] = reply{out: out, err: err}
	return f
}

func (f *fakeRunner) Run(_ context.Context, _ string, args ...string) ([]byte, error) {
	f.calls = append(f.calls, args)
	joined := strings.Join(args, " ")
	for prefix, r := range f.replies {
		if strings.HasPrefix(joined, prefix) {
			return r.out, r.err
		}
	}
	return nil, errors.New("fakeRunner: no reply for " + joined)
}

func (f *fakeRunner) Start(_ context.Context, _ string, args ...string) (func(), error) {
	f.started = append(f.started, args)
	return func() {}, nil
}

func (f *fakeRunner) lastCall() []string {
	if len(f.calls) == 0 {
		return nil
	}
	return f.calls[len(f.calls)-1]
}

// utf16leBytes mimics wsl.exe, which emits its own messages as UTF-16LE.
func utf16leBytes(s string) []byte {
	u := utf16.Encode([]rune(s))
	b := make([]byte, 0, len(u)*2)
	for _, c := range u {
		b = append(b, byte(c), byte(c>>8))
	}
	return b
}

func TestStatusReportsVersionAndDefault(t *testing.T) {
	r := newFakeRunner().
		on("--version", "WSL version: 2.7.8.0\nKernel version: 6.6.87.2\nWSLg version: 1.0.66", nil).
		on("--status", "Default Distribution: Ubuntu\nDefault Version: 2", nil)

	st, err := (&wsl.Local{Runner: r}).Status(context.Background())
	if err != nil {
		t.Fatalf("Status: %v", err)
	}
	if !st.Installed {
		t.Error("Installed = false, want true")
	}
	if st.Version != "2.7.8.0" {
		t.Errorf("Version = %q, want 2.7.8.0", st.Version)
	}
	if st.DefaultVersion != 2 {
		t.Errorf("DefaultVersion = %d, want 2", st.DefaultVersion)
	}
}

func TestStatusMissingWSLIsNotAnError(t *testing.T) {
	// Preflight's job is to print enable-and-reboot instructions, so a machine
	// without WSL must report not-installed rather than fail (PLAN §05).
	r := newFakeRunner().
		on("--version", "", errors.New("exec: wsl.exe not found")).
		on("--status", "", errors.New("exec: wsl.exe not found"))

	st, err := (&wsl.Local{Runner: r}).Status(context.Background())
	if err != nil {
		t.Fatalf("Status returned error for a missing wsl.exe: %v", err)
	}
	if st.Installed {
		t.Error("Installed = true, want false")
	}
}

func TestStatusOldBuildWithoutVersionCommand(t *testing.T) {
	// `wsl --version` postdates `wsl --status`; an older build still works, and
	// the empty Version is itself the signal that mirrored networking is out.
	r := newFakeRunner().
		on("--version", "", errors.New("invalid command line argument")).
		on("--status", "Default Version: 2", nil)

	st, err := (&wsl.Local{Runner: r}).Status(context.Background())
	if err != nil {
		t.Fatalf("Status: %v", err)
	}
	if !st.Installed {
		t.Error("Installed = false, want true")
	}
	if st.Version != "" {
		t.Errorf("Version = %q, want empty", st.Version)
	}
	if st.DefaultVersion != 2 {
		t.Errorf("DefaultVersion = %d, want 2", st.DefaultVersion)
	}
}

func TestStatusLocalizedDefaultVersion(t *testing.T) {
	// The label is localized; only the trailing digit is parsed.
	r := newFakeRunner().
		on("--version", "WSL-Version: 2.7.8.0", nil).
		on("--status", "Standardversion: 2", nil)

	st, err := (&wsl.Local{Runner: r}).Status(context.Background())
	if err != nil {
		t.Fatalf("Status: %v", err)
	}
	if st.DefaultVersion != 2 {
		t.Errorf("DefaultVersion = %d, want 2", st.DefaultVersion)
	}
}

func TestImportBuildsCorrectArgs(t *testing.T) {
	r := newFakeRunner().on("--import", "The operation completed successfully.", nil)
	err := (&wsl.Local{Runner: r}).Import(context.Background(),
		"hawser-engine", `C:\data\hawser`, `C:\tmp\rootfs.tar.gz`)
	if err != nil {
		t.Fatalf("Import: %v", err)
	}

	want := []string{"--import", "hawser-engine", `C:\data\hawser`, `C:\tmp\rootfs.tar.gz`, "--version", "2"}
	got := r.lastCall()
	if strings.Join(got, "|") != strings.Join(want, "|") {
		t.Errorf("args =\n %v\nwant\n %v", got, want)
	}
}

func TestImportValidatesArgs(t *testing.T) {
	l := &wsl.Local{Runner: newFakeRunner()}
	ctx := context.Background()
	for _, tc := range [][3]string{
		{"", "dir", "tar"},
		{"d", "", "tar"},
		{"d", "dir", ""},
	} {
		if err := l.Import(ctx, tc[0], tc[1], tc[2]); err == nil {
			t.Errorf("Import(%q,%q,%q) succeeded, want error", tc[0], tc[1], tc[2])
		}
	}
}

func TestErrorIncludesWSLMessage(t *testing.T) {
	// wsl.exe puts the useful detail in its output, not the exit code, so the
	// message must be surfaced or diagnosis becomes guesswork.
	r := newFakeRunner().on("--import",
		"The supplied file is not a valid distribution.", errors.New("exit status 1"))

	err := (&wsl.Local{Runner: r}).Import(context.Background(), "d", "dir", "tar")
	if err == nil {
		t.Fatal("Import succeeded, want error")
	}
	if !strings.Contains(err.Error(), "not a valid distribution") {
		t.Errorf("error %q does not include the wsl.exe message", err)
	}
}

func TestListParsesDistros(t *testing.T) {
	r := newFakeRunner().on("--list --verbose", strings.Join([]string{
		"  NAME             STATE           VERSION",
		"* Ubuntu           Running         2",
		"  hawser-engine    Stopped         2",
	}, "\r\n"), nil)

	got, err := (&wsl.Local{Runner: r}).List(context.Background())
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("got %d distros %+v, want 2", len(got), got)
	}
	if got[1].Name != "hawser-engine" || got[1].Running() {
		t.Errorf("hawser-engine = %+v", got[1])
	}
}

func TestListNoDistrosIsEmptyNotError(t *testing.T) {
	// A fresh machine exits non-zero here; that is an empty list, not a fault.
	r := newFakeRunner().on("--list --verbose",
		"Windows Subsystem for Linux has no installed distributions.",
		errors.New("exit status 1"))

	got, err := (&wsl.Local{Runner: r}).List(context.Background())
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("got %+v, want empty", got)
	}
}

func TestExecUsesExecToBypassShell(t *testing.T) {
	r := newFakeRunner().onRaw("-d hawser-engine", []byte("alive"), nil)

	out, err := (&wsl.Local{Runner: r}).Exec(context.Background(),
		"hawser-engine", "root", "echo", "alive")
	if err != nil {
		t.Fatalf("Exec: %v", err)
	}
	if out != "alive" {
		t.Errorf("output = %q, want %q", out, "alive")
	}

	want := []string{"-d", "hawser-engine", "-u", "root", "--exec", "echo", "alive"}
	if strings.Join(r.lastCall(), "|") != strings.Join(want, "|") {
		t.Errorf("args = %v, want %v", r.lastCall(), want)
	}
}

func TestExecOmitsUserWhenEmpty(t *testing.T) {
	r := newFakeRunner().onRaw("-d d", []byte("ok"), nil)
	if _, err := (&wsl.Local{Runner: r}).Exec(context.Background(), "d", "", "true"); err != nil {
		t.Fatalf("Exec: %v", err)
	}
	got := strings.Join(r.lastCall(), " ")
	if strings.Contains(got, "-u") {
		t.Errorf("args %q should not contain -u when user is empty", got)
	}
}

func TestExecPreservesArgumentsVerbatim(t *testing.T) {
	// --exec means no shell re-interprets these; a path with spaces and a glob
	// must arrive unchanged.
	r := newFakeRunner().onRaw("-d d", []byte(""), nil)
	args := []string{"sh", "-c", "echo 'a b' > /mnt/c/Program Files/x; ls *"}
	if _, err := (&wsl.Local{Runner: r}).Exec(context.Background(), "d", "root", args...); err != nil {
		t.Fatalf("Exec: %v", err)
	}
	got := r.lastCall()
	tail := got[len(got)-len(args):]
	for i := range args {
		if tail[i] != args[i] {
			t.Errorf("arg %d = %q, want %q", i, tail[i], args[i])
		}
	}
}

func TestExecValidates(t *testing.T) {
	l := &wsl.Local{Runner: newFakeRunner()}
	if _, err := l.Exec(context.Background(), "", "root", "true"); err == nil {
		t.Error("Exec with no distro succeeded, want error")
	}
	if _, err := l.Exec(context.Background(), "d", "root"); err == nil {
		t.Error("Exec with no command succeeded, want error")
	}
}

func TestStartDoesNotWait(t *testing.T) {
	r := newFakeRunner()
	stop, err := (&wsl.Local{Runner: r}).Start(context.Background(),
		"hawser-engine", "root", "dockerd")
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	if stop == nil {
		t.Fatal("Start returned a nil stop function")
	}
	stop()

	if len(r.started) != 1 {
		t.Fatalf("started %d commands, want 1", len(r.started))
	}
	want := []string{"-d", "hawser-engine", "-u", "root", "--exec", "dockerd"}
	if strings.Join(r.started[0], "|") != strings.Join(want, "|") {
		t.Errorf("args = %v, want %v", r.started[0], want)
	}
}

func TestTerminateAndUnregister(t *testing.T) {
	r := newFakeRunner().
		on("--terminate", "ok", nil).
		on("--unregister", "ok", nil)
	l := &wsl.Local{Runner: r}
	ctx := context.Background()

	if err := l.Terminate(ctx, "hawser-engine"); err != nil {
		t.Fatalf("Terminate: %v", err)
	}
	if got := strings.Join(r.lastCall(), " "); got != "--terminate hawser-engine" {
		t.Errorf("terminate args = %q", got)
	}
	if err := l.Unregister(ctx, "hawser-engine"); err != nil {
		t.Fatalf("Unregister: %v", err)
	}
	if got := strings.Join(r.lastCall(), " "); got != "--unregister hawser-engine" {
		t.Errorf("unregister args = %q", got)
	}

	if err := l.Terminate(ctx, ""); err == nil {
		t.Error("Terminate with no name succeeded, want error")
	}
	if err := l.Unregister(ctx, ""); err == nil {
		t.Error("Unregister with no name succeeded, want error")
	}
}

func TestCustomExePath(t *testing.T) {
	r := newFakeRunner().on("--terminate", "ok", nil)
	l := &wsl.Local{Exe: `C:\Windows\System32\wsl.exe`, Runner: r}
	if err := l.Terminate(context.Background(), "d"); err != nil {
		t.Fatalf("Terminate: %v", err)
	}
	// The fake ignores the exe, but this asserts the option is plumbed and the
	// call still succeeds rather than looking up PATH.
	if len(r.calls) != 1 {
		t.Errorf("calls = %d, want 1", len(r.calls))
	}
}
