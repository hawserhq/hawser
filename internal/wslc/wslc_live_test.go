//go:build wslc && windows

// Live checks against the real wslc.exe on this machine. Behind a build tag so
// `go test ./...` stays host-independent, the same arrangement internal/release
// uses for its network test:
//
//	go test -tags wslc ./internal/wslc/
//
// These exist because the offline tests validate parsing against captured
// output, which cannot catch the CLI changing its output shape, its argument
// handling, or its std-handle requirements between WSL releases.
package wslc

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"
)

func liveCtx(t *testing.T) context.Context {
	t.Helper()
	if _, err := os.Stat(DefaultExe); err != nil {
		t.Skipf("wslc.exe not present at %s: %v", DefaultExe, err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	t.Cleanup(cancel)
	return ctx
}

func TestLiveVersion(t *testing.T) {
	ctx := liveCtx(t)
	v, err := New().Version(ctx)
	if err != nil {
		t.Fatalf("Version: %v", err)
	}
	if !strings.HasPrefix(v, "2.") {
		t.Errorf("version = %q, want something like 2.9.11.0", v)
	}
	t.Logf("wslc version %s", v)
}

func TestLiveSessionsParses(t *testing.T) {
	ctx := liveCtx(t)
	sessions, err := New().Sessions(ctx)
	if err != nil {
		t.Fatalf("Sessions: %v", err)
	}
	// Zero sessions is legitimate (every VM idle-terminated); what must hold
	// is that anything returned is fully populated rather than a header row
	// that slipped through the parser.
	for _, s := range sessions {
		if s.ID == "" || s.CreatorPID == "" || s.DisplayName == "" {
			t.Errorf("incomplete session row: %+v", s)
		}
		if !isNumeric(s.ID) {
			t.Errorf("session ID %q is not numeric — header row leaked into the parse", s.ID)
		}
	}
	t.Logf("%d live session(s): %+v", len(sessions), sessions)
}

// The load-bearing one: a root-namespace command, driven the way the bootstrap
// will drive it. This is also the check that the CLI still accepts our std
// handles — it fails with ERROR_INVALID_HANDLE if any of the three is invalid,
// and `go test` does not hand children a console.
func TestLiveRunInSessionReachesTheRootNamespace(t *testing.T) {
	ctx := liveCtx(t)
	l := New()

	sessions, err := l.Sessions(ctx)
	if err != nil {
		t.Fatalf("Sessions: %v", err)
	}
	if len(sessions) == 0 {
		t.Skip("no wslc session running; start one with `wslc run --rm hello-world`")
	}
	name := sessions[0].DisplayName

	out, err := l.RunInSession(ctx, name, "sh", "-c", "echo skrog-live-probe; id -u")
	if err != nil {
		t.Fatalf("RunInSession(%s): %v", name, err)
	}
	if !strings.Contains(out, "skrog-live-probe") {
		t.Errorf("output %q does not contain the probe marker", out)
	}
	// The root namespace runs as root; a container would not.
	if !strings.Contains(out, "0") {
		t.Errorf("output %q suggests this did not run as root in the root namespace", out)
	}
	t.Logf("root-namespace output: %q", out)
}

// The engine is the whole point: confirm dockerd answers on the socket the
// backend will relay to, through the same primitive the dialer uses.
func TestLiveEngineSocketAnswers(t *testing.T) {
	ctx := liveCtx(t)
	l := New()

	sessions, err := l.Sessions(ctx)
	if err != nil {
		t.Fatalf("Sessions: %v", err)
	}
	if len(sessions) == 0 {
		t.Skip("no wslc session running")
	}

	out, err := l.RunInSession(ctx, sessions[0].DisplayName,
		"curl", "-s", "--unix-socket", "/var/run/docker.sock", "http://localhost/version")
	if err != nil {
		t.Fatalf("querying the engine socket: %v", err)
	}
	if !strings.Contains(out, "ApiVersion") {
		t.Fatalf("engine did not answer with a version payload: %q", out)
	}
	t.Logf("engine: %.160s", out)
}

// ReadPolicies must work on a machine with no policy deployed — the ordinary
// case — and report permissive defaults rather than failing or denying.
//
// The restrictive path needs values under HKLM\Software\Policies\WSL, which
// requires elevation to create, so it is not exercised here; the semantics it
// would exercise are unit-tested against the rules copied from wslpolicies.h.
func TestLiveReadPolicies(t *testing.T) {
	liveCtx(t) // skips when wslc is absent
	p, err := ReadPolicies()
	if err != nil {
		t.Fatalf("ReadPolicies: %v", err)
	}
	t.Logf("deployed policy: containers=%v privileged=%v allowlist=%v",
		p.ContainersAllowed, p.PrivilegedAllowed, p.RegistryAllowlist)

	// A machine with no policy must not be treated as a locked-down one: that
	// would refuse to serve every ordinary install.
	if !p.Restrictive() {
		if !p.ContainersAllowed || !p.PrivilegedAllowed {
			t.Error("an unrestrictive policy still reported a denial")
		}
	}
}
