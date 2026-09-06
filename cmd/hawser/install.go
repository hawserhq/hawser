package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"path/filepath"

	"github.com/zcsizmadia/hawser/internal/autostart"
	"github.com/zcsizmadia/hawser/internal/dockerctx"
	"github.com/zcsizmadia/hawser/internal/integrate"
	"github.com/zcsizmadia/hawser/internal/logging"
	"github.com/zcsizmadia/hawser/internal/pipeproxy"
	"github.com/zcsizmadia/hawser/internal/provision"
	"github.com/zcsizmadia/hawser/internal/release"
	"github.com/zcsizmadia/hawser/internal/wsl"
)

// cliLogger prints progress as plain lines rather than structured logfmt: this
// is a person watching an install, not a log aggregator.
func cliLogger(quiet bool) *slog.Logger {
	level := slog.LevelInfo
	if quiet {
		level = slog.LevelWarn
	}
	return slog.New(newConsoleHandler(os.Stderr, level))
}

// interruptible returns a context cancelled on Ctrl-C, so a long download stops
// promptly and leaves no partial file behind (the provisioner handles cleanup).
func interruptible() (context.Context, func()) {
	return signal.NotifyContext(context.Background(), os.Interrupt)
}

func runInstall(args []string) int {
	fs := flag.NewFlagSet("install", flag.ContinueOnError)
	var (
		engineVersion = fs.String("engine-version", "", "engine version to install (default: this build's default)")
		distro        = fs.String("distro", "", "WSL distro name (default: "+provision.DefaultDistro+")")
		dataDir       = fs.String("data-dir", "", "where the distro's VHDX lives (default: under the state dir)")
		stateDir      = fs.String("state-dir", "", "override Hawser's state directory")
		headless      = fs.Bool("headless", false, "never prompt; for unattended and CI installs")
		noAutostart   = fs.Bool("no-autostart", false, "do not register the supervisor to start at logon")
		rootfsURL     = fs.String("rootfs-url", "", "override the rootfs URL (development)")
		rootfsSHA     = fs.String("rootfs-sha256", "", "expected rootfs SHA-256; required with --rootfs-url")
		asJSON        = fs.Bool("json", false, "emit the resulting manifest as JSON")
		configPath    = fs.String("config", "", "declarative install from a hawser.yaml (see `hawser config export`)")
	)
	fs.Usage = func() {
		fmt.Fprintf(os.Stderr, `usage: hawser install [flags]

Provisions the Hawser engine distro: checks the host, downloads and verifies the
rootfs, imports it as a WSL2 distro, and starts the engine.

The rootfs is always checksum-verified. Version pinning is a contract: this
build installs exactly the components in its embedded manifest, and nothing is
fetched as "latest".

Exit codes: 0 ok, %d error, %d usage.

flags:
`, exitError, exitUsage)
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); err != nil {
		return exitUsage
	}

	log := cliLogger(*asJSON)

	if *configPath != "" {
		return runInstallFromConfig(*configPath, provision.Options{
			Distro:   *distro,
			StateDir: *stateDir,
			DataDir:  *dataDir,
			Headless: *headless,
		}, *engineVersion, *noAutostart, log)
	}

	opts := provision.Options{
		Distro:   *distro,
		StateDir: *stateDir,
		DataDir:  *dataDir,
		Headless: *headless,
	}

	// An explicit URL bypasses the manifest, so it must carry its own digest:
	// there is no code path that imports an unverified rootfs.
	switch {
	case *rootfsURL != "" && *rootfsSHA == "":
		fmt.Fprintln(os.Stderr, "hawser: --rootfs-url requires --rootfs-sha256")
		return exitUsage
	case *rootfsURL != "":
		opts.RootfsURL = *rootfsURL
		opts.RootfsSHA256 = *rootfsSHA
		opts.EngineVersion = *engineVersion
		log.Warn("using an overridden rootfs; this is a development path, not a release install",
			"url", *rootfsURL)
	default:
		m, err := release.Load()
		if err != nil {
			fmt.Fprintf(os.Stderr, "hawser: %v\n", err)
			return exitError
		}
		engine, err := m.Engine(*engineVersion)
		if err != nil {
			fmt.Fprintf(os.Stderr, "hawser: %v\n", err)
			return exitUsage
		}
		if !engine.Published() {
			fmt.Fprintf(os.Stderr, "hawser: %v\n", &release.ErrNotPublished{Version: engine.Version})
			return exitError
		}
		opts.RootfsURL = engine.Rootfs.URL
		opts.RootfsSHA256 = engine.Rootfs.SHA256
		opts.EngineVersion = engine.Version
	}

	ctx, stop := interruptible()
	defer stop()

	p := &provision.Provisioner{Logger: log}
	manifest, err := p.Install(ctx, opts)
	if err != nil {
		// A preflight failure is the user's to act on, and its remedies are
		// already formatted; anything else is reported plainly.
		var pfe *provision.PreflightError
		if errors.As(err, &pfe) {
			fmt.Fprint(os.Stderr, "hawser: cannot install yet.\n\n")
			for _, p := range pfe.Report.Problems {
				fmt.Fprintf(os.Stderr, "  %s\n    fix: %s\n\n", p.Summary, p.Remedy)
			}
			return exitError
		}
		fmt.Fprintf(os.Stderr, "hawser: install failed: %v\n", err)
		return exitError
	}

	if *asJSON {
		return emitJSON(manifest)
	}

	// The context points at whichever pipe the bridge will serve, decided the
	// same way `hawser proxy` decides it so the two cannot disagree.
	pipe, pipeReason := pipeproxy.SelectPipeName("")
	dockerHost := pipeproxy.DockerHostFor(pipe)

	// Autostart is registered by default: "install once, docker ps works
	// forever" is the headline promise, and a supervisor that only runs when
	// launched by hand does not deliver it. Refusal is honest, not fatal —
	// missing hawserw.exe (a bare go-build binary rather than a release zip)
	// downgrades to a warning with the manual alternative named.
	if !*noAutostart {
		if exe, err := os.Executable(); err == nil {
			if err := autostart.Enable(exe); err != nil {
				log.Warn("autostart not registered", "reason", err,
					"fix", "run `hawser start` after each logon, or `hawser autostart enable` from a release install")
			} else {
				log.Info("supervisor will start at logon", "disable", "hawser autostart disable")
			}
		}
	}

	// Registering the Event Log source needs elevation; cosmetic when absent
	// (entries render with a boilerplate prefix), so best-effort by design.
	if err := logging.RegisterEventSource(); err != nil {
		log.Debug("event log source not registered (needs elevation; cosmetic)", "error", err)
	}

	contextReady := false
	mgr := &dockerctx.Manager{}
	if err := mgr.Ensure(ctx, dockerHost); err != nil {
		// A missing docker CLI is not a reason to fail a working install.
		var noCLI *dockerctx.ErrNoDockerCLI
		if errors.As(err, &noCLI) {
			log.Warn("docker context not created", "reason", err)
		} else {
			log.Warn("could not wire the docker context", "error", err)
		}
	} else {
		contextReady = true
	}

	fmt.Printf(`
Engine installed and running.

  distro   %s
  engine   %s
  data     %s
  pipe     %s  (%s)

`, manifest.Distro, manifest.EngineVersion, manifest.DataDir, pipe, pipeReason)

	fmt.Printf(`Start the always-on bridge:

  hawser start

`)
	if contextReady {
		fmt.Printf(`Then use docker normally:

  docker --context %s run --rm hello-world

Or make it the default for every shell:

  docker context use %s

`, dockerctx.Name, dockerctx.Name)
	} else {
		fmt.Printf(`Then point a docker client at it:

  $env:DOCKER_HOST = "%s"
  docker run --rm hello-world

`, dockerHost)
	}
	if !*noAutostart {
		fmt.Println("From your next logon the supervisor starts automatically;")
		fmt.Println("`hawser autostart disable` turns that off.")
	}
	return exitOK
}

func runUninstall(args []string) int {
	fs := flag.NewFlagSet("uninstall", flag.ContinueOnError)
	var (
		distro   = fs.String("distro", "", "WSL distro name (default: from the install manifest)")
		stateDir = fs.String("state-dir", "", "override Hawser's state directory")
		yes      = fs.Bool("yes", false, "skip the confirmation prompt")
	)
	fs.Usage = func() {
		fmt.Fprintf(os.Stderr, `usage: hawser uninstall [--yes]

Unregisters the engine distro and removes Hawser's own state. Nothing else on
the system is touched.

This DELETES the distro, and with it every image, container and volume it
holds. Export anything you want to keep first.

Exit codes: 0 ok, %d error.

flags:
`, exitError)
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); err != nil {
		return exitUsage
	}

	opts := provision.Options{Distro: *distro, StateDir: *stateDir}
	log := cliLogger(false)
	p := &provision.Provisioner{Logger: log}

	// Say what will actually be destroyed, using the recorded manifest rather
	// than a guess, before asking.
	target := opts.Distro
	if m, err := p.ReadManifest(opts); err == nil && m.Distro != "" {
		target = m.Distro
	} else if target == "" {
		target = provision.DefaultDistro
	}

	if !*yes {
		fmt.Printf("This will unregister the WSL distro %q and delete all images, "+
			"containers and volumes in it.\nThis cannot be undone. Continue? [y/N] ", target)
		var answer string
		fmt.Scanln(&answer)
		if answer != "y" && answer != "Y" {
			fmt.Println("aborted")
			return exitOK
		}
	}

	ctx, stop := interruptible()
	defer stop()

	// Autostart goes first: a Run entry pointing at a binary that is about to
	// stop having anything to supervise would relaunch a supervisor into an
	// empty install at next logon. Ownership-checked, so uninstalling a
	// secondary install (the e2e suite beside a real one) cannot delete the
	// primary install's entry.
	if exe, err := os.Executable(); err == nil {
		if removed, err := autostart.DisableIfOwned(filepath.Dir(exe)); err != nil {
			log.Warn("could not remove the autostart entry", "error", err)
		} else if removed {
			log.Info("autostart entry removed")
		}
	}
	if err := logging.UnregisterEventSource(); err != nil {
		log.Debug("event log source not unregistered (needs elevation; cosmetic)", "error", err)
	}

	// Unwire every distro wsl-integrate touched: "nothing else on the system
	// was modified" must include profile scripts in the user's own distros.
	(&integrate.Manager{WSL: &wsl.Local{}, StateDir: opts.StateDir, Logger: log}).RemoveAll(ctx)

	// Remove the context before the distro: leaving a context pointed at a
	// pipe nobody serves would make every later docker command fail, which is
	// not "nothing else on the system was modified".
	mgr := &dockerctx.Manager{}
	if err := mgr.Remove(ctx, ""); err != nil {
		var noCLI *dockerctx.ErrNoDockerCLI
		if errors.As(err, &noCLI) {
			log.Debug("no docker CLI, nothing to unwire", "reason", err)
		} else {
			log.Warn("could not remove the docker context", "error", err)
		}
	}

	if err := p.Uninstall(ctx, opts); err != nil {
		fmt.Fprintf(os.Stderr, "hawser: %v\n", err)
		return exitError
	}
	fmt.Println("Removed. Nothing else on the system was modified.")
	return exitOK
}
