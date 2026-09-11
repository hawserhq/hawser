package main

import (
	"flag"
	"fmt"
	"os"

	"github.com/hawserhq/hawser/internal/autostart"
)

func runAutostart(args []string) int {
	fs := flag.NewFlagSet("autostart", flag.ContinueOnError)
	fs.Usage = func() {
		fmt.Fprint(os.Stderr, `usage: hawser autostart enable|disable|status

Controls whether the supervisor starts at logon, via a per-user Run entry
(visible and switchable in Task Manager's Startup tab; no admin needed). The
entry runs hawserw.exe, the windowless launcher, so nothing flashes at logon.

install registers this by default; uninstall removes it.
`)
	}
	if err := fs.Parse(args); err != nil {
		return exitUsage
	}

	switch fs.Arg(0) {
	case "enable":
		exe, err := os.Executable()
		if err != nil {
			fmt.Fprintf(os.Stderr, "hawser: %v\n", err)
			return exitError
		}
		if err := autostart.Enable(exe); err != nil {
			fmt.Fprintf(os.Stderr, "hawser: %v\n", err)
			return exitError
		}
		fmt.Println("autostart enabled: the supervisor starts at your next logon")
		return exitOK

	case "disable":
		if err := autostart.Disable(); err != nil {
			fmt.Fprintf(os.Stderr, "hawser: %v\n", err)
			return exitError
		}
		fmt.Println("autostart disabled (a running supervisor is not stopped; use `hawser stop`)")
		return exitOK

	case "status", "":
		enabled, cmd, err := autostart.Status()
		if err != nil {
			fmt.Fprintf(os.Stderr, "hawser: %v\n", err)
			return exitError
		}
		if enabled {
			fmt.Printf("enabled: %s\n", cmd)
			return exitOK
		}
		fmt.Println("disabled")
		return exitNotFound

	default:
		fs.Usage()
		return exitUsage
	}
}
