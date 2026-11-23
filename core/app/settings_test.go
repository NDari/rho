package app

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/NDari/rho/core/agent"
	"github.com/NDari/rho/core/config"
	"github.com/NDari/rho/core/provider"
	"github.com/NDari/rho/core/provider/fake"
)

// testApp creates an App in a fresh RHO_HOME and project directory, with
// the given global and project config ("" writes none) and the fake
// provider answering steps.
func testApp(t *testing.T, global, project string, extensions bool, steps ...fake.Step) *App {
	t.Helper()
	home := t.TempDir()
	t.Setenv("RHO_HOME", home)
	cwd := t.TempDir()
	if global != "" {
		writeFile(t, filepath.Join(home, "config.toml"), global)
	}
	if project != "" {
		writeFile(t, filepath.Join(cwd, ".rho", "config.toml"), project)
	}
	a, err := New(Options{Cwd: cwd, Model: "openai/test-model", NoExtensions: !extensions})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = a.Agent.Session().Close() })
	a.Registry.Set("openai", fake.New("openai", steps...))
	return a
}

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func readFile(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		t.Fatal(err)
	}
	return string(data)
}

// whileBusy runs fn while a turn is blocked waiting on the provider, then
// cancels the turn.
func whileBusy(t *testing.T, a *App, fn func()) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- a.Agent.Run(ctx, provider.UserText("wait")) }()
	deadline := time.Now().Add(5 * time.Second)
	for !a.Agent.Busy() {
		if time.Now().After(deadline) {
			cancel()
			t.Fatal("the turn never started")
		}
		time.Sleep(time.Millisecond)
	}
	fn()
	cancel()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Errorf("turn ended with %v, want context.Canceled", err)
	}
}

func TestSetSetting(t *testing.T) {
	const modelEntry = "[[models]]\nid = \"openai/test-model\"\nprovider = \"openai\"\n"
	tests := []struct {
		name      string
		global    string
		project   string
		key       string
		value     string
		wantErr   string
		wantSaved bool
		wantNote  string
		wantFile  string // a line the global config must contain afterwards
		check     func(t *testing.T, c config.Config)
	}{
		{name: "unknown key", key: "no_such_key", value: "1", wantErr: `unknown setting "no_such_key"`},
		{name: "unknown theme", key: "theme", value: "no-such-theme", wantErr: `unknown theme "no-such-theme"`},
		{name: "system is only for theme", key: "theme_dark", value: "system", wantErr: `unknown theme "system"`},
		{name: "bad int", key: "max_retries", value: "many", wantErr: "max_retries"},
		{name: "theme saved", key: "theme", value: "github-light", wantSaved: true, wantFile: `theme = "github-light"`,
			check: func(t *testing.T, c config.Config) {
				if c.Theme != "github-light" {
					t.Errorf("Theme = %q", c.Theme)
				}
			}},
		{name: "theme system", key: "theme", value: "system", wantSaved: true, wantFile: `theme = "system"`},
		{name: "int saved", key: "max_retries", value: "5", wantSaved: true, wantFile: "max_retries = 5",
			check: func(t *testing.T, c config.Config) {
				if c.MaxRetries != 5 {
					t.Errorf("MaxRetries = %d", c.MaxRetries)
				}
			}},
		{name: "bool saved", key: "tui.mouse_select", value: "false", wantSaved: true, wantFile: "mouse_select = false",
			check: func(t *testing.T, c config.Config) {
				if c.TUI.MouseSelectEnabled() {
					t.Error("mouse select still on")
				}
			}},
		{name: "per-model setting without a models entry", key: "reasoning_effort", value: "high",
			wantNote: "changed for this session only: model openai/test-model has no [[models]] entry",
			check: func(t *testing.T, c config.Config) {
				if m, _ := c.ResolveModel("openai/test-model"); m.ReasoningEffort != "high" {
					t.Errorf("ReasoningEffort = %q", m.ReasoningEffort)
				}
			}},
		{name: "per-model setting with a models entry", global: modelEntry, key: "reasoning_effort", value: "low",
			wantSaved: true, wantFile: `reasoning_effort = "low"`},
		{name: "project sets the key too", project: "theme = \"github-light\"\n", key: "theme", value: "gruvbox-dark",
			wantSaved: true, wantFile: `theme = "gruvbox-dark"`, wantNote: "the project's .rho/config.toml also sets theme"},
		{name: "project defines the model", global: modelEntry, project: modelEntry, key: "reasoning_effort", value: "medium",
			wantSaved: true, wantNote: "the project's .rho/config.toml defines model openai/test-model"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			a := testApp(t, tc.global, tc.project, false)
			before := readFile(t, a.Paths.GlobalFile())
			saved, note, err := a.SetSetting(tc.key, tc.value)
			if tc.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("err = %v, want one containing %q", err, tc.wantErr)
				}
				if after := readFile(t, a.Paths.GlobalFile()); after != before {
					t.Errorf("a rejected setting changed the config file:\n%s", after)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if saved != tc.wantSaved {
				t.Errorf("saved = %v, want %v", saved, tc.wantSaved)
			}
			if !strings.HasPrefix(note, tc.wantNote) || (tc.wantNote == "" && note != "") {
				t.Errorf("note = %q, want prefix %q", note, tc.wantNote)
			}
			file := readFile(t, a.Paths.GlobalFile())
			if tc.wantFile != "" && !strings.Contains(file, tc.wantFile) {
				t.Errorf("config file:\n%s\nwant a line %q", file, tc.wantFile)
			}
			if !saved && file != before {
				t.Errorf("an unsaved setting changed the config file:\n%s", file)
			}
			if tc.check != nil {
				tc.check(t, a.Agent.Config())
				tc.check(t, a.Config)
			}
			if saved {
				// The saved file loads back to the same value.
				cfg, _, err := config.Load(config.Paths{Home: a.Paths.Home})
				if err != nil {
					t.Fatal(err)
				}
				s, _ := config.LookupSetting(tc.key)
				if got := s.Value(cfg, "openai/test-model"); got != tc.value {
					t.Errorf("reloaded %s = %q, want %q", tc.key, got, tc.value)
				}
			}
		})
	}
}

func TestSetSettingWhileBusy(t *testing.T) {
	a := testApp(t, "", "", false, fake.Step{Wait: true})
	whileBusy(t, a, func() {
		if _, _, err := a.SetSetting("theme", "github-light"); !errors.Is(err, agent.ErrBusy) {
			t.Errorf("err = %v, want ErrBusy", err)
		}
	})
	if file := readFile(t, a.Paths.GlobalFile()); file != "" {
		t.Errorf("config written while busy:\n%s", file)
	}
}
