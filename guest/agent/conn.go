//go:build linux

package main

import (
	"os"
	"time"

	"golang.org/x/sys/unix"
)

// vsockConn wraps an accepted AF_VSOCK fd as an io.ReadWriteCloser with
// CloseWrite, which vsockproto.Relay uses to propagate EOFs. The fd is put in
// non-blocking mode before os.NewFile so reads park in Go's poller instead of
// pinning an OS thread each.
type vsockConn struct {
	f *os.File
}

func newVsockConn(fd int) (*vsockConn, error) {
	if err := unix.SetNonblock(fd, true); err != nil {
		return nil, err
	}
	return &vsockConn{f: os.NewFile(uintptr(fd), "vsock")}, nil
}

func (c *vsockConn) Read(p []byte) (int, error)  { return c.f.Read(p) }
func (c *vsockConn) Write(p []byte) (int, error) { return c.f.Write(p) }
func (c *vsockConn) Close() error                { return c.f.Close() }

// SetReadDeadline bounds a blocking read; used to time-box the handshake so a
// peer that connects and never speaks cannot pin a goroutine and fd forever.
func (c *vsockConn) SetReadDeadline(t time.Time) error { return c.f.SetReadDeadline(t) }

// CloseWrite sends EOF to the peer while reads keep working.
//
// The shutdown goes through the os.File's SyscallConn rather than a stored raw
// fd int (#84): Control holds a reference that pins the fd for the duration of
// the call, so a concurrent Close in the other relay direction cannot free the
// number and let this SHUT_WR land on an unrelated, freshly accepted
// connection. After Close the file is drained and Control is a no-op.
func (c *vsockConn) CloseWrite() error {
	rc, err := c.f.SyscallConn()
	if err != nil {
		return err
	}
	var serr error
	if err := rc.Control(func(fd uintptr) {
		serr = unix.Shutdown(int(fd), unix.SHUT_WR)
	}); err != nil {
		return err
	}
	return serr
}
