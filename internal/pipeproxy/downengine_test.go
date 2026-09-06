package pipeproxy_test

import (
	"bufio"
	"errors"
	"io"
	"net"
	"net/http"
	"testing"
	"time"

	"github.com/zcsizmadia/hawser/internal/pipeproxy"
)

// A dead engine (first response never arrives) must surface as
// ErrEngineUnreachable, not be swallowed like an ordinary hang-up (#91).
func TestFirstResponseEOFIsEngineUnreachable(t *testing.T) {
	client, bridgeClient := net.Pipe()
	engineSide, bridgeEngine := net.Pipe()

	done := make(chan error, 1)
	go func() { done <- pipeproxy.RewriteBinds(bridgeClient, bridgeEngine) }()

	// Engine: read the request, then close without answering (dockerd down).
	go func() {
		bufio.NewReader(engineSide).ReadString('\n')
		engineSide.Close()
	}()

	req, _ := http.NewRequest("GET", "http://d/_ping", nil)
	req.Write(client)

	select {
	case err := <-done:
		if !errors.Is(err, pipeproxy.ErrEngineUnreachable) {
			t.Errorf("got %v, want ErrEngineUnreachable", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("RewriteBinds did not return on a dead engine")
	}
	client.Close()
}

// Once a response has been relayed, a later EOF is an ordinary hang-up and
// must NOT be reported as engine-unreachable.
func TestEOFAfterFirstResponseIsOrdinary(t *testing.T) {
	client, bridgeClient := net.Pipe()
	engineSide, bridgeEngine := net.Pipe()

	done := make(chan error, 1)
	go func() { done <- pipeproxy.RewriteBinds(bridgeClient, bridgeEngine) }()

	go func() {
		br := bufio.NewReader(engineSide)
		br.ReadString('\n')
		// blank line
		for {
			l, err := br.ReadString('\n')
			if err != nil || l == "\r\n" {
				break
			}
		}
		engineSide.Write([]byte("HTTP/1.1 200 OK\r\nContent-Length: 2\r\n\r\nOK"))
		engineSide.Close()
	}()

	req, _ := http.NewRequest("GET", "http://d/_ping", nil)
	req.Write(client)
	br := bufio.NewReader(client)
	resp, err := http.ReadResponse(br, req)
	if err != nil {
		t.Fatal(err)
	}
	io.ReadAll(resp.Body)
	resp.Body.Close()
	client.Close()

	select {
	case err := <-done:
		if errors.Is(err, pipeproxy.ErrEngineUnreachable) {
			t.Errorf("post-response EOF misreported as engine-unreachable: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("RewriteBinds did not return")
	}
}
