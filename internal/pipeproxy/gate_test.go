package pipeproxy_test

import (
	"bufio"
	"net"
	"net/http"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/wslkit/skrog/internal/pipeproxy"
)

// fakeGate denies whatever the test tells it to, so these cases exercise the
// bridge's denial path rather than any particular rule.
type fakeGate struct {
	reason string
	// Captured AT CALL TIME: the Gate contract says the body must not be
	// retained, because translation rewrites the same map right after.
	sawKeys  []string
	sawBinds []string
}

func (g *fakeGate) DenyCreate(body map[string]any) (string, bool) {
	for k := range body {
		g.sawKeys = append(g.sawKeys, k)
	}
	if hc, ok := body["HostConfig"].(map[string]any); ok {
		if binds, ok := hc["Binds"].([]any); ok {
			for _, b := range binds {
				if s, ok := b.(string); ok {
					g.sawBinds = append(g.sawBinds, s)
				}
			}
		}
	}
	if g.reason == "" {
		return "", false
	}
	return g.reason, true
}

func (g *fakeGate) consulted() bool { return g.sawKeys != nil }

func (g *fakeGate) sawKey(k string) bool {
	for _, s := range g.sawKeys {
		if s == k {
			return true
		}
	}
	return false
}

// gatePostCreate drives one container-create request through the bridge and
// returns the response the client sees.
func gatePostCreate(t *testing.T, gate pipeproxy.Gate, body string, engineReply string) *http.Response {
	t.Helper()
	client, bridgeClient := net.Pipe()
	engineSide, bridgeEngine := net.Pipe()

	go pipeproxy.RewriteBindsGuarded(nil, gate)(bridgeClient, bridgeEngine)

	// The engine answers only if the request ever reaches it.
	go func() {
		br := bufio.NewReader(engineSide)
		for {
			line, err := br.ReadString('\n')
			if err != nil {
				return
			}
			if line == "\r\n" {
				break
			}
		}
		engineSide.Write([]byte(engineReply))
	}()

	req := "POST /v1.45/containers/create HTTP/1.1\r\nHost: d\r\nContent-Type: application/json\r\n" +
		"Content-Length: " + strconv.Itoa(len(body)) + "\r\n\r\n" + body
	go func() {
		client.Write([]byte(req))
	}()

	client.SetReadDeadline(time.Now().Add(10 * time.Second))
	resp, err := http.ReadResponse(bufio.NewReader(client), nil)
	if err != nil {
		t.Fatalf("reading response: %v", err)
	}
	t.Cleanup(func() { client.Close() })

	return resp
}

func TestGateDenialNeverReachesTheEngine(t *testing.T) {
	// The point of admission control: a denied request is refused at the
	// bridge, so the engine never creates anything.
	gate := &fakeGate{reason: "policy denies --privileged: test"}
	resp := gatePostCreate(t, gate,
		`{"Image":"ubuntu","HostConfig":{"Privileged":true}}`,
		"HTTP/1.1 201 Created\r\nContent-Length: 0\r\n\r\n")

	if resp.StatusCode != http.StatusForbidden {
		t.Errorf("status = %d, want 403 — the request is well-formed, this machine just will not run it", resp.StatusCode)
	}
	buf := make([]byte, 512)
	n, _ := resp.Body.Read(buf)
	if got := string(buf[:n]); !strings.Contains(got, "policy denies --privileged") {
		t.Errorf("the reason must reach the user verbatim, got %q", got)
	}
	// The gate must have been handed the decoded body, not raw bytes.
	if !gate.consulted() {
		t.Fatal("the gate was never consulted")
	}
	if !gate.sawKey("HostConfig") {
		t.Errorf("the gate should see a decoded body, saw keys %v", gate.sawKeys)
	}
}

func TestGateAllowsRequestThrough(t *testing.T) {
	gate := &fakeGate{} // allows everything
	resp := gatePostCreate(t, gate,
		`{"Image":"ubuntu"}`,
		"HTTP/1.1 201 Created\r\nContent-Length: 0\r\n\r\n")
	if resp.StatusCode != http.StatusCreated {
		t.Errorf("status = %d, want 201 — an allowed request must reach the engine", resp.StatusCode)
	}
	if !gate.consulted() {
		t.Error("the gate should still have been consulted")
	}
}

func TestNilGateIsNoAdmissionControl(t *testing.T) {
	// The default. Nothing is judged and nothing changes.
	resp := gatePostCreate(t, nil,
		`{"Image":"ubuntu","HostConfig":{"Privileged":true}}`,
		"HTTP/1.1 201 Created\r\nContent-Length: 0\r\n\r\n")
	if resp.StatusCode != http.StatusCreated {
		t.Errorf("status = %d, want 201 with no gate", resp.StatusCode)
	}
}

func TestGateSeesUntranslatedWindowsPaths(t *testing.T) {
	// A bind-source rule is written in the Windows terms the user typed, so
	// the gate has to run BEFORE path translation turns C:\work into /mnt/c.
	gate := &fakeGate{}
	gatePostCreate(t, gate,
		`{"Image":"ubuntu","HostConfig":{"Binds":["C:\\work:/app"]}}`,
		"HTTP/1.1 201 Created\r\nContent-Length: 0\r\n\r\n")

	if len(gate.sawBinds) == 0 {
		t.Fatalf("no Binds reached the gate (keys: %v)", gate.sawKeys)
	}
	got := gate.sawBinds[0]
	if !strings.HasPrefix(strings.ToLower(got), "c:") {
		t.Errorf("the gate saw %q; it must see the untranslated Windows path", got)
	}
}
