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

	"github.com/zcsizmadia/hawser/internal/audit"
	"github.com/zcsizmadia/hawser/internal/config"
	"github.com/zcsizmadia/hawser/internal/dockerctx"
	"github.com/zcsizmadia/hawser/internal/hostca"
	"github.com/zcsizmadia/hawser/internal/logging"
	"github.com/zcsizmadia/hawser/internal/pipeproxy"
	"github.com/zcsizmadia/hawser/internal/profile"
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
	// GPU config is re-read fresh on every engine start so `hawser enable-gpu`
	// (and --off) take effect on the next start without cycling the supervisor:
	// the supervisor persists across `hawser restart`, so a value captured once
	// at its launch would go stale (#83).
	opts := e.opts
	if c, err := config.Load(opts.StateDir); err == nil {
		opts.GPUEnabled = c.GPU
	}
	return e.p.StartEngine(ctx, opts)
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

	// Corporate-network config (#62): proxy + host CA trust, applied to the
	// engine on every start. Read once here — toggling it needs `hawser restart`
	// so the change re-applies and dockerd restarts to pick it up.
	if c, err := config.Load(opts.StateDir); err == nil {
		opts.Network = provision.NetConfig{Proxy: c.Proxy, NoProxy: c.NoProxy}
		if c.ImportHostCAs {
			if pem, err := hostca.HostRootCAs(ctx); err != nil {
				log.Warn("host CA import is on but the store could not be read", "error", err)
			} else {
				opts.Network.HostCAPEM = pem
				log.Info("importing host CA certificates into the engine")
			}
		}
		// GPU CDI spec (#83), re-applied on every start like the network config.
		opts.GPUEnabled = c.GPU
	}

	// Bind-path rewriting is always on; the audit log (#121) wraps it when
	// enabled. Read once at start — toggling it needs `hawser restart`.
	handler := pipeproxy.RewriteBinds
	if c, err := config.Load(opts.StateDir); err == nil && c.Audit {
		auditPath := filepath.Join(opts.StateDir, "audit.log")
		if w, err := logging.NewRotatingWriter(auditPath, 0, 0); err != nil {
			log.Warn("audit log disabled", "error", err)
		} else {
			defer w.Close()
			handler = pipeproxy.RewriteBindsAudited(audit.New(w))
			log.Info("audit log enabled", "path", auditPath)
		}
	}

	metrics := &pipeproxy.Metrics{}
	srv := &pipeproxy.Server{
		Logger:  log,
		Handler: handler,
		Metrics: metrics,
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
		Busy: engineBusy(dialer, p, opts, log),
		// Lifecycle hooks (#70): fire off-thread and time-bounded so a user's
		// script never blocks the reconciler.
		Hook: hookRunner(opts.StateDir, log),
	}
	srv.Dialer = &demandDialer{sup: sup, inner: dialer}
	go sup.Run(ctx)

	// Statistics the CLI cannot see from outside this process (#179), flushed
	// to a timestamped file so `hawser status --stats` can report both the
	// numbers and how old they are.
	go flushStats(ctx, opts.StateDir, sup, srv, metrics, dialer, log)

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
	withStats := fs.Bool("stats", false, "add engine, disk, VM, uptime and bridge statistics (needs a running engine for the first three)")
	if err := fs.Parse(args); err != nil {
		return exitUsage
	}

	opts := optsWithResolvedStateDir(provision.Options{StateDir: *stateDir})
	p := &provision.Provisioner{Logger: cliLogger(true)}

	st := statusJSON{
		StateDir:   opts.StateDir,
		Supervisor: "stopped",
		Engine:     "stopped",
		Desired:    string(supervise.ReadDesired(opts.StateDir)),
		Profile:    (&profile.Manager{StateDir: opts.StateDir}).Active(),
	}
	if c, err := config.Load(opts.StateDir); err == nil {
		st.GPU.Enabled = c.GPU
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
			// GPU probes need the distro up (never boot it for status, #82) and
			// are only worth two wsl calls when GPU is enabled at all.
			if st.GPU.Enabled {
				st.GPU.Probed = true
				st.GPU.Visible = p.GPUAvailable(context.Background(), opts)
				st.GPU.SpecInstalled = p.GPUSpecInstalled(context.Background(), opts)
			}
		case supervise.ReadEngineState(opts.StateDir) == supervise.EngineIdle:
			// Down by design (#41): the idle timeout elapsed, and the next
			// docker command wakes it. Scripts get to tell this from broken.
			st.Engine = "idle"
		}
	}

	// Statistics are opt-in and additive: the default shape is a pinned
	// readiness-probe contract (#179), and collecting them costs WSL calls that
	// a probe should not pay.
	if *withStats && st.Installed {
		s := gatherStats(context.Background(), opts, st.Distro, st.Engine == "running")
		st.Stats = &s
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
	if st.Profile != "" {
		fmt.Printf("profile     %s\n", st.Profile)
	}
	if st.Stats != nil {
		printStats(*st.Stats)
	}
	// Exit code mirrors engine health, so scripts can gate on it directly.
	// Idle counts as healthy: the engine is a docker command away, on purpose.
	if st.Engine != "running" && st.Engine != "idle" {
		return exitError
	}
	return exitOK
}

func fileExists(path string) bool {
	st, err := os.Stat(path)
	return err == nil && !st.IsDir()
}

// spawnSupervisor launches `hawser supervise` detached and windowless.
//
// Through hawserw.exe when it is there (a release zip, not a bare go build):
// the launcher stays resident as the supervisor's watchdog, so a crash costs
// seconds of pipe downtime rather than every docker command until the next
// `hawser start` (#166). Falling back to spawning supervise directly keeps a
// single-binary checkout working, just without the watchdog.
func spawnSupervisor(stateDir string) error {
	self, err := os.Executable()
	if err != nil {
		return err
	}
	target, args := self, []string{"supervise", "--state-dir", stateDir}
	if launcher := filepath.Join(filepath.Dir(self), "hawserw.exe"); fileExists(launcher) {
		target, args = launcher, []string{"--state-dir", stateDir}
	}
	cmd := exec.Command(target, args...)
	configureDetached(cmd)
	if err := cmd.Start(); err != nil {
		return err
	}
	// Released, not waited on: it must outlive this CLI invocation.
	return cmd.Process.Release()
}

// statsFlushInterval is how often the supervisor publishes its counters. Five
// seconds keeps a reading current enough to act on while costing one small
// atomic write; supervise.Stats.Fresh() allows six times that before calling a
// reading stale, so a busy machine never flaps between the two.
const statsFlushInterval = 5 * time.Second

// flushStats publishes the supervisor's counters until the context ends, then
// writes one last reading so a clean shutdown leaves the final numbers rather
// than a reading from five seconds before the end.
func flushStats(ctx context.Context, stateDir string, sup *supervise.Supervisor,
	srv *pipeproxy.Server, m *pipeproxy.Metrics, dialer pipeproxy.Dialer, log *slog.Logger) {
	write := func() {
		snap := m.Snapshot()
		st := supervise.Stats{
			Lifecycle: sup.LifecycleSnapshot(),
			Bridge: supervise.Bridge{
				Connections:   snap.Connections,
				BytesToEngine: snap.BytesToEngine,
				BytesToClient: snap.BytesToClient,
				ActiveConns:   srv.ActiveConns(),
				Transport:     transportName(dialer),
			},
		}
		// A statistic must never be able to take the supervisor down, so a
		// failed write is logged at debug and forgotten.
		if err := supervise.WriteStats(stateDir, st); err != nil {
			log.Debug("could not write supervisor stats", "error", err)
		}
	}

	t := time.NewTicker(statsFlushInterval)
	defer t.Stop()
	write()
	for {
		select {
		case <-ctx.Done():
			write()
			return
		case <-t.C:
			write()
		}
	}
}

// transportName reports which engine transport is carrying traffic, because
// that is the difference between ~0.6 ms and ~165 ms per connection -- and the
// answer to "docker feels slow" when the engine itself is healthy (#179).
//
//	vsock     the fast path
//	fallback  the vsock agent is unreachable and socat is carrying it
//	socat     HAWSER_NO_VSOCK pinned the slow path deliberately
//
// Anything else is reported as unknown rather than guessed at: `hawser proxy`
// and the tests wire dialers directly.
func transportName(d pipeproxy.Dialer) string {
	switch t := d.(type) {
	case *pipeproxy.FallbackDialer:
		if t.Degraded() {
			return "fallback"
		}
		return "vsock"
	case *pipeproxy.WSLDialer:
		return "socat"
	}
	return "unknown"
}
