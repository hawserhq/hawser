package pipeproxy

import (
	"io"
	"net"
	"sync/atomic"
)

// Metrics counts what the bridge carried, so `skrog status --stats` can answer
// "is docker slow because the fast path degraded?" — the one question the
// engine's own stats cannot answer, because from the engine's side nothing is
// wrong.
//
// Counters are atomic and lock-free: they are touched on every read of every
// proxied connection, and a mutex there would be a real cost on a `docker
// build` streaming a large context.
//
// A nil *Metrics is valid and disables counting, so the proxy command and tests
// need not care.
type Metrics struct {
	conns      atomic.Uint64
	fromClient atomic.Uint64
	fromEngine atomic.Uint64
}

// Snapshot is a consistent-enough read of the counters. The three values are
// read separately, so a snapshot taken mid-request can show bytes that belong
// to a connection not yet counted; over any interval that matters this is
// noise, and the alternative (locking the data path) is not worth it.
type Snapshot struct {
	// Connections is how many client connections the bridge has served since
	// the supervisor started.
	Connections uint64 `json:"connections"`
	// BytesToEngine and BytesToClient are request and response bytes.
	BytesToEngine uint64 `json:"bytesToEngine"`
	BytesToClient uint64 `json:"bytesToClient"`
}

// Snapshot reads the counters. Safe on a nil receiver.
func (m *Metrics) Snapshot() Snapshot {
	if m == nil {
		return Snapshot{}
	}
	return Snapshot{
		Connections:   m.conns.Load(),
		BytesToEngine: m.fromClient.Load(),
		BytesToClient: m.fromEngine.Load(),
	}
}

func (m *Metrics) connAccepted() {
	if m != nil {
		m.conns.Add(1)
	}
}

// countingConn wraps a client connection, counting the request bytes read from
// it. Writes are not counted here: the response bytes are counted on the engine
// side, so every byte is attributed exactly once whichever path a connection
// takes (HTTP rewrite or, after a hijack, a raw relay).
type countingConn struct {
	net.Conn
	n *atomic.Uint64
}

func (c countingConn) Read(p []byte) (int, error) {
	n, err := c.Conn.Read(p)
	if n > 0 {
		c.n.Add(uint64(n))
	}
	return n, err
}

// countingRWC is the same for the engine side, which is an io.ReadWriteCloser
// rather than a net.Conn (it may be a vsock connection or a socat pipe).
type countingRWC struct {
	io.ReadWriteCloser
	n *atomic.Uint64
}

func (c countingRWC) Read(p []byte) (int, error) {
	n, err := c.ReadWriteCloser.Read(p)
	if n > 0 {
		c.n.Add(uint64(n))
	}
	return n, err
}

// CloseWrite forwards a half-close to the wrapped connection.
//
// It has to be written out. Embedding an *interface* gives the wrapper exactly
// that interface's method set, so CloseWrite is never promoted from the
// concrete type underneath — net.Conn does not declare it, and neither does
// io.ReadWriteCloser. Without these two methods the `c.(halfCloser)` assertion
// in closeWrite silently fails whenever metrics are on, which is every
// supervisor connection: `docker run -i` then hangs forever, because the
// client's stdin EOF never reaches the engine (#237).
//
// A wrapped connection that genuinely cannot half-close does nothing, which is
// what closeWrite did for it before.
func (c countingConn) CloseWrite() error {
	if hc, ok := c.Conn.(halfCloser); ok {
		return hc.CloseWrite()
	}
	return nil
}

// CloseWrite forwards a half-close to the wrapped engine connection. See
// countingConn.CloseWrite.
func (c countingRWC) CloseWrite() error {
	if hc, ok := c.ReadWriteCloser.(halfCloser); ok {
		return hc.CloseWrite()
	}
	return nil
}

// countClient wraps a client conn for counting, or returns it unchanged when
// metrics are off. The wrapper keeps the connection's half-close and deadline
// behaviour: deadlines are promoted from the embedded net.Conn, CloseWrite is
// forwarded explicitly above.
func (m *Metrics) countClient(c net.Conn) net.Conn {
	if m == nil {
		return c
	}
	return countingConn{Conn: c, n: &m.fromClient}
}

func (m *Metrics) countEngine(e io.ReadWriteCloser) io.ReadWriteCloser {
	if m == nil {
		return e
	}
	return countingRWC{ReadWriteCloser: e, n: &m.fromEngine}
}
