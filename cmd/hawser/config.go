package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/zcsizmadia/hawser/internal/config"
	"github.com/zcsizmadia/hawser/internal/engineconfig"
	"github.com/zcsizmadia/hawser/internal/provision"
	"github.com/zcsizmadia/hawser/internal/supervise"
	"github.com/zcsizmadia/hawser/internal/wsl"
)

func runConfig(args []string) int {
	fs := flag.NewFlagSet("config", flag.ContinueOnError)
	stateDir := fs.String("state-dir", "", "override Hawser's state directory")
	fs.Usage = func() {
		fmt.Fprintf(os.Stderr, `usage: hawser config                    list all settings
       hawser config get <key>          print one value
       hawser config set <key> <value>  change one value

Hawser settings apply live: the supervisor re-reads them every few seconds.

  %s   how long the bridge must be quiet (no connections, no running
                 containers) before the engine is stopped to reclaim its RAM;
                 the next docker command starts it again. A duration like 20m
                 or 1h, or "off" (the default).

Engine settings (engine.<key>) are written into the engine's daemon.json,
validated with `+"`dockerd --validate`"+` before they replace the live file, and
applied by bouncing the engine (rolled back if it does not come back). Set an
empty value to clear a key. Lists are comma-separated; maps are k=v,k=v.

`, config.KeyIdleTimeout)
		for _, k := range engineconfig.KeyHelp() {
			fmt.Fprintf(os.Stderr, "  engine.%-24s %s\n", k.Name, k.Help)
		}
		fmt.Fprintf(os.Stderr, `
Lifecycle hooks (hook.<event>) run a script on an engine event, time-bounded
and best-effort (a failure is logged, never blocks the lifecycle). The script
gets HAWSER_EVENT and HAWSER_STATE_DIR in its environment. Set an empty value
to clear one. Events:
  %s   after the engine starts (recovery or first start)
  %s     before the engine stops on `+"`hawser stop`"+`
  %s  after the idle timeout stops the engine
  %s      after the engine cold-starts on demand
`, config.KeyHookPostStart, config.KeyHookPreStop, config.KeyHookOnIdleStop, config.KeyHookOnWake)
		fmt.Fprintf(os.Stderr, "\nExit codes: 0 ok, %d error, %d usage.\n", exitError, exitUsage)
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); err != nil {
		return exitUsage
	}

	opts := optsWithResolvedStateDir(provision.Options{StateDir: *stateDir})
	rest := fs.Args()

	switch {
	case len(rest) == 0:
		return listAllConfig(opts)

	case rest[0] == "get" && len(rest) == 2:
		return getConfig(opts, rest[1])

	case rest[0] == "set" && len(rest) == 3:
		return setConfig(opts, rest[1], rest[2])

	default:
		fmt.Fprintf(os.Stderr, "hawser: config %s: unrecognized; see `hawser config --help`\n",
			strings.Join(rest, " "))
		return exitUsage
	}
}

func listAllConfig(opts provision.Options) int {
	all, err := config.All(opts.StateDir)
	if err != nil {
		fmt.Fprintf(os.Stderr, "hawser: %v\n", err)
		return exitError
	}
	keys := make([]string, 0, len(all))
	for k := range all {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		fmt.Printf("%s = %s\n", k, all[k])
	}

	// Engine settings live in the distro; list them only when one is installed,
	// so `hawser config` still works on a machine with no engine.
	if m, ok := engineManager(opts); ok {
		eng, err := m.List(context.Background())
		if err != nil {
			fmt.Fprintf(os.Stderr, "hawser: reading engine config: %v\n", err)
			return exitError
		}
		ekeys := make([]string, 0, len(eng))
		for k := range eng {
			ekeys = append(ekeys, k)
		}
		sort.Strings(ekeys)
		for _, k := range ekeys {
			fmt.Printf("%s = %s\n", k, eng[k])
		}
	}
	return exitOK
}

func getConfig(opts provision.Options, key string) int {
	if engineconfig.IsEngineKey(key) {
		m, ok := engineManager(opts)
		if !ok {
			fmt.Fprintln(os.Stderr, "hawser: no engine installed; run `hawser install` first")
			return exitNotFound
		}
		v, err := m.Get(context.Background(), engineconfig.StripPrefix(key))
		if err != nil {
			fmt.Fprintf(os.Stderr, "hawser: %v\n", err)
			return exitError
		}
		fmt.Println(v)
		return exitOK
	}

	v, err := config.Get(opts.StateDir, key)
	if err != nil {
		fmt.Fprintf(os.Stderr, "hawser: %v\n", err)
		return exitError
	}
	fmt.Println(v)
	return exitOK
}

func setConfig(opts provision.Options, key, value string) int {
	if engineconfig.IsEngineKey(key) {
		m, ok := engineManager(opts)
		if !ok {
			fmt.Fprintln(os.Stderr, "hawser: no engine installed; run `hawser install` first")
			return exitNotFound
		}
		res, err := m.Set(context.Background(), engineconfig.StripPrefix(key), value)
		if err != nil {
			fmt.Fprintf(os.Stderr, "hawser: %v\n", err)
			return exitError
		}
		fmt.Printf("%s = %s\n", key, res.Applied)
		switch {
		case res.Restarted:
			fmt.Println("engine restarted to apply the change")
		case res.PendingRestart:
			fmt.Println("engine is not running; the change applies on the next start")
		}
		return exitOK
	}

	if err := config.Set(opts.StateDir, key, value); err != nil {
		fmt.Fprintf(os.Stderr, "hawser: %v\n", err)
		return exitError
	}
	v, _ := config.Get(opts.StateDir, key)
	fmt.Printf("%s = %s\n", key, v)
	return exitOK
}

// engineManager builds an engineconfig.Manager for the installed engine, wiring
// the running-check and restart hooks to the provisioner. Returns false when no
// engine is installed.
func engineManager(opts provision.Options) (*engineconfig.Manager, bool) {
	log := cliLogger(true)
	p := &provision.Provisioner{Logger: log}
	distro, ok := resolveDistro(p, opts)
	if !ok {
		return nil, false
	}
	opts.Distro = distro

	return &engineconfig.Manager{
		WSL:    wsl.NewLocal(),
		Distro: distro,
		EngineRunning: func(ctx context.Context) bool {
			return p.EngineRunning(ctx, opts)
		},
		// A nil return means the engine came back healthy — the signal Set needs
		// to decide whether to roll back.
		Restart: func(ctx context.Context) error {
			return bounceEngine(ctx, p, opts)
		},
	}, true
}

// bounceEngine restarts the engine so dockerd re-reads daemon.json, returning
// nil only once it answers again. When a supervisor holds the lock it is the
// sole owner of the engine's lifecycle, so the bounce goes through the
// desired-state file (stop, wait down, run, wait up) rather than a direct
// stop/start that would race the reconciler. With no supervisor, it drives the
// provisioner directly.
func bounceEngine(ctx context.Context, p *provision.Provisioner, opts provision.Options) error {
	const settle = 90 * time.Second

	if !supervise.Held(opts.StateDir) {
		if err := p.StopEngine(ctx, opts); err != nil {
			return err
		}
		return p.StartEngine(ctx, opts)
	}

	if err := supervise.WriteDesired(opts.StateDir, supervise.DesiredStopped); err != nil {
		return err
	}
	if !waitFor(ctx, settle, func() bool { return !p.EngineRunning(ctx, opts) }) {
		return fmt.Errorf("engine did not stop within %s", settle)
	}
	if err := supervise.WriteDesired(opts.StateDir, supervise.DesiredRunning); err != nil {
		return err
	}
	if !waitFor(ctx, settle, func() bool { return p.EngineRunning(ctx, opts) }) {
		return fmt.Errorf("engine did not come back within %s", settle)
	}
	return nil
}

// waitFor polls cond until it is true or the timeout elapses.
func waitFor(ctx context.Context, timeout time.Duration, cond func() bool) bool {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if cond() {
			return true
		}
		select {
		case <-ctx.Done():
			return false
		case <-time.After(1 * time.Second):
		}
	}
	return cond()
}
