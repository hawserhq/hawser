package wslc

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"
)

// blockingRunner stands in for a held root-namespace process: it blocks until
// the context is cancelled or the test releases it, which is what the real
// `sleep` does.
type blockingRunner struct {
	mu      sync.Mutex
	calls   [][]string
	release chan struct{}
	err     error
}

func newBlockingRunner() *blockingRunner {
	return &blockingRunner{release: make(chan struct{})}
}

func (b *blockingRunner) Run(ctx context.Context, _ string, args ...string) ([]byte, error) {
	b.mu.Lock()
	b.calls = append(b.calls, args)
	err := b.err
	b.mu.Unlock()

	if err != nil {
		return nil, err
	}
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-b.release:
		return nil, errors.New("process exited")
	}
}

func (b *blockingRunner) count() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return len(b.calls)
}

func (b *blockingRunner) first() []string {
	b.mu.Lock()
	defer b.mu.Unlock()
	if len(b.calls) == 0 {
		return nil
	}
	return b.calls[0]
}

func lease(r Runner) *Lease {
	return &Lease{Local: &Local{Exe: "wslc.exe", Runner: r}, Session: "s", Hold: time.Hour}
}

// The lease is a held process, not a poll: one call that blocks, not many.
func TestLeaseHoldsOneProcess(t *testing.T) {
	r := newBlockingRunner()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { lease(r).Run(ctx); close(done) }()

	time.Sleep(150 * time.Millisecond)
	if n := r.count(); n != 1 {
		t.Errorf("made %d calls while holding; a lease is one blocking call, not a poll", n)
	}

	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Run did not return after cancel")
	}
}

// It must be a root-namespace process in Skrog's session — that is what takes
// the activity reference (WSLCProcess keep-alive token).
func TestLeaseRunsASessionScopedProcess(t *testing.T) {
	r := newBlockingRunner()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go lease(r).Run(ctx)

	time.Sleep(150 * time.Millisecond)
	got := strings.Join(r.first(), " ")
	if !strings.HasPrefix(got, "--session s system session run sleep") {
		t.Errorf("lease command = %q, want a session-scoped `sleep`", got)
	}
}

// When the VM restarts the held process dies and the call returns. That is
// precisely when a new lease is needed, so it must be re-taken rather than
// treated as a failure.
func TestLeaseIsRetakenWhenTheProcessDies(t *testing.T) {
	r := newBlockingRunner()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go lease(r).Run(ctx)

	time.Sleep(100 * time.Millisecond)
	close(r.release) // the VM went away under us
	time.Sleep(1500 * time.Millisecond)

	if n := r.count(); n < 2 {
		t.Errorf("made %d calls; the lease was not re-taken after the process died", n)
	}
}

// A session that does not exist yet must not spin: the loop backs off rather
// than hammering the CLI.
func TestLeaseBacksOffWhenTheSessionIsMissing(t *testing.T) {
	r := newBlockingRunner()
	r.err = errors.New("Session not found")
	ctx, cancel := context.WithTimeout(context.Background(), 1200*time.Millisecond)
	defer cancel()
	lease(r).Run(ctx)

	// ~1s of backoff per attempt, so a little over a second should be 1-3
	// attempts, not hundreds.
	if n := r.count(); n > 5 {
		t.Errorf("made %d attempts in ~1.2s; the lease is spinning instead of backing off", n)
	}
	if r.count() == 0 {
		t.Error("made no attempt at all")
	}
}

func TestLeaseStopsWithTheContext(t *testing.T) {
	r := newBlockingRunner()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { lease(r).Run(ctx); close(done) }()

	time.Sleep(100 * time.Millisecond)
	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Run did not return after the context was cancelled")
	}
}

// A leaked lease pins a ~750 MB VM with nothing left to use it, and on Windows
// a force-killed parent does not take its children with it. The hold duration
// is the only bound on that, so it must stay short enough to matter.
func TestDefaultLeaseHoldBoundsALeak(t *testing.T) {
	if DefaultLeaseHold > 15*time.Minute {
		t.Errorf("DefaultLeaseHold = %v; an orphaned lease would pin the VM that long "+
			"after an unclean exit", DefaultLeaseHold)
	}
	// And not so short that renewal becomes the polling this design replaced.
	if DefaultLeaseHold < time.Minute {
		t.Errorf("DefaultLeaseHold = %v; renewal is a process spawn, which is the "+
			"cost a lease exists to avoid", DefaultLeaseHold)
	}
}
