package wslc

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net"
	"strings"
	"sync"
	"time"
)

// PortWatcher keeps Windows-side listeners in step with the container ports
// published inside a wslc session (#330).
//
// Watching /events rather than intercepting /containers/create is deliberate.
// The engine is the authority on what is actually published — it allocates
// ephemeral host ports, and it knows about containers Skrog never saw, such as
// one started before the bridge came up, or restarted by its own restart
// policy. Reading the intent off a create request would miss all three.
type PortWatcher struct {
	// EngineDial reaches the engine socket (the agent's engine port).
	EngineDial func(context.Context) (io.ReadWriteCloser, error)

	// ForwardDial reaches the agent's forward port, for the relays themselves.
	ForwardDial func(context.Context) (io.ReadWriteCloser, error)

	Logger *slog.Logger

	mu     sync.Mutex
	active map[string][]*Forwarder // container ID -> its listeners
}

// Run watches until the context is cancelled, reconnecting when the stream
// drops.
//
// A dropped stream is routine here: the session VM idle-terminates whenever it
// has no running containers, which ends /events and removes every container
// that could have been published. So each reconnect re-syncs from scratch
// rather than assuming the previous view still holds.
func (w *PortWatcher) Run(ctx context.Context) error {
	backoff := 500 * time.Millisecond
	const maxBackoff = 15 * time.Second

	for ctx.Err() == nil {
		if err := w.watchOnce(ctx); err != nil && ctx.Err() == nil {
			w.log().Debug("event stream ended, retrying", "error", err, "in", backoff)
			select {
			case <-ctx.Done():
			case <-time.After(backoff):
			}
			if backoff *= 2; backoff > maxBackoff {
				backoff = maxBackoff
			}
			continue
		}
		backoff = 500 * time.Millisecond
	}
	w.StopAll()
	return ctx.Err()
}

func (w *PortWatcher) watchOnce(ctx context.Context) error {
	// Sync before streaming, not after: a container that started while the
	// stream was down produces no event, and would otherwise stay unpublished
	// until it restarted.
	if err := w.sync(ctx); err != nil {
		return err
	}

	const path = "/" + APIVersion + "/events?filters=" +
		`%7B%22type%22%3A%5B%22container%22%5D%7D` // {"type":["container"]}
	return apiStream(ctx, w.EngineDial, path, func(raw []byte) {
		var ev containerEvent
		if err := json.Unmarshal(raw, &ev); err != nil {
			return
		}
		if ev.Type != "" && ev.Type != "container" {
			return
		}
		switch ev.Action {
		case "start":
			w.publish(ctx, ev.id())
		case "die", "destroy", "kill", "stop":
			w.unpublish(ev.id())
		}
	})
}

// sync makes the live set match what the engine currently reports running.
func (w *PortWatcher) sync(ctx context.Context) error {
	body, err := apiGet(ctx, w.EngineDial, "/"+APIVersion+"/containers/json")
	if err != nil {
		return err
	}
	var running []struct {
		ID string `json:"Id"`
	}
	if err := json.Unmarshal(body, &running); err != nil {
		return fmt.Errorf("listing containers: %w", err)
	}

	live := make(map[string]bool, len(running))
	for _, c := range running {
		live[c.ID] = true
	}

	w.mu.Lock()
	stale := make([]string, 0)
	for id := range w.active {
		if !live[id] {
			stale = append(stale, id)
		}
	}
	w.mu.Unlock()

	for _, id := range stale {
		w.unpublish(id)
	}
	for _, c := range running {
		w.publish(ctx, c.ID)
	}
	return nil
}

// publish opens a listener for each port the container publishes. Idempotent:
// a container already published is left alone, so a re-sync does not churn
// live listeners.
func (w *PortWatcher) publish(ctx context.Context, id string) {
	if id == "" {
		return
	}
	w.mu.Lock()
	_, exists := w.active[id]
	w.mu.Unlock()
	if exists {
		return
	}

	body, err := apiGet(ctx, w.EngineDial, "/"+APIVersion+"/containers/"+id+"/json")
	if err != nil {
		// A container that died between the event and the inspect is ordinary.
		w.log().Debug("inspect failed, not publishing", "container", short(id), "error", err)
		return
	}
	var ins inspectPorts
	if err := json.Unmarshal(body, &ins); err != nil {
		w.log().Warn("could not read published ports", "container", short(id), "error", err)
		return
	}
	ip := ins.ip()
	if ip == "" || len(ins.NetworkSettings.Ports) == 0 {
		return
	}

	var started []*Forwarder
	// One host port can appear twice, once per address family: dockerd reports
	// `-p 18200:80` as both 0.0.0.0:18200 and [::]:18200. Go's listener for a
	// wildcard address is already dual-stack, so binding the second would fail
	// with "only one usage of each socket address" — a warning on every single
	// published port, for a port that in fact worked.
	seen := map[string]bool{}
	for spec, bindings := range ins.NetworkSettings.Ports {
		containerPort, proto := splitPortSpec(spec)
		if proto != "tcp" {
			// vsock is a stream transport, so UDP would need datagram framing
			// at both ends. Named rather than silently dropped: a Compose file
			// with a udp port should not look like it worked (#330).
			if len(bindings) > 0 {
				w.log().Warn("udp published ports are not supported on the wslc backend",
					"container", short(id), "port", spec)
			}
			continue
		}
		for _, b := range bindings {
			if b.HostPort == "" {
				continue
			}
			hostIP := b.HostIP
			if ip := net.ParseIP(hostIP); hostIP == "" || (ip != nil && ip.IsUnspecified()) {
				// Docker's own default, in either family. Skrog can honour it,
				// unlike WSLC's relay, which binds loopback only.
				hostIP = "0.0.0.0"
			}
			key := net.JoinHostPort(hostIP, b.HostPort) + "->" + spec
			if seen[key] {
				continue
			}
			seen[key] = true

			f := &Forwarder{
				HostAddr: net.JoinHostPort(hostIP, b.HostPort),
				Target:   net.JoinHostPort(ip, containerPort),
				Dial:     w.ForwardDial,
				Logger:   w.Logger,
			}
			if err := f.Start(ctx); err != nil {
				w.log().Warn("could not publish port", "container", short(id),
					"host", f.HostAddr, "target", f.Target, "error", err)
				continue
			}
			w.log().Info("published port", "container", short(id),
				"host", f.Addr(), "target", f.Target)
			started = append(started, f)
		}
	}
	if len(started) == 0 {
		return
	}
	w.mu.Lock()
	if w.active == nil {
		w.active = map[string][]*Forwarder{}
	}
	// Another goroutine may have published the same container while we were
	// inspecting; keep one set and close the loser rather than leaking it.
	if prev, ok := w.active[id]; ok {
		w.mu.Unlock()
		for _, f := range started {
			f.Stop()
		}
		_ = prev
		return
	}
	w.active[id] = started
	w.mu.Unlock()
}

func (w *PortWatcher) unpublish(id string) {
	w.mu.Lock()
	fs := w.active[id]
	delete(w.active, id)
	w.mu.Unlock()

	for _, f := range fs {
		f.Stop()
		w.log().Info("unpublished port", "container", short(id), "host", f.HostAddr)
	}
}

// StopAll closes every listener, for shutdown.
func (w *PortWatcher) StopAll() {
	w.mu.Lock()
	all := w.active
	w.active = nil
	w.mu.Unlock()
	for _, fs := range all {
		for _, f := range fs {
			f.Stop()
		}
	}
}

// Published reports the live host->target map, for status output and tests.
//
// The key is the address actually bound, not the one requested. They differ
// whenever the host port is 0 — `docker run -p 80` and `-P` — and reporting
// the request there would tell a user to connect to port 0.
func (w *PortWatcher) Published() map[string]string {
	w.mu.Lock()
	defer w.mu.Unlock()
	out := map[string]string{}
	for _, fs := range w.active {
		for _, f := range fs {
			addr := f.Addr()
			if addr == "" {
				addr = f.HostAddr
			}
			out[addr] = f.Target
		}
	}
	return out
}

func (w *PortWatcher) log() *slog.Logger {
	if w.Logger != nil {
		return w.Logger
	}
	return slog.New(slog.DiscardHandler)
}

// splitPortSpec turns "80/tcp" into ("80", "tcp"). A spec with no protocol is
// tcp, which is what the engine means by it.
func splitPortSpec(spec string) (port, proto string) {
	if i := strings.LastIndex(spec, "/"); i >= 0 {
		return spec[:i], spec[i+1:]
	}
	return spec, "tcp"
}

func short(id string) string {
	if len(id) > 12 {
		return id[:12]
	}
	return id
}
