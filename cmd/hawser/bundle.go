package main

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"

	"github.com/zcsizmadia/hawser/internal/bundle"
	"github.com/zcsizmadia/hawser/internal/lockfile"
	"github.com/zcsizmadia/hawser/internal/provision"
	"github.com/zcsizmadia/hawser/internal/release"
)

func runBundle(args []string) int {
	fs := flag.NewFlagSet("bundle", flag.ContinueOnError)
	var (
		engineVersion = fs.String("engine-version", "", "engine version to bundle (default: this build's default)")
		output        = fs.String("output", "", "bundle path (default: hawser-bundle-<version>.zip)")
		stateDir      = fs.String("state-dir", "", "override Hawser's state directory (rootfs download cache)")
	)
	fs.StringVar(output, "o", "", "shorthand for --output")
	fs.Usage = func() {
		fmt.Fprintf(os.Stderr, `usage: hawser bundle [--engine-version <v>] [-o hawser-bundle.zip]

Packs the engine rootfs and a hawser.lock into one .zip for an air-gapped
install. Run this on a connected machine; the rootfs is downloaded and
checksum-verified, then copied into the bundle. On the isolated machine:

  hawser install --offline hawser-bundle.zip

installs entirely from the file — any network access on that step is a bug.

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
		fmt.Fprintf(os.Stderr, "hawser: %v\n", &release.ErrNotPublished{Version: engine.Version})
		return exitError
	}

	log := cliLogger(false)
	opts := optsWithResolvedStateDir(provision.Options{StateDir: *stateDir})
	p := &provision.Provisioner{Logger: log}

	ctx, stop := interruptible()
	defer stop()

	// Download + verify into the normal rootfs cache, so a later real install
	// reuses it and this does not re-fetch what is already present.
	cached := filepath.Join(opts.StateDir, "rootfs", filepath.Base(engine.Rootfs.URL))
	log.Info("fetching rootfs for bundle", "version", engine.Version)
	if err := p.FetchRootfs(ctx, engine.Rootfs.URL, engine.Rootfs.SHA256, cached); err != nil {
		fmt.Fprintf(os.Stderr, "hawser: %v\n", err)
		return exitError
	}

	dest := *output
	if dest == "" {
		dest = fmt.Sprintf("hawser-bundle-%s.zip", engine.Version)
	}
	if err := bundle.Create(dest, lockfile.FromEngine(engine), cached); err != nil {
		fmt.Fprintf(os.Stderr, "hawser: %v\n", err)
		return exitError
	}

	info, _ := os.Stat(dest)
	fmt.Fprintf(os.Stderr, "wrote %s", dest)
	if info != nil {
		fmt.Fprintf(os.Stderr, " (%.1f MB, engine %s)", float64(info.Size())/(1<<20), engine.Version)
	}
	fmt.Fprintln(os.Stderr)
	fmt.Fprintf(os.Stderr, "install it on an isolated machine with:\n  hawser install --offline %s\n", dest)
	return exitOK
}
