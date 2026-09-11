package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"

	"github.com/wslkit/skrog/internal/compact"
	"github.com/wslkit/skrog/internal/provision"
	"github.com/wslkit/skrog/internal/supervise"
	"github.com/wslkit/skrog/internal/wsl"
)

// exitHeld is the exit code for "something is holding the disk" — its own code
// because it is the one failure a script can act on (stop the other distro and
// retry) rather than report.
const exitHeld = 11

// runCompact is `skrog compact`: give back the disk space the engine's VHDX is
// reserving but not using (#64).
func runCompact(args []string) int {
	fs := flag.NewFlagSet("compact", flag.ContinueOnError)
	var (
		stateDir = fs.String("state-dir", "", "override Skrog's state directory")
		distro   = fs.String("distro", "", "WSL distro (default: from the install manifest)")
		noTrim   = fs.Bool("no-trim", false, "skip the in-guest fstrim (only if you just trimmed)")
		dryRun   = fs.Bool("dry-run", false, "print the steps and change nothing")
		restart  = fs.Bool("restart", false, "start the engine again afterwards")
		wait     = fs.Duration("wait", compact.DefaultWait, "how long to wait for WSL to release the disk")
		asJSON   = fs.Bool("json", false, "emit machine-readable JSON")
	)
	fs.Usage = func() {
		fmt.Fprintf(os.Stderr, `usage: skrog compact [--no-trim] [--restart] [--dry-run] [--wait <dur>] [--json]

Shrinks the engine's virtual disk. A WSL2 distro's ext4.vhdx only ever grows:
delete 50 GB of images and the file on your drive stays exactly the same size.
Two things have to happen to get that space back, and this does both —

  1. fstrim inside the engine, so the guest says which blocks it freed;
  2. CompactVirtualDisk on the file, so the disk stops reserving them.

Either one alone reclaims nothing. No administrator rights are needed.

The engine is stopped for this (it cannot be compacted while attached) and
%s brings it back. WSL keeps every distro's disk open while ANY distro is
running, so if another one is up — Docker Desktop's count — this refuses and
names it rather than running `+"`wsl --shutdown`"+` and killing your containers.
Stop them yourself and re-run.

  skrog compact --dry-run       # what it would do
  skrog compact --restart       # compact, then bring the engine back

Exit codes: 0 ok, %d error, %d usage, %d not installed, %d the disk is held.

flags:
`, "`--restart`", exitError, exitUsage, exitNotFound, exitHeld)
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); err != nil {
		return exitUsage
	}
	if len(fs.Args()) != 0 {
		fs.Usage()
		return exitUsage
	}

	ctx, stop := interruptible()
	defer stop()

	opts := optsWithResolvedStateDir(provision.Options{StateDir: *stateDir, Distro: *distro})
	p := &provision.Provisioner{Logger: cliLogger(false)}
	target, ok := resolveDistro(p, opts)
	if !ok {
		fmt.Fprintln(os.Stderr, "skrog: no install found. Run `skrog install` first.")
		return exitNotFound
	}
	opts.Distro = target

	dataDir := opts.DataDir
	if m, err := p.ReadManifest(opts); err == nil && m.DataDir != "" {
		dataDir = m.DataDir
	}
	if dataDir == "" {
		dataDir = filepath.Join(opts.StateDir, "distro")
	}
	diskPath := filepath.Join(dataDir, "ext4.vhdx")
	if _, err := os.Stat(diskPath); err != nil {
		fmt.Fprintf(os.Stderr, "skrog: no virtual disk at %s: %v\n", diskPath, err)
		return exitNotFound
	}

	r := &compact.Runner{
		WSL:  wsl.NewLocal(),
		Disk: compact.RealDisk{},
		Stop: func(ctx context.Context) error {
			// Record the desired state too, so a running supervisor does not
			// restart the engine we just stopped and re-attach the disk.
			if err := supervise.WriteDesired(opts.StateDir, supervise.DesiredStopped); err != nil {
				return err
			}
			supervise.WriteEngineState(opts.StateDir, supervise.EngineActive)
			return p.StopEngine(ctx, opts)
		},
		Start: func(ctx context.Context) error {
			if err := supervise.WriteDesired(opts.StateDir, supervise.DesiredRunning); err != nil {
				return err
			}
			return p.StartEngine(ctx, opts)
		},
	}

	rep, err := r.Run(ctx, compact.Options{
		Distro:   target,
		DiskPath: diskPath,
		NoTrim:   *noTrim,
		DryRun:   *dryRun,
		Restart:  *restart,
		Wait:     *wait,
	})

	if *asJSON {
		emitJSON(compactJSON(rep))
	}

	if err != nil {
		return reportCompactError(err, rep, *asJSON, *restart)
	}
	if !*asJSON {
		printCompactReport(rep)
	}
	return exitOK
}

// reportCompactError prints the failure in the shape the user can act on and
// returns the exit code.
func reportCompactError(err error, rep compact.Report, asJSON, restart bool) int {
	var held *compact.ErrHeldByOthers
	var still *compact.ErrStillHeld
	switch {
	case errors.As(err, &held):
		if !asJSON {
			fmt.Fprintf(os.Stderr, "skrog: %s is still open in %s\n", filepath.Base(rep.Path), joinList(held.Holders))
			fmt.Fprintln(os.Stderr, "  The WSL utility VM keeps every disk open while any distro runs, so the")
			fmt.Fprintln(os.Stderr, "  disk cannot be released while those are up. Stop them and re-run;")
			fmt.Fprintln(os.Stderr, "  Skrog will not stop distros it does not own.")
		}
		return exitHeld
	case errors.As(err, &still):
		if !asJSON {
			fmt.Fprintf(os.Stderr, "skrog: %v\n", still)
			fmt.Fprintln(os.Stderr, "  Nothing else is running, so it is still winding down. Re-run, or raise")
			fmt.Fprintln(os.Stderr, "  the wait with `--wait 3m`.")
		}
		return exitHeld
	}
	if !asJSON {
		fmt.Fprintf(os.Stderr, "skrog: %v\n", err)
		if rep.Trimmed && rep.ReclaimedBytes == 0 && !restart {
			fmt.Fprintln(os.Stderr, "  The engine is stopped; `skrog start` brings it back.")
		}
	}
	return exitError
}

func printCompactReport(rep compact.Report) {
	if rep.DryRun {
		fmt.Printf("would compact %s (%s on disk):\n", rep.Distro, humanBytes(int64(rep.BeforeBytes)))
		for _, s := range rep.Steps {
			fmt.Printf("  %s\n", s)
		}
		// "It would refuse, and here is who is holding the disk" is the answer
		// a dry run owes when other distros are up.
		if len(rep.Holders) > 0 {
			fmt.Printf("\nbut it would refuse: %s %s running, and WSL keeps every disk open while\n",
				joinList(rep.Holders), plural(len(rep.Holders), "is", "are"))
			fmt.Println("any distro does. Stop them first; Skrog will not stop distros it does not own.")
		}
		return
	}
	if rep.ReclaimedBytes == 0 {
		fmt.Printf("%s: nothing to reclaim (%s on disk)\n", rep.Distro, humanBytes(int64(rep.AfterBytes)))
	} else {
		fmt.Printf("%s: %s reclaimed (%s to %s)\n", rep.Distro,
			humanBytes(int64(rep.ReclaimedBytes)), humanBytes(int64(rep.BeforeBytes)),
			humanBytes(int64(rep.AfterBytes)))
	}
	if rep.Trimmed && rep.OfferedBytes > 0 {
		// Always labeled: fstrim reports the disk's whole free extent, which on
		// a 1 TB default disk is three orders of magnitude off from reality.
		fmt.Printf("  fstrim offered %s — that is the disk's free extent, not space reclaimed\n",
			humanBytes(int64(rep.OfferedBytes)))
	}
	if !rep.Restarted {
		fmt.Println("  the engine is stopped; `skrog start` brings it back")
	}
}

// joinList renders names as "a", "a and b", "a, b and c".
func joinList(names []string) string {
	switch len(names) {
	case 0:
		return "another process"
	case 1:
		return names[0]
	case 2:
		return names[0] + " and " + names[1]
	}
	out := ""
	for i, n := range names[:len(names)-1] {
		if i > 0 {
			out += ", "
		}
		out += n
	}
	return out + " and " + names[len(names)-1]
}

// compactJSON maps the report onto the pinned JSON shape.
func compactJSON(rep compact.Report) compactJSONShape {
	return compactJSONShape{
		Distro:         rep.Distro,
		Path:           rep.Path,
		Trimmed:        rep.Trimmed,
		OfferedBytes:   rep.OfferedBytes,
		BeforeBytes:    rep.BeforeBytes,
		AfterBytes:     rep.AfterBytes,
		ReclaimedBytes: rep.ReclaimedBytes,
		WaitedSeconds:  rep.WaitedSeconds,
		Restarted:      rep.Restarted,
		DryRun:         rep.DryRun,
		Steps:          rep.Steps,
		Held:           rep.Holders,
	}
}

// plural picks the verb form for a list length, so a refusal reads as English
// whether one distro or three are holding the disk.
func plural(n int, one, many string) string {
	if n == 1 {
		return one
	}
	return many
}
