package pipeproxy_test

import (
	"bufio"
	"net"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/wslkit/skrog/internal/pipeproxy"
)

// fakeImageGate records what it was asked about and denies on request. It
// implements both interfaces, because the handler reaches ImageGate through a
// type assertion on the Gate it was given.
type fakeImageGate struct {
	denyPull  string
	denyBuild string

	sawPull  string
	sawBuild bool
}

func (g *fakeImageGate) DenyCreate(map[string]any) (string, bool) { return "", false }

func (g *fakeImageGate) DenyPull(image string) (string, bool) {
	g.sawPull = image
	if g.denyPull == "" {
		return "", false
	}
	return g.denyPull, true
}

func (g *fakeImageGate) DenyBuild() (string, bool) {
	g.sawBuild = true
	if g.denyBuild == "" {
		return "", false
	}
	return g.denyBuild, true
}

// driveRequest sends one raw request through the bridge and returns what the
// client sees, plus whether the engine was ever contacted.
func driveRequest(t *testing.T, gate pipeproxy.Gate, rawReq string) (*http.Response, func() bool) {
	t.Helper()
	client, bridgeClient := net.Pipe()
	engineSide, bridgeEngine := net.Pipe()

	go pipeproxy.RewriteBindsGuarded(nil, gate)(bridgeClient, bridgeEngine)

	reached := make(chan struct{}, 1)
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
		select {
		case reached <- struct{}{}:
		default:
		}
		engineSide.Write([]byte("HTTP/1.1 200 OK\r\nContent-Length: 0\r\n\r\n"))
	}()

	go func() { client.Write([]byte(rawReq)) }()

	client.SetReadDeadline(time.Now().Add(10 * time.Second))
	resp, err := http.ReadResponse(bufio.NewReader(client), nil)
	if err != nil {
		t.Fatalf("reading response: %v", err)
	}
	t.Cleanup(func() { client.Close() })

	engineReached := func() bool {
		select {
		case <-reached:
			return true
		default:
			return false
		}
	}
	return resp, engineReached
}

// The whole point of gating a pull: a blocked image must not be fetched onto
// the machine at all, so the request cannot reach the engine.
func TestDeniedPullNeverReachesTheEngine(t *testing.T) {
	gate := &fakeImageGate{denyPull: "allowlist does not permit docker.io"}
	resp, engineReached := driveRequest(t, gate,
		"POST /v1.45/images/create?fromImage=busybox&tag=latest HTTP/1.1\r\nHost: d\r\nContent-Length: 0\r\n\r\n")

	if resp.StatusCode != http.StatusForbidden {
		t.Errorf("status = %d, want 403", resp.StatusCode)
	}
	if engineReached() {
		t.Error("the engine was contacted for a denied pull; the image may have been fetched")
	}
	if gate.sawPull != "busybox:latest" {
		t.Errorf("gate saw %q; the tag query parameter must be folded into the reference", gate.sawPull)
	}
	buf := make([]byte, 512)
	n, _ := resp.Body.Read(buf)
	if got := string(buf[:n]); !strings.Contains(got, "allowlist does not permit") {
		t.Errorf("the reason must reach the user verbatim, got %q", got)
	}
}

func TestAllowedPullReachesTheEngine(t *testing.T) {
	gate := &fakeImageGate{}
	resp, engineReached := driveRequest(t, gate,
		"POST /v1.45/images/create?fromImage=contoso.azurecr.io%2Fapp HTTP/1.1\r\nHost: d\r\nContent-Length: 0\r\n\r\n")

	if resp.StatusCode != http.StatusOK {
		t.Errorf("status = %d, want 200", resp.StatusCode)
	}
	if !engineReached() {
		t.Error("an allowed pull never reached the engine")
	}
	if gate.sawPull != "contoso.azurecr.io/app" {
		t.Errorf("gate saw %q", gate.sawPull)
	}
}

func TestDeniedBuildNeverReachesTheEngine(t *testing.T) {
	gate := &fakeImageGate{denyBuild: "allowlist is in force; a build cannot be attributed"}
	resp, engineReached := driveRequest(t, gate,
		"POST /v1.45/build?t=x HTTP/1.1\r\nHost: d\r\nContent-Length: 0\r\n\r\n")

	if resp.StatusCode != http.StatusForbidden {
		t.Errorf("status = %d, want 403", resp.StatusCode)
	}
	if engineReached() {
		t.Error("the engine was contacted for a denied build")
	}
	if !gate.sawBuild {
		t.Error("the build gate was never consulted")
	}
}

// An import from a tarball names no registry, so there is nothing to judge and
// it must not be mistaken for a pull.
func TestImportIsNotTreatedAsAPull(t *testing.T) {
	gate := &fakeImageGate{denyPull: "should not be consulted"}
	resp, engineReached := driveRequest(t, gate,
		"POST /v1.45/images/create?fromSrc=-&repo=x HTTP/1.1\r\nHost: d\r\nContent-Length: 0\r\n\r\n")

	if resp.StatusCode != http.StatusOK {
		t.Errorf("status = %d, want 200 — an import has no registry to gate", resp.StatusCode)
	}
	if !engineReached() {
		t.Error("an import was blocked")
	}
	if gate.sawPull != "" {
		t.Errorf("the pull gate was consulted for an import, with %q", gate.sawPull)
	}
}

// A plain Gate must keep working untouched: pulls and builds pass as they
// always have, with no type assertion surprises.
func TestGateWithoutImageGateIsUnaffected(t *testing.T) {
	resp, engineReached := driveRequest(t, &fakeGate{},
		"POST /v1.45/images/create?fromImage=busybox HTTP/1.1\r\nHost: d\r\nContent-Length: 0\r\n\r\n")

	if resp.StatusCode != http.StatusOK {
		t.Errorf("status = %d, want 200", resp.StatusCode)
	}
	if !engineReached() {
		t.Error("a pull was blocked by a gate that does not judge pulls")
	}
}

// Reads and listings must not be judged: gating them would break `docker
// images` and every status call for no security benefit.
func TestUnrelatedRequestsAreNotJudged(t *testing.T) {
	gate := &fakeImageGate{denyPull: "x", denyBuild: "y"}
	resp, engineReached := driveRequest(t, gate,
		"GET /v1.45/images/json HTTP/1.1\r\nHost: d\r\n\r\n")

	if resp.StatusCode != http.StatusOK {
		t.Errorf("status = %d, want 200", resp.StatusCode)
	}
	if !engineReached() {
		t.Error("an image listing was blocked")
	}
	if gate.sawPull != "" || gate.sawBuild {
		t.Error("the gate was consulted for a listing")
	}
}
