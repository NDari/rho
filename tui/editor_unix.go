//go:build !windows

package tui

import "os/exec"

const defaultEditor = "vi"

// editorCommand runs editor on path through the shell, so editors with
// arguments (such as "code --wait") work.
func editorCommand(editor, path string) *exec.Cmd {
	return exec.Command("sh", "-c", editor+` "$1"`, "sh", path)
}
