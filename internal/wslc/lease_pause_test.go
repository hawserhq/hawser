package wslc

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"
)

// countingRunner fails every hold, the way a terminated session does.
type countingRunner struct{ calls atomic.Int32 }

func (r *countingRunner) Run(ctx context.Context, _ string, _ ...string) ([]byte, error) {
	r.calls.Add(1)
	return nil, errors.New("Session not found")
}

// The conflict this fixes: `skrog stop` terminates the session, the held
// process dies, and the lease takes it again a second later -- either
// resurrecting the VM the user just stopped, or spinning forever.
func TestPausedLeaseStopsRetakingTheSession(t *testing.T) {
	r := &countingRunner{}
	l := &Lease{Local: &Local{Exe: "wslc.exe", Runner: r}, Session: "s", Hold: time.Minute}
	l.Pause()

	ctx, cancel := context.WithTimeout(context.Background(), 2500*time.Millisecond)
	defer cancel()
	l.Run(ctx)

	if n := r.calls.Load(); n != 0 {
		t.Errorf("a paused lease ran %d hold(s); it must take none", n)
	}
}

// And Resume puts it back to work, or `skrog start` would leave the session
// unheld and it would idle-terminate under the user.
func TestResumedLeaseTakesTheSessionAgain(t *testing.T) {
	r := &countingRunner{}
	l := &Lease{Local: &Local{Exe: "wslc.exe", Runner: r}, Session: "s", Hold: time.Minute}
	l.Pause()
	l.Resume()

	ctx, cancel := context.WithTimeout(context.Background(), 1500*time.Millisecond)
	defer cancel()
	l.Run(ctx)

	if r.calls.Load() == 0 {
		t.Error("a resumed lease never took the session")
	}
}

// A permanent failure must not spawn a process per second for months. With
// backoff, a few seconds of failure costs a handful of attempts, not dozens.
func TestFailingLeaseBacksOff(t *testing.T) {
	r := &countingRunner{}
	l := &Lease{Local: &Local{Exe: "wslc.exe", Runner: r}, Session: "gone", Hold: time.Minute}

	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Second)
	defer cancel()
	l.Run(ctx)

	// Un-backed-off, a fixed 1s retry gives ~4. Doubling gives 1s+2s+4s, so
	// three attempts inside the window.
	if n := r.calls.Load(); n > 3 {
		t.Errorf("%d attempts in 4s; the retry is not backing off", n)
	}
	if r.calls.Load() == 0 {
		t.Error("the lease never tried at all")
	}
}

// Cancelling must return promptly rather than sitting out a long backoff.
func TestLeaseReturnsOnCancel(t *testing.T) {
	r := &countingRunner{}
	l := &Lease{Local: &Local{Exe: "wslc.exe", Runner: r}, Session: "gone", Hold: time.Minute}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { l.Run(ctx); close(done) }()
	time.Sleep(200 * time.Millisecond)
	cancel()

	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("Run did not return promptly after cancel")
	}
}
