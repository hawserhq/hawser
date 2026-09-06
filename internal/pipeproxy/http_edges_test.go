package pipeproxy_test

import (
	"bufio"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/zcsizmadia/hawser/internal/pipeproxy"
)

// TestInterim1xxDoesNotDesyncTheLoop covers #90: an interim response (103
// Early Hints here; 100 Continue is the same shape) is a complete zero-body
// response, and treating it as THE response made the loop hand the real
// response to the NEXT request. Two requests on one keep-alive connection must
// each get their own answer.
func TestInterim1xxDoesNotDesyncTheLoop(t *testing.T) {
	client, bridgeClient := net.Pipe()
	engineSide, bridgeEngine := net.Pipe()

	done := make(chan error, 1)
	go func() { done <- pipeproxy.RewriteBinds(bridgeClient, bridgeEngine) }()

	// Engine: for each request, an unsolicited interim head then a distinct
	// final response.
	go func() {
		br := bufio.NewReader(engineSide)
		for i := 1; ; i++ {
			req, err := http.ReadRequest(br)
			if err != nil {
				return
			}
			io.Copy(io.Discard, req.Body)
			engineSide.Write([]byte("HTTP/1.1 103 Early Hints\r\nLink: </x>; rel=preload\r\n\r\n"))
			body := fmt.Sprintf("answer-%d", i)
			engineSide.Write([]byte(fmt.Sprintf(
				"HTTP/1.1 200 OK\r\nContent-Type: text/plain\r\nContent-Length: %d\r\n\r\n%s",
				len(body), body)))
		}
	}()

	br := bufio.NewReader(client)
	for i := 1; i <= 2; i++ {
		req, _ := http.NewRequest("GET", fmt.Sprintf("http://d/req%d", i), nil)
		if err := req.Write(client); err != nil {
			t.Fatalf("request %d: %v", i, err)
		}
		client.SetReadDeadline(time.Now().Add(5 * time.Second))
		// The net/http client machinery skips interim responses itself when
		// asked to (ReadResponse returns them one at a time) — read until a
		// final response, mirroring what a real client does.
		var resp *http.Response
		var err error
		for {
			resp, err = http.ReadResponse(br, req)
			if err != nil {
				t.Fatalf("response %d: %v", i, err)
			}
			if resp.StatusCode >= 200 {
				break
			}
		}
		got, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		want := fmt.Sprintf("answer-%d", i)
		if string(got) != want {
			t.Fatalf("request %d got %q, want %q — the loop desynced (#90)", i, got, want)
		}
	}
	client.Close()
	<-done
}

// TestEarlyErrorOnAbandonedUploadIsSalvaged covers #90's second half: the
// daemon rejects a large upload early and stops reading the body. The old
// sequential loop stayed blocked writing the body and the client saw a dead
// connection; the daemon's actual error must arrive instead.
func TestEarlyErrorOnAbandonedUploadIsSalvaged(t *testing.T) {
	client, bridgeClient := net.Pipe()
	engineSide, bridgeEngine := net.Pipe()

	go pipeproxy.RewriteBinds(bridgeClient, bridgeEngine)

	// Engine: read only the request HEAD, answer 403, then stop reading
	// entirely while keeping the connection open — the Go-server drain-limit
	// behavior in miniature.
	go func() {
		br := bufio.NewReader(engineSide)
		// Read headers only: up to the blank line.
		for {
			line, err := br.ReadString('\n')
			if err != nil {
				return
			}
			if line == "\r\n" {
				break
			}
		}
		body := "denied: quota"
		engineSide.Write([]byte(fmt.Sprintf(
			"HTTP/1.1 403 Forbidden\r\nContent-Type: text/plain\r\nContent-Length: %d\r\n\r\n%s",
			len(body), body)))
		// ...and never read again. net.Pipe has no buffer at all, so the
		// bridge's body-forward blocks immediately: the worst case.
	}()

	// Client: a 2 MiB chunked upload, written from a goroutine because the
	// bridge cannot absorb it (the engine never reads it).
	pr, pw := io.Pipe()
	req, _ := http.NewRequest("POST", "http://d/build", pr)
	req.ContentLength = -1 // chunked
	go func() {
		chunk := make([]byte, 64*1024)
		for i := 0; i < 32; i++ {
			if _, err := pw.Write(chunk); err != nil {
				return // the salvage path tore the connection down mid-upload
			}
		}
		pw.Close()
	}()
	go req.Write(client) // will not complete; the salvage must not need it to

	br := bufio.NewReader(client)
	client.SetReadDeadline(time.Now().Add(10 * time.Second))
	resp, err := http.ReadResponse(br, req)
	if err != nil {
		t.Fatalf("client never received the daemon's early error (#90): %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 403 {
		t.Fatalf("status = %d, want 403", resp.StatusCode)
	}
	got, _ := io.ReadAll(resp.Body)
	if !strings.Contains(string(got), "denied: quota") {
		t.Errorf("body = %q, want the daemon's message", got)
	}
	// The connection is written off, not reused: the response must carry
	// Connection: close.
	if !resp.Close {
		t.Error("salvaged response should mark the connection for close")
	}
	client.Close()
}
