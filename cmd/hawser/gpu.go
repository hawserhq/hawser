package main

import (
	"flag"
	"fmt"
	"os"

	"github.com/zcsizmadia/hawser/internal/config"
	"github.com/zcsizmadia/hawser/internal/gpu"
	"github.com/zcsizmadia/hawser/internal/provision"
)

// runEnableGPU is `hawser enable-gpu`: install the NVIDIA CDI spec so containers
// can use the GPU (#83). WSL2 already projects the driver into the distro; this
// tells the engine to inject it into containers.
func runEnableGPU(args []string) int {
	fs := flag.NewFlagSet("enable-gpu", flag.ContinueOnError)
	var (
		stateDir = fs.String("state-dir", "", "override Hawser's state directory")
		distro   = fs.String("distro", "", "WSL distro (default: from the install manifest)")
		off      = fs.Bool("off", false, "disable GPU access (remove the CDI spec)")
	)
	fs.Usage = func() {
		fmt.Fprintf(os.Stderr, `usage: hawser enable-gpu [--off]

Enables NVIDIA GPU access for containers, then a container reaches the GPU with:

  docker run --rm --device nvidia.com/gpu=all nvidia/cuda:12.4.1-base-ubuntu22.04 nvidia-smi

WSL2 already projects the Windows NVIDIA driver into the engine distro
(/usr/lib/wsl/lib, /dev/dxg); this installs a Container Device Interface (CDI)
spec so dockerd injects that into containers. No toolkit is installed in the
distro, and it works with the musl-based engine because the spec is hookless
(it sets LD_LIBRARY_PATH rather than running an ldconfig hook).

The setting persists: the spec is re-installed on every engine start, so a
reinstall keeps GPU access. --off removes it.

Requires an NVIDIA GPU with a WSL-capable driver. AMD/Intel GPUs expose compute
to WSL differently and are not wired up here.

Exit codes: 0 ok, %d error, %d usage, %d not installed / no GPU.

flags:
`, exitError, exitUsage, exitNotFound)
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); err != nil {
		return exitUsage
	}

	opts := optsWithResolvedStateDir(provision.Options{StateDir: *stateDir, Distro: *distro})
	log := cliLogger(false)
	p := &provision.Provisioner{Logger: log}

	targetDistro, ok := resolveDistro(p, opts)
	if !ok {
		fmt.Fprintln(os.Stderr, "hawser: no install found. Run `hawser install` first.")
		return exitNotFound
	}
	opts.Distro = targetDistro

	ctx, stop := interruptible()
	defer stop()

	if *off {
		if err := config.Set(opts.StateDir, config.KeyGPU, "off"); err != nil {
			fmt.Fprintf(os.Stderr, "hawser: %v\n", err)
			return exitError
		}
		// dockerd reads CDI specs dynamically, so removing the file takes effect
		// on the next `docker run` — no restart needed. The config flag keeps a
		// fresh engine start (a reinstall) from re-adding it.
		if err := p.ConfigureGPU(ctx, opts, false); err != nil {
			log.Warn("could not remove the CDI spec", "error", err)
		}
		fmt.Println("GPU access disabled.")
		return exitOK
	}

	// The distro must actually see the GPU, or the CDI spec would inject devices
	// that are not there. This is the honest gate for "is there an NVIDIA GPU
	// with a WSL driver".
	if !p.GPUAvailable(ctx, opts) {
		fmt.Fprintf(os.Stderr, `hawser: no NVIDIA GPU is visible to WSL in this distro.

Checked for %s and %s inside %q and did not find them. That means one of:
  - this machine has no NVIDIA GPU (AMD/Intel GPUs are not wired up here);
  - the Windows NVIDIA driver is too old for WSL GPU support — update it;
  - WSL itself is out of date — run `+"`wsl --update`"+`.
`, gpu.DxgDevice, gpu.ProbeLib, opts.Distro)
		return exitNotFound
	}

	if err := config.Set(opts.StateDir, config.KeyGPU, "on"); err != nil {
		fmt.Fprintf(os.Stderr, "hawser: %v\n", err)
		return exitError
	}
	// Write the spec into the running distro; dockerd picks up CDI specs
	// dynamically, so the GPU is usable on the next `docker run` with no restart.
	// The config flag makes a fresh engine start (after a reinstall) re-apply it.
	if err := p.ConfigureGPU(ctx, opts, true); err != nil {
		fmt.Fprintf(os.Stderr, "hawser: installing the CDI spec: %v\n", err)
		return exitError
	}

	fmt.Printf(`GPU access enabled. Run a container against it:

  docker run --rm --device nvidia.com/gpu=all nvidia/cuda:12.4.1-base-ubuntu22.04 nvidia-smi

(Compose: add a device with driver "cdi" and id "nvidia.com/gpu=all".)

Prefer glibc CUDA base images (nvidia/cuda, ubuntu); the driver libraries are
injected via LD_LIBRARY_PATH, which musl images honor too, but nvidia/cuda is
the best-tested path.
`)
	return exitOK
}
