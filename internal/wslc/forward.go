package wslc

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"sync"

	"github.com/wslkit/skrog/internal/vsockproto"
)

// ForwardPort is the agent's second vsock port, for TCP forwarding: ASCII
// "hawf". Separate from AgentPort so the engine relay keeps its narrow
// contract (see vsockproto.ForwardPrefix).
const ForwardPort uint32 = 0x68617766

// Forwarder carries one published container port to Windows (#330).
//
// It exists because dockerd publishes ports inside the session VM and the
// relay that would carry them to the host is driven from the Windows side by
// wslcsession — which a bridge talking straight to the engine socket never
// invokes. Measured: a port published through the socket is unreachable from
// Windows on every address, so Compose port mappings and Testcontainers'
// getMappedPort() fail outright until something fills the gap.
//
// Skrog's version is strictly more capable than WSLC's own, which binds
// 127.0.0.1 only: HostIP comes from the client's PortBindings, so 0.0.0.0 and
// a specific address both mean what Docker says they mean.
type Forwarder struct {
	// HostAddr is the Windows side, e.g. "0.0.0.0:8080".
	HostAddr string

	// Target is the endpoint inside the VM, e.g. "172.17.0.3:80".
	Target string

	// Dial opens an authenticated connection to the agent's forward port.
	Dial func(context.Context) (io.ReadWriteCloser, error)

	Logger *slog.Logger

	mu       sync.Mutex
	listener net.Listener
}

// Start begins accepting. It returns once the listener is open, so a caller
// that reports "port published" is not lying about it.
func (f *Forwarder) Start(ctx context.Context) error {
	if err := vsockproto.ValidTarget(f.Target); err != nil {
		return err
	}
	ln, err := net.Listen("tcp", f.HostAddr)
	if err != nil {
		return fmt.Errorf("publishing %s: %w", f.HostAddr, err)
	}
	f.mu.Lock()
	f.listener = ln
	f.mu.Unlock()

	go f.accept(ctx, ln)
	return nil
}

// Addr reports the address actually bound, which matters when the caller asked
// for port 0 and Docker needs to be told what it got.
func (f *Forwarder) Addr() string {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.listener == nil {
		return ""
	}
	return f.listener.Addr().String()
}

// Stop closes the listener. In-flight connections are left to finish: a
// container that has just stopped will close them from its end anyway, and
// tearing down a live stream mid-response is worse than a brief tail.
func (f *Forwarder) Stop() error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.listener == nil {
		return nil
	}
	err := f.listener.Close()
	f.listener = nil
	return err
}

func (f *Forwarder) accept(ctx context.Context, ln net.Listener) {
	for {
		c, err := ln.Accept()
		if err != nil {
			// A closed listener is the ordinary stop path, not a fault.
			if errors.Is(err, net.ErrClosed) || ctx.Err() != nil {
				return
			}
			if f.Logger != nil {
				f.Logger.Warn("forward accept failed", "addr", f.HostAddr, "error", err)
			}
			return
		}
		go f.handle(ctx, c)
	}
}

func (f *Forwarder) handle(ctx context.Context, client net.Conn) {
	defer client.Close()

	guest, err := f.Dial(ctx)
	if err != nil {
		// Nothing useful to say to a TCP client but a close; the log is where
		// "my published port refuses connections" gets explained.
		if f.Logger != nil {
			f.Logger.Warn("forward dial failed", "target", f.Target, "error", err)
		}
		return
	}
	defer guest.Close()

	if err := vsockproto.WriteTarget(guest, f.Target); err != nil {
		if f.Logger != nil {
			f.Logger.Warn("forward target refused", "target", f.Target, "error", err)
		}
		return
	}

	relay(client, guest)
}

// relay copies both directions and propagates half-close, so a client that
// finishes its request and waits for the response works the same way it does
// through the engine pipe.
func relay(client net.Conn, guest io.ReadWriteCloser) {
	var wg sync.WaitGroup
	wg.Add(2)

	go func() {
		defer wg.Done()
		io.Copy(guest, client)
		if hc, ok := guest.(interface{ CloseWrite() error }); ok {
			hc.CloseWrite()
		} else {
			guest.Close()
		}
	}()
	go func() {
		defer wg.Done()
		io.Copy(client, guest)
		if hc, ok := client.(interface{ CloseWrite() error }); ok {
			hc.CloseWrite()
		} else {
			client.Close()
		}
	}()

	wg.Wait()
}
