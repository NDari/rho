//go:build windows

package tools

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"
)

// setupProcess starts the command in its own process group, without a
// console window (otherwise one pops up when rho-gui, which has no
// console, runs a command).
func setupProcess(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{CreationFlags: windows.CREATE_NEW_PROCESS_GROUP | windows.CREATE_NO_WINDOW}
}

// processTree tracks a process and its descendants with a Job Object.
type processTree struct {
	job  windows.Handle
	proc *os.Process
}

func trackProcess(cmd *exec.Cmd) *processTree {
	t := &processTree{proc: cmd.Process}
	job, err := windows.CreateJobObject(nil, nil)
	if err != nil {
		return t
	}
	info := windows.JOBOBJECT_EXTENDED_LIMIT_INFORMATION{
		BasicLimitInformation: windows.JOBOBJECT_BASIC_LIMIT_INFORMATION{
			LimitFlags: windows.JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE,
		},
	}
	_, _ = windows.SetInformationJobObject(job, windows.JobObjectExtendedLimitInformation,
		uintptr(unsafe.Pointer(&info)), uint32(unsafe.Sizeof(info)))
	h, err := windows.OpenProcess(windows.PROCESS_SET_QUOTA|windows.PROCESS_TERMINATE, false, uint32(cmd.Process.Pid))
	if err != nil {
		_ = windows.CloseHandle(job)
		return t
	}
	defer windows.CloseHandle(h)
	if err := windows.AssignProcessToJobObject(job, h); err != nil {
		_ = windows.CloseHandle(job)
		return t
	}
	t.job = job
	return t
}

func (t *processTree) kill() {
	if t.job != 0 {
		_ = windows.TerminateJobObject(t.job, 1)
		return
	}
	_ = t.proc.Kill()
}

func (t *processTree) release() {
	if t.job != 0 {
		_ = windows.CloseHandle(t.job)
		t.job = 0
	}
}

// findShell locates Git Bash. WSL's bash (System32\bash.exe) is never used.
func findShell(override string) (string, error) {
	if override != "" {
		return override, nil
	}
	var candidates []string
	for _, env := range []string{"ProgramFiles", "ProgramW6432", "ProgramFiles(x86)"} {
		if dir := os.Getenv(env); dir != "" {
			candidates = append(candidates, filepath.Join(dir, "Git", "bin", "bash.exe"))
		}
	}
	if dir := os.Getenv("LOCALAPPDATA"); dir != "" {
		candidates = append(candidates, filepath.Join(dir, "Programs", "Git", "bin", "bash.exe"))
	}
	if git, err := exec.LookPath("git"); err == nil {
		gitDir := filepath.Dir(git) // ...\Git\cmd or ...\Git\bin
		candidates = append(candidates,
			filepath.Join(gitDir, "bash.exe"),
			filepath.Join(gitDir, "..", "bin", "bash.exe"))
	}
	for _, c := range candidates {
		if fi, err := os.Stat(c); err == nil && !fi.IsDir() {
			return c, nil
		}
	}
	if p, err := exec.LookPath("bash"); err == nil && !isWSLBash(p) {
		return p, nil
	}
	return "", errors.New("Git Bash not found; install Git for Windows or set bash.shell in ~/.rho/config.jsonc")
}

func isWSLBash(p string) bool {
	sys := strings.ToLower(os.Getenv("SystemRoot"))
	lp := strings.ToLower(p)
	return strings.Contains(lp, `\windows\system32\`) || (sys != "" && strings.HasPrefix(lp, sys))
}
