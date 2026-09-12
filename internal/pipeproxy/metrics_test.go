package pipeproxy

import (
	"io"
	"net"
	"sync/atomic"
	"testing"
)

// pipeConn adapts a net.Pipe end so the counting wrapper can be exercised
// without a real named pipe.
func TestCountingCountsBothDirections(t *testing.T) {
	m := &Metrics{}

	clientA, clientB := net.Pipe()
	defer clientA.Close()
	defer clientB.Close()
	counted := m.countClient(clientA)

	go func() {
		clientB.Write([]byte("request bytes")) // 13
		clientB.Close()
	}()
	if _, err := io.ReadAll(counted); err != nil {
		t.Fatalf("read: %v", err)
	}

	engineA, engineB := net.Pipe()
	defer engineA.Close()
	defer engineB.Close()
	ce := m.countEngine(engineA)
	go func() {
		engineB.Write([]byte("response")) // 8
		engineB.Close()
	}()
	if _, err := io.ReadAll(ce); err != nil {
		t.Fatalf("read: %v", err)
	}

	snap := m.Snapshot()
	if snap.BytesToEngine != 13 {
		t.Errorf("BytesToEngine = %d, want 13 (bytes read from the client)", snap.BytesToEngine)
	}
	if snap.BytesToClient != 8 {
		t.Errorf("BytesToClient = %d, want 8 (bytes read from the engine)", snap.BytesToClient)
	}
}

func TestConnectionsAreCounted(t *testing.T) {
	m := &Metrics{}
	for i := 0; i < 3; i++ {
		m.connAccepted()
	}
	if got := m.Snapshot().Connections; got != 3 {
		t.Errorf("Connections = %d, want 3", got)
	}
}

func TestNilMetricsIsSafeAndTransparent(t *testing.T) {
	// `skrog proxy` and the tests wire a Server with no Metrics; counting must
	// then cost nothing and, importantly, must not wrap the connection -- the
	// handler type-asserts on it for CloseWrite.
	var m *Metrics
	if got := m.Snapshot(); got != (Snapshot{}) {
		t.Errorf("Snapshot on nil = %+v, want zero", got)
	}
	m.connAccepted() // must not panic

	a, b := net.Pipe()
	defer a.Close()
	defer b.Close()
	if got := m.countClient(a); got != net.Conn(a) {
		t.Error("countClient wrapped the connection with metrics off")
	}
	var rwc io.ReadWriteCloser = a
	if got := m.countEngine(rwc); got != rwc {
		t.Error("countEngine wrapped the connection with metrics off")
	}
}

func TestCountedClientStaysANetConn(t *testing.T) {
	// The HTTP handler asserts for CloseWrite on the client to half-close a
	// stream; a wrapper that hid the underlying type would break `docker logs`.
	m := &Metrics{}
	a, b := net.Pipe()
	defer a.Close()
	defer b.Close()
	if _, ok := m.countClient(a).(net.Conn); !ok {
		t.Error("the counted client is not a net.Conn")
	}
}

// halfCloseConn is a net.Conn that records a half-close, so a test can tell
// "CloseWrite reached the connection" from "the assertion quietly failed".
type halfCloseConn struct {
	net.Conn
	closed *bool
}

func (c halfCloseConn) CloseWrite() error { *c.closed = true; return nil }

type halfCloseRWC struct {
	io.ReadWriteCloser
	closed *bool
}

func (c halfCloseRWC) CloseWrite() error { *c.closed = true; return nil }

// The counting wrappers must stay half-closeable.
//
// This is the check TestCountedClientStaysANetConn was written to be and was
// not: net.Conn does not declare CloseWrite, so asserting for net.Conn passes
// on a wrapper that has lost it. It did lose it — embedding an interface gives
// the wrapper that interface's method set and nothing more — and every
// `docker run -i` through the supervisor hung as a result (#237).
//
// So this asserts the behaviour rather than a type: half-close the wrapper,
// and require it to arrive at the connection underneath.
func TestCountingWrappersForwardCloseWrite(t *testing.T) {
	m := &Metrics{}

	t.Run("client", func(t *testing.T) {
		a, b := net.Pipe()
		defer a.Close()
		defer b.Close()
		var got bool
		w := m.countClient(halfCloseConn{Conn: a, closed: &got})
		hc, ok := w.(halfCloser)
		if !ok {
			t.Fatal("the counted client is not a halfCloser, so closeWrite() will skip it")
		}
		if err := hc.CloseWrite(); err != nil {
			t.Fatalf("CloseWrite: %v", err)
		}
		if !got {
			t.Error("CloseWrite did not reach the wrapped connection")
		}
	})

	t.Run("engine", func(t *testing.T) {
		a, b := net.Pipe()
		defer a.Close()
		defer b.Close()
		var got bool
		w := m.countEngine(halfCloseRWC{ReadWriteCloser: a, closed: &got})
		hc, ok := w.(halfCloser)
		if !ok {
			t.Fatal("the counted engine conn is not a halfCloser, so closeWrite() will skip it")
		}
		if err := hc.CloseWrite(); err != nil {
			t.Fatalf("CloseWrite: %v", err)
		}
		if !got {
			t.Error("CloseWrite did not reach the wrapped connection")
		}
	})

	// A connection that cannot half-close must not become an error or a panic;
	// closeWrite did nothing for it before and must still do nothing.
	t.Run("connection without CloseWrite", func(t *testing.T) {
		a, b := net.Pipe()
		defer a.Close()
		defer b.Close()
		if err := m.countClient(a).(halfCloser).CloseWrite(); err != nil {
			t.Errorf("CloseWrite on a plain conn: %v", err)
		}
	})
}

func TestCountersAreIndependent(t *testing.T) {
	m := &Metrics{}
	var n atomic.Uint64
	c := countingRWC{ReadWriteCloser: nopRWC{}, n: &n}
	buf := make([]byte, 4)
	if _, err := c.Read(buf); err != nil {
		t.Fatal(err)
	}
	if n.Load() != 4 {
		t.Errorf("counter = %d, want 4", n.Load())
	}
	if m.Snapshot().BytesToClient != 0 {
		t.Error("an unrelated counter moved")
	}
}

type nopRWC struct{}

func (nopRWC) Read(p []byte) (int, error)  { return len(p), nil }
func (nopRWC) Write(p []byte) (int, error) { return len(p), nil }
func (nopRWC) Close() error                { return nil }
