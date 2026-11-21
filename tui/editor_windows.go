//go:build windows

package tui

import (
	"os"
	"os/exec"
	"strings"
)

const defaultEditor = "notepad"

// editorCommand runs editor on path. An editor that names an existing
// file is run as it is (its path may contain spaces); otherwise it is
// split into a program and arguments at spaces (such as "code --wait").
func editorCommand(editor, path string) *exec.Cmd {
	if _, err := os.Stat(editor); err == nil {
		return exec.Command(editor, path)
	}
	f := strings.Fields(editor)
	return exec.Command(f[0], append(f[1:], path)...)
}
