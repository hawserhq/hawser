package main

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The release ships skrog-agent beside skrog.exe so the wslc backend needs no
// distro and no build step (#335). Preferring it over the distro's copy is the
// point: lifting from the distro reaches whatever the installed rootfs carries,
// and an older agent silently costs published ports.
func TestLoadGuestAgentPrefersTheShippedCopy(t *testing.T) {
	dir := t.TempDir()
	want := []byte("\x7fELF-pretend-this-is-the-shipped-agent")
	if err := os.WriteFile(filepath.Join(dir, "skrog-agent"), want, 0o755); err != nil {
		t.Fatal(err)
	}
	withExecutableIn(t, dir)

	got, err := loadGuestAgent(context.Background(), "")
	if err != nil {
		t.Fatalf("loadGuestAgent: %v", err)
	}
	if string(got) != string(want) {
		t.Errorf("loaded %q, want the binary sitting next to the executable", got)
	}
}

// An explicit --agent still wins over the shipped one.
func TestLoadGuestAgentExplicitPathWins(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "skrog-agent"), []byte("shipped"), 0o755); err != nil {
		t.Fatal(err)
	}
	withExecutableIn(t, dir)

	explicit := filepath.Join(t.TempDir(), "mine")
	if err := os.WriteFile(explicit, []byte("explicit"), 0o755); err != nil {
		t.Fatal(err)
	}
	got, err := loadGuestAgent(context.Background(), explicit)
	if err != nil {
		t.Fatalf("loadGuestAgent: %v", err)
	}
	if string(got) != "explicit" {
		t.Errorf("loaded %q, want the --agent path", got)
	}
}

// An empty file must not be mistaken for an agent: a truncated download would
// otherwise be streamed into the guest and fail there instead of here.
func TestLoadGuestAgentIgnoresAnEmptyShippedFile(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "skrog-agent"), nil, 0o755); err != nil {
		t.Fatal(err)
	}
	withExecutableIn(t, dir)
	withNoDistroAgent(t)

	_, err := loadGuestAgent(context.Background(), "")
	if err == nil {
		t.Fatal("an empty skrog-agent was accepted")
	}
}

// With nothing to load, the error has to say what to do. This is the message a
// user meets on a machine with neither a release layout nor a distro.
func TestLoadGuestAgentErrorSaysHowToGetOne(t *testing.T) {
	withExecutableIn(t, t.TempDir())
	withNoDistroAgent(t)

	_, err := loadGuestAgent(context.Background(), "")
	if err == nil {
		t.Fatal("want an error when there is no agent anywhere")
	}
	for _, want := range []string{"skrog-agent", "--agent", "GOOS=linux"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error does not mention %q: %v", want, err)
		}
	}
}

func TestLoadGuestAgentReportsAnUnreadableExplicitPath(t *testing.T) {
	_, err := loadGuestAgent(context.Background(), filepath.Join(t.TempDir(), "nope"))
	if err == nil {
		t.Fatal("want an error for a --agent path that does not exist")
	}
}

// withExecutableIn points shippedAgentPath at dir for the duration of a test,
// standing in for a release layout where skrog.exe and skrog-agent sit side by
// side.
func withExecutableIn(t *testing.T, dir string) {
	t.Helper()
	prev := executablePath
	executablePath = func() (string, error) { return filepath.Join(dir, "skrog.exe"), nil }
	t.Cleanup(func() { executablePath = prev })
}

// withNoDistroAgent makes the distro fallback fail, so a test can assert what
// happens when there is no agent anywhere. Without this the result depends on
// whether the machine running the tests happens to have an engine installed.
func withNoDistroAgent(t *testing.T) {
	t.Helper()
	prev := liftAgentFromDistro
	liftAgentFromDistro = func(context.Context, string) ([]byte, error) {
		return nil, errors.New("no engine distro on this machine")
	}
	t.Cleanup(func() { liftAgentFromDistro = prev })
}
