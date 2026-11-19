package tui

import (
	"context"

	tea "charm.land/bubbletea/v2"

	"github.com/NDari/rho/core/app"
	"github.com/NDari/rho/core/prompt"
)

// Options configure the TUI.
type Options struct {
	// Prompt is sent as the first message, if non-empty.
	Prompt string
	// Resume opens the session picker at startup.
	Resume bool
	// Extensions, if set, provides extension commands.
	Extensions ExtensionCommands
}

// Run starts the TUI and blocks until it exits.
func Run(a *app.App, o Options) error {
	m := New(a)
	m.ext = o.Extensions
	if o.Resume {
		m.resumePicker()
	}
	if o.Prompt != "" {
		m.input.SetValue(o.Prompt)
		_ = m.submit(false)
	}
	p := tea.NewProgram(m)
	_, err := p.Run()
	a.Agent.Cancel()
	a.Agent.Wait()
	_ = a.Agent.Session().Close()
	return err
}

// NewUI returns the extension UI bridge for a model.
func (m *Model) NewUI() UI { return UI{m: m} }

func (m *Model) reload() tea.Cmd {
	skills, w1 := prompt.LoadSkills(m.app.Paths)
	templates, w2 := prompt.LoadTemplates(m.app.Paths)
	m.app.Skills = skills
	m.app.Templates = templates
	m.agent.SetSkills(skills)
	for _, w := range append(w1, w2...) {
		m.notice("warn", w)
	}
	if m.ext == nil {
		m.notice("info", "reloaded skills, prompt templates and context files")
		return nil
	}
	return m.async(func() error { return m.ext.Reload(context.Background()) }, func(m *Model) tea.Cmd {
		m.notice("info", "reloaded extensions, skills, prompt templates and context files")
		return nil
	})
}
