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
