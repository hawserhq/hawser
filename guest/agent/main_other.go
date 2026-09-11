//go:build !linux

// Keeps `go build ./...` green on the Windows CI runner and dev machines; the
// agent itself only makes sense inside the distro.
package main

import (
	"fmt"
	"os"
)

func main() {
	fmt.Fprintln(os.Stderr, "skrog-agent runs inside the engine distro; build with GOOS=linux")
	os.Exit(2)
}
