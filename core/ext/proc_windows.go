//go:build windows

package ext

import (
	"os/exec"
	"syscall"

	"golang.org/x/sys/windows"
)

// hideWindow starts an extension without a console window (otherwise one
// pops up when rho-gui, which has no console, starts it).
func hideWindow(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{CreationFlags: windows.CREATE_NO_WINDOW}
}
