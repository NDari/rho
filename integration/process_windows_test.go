package integration

import (
	"os"
	"testing"
)

// interrupt would send Ctrl-C to p, but Windows can only send it to every
// process sharing a console, so the test is skipped.
func interrupt(t *testing.T, _ *os.Process) {
	t.Helper()
	t.Skip("Windows cannot send Ctrl-C to one process")
}
