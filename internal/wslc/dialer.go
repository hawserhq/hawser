package wslc

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"sync"

	"github.com/wslkit/skrog/internal/pipeproxy"
)

// Dialer reaches the engine in a wslc session, re-placing the agent whenever
// the session VM has restarted underneath it.
//
// This is not belt-and-braces; it is the normal operating mode. A wslc session
// VM idle-terminates as soon as it has no running containers, and its root
// filesystem is a tmpfs overlay — so the agent is discarded on every such
// cycle, and the next VM boot has no agent at all. Measured while bridging a
// real `docker` session: after one `run --rm` container exited, the VM
// restarted with uptime 21s and /tmp/skrog-agent was simply gone, and every
// subsequent request failed with "the pipe has been ended" (#323).
//
// The distro backend has no equivalent, because its agent lives in a rootfs
// that persists and is started by the supervisor.
type Dialer struct {
	// Inner is the transport — in production a *pipeproxy.VsockDialer with its
	// cooldown disabled, since a failure here means "re-bootstrap" rather than
	// "wait and hope". An interface so the recovery logic is testable without
	// a VM.
	Inner pipeproxy.Dialer

	// Bootstrap re-places and restarts the agent.
	Bootstrap func(context.Context) error

	// Logger receives one line per re-bootstrap. Nil is silent.
	Logger *slog.Logger

	// mu serialises recovery so a burst of failed connections — which is what
	// a VM restart produces — triggers one bootstrap rather than one per
	// connection, each streaming 2.6 MB into the guest.
	mu sync.Mutex
}

// Dial implements pipeproxy.Dialer.
func (d *Dialer) Dial(ctx context.Context) (io.ReadWriteCloser, error) {
	conn, err := d.Inner.Dial(ctx)
	if err == nil {
		return conn, nil
	}

	d.mu.Lock()
	defer d.mu.Unlock()

	// Another goroutine may have recovered while we waited for the lock, in
	// which case this retry succeeds and no second bootstrap happens.
	if conn, err2 := d.Inner.Dial(ctx); err2 == nil {
		return conn, nil
	}

	if d.Logger != nil {
		d.Logger.Info("wslc agent unreachable, re-bootstrapping the session", "error", err)
	}
	if err := d.Bootstrap(ctx); err != nil {
		return nil, fmt.Errorf("wslc: agent unreachable and re-bootstrap failed: %w", err)
	}
	conn, err = d.Inner.Dial(ctx)
	if err != nil {
		return nil, fmt.Errorf("wslc: agent still unreachable after re-bootstrap: %w", err)
	}
	if d.Logger != nil {
		d.Logger.Info("wslc agent recovered")
	}
	return conn, nil
}
