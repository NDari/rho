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
