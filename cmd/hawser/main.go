// Command hawser runs the upstream Docker Engine on Windows via WSL2:
// a provisioner, a named-pipe bridge, and a supervisor in one binary.
package main

import (
	"fmt"
	"os"
)

// buildVersion is stamped by the release build (-ldflags "-X main.buildVersion=...").
var buildVersion = "dev"

// exit codes are part of the CLI contract: CI scripts branch on them, so they
// are assigned deliberately rather than by accident (PLAN §03).
const (
	exitOK       = 0
	exitError    = 1
	exitUsage    = 2
	exitNotFound = 3 // asked about something that is not installed
)

type command struct {
	name    string
	summary string
	run     func(args []string) int
}

func commands() []command {
	return []command{
		{"audit", "print the container-affecting API audit log (`audit tail`)", runAudit},
		{"autostart", "start the supervisor at logon: enable, disable, status", runAutostart},
		{"bundle", "pack the engine into a .zip for an air-gapped `install --offline`", runBundle},
		{"cli", "install the bundled docker CLI + compose + buildx (ditch Docker Desktop)", runCLI},
		{"compact", "shrink the engine's virtual disk: fstrim + CompactVirtualDisk", runCompact},
		{"config", "list, get, or set Hawser settings (idle-timeout)", runConfig},
		{"doctor", "diagnose the host and engine; --fix applies safe remedies", runDoctor},
		{"enable-gpu", "install the NVIDIA CDI spec so containers can use the GPU", runEnableGPU},
		{"engine", "engine list, upgrade and rollback — pinned and reversible", runEngine},
		{"healthcheck", "readiness probe: exit 0 when a docker command would succeed (--wait)", runHealthcheck},
		{"install", "provision the engine distro and start it", runInstall},
		{"lock", "write a hawser.lock pinning the exact engine (reproducible installs)", runLock},
		{"logs", "supervisor, dockerd, or audit log; --follow, --json for shippers", runLogs},
		{"migrate", "copy images and volumes from Docker Desktop into the engine", runMigrate},
		{"prewarm", "pull a pinned image list ahead of need (runner warm-up, golden images)", runPrewarm},
		{"profile", "save and switch named settings profiles (work/home)", runProfile},
		{"proxy", "serve the docker pipe in the foreground (debug mode)", runProxy},
		{"prune", "reclaim disk: stopped containers, unused images, build cache", runPrune},
		{"remote", "register and switch to a remote engine served over mutual TLS", runRemote},
		{"reset", "reset the engine to a snapshot, unconditionally (runner clean slate)", runReset},
		{"restart", "stop the engine, then start it", runRestart},
		{"runner", "runner check: verify auto-logon, autostart, supervisor and engine on an unattended host", runRunner},
		{"serve", "expose the engine over the network with mutual TLS (`serve cert`)", runServe},
		{"snapshot", "save/restore/list the engine state (images, containers, volumes)", runSnapshot},
		{"start", "ensure the supervisor and engine are running", runStart},
		{"status", "report supervisor, engine and desired state", runStatus},
		{"stop", "stop the engine; it stays stopped until start", runStop},
		{"supervise", "serve the pipe and keep the engine alive (the always-on layer)", runSupervise},
		{"uninstall", "remove the engine distro and Hawser's state", runUninstall},
		{"wsl-integrate", "point docker inside your own WSL distros at the engine", runWSLIntegrate},
		{"wsl-config", "right-size the WSL2 VM: show and apply ~/.wslconfig sizing, with consent", runWSLConfig},
		{"version", "report every component version and which docker.exe is active", runVersion},
	}
}

func usage(w *os.File) {
	fmt.Fprintf(w, `hawser %s - upstream Docker Engine on Windows via WSL2

usage: hawser <command> [flags]

commands:
`, buildVersion)
	for _, c := range commands() {
		fmt.Fprintf(w, "  %-10s %s\n", c.name, c.summary)
	}
	fmt.Fprintf(w, `
Commands still in development are tracked at
https://github.com/zcsizmadia/hawser/issues

run `+"`hawser <command> --help`"+` for a command's flags
`)
}

func main() {
	if len(os.Args) < 2 {
		usage(os.Stderr)
		os.Exit(exitUsage)
	}

	name := os.Args[1]
	switch name {
	case "-h", "--help", "help":
		usage(os.Stdout)
		os.Exit(exitOK)
	case "-v", "--version":
		// Convenience alias; `hawser version` is the real command.
		name = "version"
	}

	for _, c := range commands() {
		if c.name == name {
			os.Exit(c.run(os.Args[2:]))
		}
	}

	fmt.Fprintf(os.Stderr, "hawser: unknown command %q\n\n", name)
	usage(os.Stderr)
	os.Exit(exitUsage)
}
