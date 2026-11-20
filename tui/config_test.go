package tui

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/NDari/rho/core/app"
)

func TestConfigPickerCyclesAndSaves(t *testing.T) {
	home := t.TempDir()
	t.Setenv("RHO_HOME", home)
	cfgPath := filepath.Join(home, "config.toml")
	if err := os.WriteFile(cfgPath, []byte("# my settings\ntheme = \"dark\"\n\n[bash]\ntimeout = 120\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	a, err := app.New(app.Options{Cwd: t.TempDir(), Model: "openai/test-model", NoExtensions: true})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = a.Agent.Session().Close() })
	m := New(a)

	m.runCommand("/config")
	if m.picker == nil {
		t.Fatal("no picker")
	}
	m.picker.filter = "bash.timeout"
	m.picker.refilter()
	enter := tea.KeyPressMsg{Code: tea.KeyEnter}
	for _, want := range []int{300, 600, 30} {
		m.handleKey(enter)
		if m.picker == nil {
			t.Fatal("enter closed the picker")
		}
		if got := a.Agent.Config().Bash.Timeout; got != want {
			t.Fatalf("timeout = %d, want %d", got, want)
		}
	}
	it, _ := m.picker.current()
	if f := strings.Fields(it.title); len(f) != 2 || f[1] != "30" {
		t.Errorf("row title = %q", it.title)
	}
	data, _ := os.ReadFile(cfgPath)
	if want := "# my settings\ntheme = \"dark\"\n\n[bash]\ntimeout = 30\n"; string(data) != want {
		t.Errorf("config file = %q, want %q", data, want)
	}

	m.runCommand("/config theme light")
	if m.theme.Name != "light" || a.Config.Theme != "light" {
		t.Errorf("theme = %q / %q", m.theme.Name, a.Config.Theme)
	}
	m.runCommand("/config theme nope")
	if m.theme.Name != "light" {
		t.Error("an unknown theme was applied")
	}
}

func TestConfigPerModelAndThinking(t *testing.T) {
	home := t.TempDir()
	t.Setenv("RHO_HOME", home)
	cfgPath := filepath.Join(home, "config.toml")
	file := "default_model = \"m\"\n\n[providers.p]\nbase_url = \"http://localhost:8080/v1\"\n\n[[models]]\nid = \"m\"\nprovider = \"p\"\n"
	if err := os.WriteFile(cfgPath, []byte(file), 0o644); err != nil {
		t.Fatal(err)
	}
	a, err := app.New(app.Options{Cwd: t.TempDir(), NoExtensions: true})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = a.Agent.Session().Close() })
	m := New(a)

	m.runCommand("/config")
	m.picker.filter = "reasoning"
	m.picker.refilter()
	m.handleKey(tea.KeyPressMsg{Code: tea.KeyEnter})
	if got := a.Agent.Model().ReasoningEffort; got != "low" {
		t.Fatalf("effort = %q, want low", got)
	}
	data, _ := os.ReadFile(cfgPath)
	if want := file + "reasoning_effort = \"low\"\n"; string(data) != want {
		t.Errorf("config file = %q", data)
	}
	m.handleKey(tea.KeyPressMsg{Code: tea.KeyEscape})

	m.runCommand("/config reasoning_effort default")
	if got := a.Agent.Model().ReasoningEffort; got != "" {
		t.Errorf("effort = %q after default", got)
	}
	if data, _ := os.ReadFile(cfgPath); string(data) != file {
		t.Errorf("default did not remove the key: %q", data)
	}

	if m.showThinking {
		t.Fatal("thinking shown before the setting")
	}
	m.runCommand("/config tui.show_thinking on")
	if !m.showThinking {
		t.Error("show_thinking did not apply")
	}
}
