//go:build !windows

package integration

import (
	"encoding/json"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/creack/pty"

	"github.com/NDari/rho/core/provider/replay"
)

// TestTerminalClosedDuringCommand closes the terminal of the TUI while a
// bash command runs. Commands run in a process group of their own, so the
// hangup does not reach them: rho must stop the command, record the
// cancelled turn and exit, not die and leave the command running.
func TestTerminalClosedDuringCommand(t *testing.T) {
	rho, _ := binaries(t)
	pidFile := filepath.Join(t.TempDir(), "pid")
	args, _ := json.Marshal(map[string]string{"command": "echo $$ > " + pidFile + "; exec sleep 300"})
	e := newEnv(t, replay.Reply{Body: replay.Chat{
		Thinking: "Start the server.",
		Calls:    []replay.ChatCall{{ID: "call_1", Name: "bash", Args: string(args)}},
	}.SSE()})

	cmd := exec.Command(rho, "Start the server")
	cmd.Dir = e.cwd
	cmd.Env = append(os.Environ(), "RHO_HOME="+e.home, "RHO_NO_UPDATE_CHECK=1")
	term, err := pty.StartWithSize(cmd, &pty.Winsize{Rows: 40, Cols: 120})
	if err != nil {
		t.Fatal(err)
	}
	go func() { _, _ = io.Copy(io.Discard, term) }()
	exited := make(chan struct{})
	go func() { _ = cmd.Wait(); close(exited) }()
	t.Cleanup(func() {
		_ = cmd.Process.Kill()
		<-exited
	})

	pid := waitForPid(t, pidFile)
	t.Cleanup(func() { _ = syscall.Kill(pid, syscall.SIGKILL) })

	// Closing the last handle on the terminal hangs it up: the kernel
	// sends SIGHUP to rho, the session leader.
	_ = term.Close()
	select {
	case <-exited:
	case <-time.After(20 * time.Second):
		t.Fatal("rho did not exit after the terminal closed")
	}
	if alive(pid) {
		t.Error("the command kept running after rho exited")
	}

	sessions, _ := filepath.Glob(filepath.Join(e.home, "sessions", "*", "*.jsonl"))
	if len(sessions) != 1 {
		t.Fatalf("sessions = %v", sessions)
	}
	if got := roles(reopen(t, sessions[0])); got != "user assistant(bash) tool_result" {
		t.Errorf("session = %s", got)
	}
}

// waitForPid waits for the command to write its process id.
func waitForPid(t *testing.T, path string) int {
	t.Helper()
	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		if b, err := os.ReadFile(path); err == nil && strings.HasSuffix(string(b), "\n") {
			pid, err := strconv.Atoi(strings.TrimSpace(string(b)))
			if err != nil {
				t.Fatal(err)
			}
			return pid
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("the command did not start")
	return 0
}

// alive reports whether pid still runs, allowing a moment for the process
// to be killed and reaped.
func alive(pid int) bool {
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if err := syscall.Kill(pid, 0); errors.Is(err, syscall.ESRCH) {
			return false
		}
		time.Sleep(20 * time.Millisecond)
	}
	return true
}
