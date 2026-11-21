//go:build !windows

package tui

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/NDari/rho/core/app"
)

// fakeEditor writes a script that appends "|" and its number of arguments
// to the file (its last argument).
func fakeEditor(t *testing.T) string {
	t.Helper()
	script := filepath.Join(t.TempDir(), "ed")
	body := "#!/bin/sh\nfor a; do last=$a; done\nc=$(cat \"$last\")\nprintf '%s|%d\\n' \"$c\" $# > \"$last\"\n"
	if err := os.WriteFile(script, []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}
	return script
}

func newEditorModel(t *testing.T) *Model {
	t.Helper()
	t.Setenv("RHO_HOME", t.TempDir())
	a, err := app.New(app.Options{Cwd: t.TempDir(), Model: "openai/test-model", NoExtensions: true})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = a.Agent.Session().Close() })
	return New(a)
}

func TestEditorRoundTrip(t *testing.T) {
	ed := fakeEditor(t)
	tests := []struct {
		name, visual, editor string
		want                 string
	}{
		{"VISUAL first", ed, "false", "draft|1"},
		{"EDITOR with arguments", "", ed + " --wait", "draft|2"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv("VISUAL", tt.visual)
			t.Setenv("EDITOR", tt.editor)
			m := newEditorModel(t)
			m.input.SetValue("draft")
			path, err := writeTemp(m.input.Value())
			if err != nil {
				t.Fatal(err)
			}
			err = editorCommand(editorName(), path).Run()
			m.finishEditor(editorDoneMsg{path: path, err: err})
			if got := m.input.Value(); got != tt.want {
				t.Errorf("input = %q, want %q", got, tt.want)
			}
			if _, err := os.Stat(path); !os.IsNotExist(err) {
				t.Error("temporary file not removed")
			}
		})
	}
}

func TestEditorKeepsInputOnFailureOrEmpty(t *testing.T) {
	tests := []struct {
		name, editor string
	}{
		{"editor fails", "false"},
		{"editor not found", "rho-no-such-editor"},
		{"file emptied", "truncate -s 0"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv("VISUAL", tt.editor)
			m := newEditorModel(t)
			m.input.SetValue("draft")
			path, err := writeTemp(m.input.Value())
			if err != nil {
				t.Fatal(err)
			}
			err = editorCommand(editorName(), path).Run()
			m.finishEditor(editorDoneMsg{path: path, err: err})
			if got := m.input.Value(); got != "draft" {
				t.Errorf("input = %q, want it unchanged", got)
			}
			if tt.editor == "rho-no-such-editor" && !strings.Contains(m.flash, "not found") {
				t.Errorf("flash = %q", m.flash)
			}
		})
	}
}
