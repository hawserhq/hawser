package wslc

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"
)

// countingRunner records the CLI commands the keepalive issues.
type countingRunner struct {
	mu   sync.Mutex
	args [][]string
	err  error
}

func (c *countingRunner) Run(_ context.Context, _ string, args ...string) ([]byte, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.args = append(c.args, args)
	return nil, c.err
}

func (c *countingRunner) count() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.args)
}

func (c *countingRunner) last() []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(c.args) == 0 {
		return nil
	}
	return c.args[len(c.args)-1]
}

func keepalive(r *countingRunner) *Keepalive {
	return &Keepalive{
		Local:    &Local{Exe: "wslc.exe", Runner: r},
		Session:  "s",
		Interval: 20 * time.Millisecond,
	}
}

func TestKeepalivePokesTheSession(t *testing.T) {
	r := &countingRunner{}
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	keepalive(r).Run(ctx)

	if r.count() == 0 {
		t.Fatal("keepalive never poked the session")
	}
	// Through the session, not some global command: a bare `wslc` call would
	// reset the wrong session's idle timer, or create one.
	if last := strings.Join(r.last(), " "); !strings.HasPrefix(last, "--session s system session run") {
		t.Errorf("poke was %q, want a session-scoped command", last)
	}
}

// Poking is unconditional rather than gated on "is anything running".
//
// Gating looks thrifty and is the broken version: it leaves a window each time
// the container count hits zero, the idle timer starts, and the VM is gone
// before the next container is created. Testcontainers does exactly that dance
// — create a container, start a reaper, wait — and its reaper was reaped.
func TestKeepalivePokesEvenWithNothingRunning(t *testing.T) {
	r := &countingRunner{}
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	keepalive(r).Run(ctx)

	if r.count() == 0 {
		t.Error("keepalive stopped poking with no containers running; " +
			"that is the window that kills the next container")
	}
}

// A failing poke is not fatal: the session may be mid-restart, and the next
// tick is the retry.
func TestKeepaliveSurvivesAFailedPoke(t *testing.T) {
	r := &countingRunner{err: errors.New("session not found")}
	ctx, cancel := context.WithTimeout(context.Background(), 150*time.Millisecond)
	defer cancel()
	keepalive(r).Run(ctx)

	if r.count() < 2 {
		t.Errorf("poked %d times; a failed poke must not stop the loop", r.count())
	}
}

func TestKeepaliveStopsWithTheContext(t *testing.T) {
	r := &countingRunner{}
	k := keepalive(r)
	k.Interval = 10 * time.Millisecond

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { k.Run(ctx); close(done) }()
	time.Sleep(50 * time.Millisecond)
	cancel()

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Run did not return after the context was cancelled")
	}
}
