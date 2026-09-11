package main

import (
	"flag"
	"fmt"
	"os"

	"github.com/hawserhq/hawser/internal/lockfile"
	"github.com/hawserhq/hawser/internal/release"
)

func runLock(args []string) int {
	fs := flag.NewFlagSet("lock", flag.ContinueOnError)
	var (
		engineVersion = fs.String("engine-version", "", "engine version to lock (default: this build's default)")
		output        = fs.String("output", "", "write to this file instead of stdout")
	)
	fs.StringVar(output, "o", "", "shorthand for --output")
	fs.Usage = func() {
		fmt.Fprintf(os.Stderr, `usage: hawser lock [--engine-version <v>] [-o hawser.lock]

Writes a hawser.lock pinning the exact engine this build installs: version,
rootfs URL and SHA-256, and component versions. Check it into a repo and every
developer and CI runner reproduces the same verified engine with:

  hawser install --locked hawser.lock

With no -o, the lock is printed to stdout.

Exit codes: 0 ok, %d error, %d usage.

flags:
`, exitError, exitUsage)
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); err != nil {
		return exitUsage
	}

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
		// An unpublished engine has no rootfs SHA-256, so it cannot be locked to
		// a verifiable artifact — which is the whole point of a lock.
		fmt.Fprintf(os.Stderr, "hawser: %v\n", &release.ErrNotPublished{Version: engine.Version})
		return exitError
	}

	b, err := lockfile.FromEngine(engine).Marshal()
	if err != nil {
		fmt.Fprintf(os.Stderr, "hawser: %v\n", err)
		return exitError
	}

	if *output == "" {
		os.Stdout.Write(b)
		return exitOK
	}
	if err := os.WriteFile(*output, b, 0o644); err != nil {
		fmt.Fprintf(os.Stderr, "hawser: %v\n", err)
		return exitError
	}
	fmt.Fprintf(os.Stderr, "wrote %s (engine %s)\n", *output, engine.Version)
	return exitOK
}
