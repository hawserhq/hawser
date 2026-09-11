// Package wsl hides every wsl.exe invocation behind an interface so the rest
// of the codebase is testable anywhere; only the e2e suite needs real WSL2.
package wsl

import "context"

// WSL is the seam between skrog and the wsl.exe binary. Production code
// depends on this interface, never on exec.Command directly.
type WSL interface {
	// Status reports whether WSL2 and virtualization are available.
	Status(ctx context.Context) (Status, error)
	// Import registers a distro from a rootfs tarball (wsl --import, version 2).
	Import(ctx context.Context, distro, installDir, rootfsPath string) error
	// Export writes a distro's filesystem to a tarball (wsl --export), for
	// engine snapshots. The distro should be stopped for a consistent image;
	// wsl handles terminating it.
	Export(ctx context.Context, distro, tarPath string) error
	// Unregister removes a distro and its VHDX (wsl --unregister).
	Unregister(ctx context.Context, distro string) error
	// Terminate stops a running distro (wsl --terminate).
	Terminate(ctx context.Context, distro string) error
	// List returns the distros registered for the current user.
	List(ctx context.Context) ([]Distro, error)
	// Exec runs a command inside a distro and returns its combined output.
	// user may be empty for the distro's default user.
	Exec(ctx context.Context, distro, user string, args ...string) (string, error)
	// Start launches a long-running command inside a distro without waiting.
	// The returned stop function terminates it.
	Start(ctx context.Context, distro, user string, args ...string) (stop func(), err error)
}

// Status describes the host's WSL capability, from wsl --status and wsl --version.
type Status struct {
	// Installed is false when wsl.exe is missing or reports no installation.
	Installed bool
	// DefaultVersion is the default WSL version for new distros (1 or 2).
	DefaultVersion int
	// Version is the WSL release, e.g. "2.7.8.0". Empty on older builds where
	// `wsl --version` does not exist — which is itself a useful signal, since
	// mirrored networking and DNS tunneling need a recent WSL.
	Version string
}

// Distro is one registered WSL distribution.
type Distro struct {
	Name    string
	State   string // "Running", "Stopped", ...
	Version int    // WSL 1 or 2
	Default bool
}

// Running reports whether the distro is currently up.
func (d Distro) Running() bool { return d.State == "Running" }
