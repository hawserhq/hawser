package wslc

import (
	"strings"
	"testing"
)

// A pipe bind means "this engine's socket" on every backend (#164). This is
// what Testcontainers' Ryuk relies on: testcontainers-go hands the reaper the
// host's docker endpoint to bind, and on Windows that endpoint is a pipe. It
// only rewrites the pipe itself when the engine claims to be Docker Desktop,
// and a wslc session reports "Microsoft Azure Linux 3.0" — so without this the
// reaper is handed the literal npipe string as a path and never comes up.
func TestPipeSourcesBecomeTheEngineSocket(t *testing.T) {
	pipes := []string{
		`\\.\pipe\docker_engine`,
		`//./pipe/docker_engine`,
		`\\.\pipe\skrog-wslc-test`,
		`//./pipe/skrog_engine`,
	}
	for _, pipe := range pipes {
		got, err := TranslateBindSource(pipe)
		if err != nil {
			t.Errorf("TranslateBindSource(%q): %v", pipe, err)
			continue
		}
		if got != EngineSocket {
			t.Errorf("TranslateBindSource(%q) = %q, want %q", pipe, got, EngineSocket)
		}
	}
}

// Guest-absolute sources are the whole reason this backend can do what the
// wslc CLI cannot — docker-in-docker, Ryuk, /tmp sharing — so they pass through
// untouched.
func TestGuestPathsPassThrough(t *testing.T) {
	for _, p := range []string{"/var/run/docker.sock", "/tmp", "/var/lib/data"} {
		got, err := TranslateBindSource(p)
		if err != nil {
			t.Errorf("TranslateBindSource(%q): %v", p, err)
			continue
		}
		if got != p {
			t.Errorf("TranslateBindSource(%q) = %q, want it unchanged", p, got)
		}
	}
}

// A Windows drive path must fail loudly rather than be mapped.
//
// winpath.ToWSL would turn C:\src into /mnt/c/src, which is right for a distro
// that auto-mounts drives and meaningless in a session, where each Windows
// folder is its own virtiofs share at /mnt/{GUID} and no /mnt/c exists. The
// container would receive an empty directory instead of the folder — a silent
// wrong answer where an error is far kinder (#321).
func TestWindowsPathsAreRefusedNotSilentlyMapped(t *testing.T) {
	for _, p := range []string{`C:\src`, `C:/src`, `D:\data\project`} {
		got, err := TranslateBindSource(p)
		if err == nil {
			t.Errorf("TranslateBindSource(%q) = %q with no error; want a refusal", p, got)
			continue
		}
		// The error has to point somewhere, not just say no.
		if !strings.Contains(err.Error(), "#321") || !strings.Contains(err.Error(), "/tmp") {
			t.Errorf("error for %q does not explain the situation: %v", p, err)
		}
	}
}

// `/c/src` and `//c/src` are NOT drive paths to winpath — driveLen only accepts
// the "C:" designator — so neither backend treats them as Windows sources, and
// both hand them to the engine unchanged. Asserted so this translator is not
// "fixed" later to refuse them, which would diverge from the distro backend for
// no reason.
func TestPosixLookingSourcesAreNotTreatedAsDrives(t *testing.T) {
	for _, p := range []string{"/c/src", "//c/src"} {
		got, err := TranslateBindSource(p)
		if err != nil {
			t.Errorf("TranslateBindSource(%q): unexpected refusal: %v", p, err)
			continue
		}
		if got != p {
			t.Errorf("TranslateBindSource(%q) = %q, want it unchanged", p, got)
		}
	}
}
