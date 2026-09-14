package wslc

import (
	"context"
	"fmt"
	"io"
	"net"
	"strings"
	"sync"
	"testing"
	"time"
)

// fakeEngine answers the three GETs the watcher makes, over an in-memory pipe.
// Enough to drive publish/unpublish without a session VM.
type fakeEngine struct {
	mu       sync.Mutex
	list     string            // body for /containers/json
	inspects map[string]string // id -> body for /containers/<id>/json
}

func (e *fakeEngine) dial(context.Context) (io.ReadWriteCloser, error) {
	client, server := net.Pipe()
	go func() {
		defer server.Close()
		buf := make([]byte, 4096)
		n, err := server.Read(buf)
		if err != nil {
			return
		}
		req := string(buf[:n])
		e.mu.Lock()
		body := "[]"
		switch {
		case strings.Contains(req, "/containers/json"):
			body = e.list
		default:
			for id, ins := range e.inspects {
				if strings.Contains(req, "/containers/"+id+"/json") {
					body = ins
				}
			}
		}
		e.mu.Unlock()
		fmt.Fprintf(server, "HTTP/1.1 200 OK\r\nContent-Type: application/json\r\nContent-Length: %d\r\nConnection: close\r\n\r\n%s", len(body), body)
	}()
	return client, nil
}

// echoGuest accepts a CONNECT and echoes, standing in for the agent.
func echoGuest(context.Context) (io.ReadWriteCloser, error) {
	return (&fakeGuest{gotTarget: make(chan string, 1)}).dial(context.Background())
}

func inspectJSON(ip string, ports string) string {
	return `{"NetworkSettings":{"IPAddress":"` + ip + `","Ports":` + ports + `}}`
}

func TestPublishOpensAListenerPerTCPBinding(t *testing.T) {
	const id = "abc123def4567890"
	e := &fakeEngine{inspects: map[string]string{
		id: inspectJSON("172.17.0.2", `{"80/tcp":[{"HostIp":"0.0.0.0","HostPort":"0"}]}`),
	}}
	w := &PortWatcher{EngineDial: e.dial, ForwardDial: echoGuest}
	defer w.StopAll()

	w.publish(context.Background(), id)

	pub := w.Published()
	if len(pub) != 1 {
		t.Fatalf("published %d ports, want 1: %v", len(pub), pub)
	}
	for host, target := range pub {
		if target != "172.17.0.2:80" {
			t.Errorf("target = %q, want 172.17.0.2:80", target)
		}
		if !isWildcard(t, host) {
			t.Errorf("host = %q, want a wildcard binding", host)
		}
	}
}

// Docker's default HostIp is empty, meaning all interfaces. WSLC's own relay
// binds loopback only; Skrog should not inherit that limitation.
func TestEmptyHostIPBindsAllInterfaces(t *testing.T) {
	const id = "def456"
	e := &fakeEngine{inspects: map[string]string{
		id: inspectJSON("172.17.0.5", `{"443/tcp":[{"HostIp":"","HostPort":"0"}]}`),
	}}
	w := &PortWatcher{EngineDial: e.dial, ForwardDial: echoGuest}
	defer w.StopAll()

	w.publish(context.Background(), id)
	for host := range w.Published() {
		if !isWildcard(t, host) {
			t.Errorf("host = %q, want a wildcard binding", host)
		}
	}
}

// isWildcard reports whether an address binds every interface rather than one.
//
// Go opens a dual-stack socket for "0.0.0.0:0" and reports it back as "[::]:p",
// which covers IPv4 too — so the spelling of the bound address is not the
// spelling of the request, and asserting on the request would pass without
// checking anything.
func isWildcard(t *testing.T, addr string) bool {
	t.Helper()
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		t.Fatalf("Published() key %q is not host:port: %v", addr, err)
	}
	if port == "" || port == "0" {
		t.Errorf("address %q has no concrete port", addr)
	}
	ip := net.ParseIP(host)
	return host == "" || (ip != nil && ip.IsUnspecified())
}

// Compose puts every project on a user-defined network, where the top-level
// IPAddress is empty and the address lives under Networks. That is the common
// case, so getting it wrong would break the headline workload.
func TestPublishFindsTheIPOnAUserDefinedNetwork(t *testing.T) {
	const id = "net789"
	e := &fakeEngine{inspects: map[string]string{
		id: `{"NetworkSettings":{"IPAddress":"","Ports":{"5432/tcp":[{"HostIp":"127.0.0.1","HostPort":"0"}]},` +
			`"Networks":{"proj_default":{"IPAddress":"172.20.0.3"}}}}`,
	}}
	w := &PortWatcher{EngineDial: e.dial, ForwardDial: echoGuest}
	defer w.StopAll()

	w.publish(context.Background(), id)
	pub := w.Published()
	if len(pub) != 1 {
		t.Fatalf("published %d, want 1: %v", len(pub), pub)
	}
	for _, target := range pub {
		if target != "172.20.0.3:5432" {
			t.Errorf("target = %q, want the network's address", target)
		}
	}
}

// UDP cannot ride a stream transport. It must be skipped rather than published
// as a TCP listener that silently drops every datagram.
func TestUDPPortsAreNotPublished(t *testing.T) {
	const id = "udp111"
	e := &fakeEngine{inspects: map[string]string{
		id: inspectJSON("172.17.0.9", `{"53/udp":[{"HostIp":"0.0.0.0","HostPort":"0"}]}`),
	}}
	w := &PortWatcher{EngineDial: e.dial, ForwardDial: echoGuest}
	defer w.StopAll()

	w.publish(context.Background(), id)
	if pub := w.Published(); len(pub) != 0 {
		t.Errorf("published %v for a udp-only container, want nothing", pub)
	}
}

func TestUnexposedContainerPublishesNothing(t *testing.T) {
	const id = "bare222"
	e := &fakeEngine{inspects: map[string]string{id: inspectJSON("172.17.0.4", `{}`)}}
	w := &PortWatcher{EngineDial: e.dial, ForwardDial: echoGuest}
	defer w.StopAll()

	w.publish(context.Background(), id)
	if pub := w.Published(); len(pub) != 0 {
		t.Errorf("published %v, want nothing", pub)
	}
}

// An exposed-but-unbound port (EXPOSE without -p) has an empty binding list
// and must not become a listener.
func TestExposedButUnboundPortIsNotPublished(t *testing.T) {
	const id = "exp333"
	e := &fakeEngine{inspects: map[string]string{
		id: inspectJSON("172.17.0.6", `{"8080/tcp":null}`),
	}}
	w := &PortWatcher{EngineDial: e.dial, ForwardDial: echoGuest}
	defer w.StopAll()

	w.publish(context.Background(), id)
	if pub := w.Published(); len(pub) != 0 {
		t.Errorf("published %v for an unbound exposed port, want nothing", pub)
	}
}

func TestUnpublishClosesTheListener(t *testing.T) {
	const id = "stop444"
	e := &fakeEngine{inspects: map[string]string{
		id: inspectJSON("172.17.0.2", `{"80/tcp":[{"HostIp":"127.0.0.1","HostPort":"0"}]}`),
	}}
	w := &PortWatcher{EngineDial: e.dial, ForwardDial: echoGuest}

	w.publish(context.Background(), id)
	// Published reports the bound address, which is what a client would use.
	var bound string
	for host := range w.Published() {
		bound = host
	}
	if bound == "" || strings.HasSuffix(bound, ":0") {
		t.Fatalf("Published() = %q, want a concrete bound address", bound)
	}

	w.unpublish(id)
	if pub := w.Published(); len(pub) != 0 {
		t.Errorf("still publishing %v after unpublish", pub)
	}
	if _, err := net.DialTimeout("tcp", bound, 500*time.Millisecond); err == nil {
		t.Errorf("listener at %s still accepts after unpublish", bound)
	}
}

// Publishing twice must not open a second listener on the same host port —
// the second bind would fail and, worse, churn a working one.
func TestPublishIsIdempotent(t *testing.T) {
	const id = "dup555"
	e := &fakeEngine{inspects: map[string]string{
		id: inspectJSON("172.17.0.2", `{"80/tcp":[{"HostIp":"127.0.0.1","HostPort":"0"}]}`),
	}}
	w := &PortWatcher{EngineDial: e.dial, ForwardDial: echoGuest}
	defer w.StopAll()

	w.publish(context.Background(), id)
	first := w.Published()
	w.publish(context.Background(), id)
	second := w.Published()

	if len(second) != 1 {
		t.Fatalf("after a repeat publish there are %d listeners, want 1: %v", len(second), second)
	}
	for host := range first {
		if _, ok := second[host]; !ok {
			t.Errorf("the live listener %s was replaced by a repeat publish", host)
		}
	}
}

// A container that vanished between the event and the inspect is an ordinary
// race, not a reason to fail.
func TestPublishToleratesAVanishedContainer(t *testing.T) {
	w := &PortWatcher{
		EngineDial:  func(context.Context) (io.ReadWriteCloser, error) { return nil, fmt.Errorf("gone") },
		ForwardDial: echoGuest,
	}
	defer w.StopAll()
	w.publish(context.Background(), "ghost")
	if pub := w.Published(); len(pub) != 0 {
		t.Errorf("published %v for a container that could not be inspected", pub)
	}
}

func TestSplitPortSpec(t *testing.T) {
	cases := map[string][2]string{
		"80/tcp":    {"80", "tcp"},
		"53/udp":    {"53", "udp"},
		"8080":      {"8080", "tcp"},
		"9000/sctp": {"9000", "sctp"},
	}
	for spec, want := range cases {
		port, proto := splitPortSpec(spec)
		if port != want[0] || proto != want[1] {
			t.Errorf("splitPortSpec(%q) = (%q, %q), want (%q, %q)", spec, port, proto, want[0], want[1])
		}
	}
}

// dockerd reports `-p 18200:80` as two bindings, one per address family.
// Go's wildcard listener is already dual-stack, so opening both means the
// second bind fails — a warning on every published port, for a port that
// worked. One listener is the correct answer.
func TestDualStackBindingPublishesOneListener(t *testing.T) {
	const id = "dual666"
	e := &fakeEngine{inspects: map[string]string{
		id: inspectJSON("172.17.0.2",
			`{"80/tcp":[{"HostIp":"0.0.0.0","HostPort":"0"},{"HostIp":"::","HostPort":"0"}]}`),
	}}
	w := &PortWatcher{EngineDial: e.dial, ForwardDial: echoGuest}
	defer w.StopAll()

	w.publish(context.Background(), id)

	pub := w.Published()
	if len(pub) != 1 {
		t.Fatalf("published %d listeners for one dual-stack binding, want 1: %v", len(pub), pub)
	}
}

// Two genuinely different host addresses on the same port are not duplicates
// and must both be published.
func TestDistinctHostAddressesBothPublish(t *testing.T) {
	const id = "two777"
	e := &fakeEngine{inspects: map[string]string{
		id: inspectJSON("172.17.0.2",
			`{"80/tcp":[{"HostIp":"127.0.0.1","HostPort":"0"},{"HostIp":"0.0.0.0","HostPort":"0"}]}`),
	}}
	w := &PortWatcher{EngineDial: e.dial, ForwardDial: echoGuest}
	defer w.StopAll()

	w.publish(context.Background(), id)
	if pub := w.Published(); len(pub) != 2 {
		t.Errorf("published %d listeners for two distinct host addresses, want 2: %v", len(pub), pub)
	}
}
