package pipeproxy

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net"
	"strings"
	"sync"
	"testing"
	"time"
)

// failingListener yields its queued conns, then a permanent error — the shape
// of a spontaneous listener failure.
type failingListener struct {
	mu    sync.Mutex
	conns []net.Conn
	err   error
}

func (l *failingListener) Accept() (net.Conn, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if len(l.conns) > 0 {
		c := l.conns[0]
		l.conns = l.conns[1:]
		return c, nil
	}
	return nil, l.err
}
func (l *failingListener) Close() error   { return nil }
func (l *failingListener) Addr() net.Addr { return &net.UnixAddr{Name: "test", Net: "unix"} }

func TestServeUnwedgesOnListenerFailure(t *testing.T) {
	// #85: the listener dies while a streaming client is mid-connection.
	// Neither the client nor the engine side will ever close on its own —
	// exactly a quiet `docker events` — so unless Serve tears the live
	// connections down itself, wg.Wait pins the process forever while the
	// supervisor's single-instance mutex blocks any replacement.
	clientSrv, clientHeld := net.Pipe() // clientHeld stays open in the "client"
	engineSrv, engineHeld := net.Pipe() // engineHeld stays open in the "engine"
	defer clientHeld.Close()
	defer engineHeld.Close()

	l := &failingListener{
		conns: []net.Conn{clientSrv},
		err:   errors.New("listener exploded"),
	}
	srv := &Server{
		Dialer: DialerFunc(func(context.Context) (io.ReadWriteCloser, error) {
			return engineSrv, nil
		}),
		Logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
	}

	done := make(chan error, 1)
	go func() { done <- srv.Serve(context.Background(), l) }()

	select {
	case err := <-done:
		if err == nil || !strings.Contains(err.Error(), "listener exploded") {
			t.Errorf("Serve returned %v, want the listener failure", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("Serve wedged after listener failure (#85)")
	}
}
