// Package pipeproxy bridges the Windows named pipe that stock docker.exe
// expects to the engine's Unix socket inside the WSL2 distro.
//
// This is the load-bearing component the project is named for. Spike A (#2)
// proved the design end to end and measured it: the per-connection relay
// process starts in ~6 ms, while the surrounding wsl.exe startup costs ~165 ms
// per connection — the cost the v0.2 vsock agent removes. Correctness matters
// more than either number, because every Docker client behavior rides on it:
// hijacked streams (exec -it, attach), incremental streaming (logs -f), and
// half-closes (build, save) all break in different, confusing ways if the relay
// is sloppy.
//
// The transport deliberately knows nothing about HTTP. Byte-for-byte fidelity
// is what makes hijacked connections work at all; request rewriting (bind path
// translation) is layered on top rather than baked in here.
package pipeproxy

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"sync"
	"sync/atomic"
	"time"
)

// Dialer opens one connection to the engine socket. Production dials through
// wsl.exe; tests substitute a local socket, which is why the transport can be
// verified on any machine without WSL2.
type Dialer interface {
	Dial(ctx context.Context) (io.ReadWriteCloser, error)
}

// DialerFunc adapts a function to Dialer.
type DialerFunc func(ctx context.Context) (io.ReadWriteCloser, error)

// Dial implements Dialer.
func (f DialerFunc) Dial(ctx context.Context) (io.ReadWriteCloser, error) { return f(ctx) }

// halfCloser is implemented by connections that can signal "no more writes"
// without tearing the whole connection down. Propagating this is what lets a
// client send a request body, half-close, and still read the response — the
// docker build and docker save pattern.
type halfCloser interface {
	CloseWrite() error
}

// Server accepts client connections and relays each to the engine.
type Server struct {
	// Dialer opens the engine side of each connection. Required.
	Dialer Dialer

	// Logger receives connection lifecycle events. Defaults to slog.Default().
	Logger *slog.Logger

	// Handler relays one accepted connection. Defaults to Relay, a byte-for-byte
	// copy. RewriteBinds substitutes an HTTP-aware handler that translates
	// Windows bind paths; the transport itself stays oblivious either way.
	Handler func(client net.Conn, engine io.ReadWriteCloser) error

	wg sync.WaitGroup

	mu     sync.Mutex
	active map[io.Closer]struct{}

	// Metrics counts connections and bytes for `skrog status --stats` (#179).
	// Nil disables counting.
	Metrics *Metrics

	// clients and lastActivity feed idle detection (supervise.Activity):
	// how many client connections are open, and when one last opened or
	// closed (unix nanos; 0 = never).
	clients      atomic.Int64
	lastActivity atomic.Int64
}

// ActiveConns is how many client connections are open right now.
func (s *Server) ActiveConns() int { return int(s.clients.Load()) }

// LastActivity is when a connection last opened or closed; zero time if none.
func (s *Server) LastActivity() time.Time {
	ns := s.lastActivity.Load()
	if ns == 0 {
		return time.Time{}
	}
	return time.Unix(0, ns)
}

func (s *Server) touch() { s.lastActivity.Store(time.Now().UnixNano()) }

// track registers a connection so shutdown can close it. Without this, a
// client holding a streaming connection open (docker logs -f, docker events)
// would block service shutdown indefinitely: the relay only returns when one
// side goes away, and neither side has a reason to.
func (s *Server) track(c io.Closer) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.active == nil {
		s.active = make(map[io.Closer]struct{})
	}
	s.active[c] = struct{}{}
}

func (s *Server) untrack(c io.Closer) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.active, c)
}

// closeActive tears down every live connection, unblocking their relays.
func (s *Server) closeActive() {
	s.mu.Lock()
	defer s.mu.Unlock()
	for c := range s.active {
		c.Close()
	}
}

func (s *Server) logger() *slog.Logger {
	if s.Logger != nil {
		return s.Logger
	}
	return slog.Default()
}

// Serve accepts connections until ctx is cancelled or the listener fails, then
// waits for in-flight connections to finish. Closing the listener is the
// caller's job: Serve returns as soon as Accept fails, which is what a closed
// listener produces.
func (s *Server) Serve(ctx context.Context, l net.Listener) error {
	if s.Dialer == nil {
		return errors.New("pipeproxy: Dialer is required")
	}

	// Unblock Accept on cancellation — a named pipe Accept has no deadline —
	// and drop live connections so a streaming client cannot pin shutdown.
	// The watcher exits with Serve rather than leaking when Serve returns for
	// a reason other than cancellation.
	serveDone := make(chan struct{})
	defer close(serveDone)
	go func() {
		select {
		case <-ctx.Done():
			l.Close()
			s.closeActive()
		case <-serveDone:
		}
	}()

	for {
		conn, err := l.Accept()
		if err != nil {
			// However Accept ended — cancellation or a spontaneous listener
			// failure — no new connections are coming, and the live ones must
			// not pin this process (#85): a wedged Serve still holds the
			// supervisor's single-instance mutex, so a dead pipe would also
			// block every replacement supervisor until someone kills us.
			l.Close()
			s.closeActive()
			s.wg.Wait()
			if ctx.Err() != nil {
				return nil // shutdown, not failure
			}
			return fmt.Errorf("pipeproxy: accept: %w", err)
		}

		// Tracked here, synchronously, not inside the handler goroutine: a
		// teardown racing a just-accepted connection must still find it in
		// the active set, or closeActive misses it and wg.Wait wedges anyway.
		s.wg.Add(1)
		s.track(conn)
		go func() {
			defer s.wg.Done()
			defer s.untrack(conn)
			s.handle(ctx, conn)
		}()
	}
}

func (s *Server) handle(ctx context.Context, client net.Conn) {
	log := s.logger()
	defer client.Close()

	s.clients.Add(1)
	s.Metrics.connAccepted()
	s.touch()
	defer func() {
		s.clients.Add(-1)
		s.touch()
	}()

	engine, err := s.Dialer.Dial(ctx)
	if err != nil {
		// The client is left to see a closed connection; there is no HTTP
		// response to send, because the transport does not speak HTTP.
		log.Error("dial engine", "error", err)
		return
	}
	defer engine.Close()

	s.track(engine)
	defer s.untrack(engine)

	handler := s.Handler
	if handler == nil {
		// Relay accepts the more general io.ReadWriteCloser for its client, so
		// it needs a thin adapter to fit the Handler signature.
		handler = func(c net.Conn, e io.ReadWriteCloser) error { return Relay(c, e) }
	}
	// filterClosed here as well as inside Relay: a handler like RewriteBinds
	// returns transport errors from its own HTTP forwarding (wrapped with %w,
	// so errors.Is still sees them), and a docker CLI exiting mid-response is
	// ordinary shutdown regardless of which layer noticed it first.
	// Counted at the outermost point, so both proxy paths -- the HTTP rewrite
	// and the raw relay a hijack turns into -- are measured by the same code.
	if err := filterClosed(handler(s.Metrics.countClient(client), s.Metrics.countEngine(engine))); err != nil {
		// Warn, not Debug: the ordinary ways a docker client hangs up are
		// filtered, so anything left is a real fault the user wants to see. An
		// engine that is down otherwise looks like the bridge doing nothing at
		// all.
		log.Warn("connection failed", "error", err)
	}
}

// Relay copies bytes in both directions until both are done. It satisfies the
// Server.Handler signature and is the default.
//
// Each direction propagates its own end-of-stream as a half-close rather than a
// full close, so a client that finished sending can still read the response.
// Both directions must be allowed to drain: returning as soon as one finishes
// would truncate the other, which is how naive relays break `docker build`.
func Relay(client, engine io.ReadWriteCloser) error {
	var wg sync.WaitGroup
	errs := make([]error, 2)

	wg.Add(2)
	go func() {
		defer wg.Done()
		n, err := io.Copy(engine, client)
		trace("relay c->e done n=%d err=%v", n, err)
		errs[0] = err
		if err != nil {
			// The client died rather than half-closing: nothing is waiting for
			// a response, and the engine side will not end on its own — socat
			// does not exit when its stdin closes. Half-closing here would
			// strand the other direction forever, leaking the relay process
			// (#35), so tear the engine connection down instead.
			engine.Close()
			return
		}
		closeWrite(engine)
	}()
	go func() {
		defer wg.Done()
		n, err := io.Copy(client, engine)
		trace("relay e->c done n=%d err=%v", n, err)
		errs[1] = err
		// The engine finishing the stream is the end of the conversation, and
		// it must arrive at the client as a full close: winio named pipes have
		// no half-close, so a CloseWrite here is a silent no-op and the client
		// blocks forever waiting for an EOF that never comes. That was the
		// `docker run --rm` never-exits hang — the container finished, dockerd
		// closed the attach stream, and the CLI waited on a pipe nobody would
		// ever close. Closing both sides also unblocks the client→engine copy
		// above, so the relay unwinds instead of waiting on a dead client.
		client.Close()
		engine.Close()
	}()
	wg.Wait()

	return errors.Join(filterClosed(errs[0]), filterClosed(errs[1]))
}

// closeWrite signals end-of-stream to the peer, preferring a half-close so the
// other direction survives. Types without CloseWrite get nothing: a full Close
// here would kill the still-active reverse direction.
func closeWrite(c io.ReadWriteCloser) {
	if hc, ok := c.(halfCloser); ok {
		hc.CloseWrite()
	}
}

// ErrEngineUnreachable marks a connection that never got a single response
// from the engine — dockerd is down or its socket is dead. Unlike an ordinary
// mid-conversation hang-up, this IS worth logging (#91): otherwise a down
// engine looks exactly like the bridge doing nothing. filterClosed keeps it.
var ErrEngineUnreachable = errors.New("engine unreachable")

// filterClosed drops the errors that mean "the other end went away", which is
// the normal way a docker client ends a connection, not a fault worth logging.
func filterClosed(err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, ErrEngineUnreachable) {
		return err // a down engine is a real fault, never swallowed
	}
	if errors.Is(err, io.EOF) ||
		errors.Is(err, net.ErrClosed) ||
		errors.Is(err, io.ErrClosedPipe) {
		return nil
	}
	// Platform-specific "went away normally" errors, registered from the
	// platform files: winio's ErrFileClosed is what a named pipe returns when
	// the relay itself closes the other direction.
	for _, e := range platformClosedErrors {
		if errors.Is(err, e) {
			return nil
		}
	}
	return err
}

// platformClosedErrors is appended to by platform-specific files.
var platformClosedErrors []error
