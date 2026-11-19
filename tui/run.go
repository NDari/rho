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
}

// extAdapter exposes the app's extension manager to the TUI.
type extAdapter struct{ a *app.App }

func (e extAdapter) Commands() []ExtensionCommand {
	var out []ExtensionCommand
	for _, c := range e.a.Ext.Commands() {
		out = append(out, ExtensionCommand{Name: c.Name, Description: c.Description + " (" + c.Extension + ")"})
	}
	return out
}

func (e extAdapter) Run(ctx context.Context, name, args string) error {
	return e.a.Ext.RunCommand(ctx, name, args)
}

func (e extAdapter) Describe() string { return e.a.Ext.Describe() }

func (e extAdapter) Reload(ctx context.Context) error { return e.a.ReloadExtensions(ctx) }

// Run starts the TUI and blocks until it exits.
func Run(a *app.App, o Options) error {
	m := New(a)
	if a.Ext != nil {
		m.ext = extAdapter{a}
	}
	if o.Resume {
		m.resumePicker()
	}
	if o.Prompt != "" {
		m.input.SetValue(o.Prompt)
		_ = m.submit(false)
	}
	p := tea.NewProgram(m)
	if a.Ext != nil {
		// Extensions start once the UI runs, so approval prompts can show.
		go a.StartExtensions(context.Background(), m.NewUI())
	}
	_, err := p.Run()
	a.Agent.Cancel()
	a.Agent.Wait()
	a.StopExtensions()
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
