package wslc

import (
	"context"

	"log/slog"
	"time"
)

// DefaultKeepaliveInterval is how often the session is poked while the bridge$
// is serving. The default session idle timeout is 30 s — `session.idleTimeout`
// in wslc's own settings.yaml — so the interval has to sit comfortably inside
// it. The poke costs ~40 ms.
const DefaultKeepaliveInterval = 10 * time.Second

// Keepalive stops the session VM being terminated out from under running
// containers.
//
// WSLC's idle timer only counts containers it knows about — ones created
// through the WSLC API, by wslcsession. Skrog talks to dockerd directly over
// vsock, so its containers are invisible to that accounting and the session
// looks idle however busy the engine is. The VM is then torn down on schedule,
// SIGKILLing them.
//
// Measured on 2.9.11.0, same host, same fresh default VM, 75 s of silence:
//
//	created with `wslc run -d`        -> still running
//	created on the engine socket      -> exited 137, VM rebooted
//
// So this is not "WSLC kills running containers" in general — through its own
// CLI it behaves correctly. It is the Docker-API plane having no Windows-side
// integration (microsoft/WSL#40957), with VM lifetime as the consequence.
// Skrog has to work on the shipped build, so it generates the activity itself.
//
// There is a supported alternative: raising `session.idleTimeout` in wslc's
// settings.yaml (0 is rejected; the maximum uint32 is effectively "never").
// Skrog does not set it, because that file is global to every wslc session the
// user has, and reaching into global config without asking is the thing
// `skrog wsl-config` exists to avoid doing. A poke that lives and dies with the
// bridge costs one CLI spawn per 10 s and leaves the user's configuration
// alone.
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
