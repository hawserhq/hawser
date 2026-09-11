package pipeproxy

import (
	"context"
	"fmt"
	"io"
	"os/exec"
	"strconv"
	"sync"
	"time"
)

// WSLDialer reaches the engine socket by running socat inside the distro and
// relaying its stdio — the v0.1 transport Spike A validated.
//
// One process per connection is the known cost: ~165 ms of wsl.exe startup,
// measured in #2. It is accepted deliberately because it needs no guest agent,
// no vsock plumbing, and no open port; v0.2 replaces it with a persistent agent
// once the correctness this buys is locked in by tests.
type WSLDialer struct {
	// Distro is the WSL distribution name, e.g. "skrog-engine".
	Distro string

	// SocketPath is the engine socket inside the distro.
	// Defaults to DefaultSocketPath.
	SocketPath string

	// Exe overrides the wsl.exe path. Empty uses PATH.
	Exe string

	// IdleTimeout bounds how long socat will sit on a half-closed connection
	// with no traffic. Zero uses DefaultIdleTimeout; negative disables it.
	//
	// This exists because socat does not exit when its stdin reaches EOF: it
	// half-closes and waits on the other direction indefinitely. When a docker
	// client is killed mid-request, that leaves socat — and the wsl.exe holding
	// it — alive forever, and enough of them make the bridge stop responding
	// (#35). The timeout is the backstop; the relay closes the engine promptly
	// in the common case.
	IdleTimeout time.Duration
}

// DefaultIdleTimeout is generous enough for a slow `docker build` upload to
// pause without being torn down, and short enough that a leaked relay is
// measured in minutes rather than for the life of the session.
const DefaultIdleTimeout = 5 * time.Minute

// DefaultSocketPath is where dockerd listens inside the Skrog distro.
const DefaultSocketPath = "/var/run/docker.sock"

func (d *WSLDialer) idleTimeout() time.Duration {
	switch {
	case d.IdleTimeout < 0:
		return 0
	case d.IdleTimeout == 0:
		return DefaultIdleTimeout
	default:
		return d.IdleTimeout
	}
}

// relayArgs builds the wsl.exe argument list for one relay.
//
// --exec skips the shell, so nothing re-interprets the socket path and there is
// no shell process between us and socat.
func (d *WSLDialer) relayArgs(socket string) []string {
	args := []string{"-d", d.Distro, "-u", "root", "--exec", "socat"}
	if t := d.idleTimeout(); t > 0 {
		// -T is socat's TOTAL inactivity timeout: socat exits when no data has
		// moved in EITHER direction for the interval, not only in the
		// half-closed state (#91 corrected the earlier claim). The intended
		// target is a stranded half-open relay from a vanished client, but the
		// same timer also cuts a legitimately quiet live stream — `docker
		// events` or `logs -f` on an idle container — after the interval. That
		// trade-off only exists on this socat FALLBACK path; the vsock agent,
		// which owns both ends, has no such timer. Kept generous
		// (DefaultIdleTimeout) so the common case is unaffected, and the vsock
		// path is what a normal install uses.
		args = append(args, "-T", strconv.Itoa(int(t.Seconds())))
	}
	return append(args, "STDIO", "UNIX-CONNECT:"+socket)
}

// Dial starts the relay process and returns its stdio as a connection.
func (d *WSLDialer) Dial(ctx context.Context) (io.ReadWriteCloser, error) {
	if d.Distro == "" {
		return nil, fmt.Errorf("pipeproxy: WSLDialer.Distro is required")
	}
	socket := d.SocketPath
	if socket == "" {
		socket = DefaultSocketPath
	}
	exe := d.Exe
	if exe == "" {
		exe = "wsl.exe"
	}

	cmd := exec.CommandContext(ctx, exe, d.relayArgs(socket)...)

	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, fmt.Errorf("pipeproxy: stdin pipe: %w", err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		stdin.Close()
		return nil, fmt.Errorf("pipeproxy: stdout pipe: %w", err)
	}

	if err := cmd.Start(); err != nil {
		stdin.Close()
		stdout.Close()
		return nil, fmt.Errorf("pipeproxy: start %s: %w", exe, err)
	}

	return &procConn{cmd: cmd, in: stdin, out: stdout}, nil
}

// procConn presents a child process's stdio as a connection. Closing stdin is a
// genuine half-close — socat sees EOF and shuts down its write side to the
// engine socket, which is what makes request-body-then-read work.
type procConn struct {
	cmd *exec.Cmd
	in  io.WriteCloser
	out io.ReadCloser
	// Close can now arrive from two goroutines at once — the relay closes the
	// engine when the client dies, and handle closes it again on the way out —
	// so teardown has to be exactly-once rather than merely idempotent-ish.
	once sync.Once
}

func (p *procConn) Read(b []byte) (int, error)  { return p.out.Read(b) }
func (p *procConn) Write(b []byte) (int, error) { return p.in.Write(b) }

// CloseWrite propagates end-of-request without ending the response.
func (p *procConn) CloseWrite() error { return p.in.Close() }

// Close tears down the process and reaps it, so a busy client cannot leave a
// trail of zombie wsl.exe processes behind.
func (p *procConn) Close() error {
	p.once.Do(func() {
		p.in.Close()
		p.out.Close()
		// Only ever this process's own child, by handle. Never by image name:
		// Docker Desktop, Rancher and the user's own distros all run wsl.exe,
		// and killing theirs leaves mounts behind that outlive the process
		// (see the coexistence note on #35).
		if p.cmd.Process != nil {
			p.cmd.Process.Kill()
		}
		p.cmd.Wait()
	})
	return nil
}
