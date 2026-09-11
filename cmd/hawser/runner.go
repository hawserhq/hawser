package main

import (
	"context"
	"flag"
	"fmt"
	"os"

	"github.com/hawserhq/hawser/internal/autostart"
	"github.com/hawserhq/hawser/internal/provision"
	"github.com/hawserhq/hawser/internal/runner"
	"github.com/hawserhq/hawser/internal/supervise"
)

// runRunner is `hawser runner check` (#150): one verdict on whether an
// unattended host — a CI runner, a build agent — will bring the engine back
// after a reboot. Read-only, no elevation; it configures nothing (setting up
// auto-logon writes a credential into the machine, which stays a documented
// owner step — docs/auto-logon-runner.md).
func runRunner(args []string) int {
	fs := flag.NewFlagSet("runner", flag.ContinueOnError)
	var (
		stateDir = fs.String("state-dir", "", "override Hawser's state directory")
		asJSON   = fs.Bool("json", false, "emit machine-readable JSON")
	)
	fs.Usage = func() {
		fmt.Fprintf(os.Stderr, `usage: hawser runner [--json] check

Verifies the pieces an unattended runner depends on, and names the missing one:

  auto-logon configured (Winlogon), for this account, without a clear-text
  password in the registry; the logon autostart registered; the supervisor
  running; the engine running or idle.

Nothing is changed. The auto-logon account name is compared, never printed, and
the password value is only probed for existence.

Exit codes: 0 ready (warnings allowed), %d not ready, %d usage, %d not installed.

flags:
`, exitError, exitUsage, exitNotFound)
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); err != nil {
		return exitUsage
	}
	rest := fs.Args()
	if len(rest) != 1 || rest[0] != "check" {
		fs.Usage()
		return exitUsage
	}

	opts := optsWithResolvedStateDir(provision.Options{StateDir: *stateDir})
	p := &provision.Provisioner{Logger: cliLogger(true)}
	ctx := context.Background()

	facts := runner.Facts{Engine: "not-installed"}
	if w, err := runner.ReadWinlogon(); err == nil {
		facts.AutoLogonConfigured = w.Configured()
		facts.AutoLogonUser, facts.AutoLogonDomain = w.DefaultUserName, w.DefaultDomainName
		facts.PlaintextPassword = w.HasDefaultPassword
	}
	facts.CurrentUser, facts.CurrentDomain = runner.CurrentAccount()
	facts.AutostartRegistered, _, _ = autostart.Status()
	facts.SupervisorRunning = supervise.Held(opts.StateDir)
	if distro, ok := resolveDistro(p, opts); ok {
		opts.Distro = distro
		switch {
		case p.EngineRunning(ctx, opts):
			facts.Engine = "running"
		case supervise.ReadEngineState(opts.StateDir) == supervise.EngineIdle:
			facts.Engine = "idle"
		default:
			facts.Engine = "stopped"
		}
	}

	findings := runner.Evaluate(facts)
	ready := runner.Ready(findings)
	code := exitOK
	switch {
	case ready:
	case facts.Engine == "not-installed":
		code = exitNotFound
	default:
		code = exitError
	}

	if *asJSON {
		if c := emitJSON(runnerCheckJSON{Ready: ready, Findings: findings}); c != exitOK {
			return c
		}
		return code
	}

	for _, f := range findings {
		tag := "  ok "
		switch f.Status {
		case runner.Warn:
			tag = " warn"
		case runner.Fail:
			tag = " FAIL"
		}
		fmt.Printf("[%s] %-18s %s\n", tag, f.Name, f.Summary)
		if f.Remedy != "" {
			fmt.Printf("        fix: %s\n", f.Remedy)
		}
	}
	if ready {
		fmt.Println("\nready: this machine will bring the engine back after a reboot")
	} else {
		fmt.Println("\nnot ready: fix the FAIL items above (docs/auto-logon-runner.md)")
	}
	return code
}
