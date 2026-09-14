package wslc

import (
	"context"

	"log/slog"
	"time"
)

// DefaultKeepaliveInterval is how often the session is poked while the bridge$
// is serving. Measured: a `sleep 600` container was SIGKILLed about 30 s
// after the last CLI activity, so the interval has to be comfortably inside
// that, and the poke costs ~40 ms.
const DefaultKeepaliveInterval = 10 * time.Second

// Keepalive stops the session VM being terminated out from under running
// containers.
//
// WSLC's idle timer counts *its own* API traffic — work that goes through
// wslcsession — and Skrog deliberately bypasses that, talking to dockerd over
// vsock. From the session manager's point of view the session is therefore
// perfectly idle no matter how busy the engine is, and it reaps the VM on
// schedule. Measured on 2.9.11.0: a `docker run -d busybox sleep 600` through
// the bridge exited 137 (SIGKILL) after ~30 s, with nothing else touching the
// machine. The same container survived indefinitely when a `wslc` command was
// run every 10 s.
//
// That is arguably a WSLC bug — killing running containers because no one
// called the CLI recently is surprising however the containers were started —
// but Skrog has to work on the shipped build, so it generates the activity.
//
// Only while something is running, though. Idle termination is the feature
// that keeps a ~750 MB VM from sitting around after work stops, and defeating
// it unconditionally would be a worse trade than the one Microsoft chose.
type Keepalive struct {
	Local   *Local
	Session string

	// Interval between pokes. Zero uses DefaultKeepaliveInterval.
	Interval time.Duration

	Logger *slog.Logger
}

// Run pokes the session while containers are running, until ctx is cancelled.
func (k *Keepalive) Run(ctx context.Context) {
	interval := k.Interval
	if interval <= 0 {
		interval = DefaultKeepaliveInterval
	}
	t := time.NewTicker(interval)
	defer t.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}

		// Poke unconditionally, not only while containers are running.
		//
		// Gating on "something is running" looks like the thrifty choice and is
		// actually the broken one: it leaves a window every time the container
		// count reaches zero, the idle timer starts, and the VM is gone before
		// the next container is created — so `docker run` lands in a VM that is
		// already being torn down. Measured against Testcontainers, which does
		// exactly that dance: create a container, start a reaper, wait. The
		// reaper was reaped, repeatedly.
		//
		// Holding the VM up for as long as the bridge is serving also matches
		// what the distro backend does and what a user expects: the bridge is
		// up, so the engine is up. Reclaiming the ~750 MB when nobody is using
		// docker is Skrog's existing idle-timeout to decide, at the level of
		// the whole bridge, where it can be done without racing anything.
		if _, err := k.Local.RunInSession(ctx, k.Session, "true"); err != nil && ctx.Err() == nil {
			k.log().Debug("keepalive poke failed", "error", err)
		}
	}
}

func (k *Keepalive) log() *slog.Logger {
	if k.Logger != nil {
		return k.Logger
	}
	return slog.New(slog.DiscardHandler)
}
