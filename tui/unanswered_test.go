package tui

import (
	"errors"
	"path/filepath"
	"testing"

	"github.com/NDari/rho/core/agent"
	"github.com/NDari/rho/core/app"
	"github.com/NDari/rho/core/config"
	"github.com/NDari/rho/core/provider"
	"github.com/NDari/rho/core/provider/fake"
	"github.com/NDari/rho/core/session"
	"github.com/NDari/rho/core/tools"
)

type fakeProviders map[string]provider.Provider

func (p fakeProviders) Get(name string) (provider.Provider, error) {
	if pr, ok := p[name]; ok {
		return pr, nil
	}
	return nil, errors.New("unknown provider " + name)
}

// newTestModel builds a TUI model around an agent with the fake provider.
func newTestModel(t *testing.T, steps ...fake.Step) (*Model, *agent.Agent) {
	t.Helper()
	dir := t.TempDir()
	cfg := config.Defaults()
	cfg.Providers["fake"] = config.Provider{Type: config.ProviderOpenAICompat, BaseURL: "http://x"}
	model := config.Model{ID: "m", Provider: "fake", ContextWindow: 100000, MaxOutput: 1000}
	cfg.Models = []config.Model{model}
	paths := config.Paths{Home: filepath.Join(dir, "home"), Cwd: dir, ProjectRoot: dir, ProjectDir: filepath.Join(dir, ".rho")}
	ag := agent.New(agent.Options{
		Config:    cfg,
		Paths:     paths,
		Session:   session.New("", dir, "test"),
		Providers: fakeProviders{"fake": fake.New("fake", steps...)},
		Model:     model,
		Tools:     tools.Builtins(),
	})
	m := New(&app.App{Config: cfg, Paths: paths, Agent: ag})
	m.width, m.height = 80, 24
	return m, ag
}

// TestUnansweredPromptReturnsToEditor sends a prompt that fails before any
// reply and checks that it leaves the transcript and goes back into the
// editor.
func TestUnansweredPromptReturnsToEditor(t *testing.T) {
	tests := []struct{ typed, want string }{
		{"look at @notes.md", "look at @notes.md"},
		{"//literal slash", "//literal slash"},
	}
	for _, tt := range tests {
		t.Run(tt.typed, func(t *testing.T) {
			m, ag := newTestModel(t, fake.Step{Err: &provider.Error{Kind: provider.ErrAuth, StatusCode: 401, Message: "bad key"}})
			m.input.SetValue(tt.typed)
			m.submit(false)
			ag.Wait()
			for _, e := range m.events.drain() {
				m.handleEvent(e)
			}
			if got := m.input.Value(); got != tt.want {
				t.Errorf("editor = %q, want %q", got, tt.want)
			}
			for _, it := range m.items {
				if _, ok := it.(*userItem); ok {
					t.Errorf("withdrawn prompt still in the transcript")
				}
			}
		})
	}
}
