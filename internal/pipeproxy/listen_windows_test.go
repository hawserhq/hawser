//go:build windows

package pipeproxy_test

import (
	"context"
	"fmt"
	"io"
	"net"
	"os"
	"testing"
	"time"

	"github.com/Microsoft/go-winio"
	"github.com/wslkit/skrog/internal/pipeproxy"
)

// testPipeName keeps concurrent runs (and a developer's real Skrog install)
// from colliding.
func testPipeName(t *testing.T) string {
	t.Helper()
	return fmt.Sprintf(`\\.\pipe\skrog-test-%d-%s`, os.Getpid(), t.Name())
}

// TestListenServesOverRealPipe exercises the actual named pipe rather than a
// TCP stand-in: this is the transport stock docker.exe uses, so the SDDL, byte
// mode, and go-winio half-close behavior all get covered here.
func TestListenServesOverRealPipe(t *testing.T) {
	engine := newServer(t, func(c net.Conn) { io.Copy(c, c) })

	name := testPipeName(t)
	l, err := pipeproxy.Listen(name, "")
	if err != nil {
		t.Fatalf("Listen(%s): %v", name, err)
	}
	defer l.Close()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	srv := &pipeproxy.Server{Dialer: engine.dialer()}
	done := make(chan error, 1)
	go func() { done <- srv.Serve(ctx, l) }()

	conn, err := winio.DialPipe(name, nil)
	if err != nil {
		t.Fatalf("DialPipe: %v", err)
	}
	defer conn.Close()

	want := "GET /_ping HTTP/1.1\r\n\r\n"
	if _, err := io.WriteString(conn, want); err != nil {
		t.Fatalf("write: %v", err)
	}
	buf := make([]byte, len(want))
	if err := conn.SetReadDeadline(time.Now().Add(5 * time.Second)); err != nil {
		t.Fatalf("deadline: %v", err)
	}
	if _, err := io.ReadFull(conn, buf); err != nil {
		t.Fatalf("read: %v", err)
	}
	if string(buf) != want {
		t.Errorf("got %q, want %q", buf, want)
	}

	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Errorf("Serve: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Error("Serve did not return")
	}
}

// TestListenBinaryOverRealPipe guards large-payload fidelity through the pipe
// (message mode since #57; winio presents it as a byte stream, and this test
// is the proof that framing does not truncate bulk transfers).
func TestListenBinaryOverRealPipe(t *testing.T) {
	engine := newServer(t, func(c net.Conn) { io.Copy(c, c) })

	name := testPipeName(t)
	l, err := pipeproxy.Listen(name, "")
	if err != nil {
		t.Fatalf("Listen: %v", err)
	}
	defer l.Close()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go (&pipeproxy.Server{Dialer: engine.dialer()}).Serve(ctx, l)

	conn, err := winio.DialPipe(name, nil)
	if err != nil {
		t.Fatalf("DialPipe: %v", err)
	}
	defer conn.Close()

	payload := make([]byte, 128*1024)
	for i := range payload {
		payload[i] = byte(i)
	}
	go func() {
		conn.Write(payload)
		if hc, ok := conn.(interface{ CloseWrite() error }); ok {
			hc.CloseWrite()
		}
	}()

	if err := conn.SetReadDeadline(time.Now().Add(10 * time.Second)); err != nil {
		t.Fatalf("deadline: %v", err)
	}
	got := make([]byte, len(payload))
	if _, err := io.ReadFull(conn, got); err != nil {
		t.Fatalf("read %d bytes: %v", len(payload), err)
	}
	for i := range payload {
		if got[i] != payload[i] {
			t.Fatalf("byte %d differs: got %#x want %#x", i, got[i], payload[i])
		}
	}
}

func TestListenRejectsEmptyName(t *testing.T) {
	if _, err := pipeproxy.Listen("", ""); err == nil {
		t.Error("Listen with empty name succeeded, want error")
	}
}

func TestListenRejectsBadSDDL(t *testing.T) {
	// A malformed descriptor must fail loudly at startup, not silently widen
	// access — this is the pipe's security boundary.
	if _, err := pipeproxy.Listen(testPipeName(t), "not-a-descriptor"); err == nil {
		t.Error("Listen with invalid SDDL succeeded, want error")
	}
}
