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
		{"config", "list, get, or set Hawser settings (idle-timeout)", runConfig},
		{"doctor", "diagnose the host and engine; --fix applies safe remedies", runDoctor},
		{"install", "provision the engine distro and start it", runInstall},
		{"lock", "write a hawser.lock pinning the exact engine (reproducible installs)", runLock},
		{"migrate", "copy images and volumes from Docker Desktop into the engine", runMigrate},
		{"profile", "save and switch named settings profiles (work/home)", runProfile},
		{"proxy", "serve the docker pipe in the foreground (debug mode)", runProxy},
		{"restart", "stop the engine, then start it", runRestart},
		{"start", "ensure the supervisor and engine are running", runStart},
		{"status", "report supervisor, engine and desired state", runStatus},
		{"stop", "stop the engine; it stays stopped until start", runStop},
		{"supervise", "serve the pipe and keep the engine alive (the always-on layer)", runSupervise},
		{"uninstall", "remove the engine distro and Hawser's state", runUninstall},
		{"wsl-integrate", "point docker inside your own WSL distros at the engine", runWSLIntegrate},
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
