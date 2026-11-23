//go:build !windows

package integration

import (
	"os"
	"testing"
)

// interrupt sends Ctrl-C (SIGINT) to p.
func interrupt(t *testing.T, p *os.Process) {
	t.Helper()
	if err := p.Signal(os.Interrupt); err != nil {
		t.Fatal(err)
	}
}
