//go:build windows

package pipeproxy

import (
	"context"
	"errors"
	"fmt"
	"io"
	"sync"
	"time"

	"github.com/Microsoft/go-winio"
	"github.com/Microsoft/go-winio/pkg/guid"
	"github.com/hawserhq/hawser/internal/vsockproto"
	"golang.org/x/sys/windows/registry"
)

// VsockDialer reaches the engine through the in-distro hawser-agent over
// AF_HYPERV — the v0.2 transport (#40), validated by Spike C. Per-connection
// cost is ~0.6 ms against socat's ~165 ms, and both ends support half-close,
// which removes the leak class of #35 outright.
type VsockDialer struct {
	// Port is the agent's vsock port. Zero uses vsockproto.Port.
	Port uint32

	// DialTimeout bounds one dial+handshake attempt. Zero uses 2s: the VM is
	// local, so anything slower than that is a "not there", not a "be patient".
	DialTimeout time.Duration

	// Cooldown is how long Dial fails instantly after a failed attempt,
	// instead of probing again. Zero uses 15s; negative disables the cache.
	// Without it, an agent-less rootfs would pay a full dial timeout on
	// every connection while the fallback transport does the real work.
	Cooldown time.Duration

	// Secret authenticates the agent (#81). When set, the handshake requires
	// the agent to prove the same per-install secret, so a sibling distro
	// squatting the vsock port while the engine is idle-stopped is rejected
	// before any docker traffic flows. Empty keeps the pre-#81 handshake.
	Secret string

	// dialHV overrides the transport dial in tests.
	dialHV func(ctx context.Context, vmid guid.GUID) (io.ReadWriteCloser, error)

	// mu guards the discovery/failure cache. vmid is the verified utility VM,
	// so registry enumeration is skipped per connection; a dial failure clears
	// it (a restarted VM gets a new GUID) and stamps lastFailure.
	mu          sync.Mutex
	vmid        *guid.GUID
	lastFailure time.Time
}

// ErrCoolingDown is returned while the dialer sits out its post-failure
// cooldown. Callers with a fallback treat it like any dial error, just an
// instant one.
var ErrCoolingDown = errors.New("pipeproxy: vsock transport cooling down after a failure")

// computeSystemKey is the Host Compute Service's mirror of running compute
// systems. Readable by standard users — the property Spike C established that
// makes this transport possible without elevation (hcsdiag needs Hyper-V
// admin). The key only lists *running* systems: with the distro stopped there
// are no candidates, which correctly reads as "agent unreachable".
const computeSystemKey = `SOFTWARE\Microsoft\Windows NT\CurrentVersion\HostComputeService\VolatileStore\ComputeSystem`

func discoverVMIDs() ([]guid.GUID, error) {
	k, err := registry.OpenKey(registry.LOCAL_MACHINE, computeSystemKey, registry.ENUMERATE_SUB_KEYS)
	if err != nil {
		return nil, fmt.Errorf("pipeproxy: enumerating compute systems: %w", err)
	}
	defer k.Close()
	names, err := k.ReadSubKeyNames(-1)
	if err != nil {
		return nil, fmt.Errorf("pipeproxy: reading compute systems: %w", err)
	}
	ids := make([]guid.GUID, 0, len(names))
	for _, n := range names {
		if id, err := guid.FromString(n); err == nil {
			ids = append(ids, id)
		}
	}
	return ids, nil
}

func (d *VsockDialer) port() uint32 {
	if d.Port == 0 {
		return vsockproto.Port
	}
	return d.Port
}

func (d *VsockDialer) timeout() time.Duration {
	if d.DialTimeout == 0 {
		return 2 * time.Second
	}
	return d.DialTimeout
}

// rawDial is the transport dial, a seam for tests. The production dial wraps
// winio in a goroutine because winio's ConnectEx wait does not check the
// context ("asyncIO doesn't check the context" — winio's own comment), and an
// hv-socket connect to a port nobody listens on is not refused, it times out
// at OS level after ~16s. Measured: an agent-less rootfs turned every docker
// command into a 32s crawl. The goroutine is abandoned on timeout and closes
// the connection if the OS ever completes it.
func (d *VsockDialer) rawDial(ctx context.Context, vmid guid.GUID) (io.ReadWriteCloser, error) {
	if d.dialHV != nil {
		return d.dialHV(ctx, vmid)
	}
	type result struct {
		conn *winio.HvsockConn
		err  error
	}
	ch := make(chan result, 1)
	go func() {
		conn, err := winio.Dial(ctx, &winio.HvsockAddr{
			VMID:      vmid,
			ServiceID: winio.VsockServiceID(d.port()),
		})
		ch <- result{conn, err}
	}()
	select {
	case r := <-ch:
		return r.conn, r.err
	case <-time.After(d.timeout()):
		go func() {
			if r := <-ch; r.conn != nil {
				r.conn.Close()
			}
		}()
		return nil, fmt.Errorf("pipeproxy: vsock dial timed out after %s", d.timeout())
	case <-ctx.Done():
		go func() {
			if r := <-ch; r.conn != nil {
				r.conn.Close()
			}
		}()
		return nil, ctx.Err()
	}
}

// dialVM makes one verified connection: transport dial plus the vsockproto
// handshake. The handshake is what turns "some listener answered on our port
// in some VM" into "this is a hawser agent" — non-WSL utility VMs can appear
// in the compute-system list, and the port space inside the WSL VM is shared
// with every other distro.
func (d *VsockDialer) dialVM(ctx context.Context, vmid guid.GUID) (io.ReadWriteCloser, error) {
	conn, err := d.rawDial(ctx, vmid)
	if err != nil {
		return nil, err
	}
	// The handshake gets its own deadline when the transport supports one;
	// cleared before the transparent stream goes to the relay.
	type deadliner interface{ SetDeadline(time.Time) error }
	if dc, ok := conn.(deadliner); ok {
		dc.SetDeadline(time.Now().Add(d.timeout()))
		defer dc.SetDeadline(time.Time{})
	}
	if _, err := vsockproto.ClientHandshake(conn, d.Secret); err != nil {
		conn.Close()
		return nil, err
	}
	return conn, nil
}

func (d *VsockDialer) cooldown() time.Duration {
	switch {
	case d.Cooldown < 0:
		return 0
	case d.Cooldown == 0:
		return 15 * time.Second
	default:
		return d.Cooldown
	}
}

// Dial implements Dialer.
func (d *VsockDialer) Dial(ctx context.Context) (io.ReadWriteCloser, error) {
	d.mu.Lock()
	cached := d.vmid
	coolingDown := !d.lastFailure.IsZero() && time.Since(d.lastFailure) < d.cooldown()
	d.mu.Unlock()

	if coolingDown {
		return nil, ErrCoolingDown
	}

	conn, err := d.dialAny(ctx, cached)
	d.mu.Lock()
	if err != nil {
		d.lastFailure = time.Now()
	} else {
		d.lastFailure = time.Time{}
	}
	d.mu.Unlock()
	return conn, err
}

func (d *VsockDialer) dialAny(ctx context.Context, cached *guid.GUID) (io.ReadWriteCloser, error) {
	if cached != nil {
		if conn, err := d.dialVM(ctx, *cached); err == nil {
			return conn, nil
		}
		// The VM may have restarted under a new GUID; fall through to a
		// fresh discovery rather than failing on stale state.
		d.mu.Lock()
		d.vmid = nil
		d.mu.Unlock()
	}

	ids, err := discoverVMIDs()
	if err != nil {
		return nil, err
	}
	var lastErr error = fmt.Errorf("pipeproxy: no running compute systems")
	for i := range ids {
		conn, err := d.dialVM(ctx, ids[i])
		if err != nil {
			lastErr = err
			continue
		}
		d.mu.Lock()
		d.vmid = &ids[i]
		d.mu.Unlock()
		return conn, nil
	}
	return nil, fmt.Errorf("pipeproxy: no hawser agent reachable: %w", lastErr)
}
