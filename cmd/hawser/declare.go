package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"

	"github.com/hawserhq/hawser/internal/autostart"
	"github.com/hawserhq/hawser/internal/config"
	"github.com/hawserhq/hawser/internal/dockerctx"
	"github.com/hawserhq/hawser/internal/engineconfig"
	"github.com/hawserhq/hawser/internal/hawserfile"
	"github.com/hawserhq/hawser/internal/integrate"
	"github.com/hawserhq/hawser/internal/pipeproxy"
	"github.com/hawserhq/hawser/internal/provision"
	"github.com/hawserhq/hawser/internal/release"
	"github.com/hawserhq/hawser/internal/wsl"
)

// runInstallFromConfig provisions and converges an install from a hawser.yaml.
// It is idempotent: an existing install skips provisioning and only re-applies
// settings, so the same file drives both first install and later convergence —
// the infrastructure-as-code story (#69). Explicit flags win over file fields.
func runInstallFromConfig(configPath string, opts provision.Options, engineVersion string, noAutostart bool, log *slog.Logger) int {
	f, err := hawserfile.Load(configPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "hawser: %v\n", err)
		return exitError
	}
	if opts.Distro == "" {
		opts.Distro = f.Distro
	}
	if opts.DataDir == "" {
		opts.DataDir = f.DataDir
	}
	if engineVersion == "" {
		engineVersion = f.EngineVersion
	}
	opts = optsWithResolvedStateDir(opts)

	ctx, stop := interruptible()
	defer stop()

	p := &provision.Provisioner{Logger: log}
	if _, err := p.ReadManifest(opts); err == nil {
		log.Info("engine already installed; converging settings from config")
	} else {
		rel, err := release.Load()
		if err != nil {
			fmt.Fprintf(os.Stderr, "hawser: %v\n", err)
			return exitError
		}
		engine, err := rel.Engine(engineVersion)
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
		if _, err := p.Install(ctx, opts); err != nil {
			var pfe *provision.PreflightError
			if errors.As(err, &pfe) {
				fmt.Fprint(os.Stderr, "hawser: cannot install yet.\n\n")
				for _, pr := range pfe.Report.Problems {
					fmt.Fprintf(os.Stderr, "  %s\n    fix: %s\n\n", pr.Summary, pr.Remedy)
				}
				return exitError
			}
			fmt.Fprintf(os.Stderr, "hawser: install failed: %v\n", err)
			return exitError
		}
		log.Info("engine installed", "distro", opts.Distro, "engine", opts.EngineVersion)
	}

	if err := applyHawserFile(ctx, f, opts, noAutostart, log); err != nil {
		fmt.Fprintf(os.Stderr, "hawser: applying config: %v\n", err)
		return exitError
	}

	// Wire the docker context so `docker` targets the engine; best-effort.
	pipe, _ := pipeproxy.SelectPipeName("")
	if err := (&dockerctx.Manager{}).Ensure(ctx, pipeproxy.DockerHostFor(pipe)); err != nil {
		log.Warn("docker context not wired", "reason", err)
	}

	fmt.Printf("Install converged from %s. Run `hawser start` to bring up the bridge.\n", configPath)
	return exitOK
}

// applyHawserFile converges the install to match a hawser.yaml: idle timeout,
// lifecycle hooks, engine daemon.json keys, WSL integrations and autostart.
// Each step is idempotent, so re-running against an existing install is safe.
// noAutostart forces autostart off regardless of the file (the --no-autostart
// flag always wins).
func applyHawserFile(ctx context.Context, f hawserfile.File, opts provision.Options, noAutostart bool, log *slog.Logger) error {
	sd := opts.StateDir

	if f.IdleTimeout != "" {
		if err := config.Set(sd, config.KeyIdleTimeout, f.IdleTimeout); err != nil {
			return fmt.Errorf("idle-timeout: %w", err)
		}
		log.Info("applied setting", "idle-timeout", f.IdleTimeout)
	}

	for event, path := range f.Hooks {
		if err := config.Set(sd, "hook."+event, path); err != nil {
			return fmt.Errorf("hook %s: %w", event, err)
		}
		log.Info("applied hook", "event", event, "path", path)
	}

	if len(f.Engine) > 0 {
		m, ok := engineManager(opts)
		if !ok {
			return fmt.Errorf("cannot apply engine settings: no engine installed")
		}
		if _, err := m.SetMany(ctx, f.Engine); err != nil {
			return fmt.Errorf("engine settings: %w", err)
		}
		log.Info("applied engine settings", "keys", len(f.Engine))
	}

	if len(f.Integrations) > 0 {
		p := &provision.Provisioner{Logger: log}
		distro, ok := resolveDistro(p, opts)
		if !ok {
			return fmt.Errorf("cannot wire integrations: no engine installed")
		}
		m := &integrate.Manager{WSL: &wsl.Local{}, StateDir: sd, Logger: log}
		for _, d := range f.Integrations {
			if err := m.Integrate(ctx, d, distro, provision.SharedSocketPath(distro)); err != nil {
				return fmt.Errorf("integrate %s: %w", d, err)
			}
			log.Info("wired integration", "distro", d)
		}
	}

	// Autostart: enabled by default (matching plain install), overridden by the
	// file's explicit value, and forced off by --no-autostart which always wins.
	want := !noAutostart
	if f.Autostart != nil {
		want = *f.Autostart && !noAutostart
	}
	if err := setAutostart(want, log); err != nil {
		return err
	}
	return nil
}

func setAutostart(want bool, log *slog.Logger) error {
	if want {
		exe, err := os.Executable()
		if err != nil {
			return err
		}
		if err := autostart.Enable(exe); err != nil {
			log.Warn("autostart not registered", "reason", err)
			return nil // a bare go-build binary can't self-register; not fatal
		}
		log.Info("autostart enabled")
		return nil
	}
	if err := autostart.Disable(); err != nil {
		return err
	}
	log.Info("autostart disabled")
	return nil
}

// exportHawserFile builds a hawser.yaml from the current install state, so an
// existing setup round-trips into a file that reproduces it.
func exportHawserFile(opts provision.Options) (hawserfile.File, error) {
	p := &provision.Provisioner{Logger: cliLogger(true)}
	m, err := p.ReadManifest(opts)
	if err != nil {
		return hawserfile.File{}, fmt.Errorf("no engine installed to export (run `hawser install` first): %w", err)
	}

	f := hawserfile.File{
		Distro:        m.Distro,
		DataDir:       m.DataDir,
		EngineVersion: m.EngineVersion,
	}

	if v, err := config.Get(opts.StateDir, config.KeyIdleTimeout); err == nil && v != "" && v != "off" {
		f.IdleTimeout = v
	}

	hooks := map[string]string{}
	for _, k := range config.HookKeys() {
		if v, err := config.Get(opts.StateDir, k); err == nil && v != "" {
			hooks[trimHookPrefix(k)] = v
		}
	}
	if len(hooks) > 0 {
		f.Hooks = hooks
	}

	// Engine daemon.json keys, only the ones actually set.
	if em, ok := engineManager(opts); ok {
		if eng, err := em.List(context.Background()); err == nil {
			set := map[string]string{}
			for full, v := range eng {
				if v != "" {
					set[engineconfig.StripPrefix(full)] = v
				}
			}
			if len(set) > 0 {
				f.Engine = set
			}
		}
	}

	// Integrations.
	im := &integrate.Manager{WSL: &wsl.Local{}, StateDir: opts.StateDir, Logger: cliLogger(true)}
	if wired, err := im.List(); err == nil && len(wired) > 0 {
		f.Integrations = wired
	}

	// Autostart status.
	if on, _, err := autostart.Status(); err == nil {
		f.Autostart = &on
	}

	return f, nil
}

func trimHookPrefix(key string) string {
	const p = "hook."
	if len(key) > len(p) && key[:len(p)] == p {
		return key[len(p):]
	}
	return key
}
