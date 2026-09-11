package main

import (
	"flag"
	"fmt"
	"os"
	"time"

	"github.com/hawserhq/hawser/internal/provision"
	"github.com/hawserhq/hawser/internal/snapshot"
	"github.com/hawserhq/hawser/internal/wsl"
)

// runReset is `hawser reset --to <snapshot>` (#142): the runner's clean slate.
// It is `snapshot restore` with the interactive guards implied — no --yes, no
// running-container check — because the caller is a job script that has
// already decided: bake base images into a golden snapshot once, reset to it
// before each job, and get a clean engine with the images already present
// instead of a pull. Docker Desktop has no engine snapshot to reset to.
func runReset(args []string) int {
	fs := flag.NewFlagSet("reset", flag.ContinueOnError)
	var (
		stateDir = fs.String("state-dir", "", "override Hawser's state directory")
		to       = fs.String("to", "", "snapshot to reset the engine to (required)")
		asJSON   = fs.Bool("json", false, "emit machine-readable JSON")
	)
	fs.Usage = func() {
		fmt.Fprintf(os.Stderr, `usage: hawser reset --to <snapshot> [--json]

Replaces the engine with a snapshot, unconditionally: every image, container and
volume not in the snapshot is lost, running containers included. This is the
non-interactive form of `+"`hawser snapshot restore --yes --force`"+`, meant for
runner job scripts:

  hawser snapshot save golden          # once: after pulling your base images
  hawser reset --to golden             # before each job: clean slate, images present

Exit codes: 0 ok, %d error, %d usage, %d no such snapshot / not installed.

flags:
`, exitError, exitUsage, exitNotFound)
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); err != nil {
		return exitUsage
	}
	if *to == "" {
		fmt.Fprintln(os.Stderr, "hawser: reset needs --to <snapshot>")
		return exitUsage
	}

	opts := optsWithResolvedStateDir(provision.Options{StateDir: *stateDir})
	log := cliLogger(*asJSON)
	p := &provision.Provisioner{Logger: log}
	m, err := p.ReadManifest(opts)
	if err != nil {
		fmt.Fprintln(os.Stderr, "hawser: no install found. Run `hawser install` first.")
		return exitNotFound
	}
	opts.Distro, opts.DataDir = m.Distro, m.DataDir

	mgr := &snapshot.Manager{
		WSL:      &wsl.Local{},
		StateDir: opts.StateDir,
		Distro:   m.Distro,
		DataDir:  m.DataDir,
		Logger:   log,
	}
	if !mgr.Exists(*to) {
		fmt.Fprintf(os.Stderr, "hawser: no such snapshot %q\n", *to)
		return exitNotFound
	}

	ctx, stop := interruptible()
	defer stop()

	start := time.Now()
	if err := restoreWithEngine(ctx, mgr, p, opts, *to); err != nil {
		fmt.Fprintf(os.Stderr, "hawser: %v\n", err)
		return exitError
	}
	elapsed := time.Since(start)

	res := resetJSON{Snapshot: *to, Millis: elapsed.Milliseconds()}
	if snaps, err := mgr.List(); err == nil {
		for _, s := range snaps {
			if s.Name == *to {
				res.EngineVersion = s.EngineVersion
			}
		}
	}
	if *asJSON {
		return emitJSON(res)
	}
	fmt.Printf("reset to snapshot %q in %s; the engine is running on that state\n",
		*to, elapsed.Round(100*time.Millisecond))
	return exitOK
}
