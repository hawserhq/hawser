//go:build wslc && windows

package wslc

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/wslkit/skrog/internal/pipeproxy"
)

// dockerAPI runs one request against the session engine over the agent's
// engine port, which is also a check that the two vsock ports stay separate:
// this must keep working while the forward port is in use.
func dockerAPI(t *testing.T, secret, method, path, body string) string {
	t.Helper()
	d := &pipeproxy.VsockDialer{Port: AgentPort, Secret: secret, Cooldown: -1}
	conn, err := d.Dial(context.Background())
	if err != nil {
		t.Fatalf("dialing the engine: %v", err)
	}
	defer conn.Close()

	req := method + " " + path + " HTTP/1.1\r\nHost: docker\r\nConnection: close\r\n"
	if body != "" {
		req += fmt.Sprintf("Content-Type: application/json\r\nContent-Length: %d\r\n", len(body))
	}
	req += "\r\n" + body
	if _, err := io.WriteString(conn, req); err != nil {
		t.Fatalf("writing %s %s: %v", method, path, err)
	}
	out, _ := io.ReadAll(conn)
	return string(out)
}

// The proof for #330: a container's port, published through the engine socket
// (which WSLC's own relay never sees), reachable from Windows because Skrog
// carries it.
func TestLiveForwardReachesAPublishedPort(t *testing.T) {
	l, ctx, session := liveSession(t)
	const secret = "live-test-secret"
	const name = "skrogfwdtest"

	if err := l.Bootstrap(ctx, session, buildAgent(t), secret); err != nil {
		t.Fatalf("Bootstrap: %v", err)
	}

	// Clean slate, then a container that serves HTTP inside the VM.
	dockerAPI(t, secret, "DELETE", "/v1.44/containers/"+name+"?force=1", "")
	create := dockerAPI(t, secret, "POST", "/v1.44/containers/create?name="+name,
		`{"Image":"busybox","Cmd":["httpd","-f","-p","80"]}`)
	if !strings.Contains(create, `"Id"`) {
		t.Fatalf("creating the container: %s", create)
	}
	t.Cleanup(func() {
		dockerAPI(t, secret, "DELETE", "/v1.44/containers/"+name+"?force=1", "")
	})
	if out := dockerAPI(t, secret, "POST", "/v1.44/containers/"+name+"/start", ""); !strings.Contains(out, "204") {
		t.Fatalf("starting the container: %s", out)
	}

	// The container's address inside the VM is what the guest half dials.
	inspect := dockerAPI(t, secret, "GET", "/v1.44/containers/"+name+"/json", "")
	ip := jsonField(inspect, `"IPAddress":"`)
	if ip == "" {
		t.Fatalf("no container IP in inspect output: %.400s", inspect)
	}
	t.Logf("container IP %s", ip)

	// The production shape: the forward transport needs the same recovery the
	// engine transport does, because the same idle termination takes both
	// listeners down. Without it this test is flaky for a real reason — a VM
	// restart between bootstrap and the request leaves nothing to dial.
	fwdDialer := &Dialer{
		Inner:     &pipeproxy.VsockDialer{Port: ForwardPort, Secret: secret, Cooldown: -1},
		Bootstrap: func(ctx context.Context) error { return l.Bootstrap(ctx, session, buildAgent(t), secret) },
	}
	f := &Forwarder{
		HostAddr: "127.0.0.1:0",
		Target:   net.JoinHostPort(ip, "80"),
		Dial:     fwdDialer.Dial,
	}
	if err := f.Start(ctx); err != nil {
		t.Fatalf("starting the forwarder: %v", err)
	}
	defer f.Stop()
	t.Logf("published on %s -> %s", f.Addr(), f.Target)

	// busybox httpd answers 404 with no document root, which is a complete
	// HTTP response and all this needs to prove.
	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Get("http://" + f.Addr() + "/")
	if err != nil {
		t.Fatalf("reaching the published port from Windows: %v", err)
	}
	defer resp.Body.Close()
	t.Logf("HTTP %s from a container port, via Skrog's relay", resp.Status)
	if resp.StatusCode == 0 {
		t.Fatal("no HTTP response")
	}
}

// A forwarder pointed at a port nothing listens on must fail the connection,
// not hang: that is the shape of a container that exited.
func TestLiveForwardToADeadPortClosesCleanly(t *testing.T) {
	l, ctx, session := liveSession(t)
	const secret = "live-test-secret"

	if err := l.Bootstrap(ctx, session, buildAgent(t), secret); err != nil {
		t.Fatalf("Bootstrap: %v", err)
	}

	fwdDialer := &pipeproxy.VsockDialer{Port: ForwardPort, Secret: secret, Cooldown: -1}
	f := &Forwarder{HostAddr: "127.0.0.1:0", Target: "127.0.0.1:9", Dial: fwdDialer.Dial}
	if err := f.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer f.Stop()

	c, err := net.DialTimeout("tcp", f.Addr(), 5*time.Second)
	if err != nil {
		t.Fatalf("connecting: %v", err)
	}
	defer c.Close()
	c.SetReadDeadline(time.Now().Add(10 * time.Second))
	if _, err := io.ReadAll(c); err != nil {
		t.Fatalf("want a clean close for an unreachable target, got %v", err)
	}
}

// jsonField is enough to pull one string value out of an inspect payload
// without pulling in a schema this test does not otherwise need.
func jsonField(body, key string) string {
	i := strings.LastIndex(body, key)
	if i < 0 {
		return ""
	}
	rest := body[i+len(key):]
	j := strings.IndexByte(rest, '"')
	if j < 0 {
		return ""
	}
	return rest[:j]
}
