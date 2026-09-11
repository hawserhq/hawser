//go:build !windows

package tray

import "os/exec"

// hideWindow is a no-op off Windows: there is no console window to suppress.
// The package is kept buildable here so its logic can be tested without a
// desktop, which is the reason it is separate from the GUI shell at all.
func hideWindow(cmd *exec.Cmd) *exec.Cmd { return cmd }
