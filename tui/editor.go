package tui

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"

	tea "charm.land/bubbletea/v2"
)

// editorDoneMsg reports that the external editor exited.
type editorDoneMsg struct {
	path string
	err  error
}

// editorName is the editor to run: $VISUAL, then $EDITOR, then the
// platform default.
func editorName() string {
	for _, v := range []string{"VISUAL", "EDITOR"} {
		if e := strings.TrimSpace(os.Getenv(v)); e != "" {
			return e
		}
	}
	return defaultEditor
}

// openEditor writes the input to a temporary file and suspends the TUI
// while the user's editor runs on it.
func (m *Model) openEditor() tea.Cmd {
	path, err := writeTemp(m.input.Value())
	if err != nil {
		m.setFlash("editor: " + err.Error())
		return nil
	}
	return tea.ExecProcess(editorCommand(editorName(), path), func(err error) tea.Msg {
		return editorDoneMsg{path: path, err: err}
	})
}

// notFound reports whether the editor could not be started: exec found no
// program, or the shell exited with 127 (command not found).
func notFound(err error) bool {
	var exit *exec.ExitError
	return errors.Is(err, exec.ErrNotFound) || errors.As(err, &exit) && exit.ExitCode() == 127
}

// writeTemp writes text to a new temporary Markdown file.
func writeTemp(text string) (string, error) {
	f, err := os.CreateTemp("", "rho-prompt-*.md")
	if err != nil {
		return "", err
	}
	_, err = f.WriteString(text)
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		_ = os.Remove(f.Name())
		return "", err
	}
	return f.Name(), nil
}

// finishEditor puts the edited text back into the input. An empty file or
// a failed editor leaves the input unchanged.
func (m *Model) finishEditor(msg editorDoneMsg) {
	data, err := os.ReadFile(msg.path)
	_ = os.Remove(msg.path)
	m.dirty = true
	switch {
	case notFound(msg.err):
		m.setFlash(fmt.Sprintf("editor %s not found; set $VISUAL or $EDITOR", editorName()))
		return
	case msg.err != nil:
		m.setFlash(fmt.Sprintf("editor %s: %v; input unchanged", editorName(), msg.err))
		return
	case err != nil:
		m.setFlash("editor: " + err.Error())
		return
	}
	text := strings.TrimRight(strings.ReplaceAll(string(data), "\r\n", "\n"), "\n")
	if strings.TrimSpace(text) == "" {
		m.setFlash("editor: empty file; input unchanged")
		return
	}
	m.input.SetValue(text)
	m.afterInputChange()
}
