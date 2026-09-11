package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"time"

	"github.com/hawserhq/hawser/internal/dockercli"
	"github.com/hawserhq/hawser/internal/provision"
	"github.com/hawserhq/hawser/internal/release"
	"github.com/hawserhq/hawser/internal/upgrade"
)

// runUpgrade is `hawser upgrade`: one answer to "am I current?" across the
// app, the engine and the bundled docker CLI (#191).
func runUpgrade(args []string) int {
	fs := flag.NewFlagSet("upgrade", flag.ContinueOnError)
	var (
		check    = fs.Bool("check", false, "report only (this build checks only either way)")
		offline  = fs.Bool("offline", false, "skip the network check for the app version")
		asJSON   = fs.Bool("json", false, "emit machine-readable JSON")
		stateDir = fs.String("state-dir", "", "override Hawser's state directory")
		timeout  = fs.Duration("timeout", 15*time.Second, "how long to wait for the releases API")
	)
	fs.Usage = func() {
		fmt.Fprintf(os.Stderr, `usage: hawser upgrade [--check] [--offline] [--json]

Reports whether the app, the engine and the bundled docker CLI are current,
and what to run for each one that is not.

  hawser upgrade            everything
  hawser engine upgrade     just the engine

Why this is not just a convenience: the engines `+"`hawser engine upgrade`"+` can
reach are pinned in THIS binary's manifest. A newer engine can therefore need
a newer hawser first — so "engine: current" is only ever true of the build you
are running, and this command says so when it matters.

Nothing here auto-updates or polls in the background. It runs when you ask,
and makes exactly one outbound request — to the releases API, for the app
version. The engine and CLI answers are local (both manifests are compiled
in), so `+"`--offline`"+` still reports those. Air-gapped installs should use it.

This build REPORTS ONLY; it applies nothing. Run the commands it prints.

Exit codes: 0 up to date, %d error, %d usage, %d something can be upgraded.

flags:
`, exitError, exitUsage, exitNotFound)
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); err != nil {
		return exitUsage
	}
	_ = check // accepted for forward compatibility; this build only ever checks

	opts := optsWithResolvedStateDir(provision.Options{StateDir: *stateDir})

	c := &upgrade.Checker{
		App:          buildVersion,
		EngineLatest: newestPublishedEngine(),
		CLILatest:    bundledCLIVersion(),
		Installed: upgrade.Installed{
			EngineVersion: installedEngineVersion(opts),
			CLIVersion:    installedCLIVersion(opts),
		},
	}
	if !*offline {
		c.Releases = &upgrade.GitHubReleases{}
	}

	ctx, cancel := context.WithTimeout(context.Background(), *timeout)
	defer cancel()
	rep := c.Check(ctx)

	if *asJSON {
		if code := emitJSON(rep); code != exitOK {
			return code
		}
	} else {
		if err := rep.WriteText(os.Stdout); err != nil {
			fmt.Fprintf(os.Stderr, "hawser: %v\n", err)
			return exitError
		}
	}

	// Exit 3 for "there is something to install", matching `hawser cli status`
	// so a script can gate on either the same way.
	if rep.Available() {
		return exitNotFound
	}
	return exitOK
}

// newestPublishedEngine is the newest engine this build can actually install.
//
// Published, not merely listed: an entry without a checksum is a placeholder
// for a rootfs release that has not been cut, and offering it as an upgrade
// would point `hawser engine upgrade` at something it will refuse.
func newestPublishedEngine() string {
	m, err := release.Load()
	if err != nil {
		return ""
	}
	best := ""
	for _, e := range m.Engines {
		if !e.Published() {
			continue
		}
		if best == "" || upgrade.Compare(e.Version, best) > 0 {
			best = e.Version
		}
	}
	return best
}

// bundledCLIVersion is the docker CLI version this build ships, taken from the
// `docker` component itself rather than from compose or buildx.
func bundledCLIVersion() string {
	m, err := dockercli.Load()
	if err != nil {
		return ""
	}
	for _, c := range m.Components {
		if c.Role == dockercli.RoleCLI {
			return c.Version
		}
	}
	return ""
}

// installedEngineVersion reads what the install manifest recorded. Empty means
// no engine is installed, which is a different answer from "unknown".
func installedEngineVersion(opts provision.Options) string {
	p := &provision.Provisioner{}
	m, err := p.ReadManifest(opts)
	if err != nil {
		return ""
	}
	return m.EngineVersion
}

// dockerVersionLine matches `docker --version` output: "Docker version
// 29.8.0, build 1234567".
var dockerVersionLine = regexp.MustCompile(`(?i)version\s+([0-9][0-9.\-]*)`)

// installedCLIVersion asks the installed docker.exe what it is.
//
// Asking the binary rather than trusting the file's presence is the point:
// `hawser cli status` reports a stale docker.exe as "installed", which is
// true and unhelpful — the question here is whether it is the version this
// build ships, and only the binary knows that.
func installedCLIVersion(opts provision.Options) string {
	exe := filepath.Join(cliBinDir(opts.StateDir), "docker.exe")
	if _, err := os.Stat(exe); err != nil {
		return ""
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	out, err := exec.CommandContext(ctx, exe, "--version").Output()
	if err != nil {
		return ""
	}
	if m := dockerVersionLine.FindSubmatch(out); m != nil {
		return string(m[1])
	}
	return ""
}
