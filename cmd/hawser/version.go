package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"

	"github.com/hawserhq/hawser/internal/provision"
	"github.com/hawserhq/hawser/internal/version"
	"github.com/hawserhq/hawser/internal/wsl"
)

func runVersion(args []string) int {
	fs := flag.NewFlagSet("version", flag.ContinueOnError)
	asJSON := fs.Bool("json", false, "emit machine-readable JSON")
	stateDir := fs.String("state-dir", "", "override Hawser's state directory")
	fs.Usage = func() {
		fmt.Fprintf(os.Stderr, `usage: hawser version [--json]

Reports every component version, which docker.exe actually runs, the active
docker context, and the negotiated engine API version.

Exit codes: 0 ok, %d error, %d no engine installed.

flags:
`, exitError, exitNotFound)
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); err != nil {
		return exitUsage
	}

	opts := provision.Options{StateDir: *stateDir}
	exe, err := os.Executable()
	if err != nil {
		exe = ""
	}

	c := &version.Collector{
		App: buildVersion,
		Env: version.Env{
			// Hawser's bundled CLI sits beside the binary, which is how its
			// own docker.exe is recognized on PATH.
			HawserBin: filepath.Dir(exe),
		},
		WSL:         wsl.NewLocal(),
		Provisioner: &provision.Provisioner{},
		Options:     opts,
	}

	report := c.Collect(context.Background())

	if *asJSON {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		if err := enc.Encode(report); err != nil {
			fmt.Fprintf(os.Stderr, "hawser: %v\n", err)
			return exitError
		}
	} else if err := report.WriteText(os.Stdout); err != nil {
		fmt.Fprintf(os.Stderr, "hawser: %v\n", err)
		return exitError
	}

	// A distinct code so `hawser version` doubles as an install check in a
	// provisioning script, without needing to parse the output.
	if !report.Engine.Installed {
		return exitNotFound
	}
	return exitOK
}
