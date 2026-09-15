package wslc

import (
	"context"
	"log/slog"
	"strconv"
	"sync/atomic"
	"time"
)

// Lease keeps the session VM alive for as long as Skrog is serving it.
//
// A wslc session is torn down when its activity refcount reaches zero, and the
// things that hold that refcount are the ones WSLC knows about: in-flight API
// operations, and containers it created itself. Skrog talks to dockerd directly
// over vsock, so its containers are invisible to that accounting — the session
// looks idle however busy the engine is, and the VM is destroyed with the
// containers still running. Measured on 2.9.11.0, same host, same fresh default
// VM, 80 s of silence:
//
//	created with `wslc run -d`      -> still running
//	created on the engine socket    -> exited 137, VM rebooted
//
// WSLC provides the answer for precisely this case. From WSLCProcess.h: "A
// root-namespace process is not tracked as a container, so it relies on this
// token to hold an activity reference on the owning session for as long as the
// client keeps the process alive, preventing the idle worker from tearing the
// VM down (and killing the process) underneath it."
//
// So the lease is one long-lived root-namespace process, held open. That is a
// reference, not a sample: there is no interval to tune and no window to miss.
// It costs one resident wslc.exe — 10 MB working set, 2 MB private, and 0.0 ms
// of CPU over a measured minute — against the 360 process spawns an hour that
// polling the CLI would need.
//
// One lease covers the whole session. The refcount is per-session, so any
// single holder keeps every container in that VM alive; there is no need for
// one per container.
type Lease struct {
	Local   *Local
	Session string
	Logger  *slog.Logger

	// Hold is how long each held process sleeps before the lease renews it.
	// Zero uses DefaultLeaseHold.
	Hold time.Duration

	// paused suspends the lease while the engine is deliberately stopped.
	//
	// Without it the lease and the supervisor fight: `skrog stop` terminates
	// the session, the blocking hold returns an error, and one second later
	// the lease takes it again -- either resurrecting the VM the user just
	// stopped, or spinning a wslc.exe per second for the life of the process.
	paused atomic.Bool
}

// Pause suspends the lease. Called when the engine is stopped on purpose, so
// nothing here holds the session open against that decision.
func (l *Lease) Pause() { l.paused.Store(true) }

// Resume allows the lease to be taken again.
func (l *Lease) Resume() { l.paused.Store(false) }

// DefaultLeaseHold bounds how long a leaked lease can pin the VM.
//
// On a clean stop the context is cancelled and exec kills the child, so the
// lease goes with the bridge. On Windows, though, killing a process does not
// kill its children — so if skrogw is force-killed (Task Manager, a crash, a
// `Stop-Process -Force`), the held wslc.exe survives and keeps a ~750 MB VM
// resident with nothing left to use it. Observed exactly that while testing.
//
// Renewal is the bound: the guest process exits on its own after this long, and
// an orphan takes the VM down with it. Ten minutes costs six process spawns an
// hour — against the 360 that polling the CLI every ten seconds would need —
// and caps the damage from an unclean exit at ten minutes of idle VM.
const DefaultLeaseHold = 10 * time.Minute

// Run holds the lease until ctx is cancelled.
//
// The call blocks for as long as the guest process lives, so the blocking call
// IS the lease. It returns early when the VM restarts underneath it, which is
// exactly when a new one is needed — so the loop re-establishes rather than
// treating it as an error.
func (l *Lease) Run(ctx context.Context) {
	hold := l.Hold
	if hold <= 0 {
		hold = DefaultLeaseHold
	}
	seconds := strconv.Itoa(int(hold.Seconds()))

	var held bool
	backoff := minLeaseRetry
	for ctx.Err() == nil {
		if l.paused.Load() {
			// Stopped on purpose. Idle here rather than re-taking the session
			// the supervisor just terminated.
			held = false
			if !sleepCtx(ctx, minLeaseRetry) {
				return
			}
			continue
		}
		if !held {
			l.log().Debug("holding a session lease", "session", l.Session)
			held = true
		}
		// Blocks until the process exits: normally when ctx is cancelled and
		// the child is killed, otherwise when the VM went away.
		_, err := l.Local.RunInSession(ctx, l.Session, "sleep", seconds)
		if err == nil {
			backoff = minLeaseRetry
			continue
		}
		if ctx.Err() != nil {
			return
		}
		// A session that does not exist yet, or a VM mid-restart. The
		// dialer's re-bootstrap handles the engine side; here it is enough
		// to wait and take the lease again.
		//
		// Backed off, because the failure may be permanent -- a session name
		// that no longer exists never comes back, and a fixed one-second
		// retry then spawns 86,400 processes a day, at Debug, for months.
		l.log().Debug("session lease ended, re-taking it", "error", err, "in", backoff)
		held = false
		if !sleepCtx(ctx, backoff) {
			return
		}
		if backoff *= 2; backoff > maxLeaseRetry {
			backoff = maxLeaseRetry
		}
	}
}

// Retry bounds for a lease that cannot be taken.
const (
	minLeaseRetry = time.Second
	maxLeaseRetry = 30 * time.Second
)

// sleepCtx waits, reporting false if the context ended first.
func sleepCtx(ctx context.Context, d time.Duration) bool {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-t.C:
		return true
	}
}

func (l *Lease) log() *slog.Logger {
	if l.Logger != nil {
		return l.Logger
	}
	return slog.New(slog.DiscardHandler)
}
