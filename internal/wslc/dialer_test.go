package wslc

import (
	"context"
	"errors"
	"io"
	"strings"
	"sync"
	"testing"
)

// flakyDialer fails until healed, which is how a session VM behaves across an
// idle termination: every dial fails until the agent is back.
type flakyDialer struct {
	mu      sync.Mutex
	healthy bool
	dials   int
}

func (f *flakyDialer) Dial(context.Context) (io.ReadWriteCloser, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.dials++
	if !f.healthy {
		return nil, errors.New("no skrog agent reachable")
	}
	return nopConn{}, nil
}

func (f *flakyDialer) heal() {
	f.mu.Lock()
	f.healthy = true
	f.mu.Unlock()
}

type nopConn struct{}

func (nopConn) Read([]byte) (int, error)    { return 0, io.EOF }
func (nopConn) Write(b []byte) (int, error) { return len(b), nil }
func (nopConn) Close() error                { return nil }

func TestDialerRecoversByReBootstrapping(t *testing.T) {
	inner := &flakyDialer{}
	var bootstraps int
	d := &Dialer{
		Inner: inner,
		Bootstrap: func(context.Context) error {
			bootstraps++
			inner.heal()
			return nil
		},
	}

	conn, err := d.Dial(context.Background())
	if err != nil {
		t.Fatalf("Dial: %v", err)
	}
	conn.Close()
	if bootstraps != 1 {
		t.Errorf("bootstraps = %d, want exactly 1", bootstraps)
	}
}

func TestDialerDoesNotBootstrapWhenHealthy(t *testing.T) {
	inner := &flakyDialer{healthy: true}
	d := &Dialer{
		Inner:     inner,
		Bootstrap: func(context.Context) error { t.Fatal("bootstrap ran on a healthy transport"); return nil },
	}
	if _, err := d.Dial(context.Background()); err != nil {
		t.Fatalf("Dial: %v", err)
	}
}

// A VM restart fails every in-flight connection at once. Each one must not
// stream the agent in again: that is 2.6 MB per connection through a relay
// that handles one at a time.
func TestConcurrentFailuresBootstrapOnce(t *testing.T) {
	inner := &flakyDialer{}
	var mu sync.Mutex
	bootstraps := 0
	d := &Dialer{
		Inner: inner,
		Bootstrap: func(context.Context) error {
			mu.Lock()
			bootstraps++
			mu.Unlock()
			inner.heal()
			return nil
		},
	}

	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if c, err := d.Dial(context.Background()); err == nil {
				c.Close()
			}
		}()
	}
	wg.Wait()

	mu.Lock()
	defer mu.Unlock()
	if bootstraps != 1 {
		t.Errorf("bootstraps = %d across 20 concurrent failed dials, want 1", bootstraps)
	}
}

func TestDialerReportsABootstrapFailure(t *testing.T) {
	d := &Dialer{
		Inner:     &flakyDialer{},
		Bootstrap: func(context.Context) error { return errors.New("session is gone") },
	}
	_, err := d.Dial(context.Background())
	if err == nil {
		t.Fatal("want an error when the re-bootstrap fails")
	}
	if !strings.Contains(err.Error(), "session is gone") {
		t.Errorf("error %q does not carry the bootstrap failure", err)
	}
}

// A bootstrap that succeeds but leaves the agent unreachable must not be
// reported as success — that would hand the relay a nil connection.
func TestDialerReportsAStillDeadAgent(t *testing.T) {
	d := &Dialer{
		Inner:     &flakyDialer{}, // never heals
		Bootstrap: func(context.Context) error { return nil },
	}
	if _, err := d.Dial(context.Background()); err == nil {
		t.Fatal("want an error when the agent is still unreachable after bootstrap")
	}
}
