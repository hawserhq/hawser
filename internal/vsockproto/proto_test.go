package vsockproto

import (
	"io"
	"net"
	"strings"
	"testing"
	"time"
)

func TestHandshakeRoundTrip(t *testing.T) {
	client, server := net.Pipe()
	defer client.Close()
	defer server.Close()

	done := make(chan error, 1)
	go func() { done <- ServerHandshake(server, "hawser-agent/1", "") }()

	got, err := ClientHandshake(client, "")
	if err != nil {
		t.Fatalf("ClientHandshake: %v", err)
	}
	if got != "hawser-agent/1" {
		t.Errorf("agent identity = %q, want hawser-agent/1", got)
	}
	if err := <-done; err != nil {
		t.Fatalf("ServerHandshake: %v", err)
	}
}

func TestServerRejectsStrangerSilently(t *testing.T) {
	client, server := net.Pipe()
	defer client.Close()
	defer server.Close()

	done := make(chan error, 1)
	go func() { done <- ServerHandshake(server, "v", "") }()

	// A Docker HTTP request head, i.e. what would arrive if a client skipped
	// the handshake or the wrong service was dialed.
	go client.Write([]byte("GET /_ping HTTP/1.1\n"))
	if err := <-done; err == nil {
		t.Fatal("ServerHandshake accepted a non-hawser hello")
	}

	// And nothing was written back: the stranger learns nothing.
	client.SetReadDeadline(time.Now().Add(50 * time.Millisecond))
	buf := make([]byte, 1)
	if n, _ := client.Read(buf); n != 0 {
		t.Errorf("server replied %q to a stranger", buf[:n])
	}
}

func TestClientRejectsNonAgentPeer(t *testing.T) {
	client, server := net.Pipe()
	defer client.Close()
	defer server.Close()

	go func() {
		io.ReadFull(server, make([]byte, len("HAWSER/1\n")))
		server.Write([]byte("HTTP/1.1 400 Bad Request\n"))
	}()
	if _, err := ClientHandshake(client, ""); err == nil {
		t.Fatal("ClientHandshake accepted a non-agent banner")
	}
}

func TestHandshakeDoesNotEatTransparentBytes(t *testing.T) {
	// The byte after the banner's newline belongs to the relay phase; a
	// buffered handshake reader would swallow it.
	client, server := net.Pipe()
	defer client.Close()
	defer server.Close()

	go func() {
		ServerHandshake(server, "v1", "")
		server.Write([]byte("payload"))
	}()

	if _, err := ClientHandshake(client, ""); err != nil {
		t.Fatal(err)
	}
	buf := make([]byte, 7)
	client.SetReadDeadline(time.Now().Add(time.Second))
	if _, err := io.ReadFull(client, buf); err != nil {
		t.Fatalf("reading post-handshake payload: %v", err)
	}
	if string(buf) != "payload" {
		t.Errorf("post-handshake bytes = %q, want payload", buf)
	}
}

func TestHandshakeLineBounded(t *testing.T) {
	client, server := net.Pipe()
	defer client.Close()
	defer server.Close()

	done := make(chan error, 1)
	go func() { done <- ServerHandshake(server, "v", "") }()
	go client.Write([]byte(strings.Repeat("A", 4096)))
	if err := <-done; err == nil {
		t.Fatal("unbounded handshake line accepted")
	}
}

// tcpPair returns two connected TCP conns, which support CloseWrite on every
// platform — the same half-close shape as vsock and unix sockets.
func tcpPair(t *testing.T) (net.Conn, net.Conn) {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	ch := make(chan net.Conn, 1)
	go func() {
		c, err := l.Accept()
		if err == nil {
			ch <- c
		}
	}()
	c1, err := net.Dial("tcp", l.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	c2 := <-ch
	t.Cleanup(func() { c1.Close(); c2.Close() })
	return c1, c2
}

func TestRelayPropagatesHalfClose(t *testing.T) {
	// client <-> (relay) <-> backend. The client half-closes after its
	// request; the backend must see EOF, and its response must still flow —
	// exactly the docker-build shape that socat could not distinguish from a
	// disconnect.
	clientSide, relayLeft := tcpPair(t)
	relayRight, backendSide := tcpPair(t)

	relayDone := make(chan error, 1)
	go func() { relayDone <- Relay(relayLeft, relayRight) }()

	// Backend: read everything (until EOF), then respond, then close.
	backendDone := make(chan string, 1)
	go func() {
		req, _ := io.ReadAll(backendSide)
		backendSide.Write([]byte("response"))
		backendSide.Close()
		backendDone <- string(req)
	}()

	clientSide.Write([]byte("request"))
	clientSide.(*net.TCPConn).CloseWrite()

	resp, err := io.ReadAll(clientSide)
	if err != nil {
		t.Fatalf("client read: %v", err)
	}
	if string(resp) != "response" {
		t.Errorf("client got %q, want response", resp)
	}
	if req := <-backendDone; req != "request" {
		t.Errorf("backend got %q, want request", req)
	}
	if err := <-relayDone; err != nil {
		t.Errorf("Relay returned %v", err)
	}
}

func TestRelayFullClosesWithoutCloseWrite(t *testing.T) {
	// net.Pipe has no CloseWrite; the relay must degrade to a full close
	// rather than deadlock.
	clientSide, relayLeft := net.Pipe()
	relayRight, backendSide := net.Pipe()

	relayDone := make(chan error, 1)
	go func() { relayDone <- Relay(relayLeft, relayRight) }()

	go func() {
		buf := make([]byte, 3)
		io.ReadFull(backendSide, buf)
		backendSide.Close()
	}()

	clientSide.Write([]byte("hey"))
	clientSide.Close()

	select {
	case <-relayDone:
	case <-time.After(5 * time.Second):
		t.Fatal("Relay deadlocked on a transport without CloseWrite")
	}
}

func TestMutualAuthRoundTrip(t *testing.T) {
	client, server := net.Pipe()
	defer client.Close()
	defer server.Close()
	const secret = "s3cr3t-per-install"

	done := make(chan error, 1)
	go func() { done <- ServerHandshake(server, "hawser-agent/1", secret) }()

	id, err := ClientHandshake(client, secret)
	if err != nil {
		t.Fatalf("ClientHandshake: %v", err)
	}
	if id != "hawser-agent/1" {
		t.Errorf("identity = %q", id)
	}
	if err := <-done; err != nil {
		t.Fatalf("ServerHandshake: %v", err)
	}
}

func TestClientRejectsWrongSecret(t *testing.T) {
	client, server := net.Pipe()
	defer client.Close()

	done := make(chan error, 1)
	go func() {
		err := ServerHandshake(server, "v1", "the-real-secret")
		server.Close() // the caller closes on failure; a stranger reads no proof
		done <- err
	}()

	if _, err := ClientHandshake(client, "a-different-secret"); err == nil {
		t.Fatal("client accepted an agent that could not prove the secret")
	}
	if err := <-done; err == nil {
		t.Fatal("server accepted a client with the wrong secret")
	}
}

func TestSecretClientRefusesUnauthenticatedAgent(t *testing.T) {
	// The squatter case: an agent (or impostor) that answers v1 while the host
	// holds a secret must be refused — no silent downgrade.
	client, server := net.Pipe()
	defer client.Close()
	defer server.Close()

	// Server has NO secret, so it answers the plain v1 banner.
	go func() { ServerHandshake(server, "impostor", "") }()

	if _, err := ClientHandshake(client, "host-has-a-secret"); err == nil {
		t.Fatal("secret-holding client accepted an unauthenticated agent (downgrade)")
	}
}

func TestSecretlessClientStillTalksToSecretlessAgent(t *testing.T) {
	// Backward compatibility: neither side has a secret (pre-#81 install).
	client, server := net.Pipe()
	defer client.Close()
	defer server.Close()

	done := make(chan error, 1)
	go func() { done <- ServerHandshake(server, "hawser-agent/1", "") }()
	id, err := ClientHandshake(client, "")
	if err != nil || id != "hawser-agent/1" {
		t.Fatalf("v1 round-trip broke: id=%q err=%v", id, err)
	}
	<-done
}
