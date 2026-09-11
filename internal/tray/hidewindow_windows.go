//go:build windows

package tray

import (
	"os/exec"
	"syscall"

	"golang.org/x/sys/windows"
)

// hideWindow keeps a console window from appearing when the tray shells out.
//
// hawsertray is built for the GUI subsystem, so it has no console of its own.
// Every console child it spawns therefore gets a brand new one — which Windows
// shows. On the status poll that is a console window blinking on the user's
// desktop every few seconds, forever (#195).
//
// cmd/hawserw already does exactly this for the supervisor it launches, which
// is why the supervisor's own wsl.exe calls — several a second — never flash:
// the no-console state is inherited. The tray simply never applied it.
func hideWindow(cmd *exec.Cmd) *exec.Cmd {
	cmd.SysProcAttr = &syscall.SysProcAttr{
		HideWindow:    true,
		CreationFlags: windows.CREATE_NO_WINDOW,
	}
	return cmd
}
