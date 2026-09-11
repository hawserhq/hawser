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

	"github.com/wslkit/skrog/internal/dockercli"
	"github.com/wslkit/skrog/internal/provision"
	"github.com/wslkit/skrog/internal/release"
	"github.com/wslkit/skrog/internal/upgrade"
)

// runUpgrade is `skrog upgrade`: one answer to "am I current?" across the
// app, the engine and the bundled docker CLI (#191).
func runUpgrade(args []string) int {
	fs := flag.NewFlagSet("upgrade", flag.ContinueOnError)
	var (
		check    = fs.Bool("check", false, "report only; change nothing")
		dryRun   = fs.Bool("dry-run", false, "print what would be applied, and apply nothing")
		yes      = fs.Bool("yes", false, "skip the confirmation prompt (for runners)")
		offline  = fs.Bool("offline", false, "skip the network check for the app version")
		asJSON   = fs.Bool("json", false, "emit machine-readable JSON (implies --check)")
		stateDir = fs.String("state-dir", "", "override Skrog's state directory")
		timeout  = fs.Duration("timeout", 15*time.Second, "how long to wait for the releases API")
	)
	fs.Usage = func() {
		fmt.Fprintf(os.Stderr, `usage: skrog upgrade [--check|--dry-run] [--yes] [--offline] [--json]

Reports whether the app, the engine and the bundled docker CLI are current,
then brings forward the two it owns — after showing you what it will do.

  skrog upgrade            everything
  skrog engine upgrade     just the engine

The app is REPORTED, never applied: a running .exe cannot cleanly replace
itself on Windows, and a signed installer is the right owner of that path.

  --check     report only; change nothing
  --dry-run   print exactly what would be applied, and apply nothing
  --yes       do not ask (runners)

Why this is not just a convenience: the engines `+"`skrog engine upgrade`"+` can
reach are pinned in THIS binary's manifest. A newer engine can therefore need
a newer skrog first — so "engine: current" is only ever true of the build you
are running, and this command says so when it matters.

Nothing here auto-updates or polls in the background. It runs when you ask,
and makes exactly one outbound request — to the releases API, for the app
version. The engine and CLI answers are local (both manifests are compiled
in), so `+"`--offline`"+` still reports those. Air-gapped installs should use it.

The CLI is applied before the engine: it is a file copy that costs no
downtime, where an engine upgrade stops and restarts the engine. A failed
engine upgrade therefore leaves the CLI already current rather than nothing
done, and `+"`skrog engine rollback`"+` reverses the engine half on its own.

Exit codes: 0 nothing to do or everything applied, %d error, %d usage,
%d something can be upgraded (--check and --dry-run only).

flags:
`, exitError, exitUsage, exitNotFound)
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); err != nil {
		return exitUsage
	}
	// --json is a report format, and there is no way to ask a question in
	// JSON — so it never applies anything, the same rule `skrog wsl-config`
	// follows.
	reportOnly := *check || *dryRun || *asJSON

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
			fmt.Fprintf(os.Stderr, "skrog: %v\n", err)
			return exitError
		}
	}

	plan := rep.Plan()
	if reportOnly {
		if *dryRun && len(plan) > 0 {
			fmt.Println("\nwould run:")
			printUpgradePlan(plan)
			fmt.Println("\nnothing was changed (--dry-run)")
		}
		// Exit 3 for "there is something to install", matching
		// `skrog cli status` so a script can gate on either the same way.
		if rep.Available() {
			return exitNotFound
		}
		return exitOK
	}

	if len(plan) == 0 {
		// The app being behind is not something this command can act on, and
		// saying "nothing to do" without that qualification would read as
		// "you are current".
		if rep.Available() {
			fmt.Println("\nnothing here is Skrog's to apply — see above")
		}
		return exitOK
	}

	if !*yes {
		fmt.Println("\nwill run:")
		printUpgradePlan(plan)
		if !confirm("\nproceed?") {
			fmt.Println("nothing was changed")
			return exitOK
		}
	}
	return applyUpgradePlan(plan, *stateDir)
}

// printUpgradePlan shows the commands, not a summary of them: the reader can
// then run any of them by hand, and can see that nothing else is happening.
func printUpgradePlan(plan []upgrade.Action) {
	for _, a := range plan {
		fmt.Printf("  skrog %-16s %s -> %s\n", joinArgs(a.Args), orUnset(a.From), a.To)
	}
}

func joinArgs(args []string) string {
	out := ""
	for i, a := range args {
		if i > 0 {
			out += " "
		}
		out += a
	}
	return out
}

func orUnset(s string) string {
	if s == "" {
		return "(none)"
	}
	return s
}

// applyUpgradePlan runs each action in order, stopping at the first failure.
//
// Stopping rather than continuing is deliberate: the actions are independent,
// so what already succeeded stands and needs no unwinding, and pressing on
// after an engine upgrade failed would stack a second change on top of a
// machine whose state nobody has looked at yet.
func applyUpgradePlan(plan []upgrade.Action, stateDir string) int {
	for _, a := range plan {
		args := append([]string(nil), a.Args[1:]...)
		if stateDir != "" {
			args = append(args, "--state-dir", stateDir)
		}
		fmt.Printf("\n== %s: %s -> %s\n", a.Stream, orUnset(a.From), a.To)

		var code int
		switch a.Args[0] {
		case "cli":
			code = runCLI(args)
		case "engine":
			code = runEngine(args)
		default:
			fmt.Fprintf(os.Stderr, "skrog: no way to apply %q\n", a.Stream)
			return exitError
		}
		if code != exitOK {
			fmt.Fprintf(os.Stderr,
				"\nskrog: %s upgrade failed (exit %d); stopping before anything else\n",
				a.Stream, code)
			if a.Stream == "engine" {
				fmt.Fprintln(os.Stderr,
					"the engine upgrade is reversible: `skrog engine rollback`")
			}
			return code
		}
	}
	fmt.Println("\nup to date")
	return exitOK
}

// newestPublishedEngine is the newest engine this build can actually install.
//
// Published, not merely listed: an entry without a checksum is a placeholder
// for a rootfs release that has not been cut, and offering it as an upgrade
// would point `skrog engine upgrade` at something it will refuse.
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
// `skrog cli status` reports a stale docker.exe as "installed", which is
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
