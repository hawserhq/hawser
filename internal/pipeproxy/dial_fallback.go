package pipeproxy

import (
	"context"
	"io"
	"log/slog"
	"sync/atomic"
)

// FallbackDialer tries Primary on every connection and uses Secondary when it
// fails. Built for the vsock→socat pairing: a rootfs that predates the agent,
// an agent that died, or a VM mid-restart must degrade to the slow path, not
// to an outage — and must return to the fast path by itself, so the primary
// is re-attempted every time rather than latched off.
//
// Logging is edge-triggered: one line when the primary is first unavailable,
// one when it recovers. Per-connection failure logs at that rate would be
// noise on an agent-less rootfs, where failing is the expected steady state.
type FallbackDialer struct {
	Primary   Dialer
	Secondary Dialer

	// Logger receives the transition lines. Nil means silent.
	Logger *slog.Logger

	// degraded is 0 while the primary works, 1 while falling back.
	degraded atomic.Bool
}

// Dial implements Dialer.
func (d *FallbackDialer) Dial(ctx context.Context) (io.ReadWriteCloser, error) {
	conn, err := d.Primary.Dial(ctx)
	if err == nil {
		if d.degraded.CompareAndSwap(true, false) && d.Logger != nil {
			d.Logger.Info("engine transport recovered to the fast path")
		}
		return conn, nil
	}
	if d.degraded.CompareAndSwap(false, true) && d.Logger != nil {
		d.Logger.Warn("engine fast path unavailable, using fallback transport",
			"error", err)
	}
	return d.Secondary.Dial(ctx)
}

// Degraded reports whether the last dial fell back to the slow transport. It
// is the one number that explains "docker feels slow" from the outside: the
// engine is healthy, every request works, and each one is paying ~165 ms for
// a socat relay instead of ~0.6 ms over vsock (#179).
func (d *FallbackDialer) Degraded() bool { return d.degraded.Load() }
