//go:build !windows

// The tray is Windows-only (Shell_NotifyIcon); this stub keeps `go build
// ./...` working on other platforms.
package main

import (
	"fmt"
	"os"
)

func main() {
	fmt.Fprintln(os.Stderr, "skrogtray is Windows-only")
	os.Exit(1)
}
