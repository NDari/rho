//go:build !windows

package tools

import (
	"errors"
	"os/exec"
	"syscall"
	"time"
)

// setupProcess starts the command in its own process group so the whole
// tree can be killed.
func setupProcess(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
}

type processTree struct{ pid int }

func trackProcess(cmd *exec.Cmd) *processTree { return &processTree{pid: cmd.Process.Pid} }

// kill terminates the process group: SIGTERM first, SIGKILL shortly after.
func (t *processTree) kill() {
	_ = syscall.Kill(-t.pid, syscall.SIGTERM)
	go func() {
		time.Sleep(2 * time.Second)
		_ = syscall.Kill(-t.pid, syscall.SIGKILL)
	}()
}

func (t *processTree) release() {}

// findShell returns the shell executable to use.
func findShell(override string) (string, error) {
	if override != "" {
		return override, nil
	}
	for _, s := range []string{"bash", "sh"} {
		if p, err := exec.LookPath(s); err == nil {
			return p, nil
		}
	}
	return "", errors.New("no bash or sh found in PATH")
}
