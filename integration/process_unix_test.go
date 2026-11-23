//go:build !windows

package integration

import (
	"os"
	"os/exec"
	"syscall"
	"testing"
)

// stopSignals are the signals that cancel a turn.
var stopSignals = []os.Signal{os.Interrupt, syscall.SIGTERM, syscall.SIGHUP}

// ownProcessGroup is only needed on Windows.
func ownProcessGroup(*exec.Cmd) {}

func sendSignal(t *testing.T, p *os.Process, sig os.Signal) {
	t.Helper()
	if err := p.Signal(sig); err != nil {
		t.Fatal(err)
	}
}
