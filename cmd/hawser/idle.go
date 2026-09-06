package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"time"

	"github.com/zcsizmadia/hawser/internal/pipeproxy"
	"github.com/zcsizmadia/hawser/internal/provision"
	"github.com/zcsizmadia/hawser/internal/supervise"
)

// demandDialer wakes an idle-stopped engine before dialing it: the first
// docker command after an idle stop pays the cold start and works, instead of
// failing against a stopped engine (#41).
type demandDialer struct {
	sup   *supervise.Supervisor
	inner pipeproxy.Dialer
}

func (d *demandDialer) Dial(ctx context.Context) (io.ReadWriteCloser, error) {
	// Demand is a no-op unless the engine is idle-stopped, so the healthy
	// path pays one flag check, not an engine probe.
	if err := d.sup.Demand(ctx); err != nil {
		return nil, fmt.Errorf("waking the engine: %w", err)
	}
	return d.inner.Dial(ctx)
}

// rwcConn adapts the dialer's io.ReadWriteCloser to net.Conn for http.
// Deadlines are no-ops; the probe's lifetime is bounded by its context.
type rwcConn struct {
	io.ReadWriteCloser
}

type dummyAddr string

func (a dummyAddr) Network() string { return string(a) }
func (a dummyAddr) String() string  { return string(a) }

func (rwcConn) LocalAddr() net.Addr              { return dummyAddr("hawser") }
func (rwcConn) RemoteAddr() net.Addr             { return dummyAddr("engine") }
func (rwcConn) SetDeadline(time.Time) error      { return nil }
func (rwcConn) SetReadDeadline(time.Time) error  { return nil }
func (rwcConn) SetWriteDeadline(time.Time) error { return nil }

// busyProbe asks the engine whether any containers are running, over the same
// transport the bridge uses. The idle stop needs a definite "no": any error
// here vetoes it (supervise.Supervisor.Busy's contract).
func busyProbe(dialer pipeproxy.Dialer) func(ctx context.Context) (bool, error) {
	client := &http.Client{
		Transport: &http.Transport{
			DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
				rwc, err := dialer.Dial(ctx)
				if err != nil {
					return nil, err
				}
				return rwcConn{rwc}, nil
			},
			// One probe, one connection: the engine socket is not a place to
			// pool idle keep-alives that would themselves look like activity.
			DisableKeepAlives: true,
		},
	}
	return func(ctx context.Context) (bool, error) {
		ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
		defer cancel()
		// /containers/json lists RUNNING containers by default, which is
		// exactly the "would an idle stop kill something" question.
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://engine/containers/json", nil)
		if err != nil {
			return false, err
		}
		resp, err := client.Do(req)
		if err != nil {
			return false, err
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			return false, fmt.Errorf("engine returned %s to the container probe", resp.Status)
		}
		var containers []json.RawMessage
		if err := json.NewDecoder(resp.Body).Decode(&containers); err != nil {
			return false, err
		}
		return len(containers) > 0, nil
	}
}

// engineBusy vetoes an idle stop when the engine is in use in any way the pipe
// cannot see (#72): a running container, OR an active connection to the engine
// socket from an integrated distro over the /mnt/wsl share. Either signal, or
// an error from probing them, keeps the engine up — stopping mid-operation
// would kill a docker build or pull running in that distro.
func engineBusy(dialer pipeproxy.Dialer, p *provision.Provisioner, opts provision.Options) func(ctx context.Context) (bool, error) {
	containers := busyProbe(dialer)
	return func(ctx context.Context) (bool, error) {
		if busy, err := containers(ctx); err != nil || busy {
			return busy, err
		}
		return p.SocketBusy(ctx, opts)
	}
}
