package integration

import (
	"os"
	"os/exec"
	"syscall"
	"testing"

	"golang.org/x/sys/windows"
)

// stopSignals holds only Ctrl-C: rho gets SIGTERM when its console window
// is closed, which a test cannot do.
var stopSignals = []os.Signal{os.Interrupt}

// ownProcessGroup starts cmd in a process group of its own, so that a
// Ctrl-Break sent to it reaches rho and not the tests.
func ownProcessGroup(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{CreationFlags: windows.CREATE_NEW_PROCESS_GROUP}
}

// sendSignal sends Ctrl-Break to p's process group, which Go delivers to
// rho as os.Interrupt. Windows cannot send Ctrl-C to a single group.
func sendSignal(t *testing.T, p *os.Process, sig os.Signal) {
	t.Helper()
	if sig != os.Interrupt {
		t.Fatalf("cannot send %v on Windows", sig)
	}
	if err := windows.GenerateConsoleCtrlEvent(windows.CTRL_BREAK_EVENT, uint32(p.Pid)); err != nil {
		t.Fatalf("send Ctrl-Break: %v", err)
	}
}
