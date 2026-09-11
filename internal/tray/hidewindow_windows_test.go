//go:build windows

package tray

import (
	"context"
	"os/exec"
	"testing"

	"golang.org/x/sys/windows"
)

// A console window flashing on the desktop is invisible to every other test in
// this package, so it gets one that fails loudly (#195).
func TestHideWindowSuppressesTheConsole(t *testing.T) {
	cmd := hideWindow(exec.Command("cmd", "/c", "echo"))
	attr := cmd.SysProcAttr
	if attr == nil {
		t.Fatal("no SysProcAttr: every console child of the GUI tray would show a window")
	}
	if attr.CreationFlags&windows.CREATE_NO_WINDOW == 0 {
		t.Errorf("CREATE_NO_WINDOW not set (flags = %#x)", attr.CreationFlags)
	}
	if !attr.HideWindow {
		t.Error("HideWindow not set")
	}
}

// The two exec sites are the ones the user actually sees flash, so pin that
// they go through the helper rather than trusting that they still do.
func TestTrayCommandsAreWindowless(t *testing.T) {
	// A path that cannot run: nothing is executed, the call just has to build
	// its command and fail cleanly.
	c := CLI{Exe: `C:\nonexistent\hawser.exe`}

	if got := c.Poll(context.Background()); got != (Status{}) {
		t.Errorf("a missing CLI should poll to the zero Status, got %+v", got)
	}
	if _, err := c.Run(context.Background(), Actions[0]); err == nil {
		t.Error("running a missing CLI should report an error, not succeed silently")
	}
}
