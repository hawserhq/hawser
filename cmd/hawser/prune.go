package main

import (
	"errors"
	"flag"
	"fmt"
	"os"

	"github.com/zcsizmadia/hawser/internal/dockerctx"
	"github.com/zcsizmadia/hawser/internal/prune"
)

// runPrune is `hawser prune` (#145): reclaim disk on the engine — stopped
// containers, unused images, optionally volumes and the BuildKit cache — through
// whatever docker currently targets. Runners die of full disks; this is what a
// post-job step or a scheduled task calls.
func runPrune(args []string) int {
	fs := flag.NewFlagSet("prune", flag.ContinueOnError)
	var (
		all        = fs.Bool("all", false, "remove all unused images, not only dangling (untagged) ones")
		until      = fs.Duration("until", 0, "only remove objects older than this, e.g. 168h (0 = any age)")
		buildCache = fs.Bool("build-cache", false, "also prune the BuildKit cache")
		volumes    = fs.Bool("volumes", false, "also prune unused volumes — they hold data, so off by default")
		asJSON     = fs.Bool("json", false, "emit machine-readable JSON")
	)
	fs.Usage = func() {
		fmt.Fprintf(os.Stderr, `usage: hawser prune [--all] [--until <age>] [--build-cache] [--volumes] [--json]

Frees disk on the engine: stopped containers first (so their images become
unused), then unused images; --build-cache and --volumes widen it. A failed step
never stops the others. Acts on whatever docker currently targets — the local
engine or the remote selected with `+"`hawser remote use`"+`.

  hawser prune --until 168h               # keep anything from the last week
  hawser prune --all --build-cache        # the full sweep after a job

Exit codes: 0 ok, %d a step failed, %d usage.

flags:
`, exitError, exitUsage)
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

	if err := (&dockerctx.Manager{}).Available(ctx); err != nil {
		var noCLI *dockerctx.ErrNoDockerCLI
		if errors.As(err, &noCLI) {
			fmt.Fprintf(os.Stderr, "hawser: %v\n  (`hawser cli install` provides one)\n", err)
			return exitError
		}
	}

	res := prune.Run(ctx, prune.DockerRunner{}, prune.Options{
		All: *all, Until: *until, BuildCache: *buildCache, Volumes: *volumes,
	})

	report := pruneJSON{ReclaimedBytes: res.ReclaimedBytes, Failed: res.Failed, Steps: []pruneStepJSON{}}
	for _, st := range res.Steps {
		report.Steps = append(report.Steps, pruneStepJSON{Name: st.Name, ReclaimedBytes: st.ReclaimedBytes, Error: st.Err})
	}
	code := exitOK
	if res.Failed > 0 {
		code = exitError
	}

	if *asJSON {
		if c := emitJSON(report); c != exitOK {
			return c
		}
		return code
	}

	for _, st := range report.Steps {
		if st.Error != "" {
			fmt.Printf("  %-12s FAILED: %s\n", st.Name, st.Error)
			continue
		}
		fmt.Printf("  %-12s reclaimed %s\n", st.Name, humanBytes(st.ReclaimedBytes))
	}
	fmt.Printf("\nreclaimed %s in total\n", humanBytes(res.ReclaimedBytes))
	return code
}
