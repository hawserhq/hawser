package main

import (
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/zcsizmadia/hawser/internal/dockercli"
	"github.com/zcsizmadia/hawser/internal/provision"
)

// runCLI is `hawser cli`: install the bundled docker CLI + compose + buildx +
// credential helper so Docker Desktop can be removed entirely (#66).
func runCLI(args []string) int {
	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, "usage: hawser cli <install|status|uninstall> [flags]")
		return exitUsage
	}
	switch args[0] {
	case "install":
		return runCLIInstall(args[1:])
	case "status":
		return runCLIStatus(args[1:])
	case "uninstall":
		return runCLIUninstall(args[1:])
	default:
		fmt.Fprintf(os.Stderr, "hawser cli: unknown subcommand %q (install|status|uninstall)\n", args[0])
		return exitUsage
	}
}

// cliBinDir is where the bundled docker.exe and credential helper live: the
// directory Hawser puts on PATH.
func cliBinDir(stateDir string) string { return filepath.Join(stateDir, "bin") }

// cliPluginDir is docker's cli-plugins directory, honoring DOCKER_CONFIG.
func cliPluginDir() string {
	if d := os.Getenv("DOCKER_CONFIG"); d != "" {
		return filepath.Join(d, "cli-plugins")
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, ".docker", "cli-plugins")
}

func runCLIInstall(args []string) int {
	fs := flag.NewFlagSet("cli install", flag.ContinueOnError)
	var (
		stateDir = fs.String("state-dir", "", "override Hawser's state directory")
		noPath   = fs.Bool("no-path", false, "do not add the bundle to your user PATH")
	)
	fs.Usage = func() {
		fmt.Fprintf(os.Stderr, `usage: hawser cli install [--no-path]

Downloads and installs the upstream Docker command-line tools pinned in this
build — the docker CLI, the compose and buildx plugins, and the wincred
credential helper — so you can uninstall Docker Desktop and still run docker on
Windows. Every download is checksum-verified against the embedded manifest;
nothing is fetched as "latest".

The docker CLI and the credential helper go in the bin/ directory under the
state dir (added to your user PATH unless --no-path); compose and buildx go in
docker's cli-plugins directory so `+"`docker compose`"+` and `+"`docker buildx`"+` work.

Exit codes: 0 ok, %d error, %d usage.

flags:
`, exitError, exitUsage)
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); err != nil {
		return exitUsage
	}

	opts := optsWithResolvedStateDir(provision.Options{StateDir: *stateDir})
	binDir := cliBinDir(opts.StateDir)
	pluginDir := cliPluginDir()
	log := cliLogger(false)

	m, err := dockercli.Load()
	if err != nil {
		fmt.Fprintf(os.Stderr, "hawser: %v\n", err)
		return exitError
	}

	ctx, stop := interruptible()
	defer stop()

	res, err := dockercli.Stage(ctx, m, dockercli.Options{
		BinDir:    binDir,
		PluginDir: pluginDir,
		CacheDir:  filepath.Join(opts.StateDir, "cli", "cache"),
		Logger:    log,
	})
	if err != nil {
		fmt.Fprintf(os.Stderr, "hawser: installing the docker CLI bundle: %v\n", err)
		return exitError
	}

	fmt.Printf("\nInstalled the Docker CLI bundle (%s):\n\n", dockercli.HostArch())
	for _, t := range res.Installed {
		fmt.Printf("  %-10s %-8s  %s\n", t.Name, t.Version, t.Path)
	}
	if len(res.Skipped) > 0 {
		fmt.Printf("\nNot available for %s in this build: %s\n",
			dockercli.HostArch(), strings.Join(res.Skipped, ", "))
	}

	if !*noPath {
		added, err := dockercli.AddToUserPath(binDir)
		switch {
		case err != nil:
			fmt.Fprintf(os.Stderr, "\nhawser: could not update your PATH (%v).\n"+
				"Add this directory to PATH yourself:\n  %s\n", err, binDir)
		case added:
			fmt.Printf("\nAdded to your user PATH:\n  %s\n"+
				"Open a NEW terminal for `docker` to resolve here.\n", binDir)
		default:
			fmt.Printf("\nAlready on your user PATH:\n  %s\n", binDir)
		}
	} else {
		fmt.Printf("\nAdd this directory to your PATH to use the bundled docker:\n  %s\n", binDir)
	}

	warnIfDesktopShadows(binDir)
	fmt.Printf("\nVerify in a new terminal:  docker version   docker compose version   docker buildx version\n")
	return exitOK
}

func runCLIStatus(args []string) int {
	fs := flag.NewFlagSet("cli status", flag.ContinueOnError)
	stateDir := fs.String("state-dir", "", "override Hawser's state directory")
	if err := fs.Parse(args); err != nil {
		return exitUsage
	}
	opts := optsWithResolvedStateDir(provision.Options{StateDir: *stateDir})
	binDir := cliBinDir(opts.StateDir)
	pluginDir := cliPluginDir()

	m, err := dockercli.Load()
	if err != nil {
		fmt.Fprintf(os.Stderr, "hawser: %v\n", err)
		return exitError
	}

	fmt.Printf("Docker CLI bundle (%s):\n\n", dockercli.HostArch())
	anyMissing := false
	for _, c := range m.Components {
		dir := binDir
		if c.Role == dockercli.RolePlugin {
			dir = pluginDir
		}
		path := filepath.Join(dir, c.Target)
		state := "missing"
		if _, err := os.Stat(path); err == nil {
			state = "installed"
		} else if !c.Published(dockercli.HostArch()) {
			state = "n/a for " + dockercli.HostArch()
		} else {
			anyMissing = true
		}
		fmt.Printf("  %-10s %-8s %-16s %s\n", c.Name, c.Version, state, path)
	}

	onPath, _ := dockercli.UserPathContains(binDir)
	fmt.Printf("\n  bin on user PATH: %v  (%s)\n", onPath, binDir)
	if active, err := exec.LookPath("docker"); err == nil {
		fmt.Printf("  active docker:    %s\n", active)
	} else {
		fmt.Printf("  active docker:    none on PATH\n")
	}

	if anyMissing {
		fmt.Printf("\nRun `hawser cli install` to install the missing tools.\n")
		return exitNotFound
	}
	return exitOK
}

func runCLIUninstall(args []string) int {
	fs := flag.NewFlagSet("cli uninstall", flag.ContinueOnError)
	stateDir := fs.String("state-dir", "", "override Hawser's state directory")
	if err := fs.Parse(args); err != nil {
		return exitUsage
	}
	opts := optsWithResolvedStateDir(provision.Options{StateDir: *stateDir})
	binDir := cliBinDir(opts.StateDir)
	pluginDir := cliPluginDir()

	m, _ := dockercli.Load()

	// Remove the plugins we placed (leave any the user put there themselves).
	if m != nil && pluginDir != "" {
		for _, c := range m.Components {
			if c.Role == dockercli.RolePlugin {
				os.Remove(filepath.Join(pluginDir, c.Target))
			}
		}
	}
	// Take our bin dir off PATH, then remove it.
	if removed, err := dockercli.RemoveFromUserPath(binDir); err != nil {
		fmt.Fprintf(os.Stderr, "hawser: could not update PATH: %v\n", err)
	} else if removed {
		fmt.Printf("Removed from user PATH: %s\n", binDir)
	}
	if err := os.RemoveAll(binDir); err != nil {
		fmt.Fprintf(os.Stderr, "hawser: removing %s: %v\n", binDir, err)
		return exitError
	}
	fmt.Println("Docker CLI bundle removed.")
	return exitOK
}

// warnIfDesktopShadows notes when the docker that resolves on PATH is NOT the
// one we just installed — typically Docker Desktop still ahead on PATH. The new
// entry wins only in a fresh shell, so this is guidance, not an error.
func warnIfDesktopShadows(binDir string) {
	active, err := exec.LookPath("docker")
	if err != nil {
		return
	}
	if !strings.EqualFold(filepath.Dir(active), filepath.Clean(binDir)) {
		fmt.Printf("\nNote: `docker` currently resolves to %s\n"+
			"      (likely Docker Desktop). Hawser's docker takes over in a new terminal;\n"+
			"      once you're happy, you can uninstall Docker Desktop.\n", active)
	}
}
