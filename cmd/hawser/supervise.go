package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"time"

	"github.com/zcsizmadia/hawser/internal/config"
	"github.com/zcsizmadia/hawser/internal/dockerctx"
	"github.com/zcsizmadia/hawser/internal/logging"
	"github.com/zcsizmadia/hawser/internal/pipeproxy"
	"github.com/zcsizmadia/hawser/internal/provision"
	"github.com/zcsizmadia/hawser/internal/supervise"
)

// engineAdapter satisfies supervise.Engine with the provisioner's primitives.
type engineAdapter struct {
	p    *provision.Provisioner
	opts provision.Options
}

func (e engineAdapter) Running(ctx context.Context) bool {
	return e.p.EngineRunning(ctx, e.opts)
}
func (e engineAdapter) Start(ctx context.Context) error {
	return e.p.StartEngine(ctx, e.opts)
}
func (e engineAdapter) Stop(ctx context.Context) error {
	return e.p.StopEngine(ctx, e.opts)
}

// resolveDistro prefers the install manifest, like proxy does: it records
// which distro this machine actually has.
func resolveDistro(p *provision.Provisioner, opts provision.Options) (string, bool) {
	if m, err := p.ReadManifest(opts); err == nil && m.Distro != "" {
		return m.Distro, true
	}
	if opts.Distro != "" {
		return opts.Distro, true
	}
	return "", false
}

func runSupervise(args []string) int {
	fs := flag.NewFlagSet("supervise", flag.ContinueOnError)
	var (
		distro    = fs.String("distro", "", "WSL distro (default: from the install manifest)")
		stateDir  = fs.String("state-dir", "", "override Hawser's state directory")
		pipeName  = fs.String("pipe", "", "pipe to serve (default: "+pipeproxy.DefaultPipeName+", or Hawser's own if taken)")
		noContext = fs.Bool("no-context", false, "do not create or update the hawser docker context")
	)
	fs.Usage = func() {
		fmt.Fprintf(os.Stderr, `usage: hawser supervise [flags]

The always-on layer: serves the docker pipe AND keeps the engine alive —
crash restart with backoff, recovery from `+"`wsl --shutdown`"+` and sleep/resume,
honoring `+"`hawser stop`"+` until `+"`hawser start`"+`. One instance per install.

Runs in the foreground; `+"`hawser start`"+` spawns it in the background, and the
logon autostart (`+"`hawser autostart`"+`) runs it for you. Logs go to supervisor.log in the
state directory (rotated) as well as stderr.

flags:
`)
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); err != nil {
		return exitUsage
	}

	opts := provision.Options{Distro: *distro, StateDir: *stateDir}
	opts = optsWithResolvedStateDir(opts)

	// Single instance before anything else: two supervisors would fight over
	// the pipe and the engine.
	lock, err := supervise.Acquire(opts.StateDir)
	if err != nil {
		fmt.Fprintf(os.Stderr, "hawser: %v\n", err)
		return exitError
	}
	defer lock.Close()

	// Log to a rotating file and stderr both: the file for the months-long
	// logon session, stderr for a human running it in the foreground.
	logFile, err := logging.NewRotatingWriter(
		filepath.Join(opts.StateDir, "supervisor.log"), 0, 0)
	if err != nil {
		fmt.Fprintf(os.Stderr, "hawser: %v\n", err)
		return exitError
	}
	defer logFile.Close()
	// Warn+ mirrors to the Event Log for admins; the file keeps everything.
	log := slog.New(logging.NewEventLogHandler(
		slog.NewTextHandler(io.MultiWriter(logFile, os.Stderr), nil),
		logging.EventSource))

	p := &provision.Provisioner{Logger: log}
	targetDistro, ok := resolveDistro(p, opts)
	if !ok {
		fmt.Fprintln(os.Stderr, "hawser: no install found. Run `hawser install` first.")
		return exitNotFound
	}
	opts.Distro = targetDistro

	selected, reason := pipeproxy.SelectPipeName(*pipeName)
	listener, err := pipeproxy.Listen(selected, "")
	if err != nil {
		fmt.Fprintf(os.Stderr, "hawser: %v\n", err)
		return exitError
	}
	defer listener.Close()
	log.Info("serving pipe", "pipe", selected, "reason", reason)

	if !*noContext {
		if err := (&dockerctx.Manager{}).Ensure(context.Background(),
			pipeproxy.DockerHostFor(selected)); err != nil {
			log.Warn("docker context not wired", "reason", err)
		}
	}

	ctx, stop := interruptible()
	defer stop()

	// Server and supervisor are deliberately entangled (#41): the server's
	// traffic feeds the supervisor's idle detection, and the supervisor's
	// Demand wakes an idle-stopped engine for the server's next connection.
	dialer := engineDialer(targetDistro, "", opts.StateDir, log)
	srv := &pipeproxy.Server{
		Logger:  log,
		Handler: pipeproxy.RewriteBinds,
	}
	sup := &supervise.Supervisor{
		Engine:   engineAdapter{p: p, opts: opts},
		Config:   supervise.Config{StateDir: opts.StateDir},
		Log:      log,
		Activity: srv,
		// Read per tick, so `hawser config set idle-timeout` applies live. A
		// corrupt config file reads as "off": never idle-stop on a guess.
		IdleTimeout: func() time.Duration {
			c, err := config.Load(opts.StateDir)
			if err != nil {
				log.Warn("config unreadable; idle stops disabled", "error", err)
				return 0
			}
			return c.IdleTimeout
		},
		Busy: busyProbe(dialer),
	}
	srv.Dialer = &demandDialer{sup: sup, inner: dialer}
	go sup.Run(ctx)

	// The pipe server carries traffic; both stop together.
	if err := srv.Serve(ctx, listener); err != nil {
		fmt.Fprintf(os.Stderr, "hawser: %v\n", err)
		return exitError
	}
	log.Info("supervisor stopped")
	return exitOK
}

// optsWithResolvedStateDir freezes the default state dir into the options, so
// lock names, logs and desired-state files all agree on one path.
func optsWithResolvedStateDir(opts provision.Options) provision.Options {
	if opts.StateDir == "" {
		if base := os.Getenv("LOCALAPPDATA"); base != "" {
			opts.StateDir = filepath.Join(base, "Hawser")
		}
	}
	return opts
}

func runStart(args []string) int {
	fs := flag.NewFlagSet("start", flag.ContinueOnError)
	stateDir := fs.String("state-dir", "", "override Hawser's state directory")
	timeout := fs.Duration("timeout", 2*time.Minute, "how long to wait for the engine")
	fs.Usage = func() {
		fmt.Fprintf(os.Stderr, `usage: hawser start

Records the desired state as running, launches the supervisor when none is
running, and waits for the engine to answer.
`)
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); err != nil {
		return exitUsage
	}

	opts := optsWithResolvedStateDir(provision.Options{StateDir: *stateDir})
	if err := supervise.WriteDesired(opts.StateDir, supervise.DesiredRunning); err != nil {
		fmt.Fprintf(os.Stderr, "hawser: %v\n", err)
		return exitError
	}
	// Removing the idle marker is the wake-up poke: an idle-stopped engine is
	// down on purpose, and the supervisor will not restart it while the marker
	// stands — but `hawser start` is the user saying now.
	if err := supervise.WriteEngineState(opts.StateDir, supervise.EngineActive); err != nil {
		fmt.Fprintf(os.Stderr, "hawser: %v\n", err)
		return exitError
	}

	p := &provision.Provisioner{Logger: cliLogger(false)}
	distro, ok := resolveDistro(p, opts)
	if !ok {
		fmt.Fprintln(os.Stderr, "hawser: no install found. Run `hawser install` first.")
		return exitNotFound
	}
	// The resolved name must actually be used: polling the default distro
	// while the install lives under a custom name reports a healthy engine as
	// missing — the poll timed out while `hawser status` said running.
	opts.Distro = distro

	if !supervise.Held(opts.StateDir) {
		fmt.Fprintln(os.Stderr, "  starting the supervisor in the background")
		if err := spawnSupervisor(opts.StateDir); err != nil {
			fmt.Fprintf(os.Stderr, "hawser: launching supervisor: %v\n", err)
			return exitError
		}
	}

	deadline := time.Now().Add(*timeout)
	for time.Now().Before(deadline) {
		if p.EngineRunning(context.Background(), opts) {
			fmt.Println("engine is running")
			return exitOK
		}
		time.Sleep(time.Second)
	}
	fmt.Fprintf(os.Stderr, "hawser: engine did not come up within %s; see supervisor.log in %s\n",
		*timeout, opts.StateDir)
	return exitError
}

func runStop(args []string) int {
	fs := flag.NewFlagSet("stop", flag.ContinueOnError)
	stateDir := fs.String("state-dir", "", "override Hawser's state directory")
	timeout := fs.Duration("timeout", time.Minute, "how long to wait for the engine to stop")
	fs.Usage = func() {
		fmt.Fprintf(os.Stderr, `usage: hawser stop

Records the desired state as stopped and waits for the engine to stop. The
supervisor keeps honoring this until `+"`hawser start`"+` — a stopped engine stays
stopped. Only Hawser's own distro is touched, never other WSL distros.
`)
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); err != nil {
		return exitUsage
	}

	opts := optsWithResolvedStateDir(provision.Options{StateDir: *stateDir})
	if err := supervise.WriteDesired(opts.StateDir, supervise.DesiredStopped); err != nil {
		fmt.Fprintf(os.Stderr, "hawser: %v\n", err)
		return exitError
	}
	// An explicit stop supersedes an idle stop; the marker would make status
	// claim "idle (wakes on demand)" about an engine that must stay down.
	supervise.WriteEngineState(opts.StateDir, supervise.EngineActive)

	p := &provision.Provisioner{Logger: cliLogger(false)}
	distro, ok := resolveDistro(p, opts)
	if !ok {
		fmt.Fprintln(os.Stderr, "hawser: no install found; nothing to stop")
		return exitNotFound
	}
	opts.Distro = distro

	// With no supervisor to do it, stop the engine directly.
	if !supervise.Held(opts.StateDir) {
		if err := p.StopEngine(context.Background(), opts); err != nil {
			fmt.Fprintf(os.Stderr, "hawser: %v\n", err)
			return exitError
		}
	}

	deadline := time.Now().Add(*timeout)
	for time.Now().Before(deadline) {
		if !p.EngineRunning(context.Background(), opts) {
			fmt.Println("engine is stopped (and stays stopped until `hawser start`)")
			return exitOK
		}
		time.Sleep(time.Second)
	}
	fmt.Fprintf(os.Stderr, "hawser: engine still running after %s\n", *timeout)
	return exitError
}

func runRestart(args []string) int {
	if code := runStop(args); code != exitOK && code != exitNotFound {
		return code
	}
	return runStart(args)
}

func runStatus(args []string) int {
	fs := flag.NewFlagSet("status", flag.ContinueOnError)
	stateDir := fs.String("state-dir", "", "override Hawser's state directory")
	asJSON := fs.Bool("json", false, "emit machine-readable JSON")
	if err := fs.Parse(args); err != nil {
		return exitUsage
	}

	opts := optsWithResolvedStateDir(provision.Options{StateDir: *stateDir})
	p := &provision.Provisioner{Logger: cliLogger(true)}

	st := struct {
		Installed  bool   `json:"installed"`
		Distro     string `json:"distro,omitempty"`
		Supervisor string `json:"supervisor"`
		Engine     string `json:"engine"`
		Desired    string `json:"desired"`
	}{
		Supervisor: "stopped",
		Engine:     "stopped",
		Desired:    string(supervise.ReadDesired(opts.StateDir)),
	}

	if distro, ok := resolveDistro(p, opts); ok {
		st.Installed = true
		st.Distro = distro
		opts.Distro = distro
		if supervise.Held(opts.StateDir) {
			st.Supervisor = "running"
		}
		switch {
		case p.EngineRunning(context.Background(), opts):
			st.Engine = "running"
		case supervise.ReadEngineState(opts.StateDir) == supervise.EngineIdle:
			// Down by design (#41): the idle timeout elapsed, and the next
			// docker command wakes it. Scripts get to tell this from broken.
			st.Engine = "idle"
		}
	}

	if *asJSON {
		return emitJSON(st)
	}
	if !st.Installed {
		fmt.Println("not installed (run `hawser install`)")
		return exitNotFound
	}
	fmt.Printf("distro      %s\nsupervisor  %s\nengine      %s\ndesired     %s\n",
		st.Distro, st.Supervisor, st.Engine, st.Desired)
	// Exit code mirrors engine health, so scripts can gate on it directly.
	// Idle counts as healthy: the engine is a docker command away, on purpose.
	if st.Engine != "running" && st.Engine != "idle" {
		return exitError
	}
	return exitOK
}

// spawnSupervisor launches `hawser supervise` detached and windowless.
func spawnSupervisor(stateDir string) error {
	self, err := os.Executable()
	if err != nil {
		return err
	}
	cmd := exec.Command(self, "supervise", "--state-dir", stateDir)
	configureDetached(cmd)
	if err := cmd.Start(); err != nil {
		return err
	}
	// Released, not waited on: it must outlive this CLI invocation.
	return cmd.Process.Release()
}
