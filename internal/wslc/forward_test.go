package wslc

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"strings"
	"testing"
	"time"

	"github.com/wslkit/skrog/internal/vsockproto"
)

// fakeGuest stands in for the agent's forward listener: it reads the CONNECT
// line and then echoes, so a test can assert both the target it was asked for
// and that bytes flow in both directions. Running over a local TCP pair keeps
// this host-independent.
type fakeGuest struct {
	gotTarget chan string
}

func (g *fakeGuest) dial(context.Context) (io.ReadWriteCloser, error) {
	client, server := net.Pipe()
	go func() {
		defer server.Close()
		target, err := vsockproto.ReadTarget(server)
		if err != nil {
			return
		}
		select {
		case g.gotTarget <- target:
		default:
		}
		io.Copy(server, server) // echo
	}()
	return client, nil
}

func freePort(t *testing.T) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("picking a port: %v", err)
	}
	addr := ln.Addr().String()
	ln.Close()
	return addr
}

func TestForwarderCarriesBytesAndNamesTheTarget(t *testing.T) {
	g := &fakeGuest{gotTarget: make(chan string, 1)}
	f := &Forwarder{
		HostAddr: freePort(t),
		Target:   "172.17.0.3:80",
		Dial:     g.dial,
	}
	if err := f.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer f.Stop()

	c, err := net.DialTimeout("tcp", f.Addr(), 3*time.Second)
	if err != nil {
		t.Fatalf("connecting to the published port: %v", err)
	}
	defer c.Close()

	if _, err := fmt.Fprint(c, "ping\n"); err != nil {
		t.Fatalf("write: %v", err)
	}
	buf := make([]byte, 5)
	c.SetReadDeadline(time.Now().Add(3 * time.Second))
	if _, err := io.ReadFull(c, buf); err != nil {
		t.Fatalf("read back: %v", err)
	}
	if string(buf) != "ping\n" {
		t.Errorf("echoed %q, want %q", buf, "ping\n")
	}

	select {
	case got := <-g.gotTarget:
		if got != "172.17.0.3:80" {
			t.Errorf("guest was asked for %q", got)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("guest never received a CONNECT line")
	}
}

// A published port whose container has gone is an ordinary race, not a crash:
// the client must get a closed connection rather than the forwarder dying and
// taking every other published port with it.
func TestForwarderSurvivesADeadGuest(t *testing.T) {
	f := &Forwarder{
		HostAddr: freePort(t),
		Target:   "172.17.0.3:80",
		Dial: func(context.Context) (io.ReadWriteCloser, error) {
			return nil, errors.New("agent unreachable")
		},
	}
	if err := f.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer f.Stop()

	c, err := net.DialTimeout("tcp", f.Addr(), 3*time.Second)
	if err != nil {
		t.Fatalf("connecting: %v", err)
	}
	defer c.Close()
	c.SetReadDeadline(time.Now().Add(3 * time.Second))
	if _, err := io.ReadAll(c); err != nil {
		t.Fatalf("want a clean close, got %v", err)
	}

	// And the listener is still serving.
	c2, err := net.DialTimeout("tcp", f.Addr(), 3*time.Second)
	if err != nil {
		t.Fatalf("forwarder stopped accepting after a failed dial: %v", err)
	}
	c2.Close()
}

// The target is validated before a listener is opened: publishing a port that
// can never connect anywhere is worse than refusing up front.
func TestForwarderRefusesAnInvalidTarget(t *testing.T) {
	f := &Forwarder{HostAddr: freePort(t), Target: "not-a-host-port",
		Dial: func(context.Context) (io.ReadWriteCloser, error) { return nil, nil }}
	err := f.Start(context.Background())
	if err == nil {
		f.Stop()
		t.Fatal("want an error for an invalid target")
	}
	if !strings.Contains(err.Error(), "host:port") {
		t.Errorf("error %q does not explain the problem", err)
	}
}

func TestStopIsIdempotent(t *testing.T) {
	f := &Forwarder{HostAddr: freePort(t), Target: "172.17.0.3:80",
		Dial: func(context.Context) (io.ReadWriteCloser, error) { return nil, nil }}
	if err := f.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	if err := f.Stop(); err != nil {
		t.Fatalf("first Stop: %v", err)
	}
	if err := f.Stop(); err != nil {
		t.Errorf("second Stop: %v", err)
	}
}

// Docker's "publish on any free port" needs the bound port reported back.
func TestAddrReportsTheBoundPort(t *testing.T) {
	f := &Forwarder{HostAddr: "127.0.0.1:0", Target: "172.17.0.3:80",
		Dial: func(context.Context) (io.ReadWriteCloser, error) { return nil, nil }}
	if err := f.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer f.Stop()
	_, port, err := net.SplitHostPort(f.Addr())
	if err != nil || port == "0" || port == "" {
		t.Errorf("Addr() = %q, want a concrete bound port", f.Addr())
	}
}
