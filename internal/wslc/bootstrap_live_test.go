//go:build wslc && windows

package wslc

import (
	"bufio"
	"context"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/wslkit/skrog/internal/pipeproxy"
)

// buildAgent compiles guest/agent for linux, the same artifact the installer
// ships in the rootfs. Building here rather than depending on a published one
// keeps the test honest about the agent in this working tree.
func buildAgent(t *testing.T) []byte {
	t.Helper()
	out := filepath.Join(t.TempDir(), "skrog-agent")
	cmd := exec.Command("go", "build", "-ldflags=-s -w", "-o", out, "../../guest/agent")
	cmd.Env = append(os.Environ(), "GOOS=linux", "GOARCH=amd64", "CGO_ENABLED=0")
	if b, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("building the guest agent: %v\n%s", err, b)
	}
	blob, err := os.ReadFile(out)
	if err != nil {
		t.Fatalf("reading the built agent: %v", err)
	}
	return blob
}

// liveSession resolves a session to work in. It deliberately does NOT create
// or terminate one: the CLI cannot create a persistent named session (see
// SessionName), so this usually lands on the user's default session — and
// terminating that would take down whatever they are running by hand, which is
// the rule internal/supervise follows for distros and #35 was about.
//
// The agent it leaves behind is harmless: /tmp is on the tmpfs overlay, so it
// is gone at the next VM boot, and it only listens on Skrog's own vsock port.
func liveSession(t *testing.T) (*Local, context.Context, string) {
	t.Helper()
	ctx := liveCtx(t)
	l := New()
	if _, err := l.Version(ctx); err != nil {
		t.Skipf("wslc unusable: %v", err)
	}
	session, err := l.ResolveSession(ctx)
	if err != nil {
		t.Skipf("no wslc session to work in: %v", err)
	}
	t.Cleanup(func() {
		// Stop the agent, but leave the session alone.
		_, _ = l.RunInSession(context.Background(), session, "sh", "-c", stopScript)
	})
	t.Logf("using session %q", session)
	return l, ctx, session
}

func TestLiveBootstrapStartsTheAgent(t *testing.T) {
	l, ctx, session := liveSession(t)
	agent := buildAgent(t)

	if err := l.Bootstrap(ctx, session, agent, "live-test-secret"); err != nil {
		t.Fatalf("Bootstrap: %v", err)
	}

	running, err := l.AgentRunning(ctx, session)
	if err != nil {
		t.Fatalf("AgentRunning: %v", err)
	}
	if !running {
		log, _ := l.RunInSession(ctx, session, "cat", AgentLogPath)
		t.Fatalf("agent is not running after a successful Bootstrap; log:\n%s", log)
	}

	// Bootstrap must be safe to repeat: it is the recovery path after the VM
	// idle-terminates, so it runs again on every boot.
	if err := l.Bootstrap(ctx, session, agent, "live-test-secret"); err != nil {
		t.Fatalf("second Bootstrap (the re-bootstrap path): %v", err)
	}
}

// The one that matters: Windows dials the agent over AF_HYPERV and speaks
// Docker to the engine Microsoft ships, with no wslc CLI in the data path.
func TestLiveVsockReachesTheWslcEngine(t *testing.T) {
	l, ctx, session := liveSession(t)
	const secret = "live-test-secret"

	if err := l.Bootstrap(ctx, session, buildAgent(t), secret); err != nil {
		t.Fatalf("Bootstrap: %v", err)
	}

	d := &pipeproxy.VsockDialer{Port: AgentPort, Secret: secret}
	var conn interface {
		Read([]byte) (int, error)
		Write([]byte) (int, error)
		Close() error
	}
	var err error
	// The first dial may enumerate a VM with no listener on our port and pay
	// the dialer's timeout before finding the right one.
	deadline := time.Now().Add(30 * time.Second)
	for {
		conn, err = d.Dial(ctx)
		if err == nil || time.Now().After(deadline) {
			break
		}
		time.Sleep(500 * time.Millisecond)
	}
	if err != nil {
		t.Fatalf("dialing the wslc agent over vsock: %v", err)
	}
	defer conn.Close()

	if _, err := fmt.Fprint(conn, "GET /version HTTP/1.1\r\nHost: docker\r\nConnection: close\r\n\r\n"); err != nil {
		t.Fatalf("writing the request: %v", err)
	}
	resp, err := http.ReadResponse(bufio.NewReader(conn), nil)
	if err != nil {
		t.Fatalf("reading the engine response: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("engine returned %s", resp.Status)
	}
	buf := make([]byte, 400)
	n, _ := resp.Body.Read(buf)
	body := string(buf[:n])

	// Microsoft's engine, not skrog-engine: proof the port keyed us to the
	// right VM rather than the distro agent answering on the other port.
	if !strings.Contains(body, `"ApiVersion"`) {
		t.Fatalf("not a version payload: %q", body)
	}
	if strings.Contains(body, `"Version":"29.`) {
		t.Fatalf("reached skrog-engine, not the wslc session: %q", body)
	}
	t.Logf("wslc engine over vsock: %.200s", body)
}
