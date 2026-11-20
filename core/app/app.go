// Package app wires configuration, sessions, providers, tools and the
// agent together. Frontends create an App and drive its Agent.
package app

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/NDari/rho/core/agent"
	"github.com/NDari/rho/core/config"
	"github.com/NDari/rho/core/events"
	"github.com/NDari/rho/core/ext"
	"github.com/NDari/rho/core/prompt"
	"github.com/NDari/rho/core/provider/registry"
	"github.com/NDari/rho/core/session"
	"github.com/NDari/rho/core/tools"
)

// Options configure App creation.
type Options struct {
	Cwd          string
	Model        string
	Continue     bool
	SessionFile  string
	Version      string
	Sink         events.Sink
	NoExtensions bool
}

// App holds the assembled core.
type App struct {
	Config    config.Config
	Paths     config.Paths
	Registry  *registry.Registry
	Agent     *agent.Agent
	Templates []prompt.Template
	Skills    []prompt.Skill
	Warnings  []string
	Version   string
	// Ext is nil when extensions are disabled.
	Ext *ext.Manager
}

// New loads configuration and opens (or creates) a session.
func New(o Options) (*App, error) {
	cwd := o.Cwd
	if cwd == "" {
		var err error
		if cwd, err = os.Getwd(); err != nil {
			return nil, err
		}
	}
	paths, err := config.ResolvePaths(cwd)
	if err != nil {
		return nil, err
	}
	cfg, warnings, err := config.Load(paths)
	if err != nil {
		return nil, err
	}
	a := &App{Config: cfg, Paths: paths, Registry: registry.New(cfg), Warnings: warnings, Version: o.Version}

	sess, err := a.openSession(o)
	if err != nil {
		return nil, err
	}

	model, err := cfg.ResolveModel(o.Model)
	if err != nil {
		// A session may still name a usable model; otherwise this is fatal.
		cv := sess.BuildContext()
		if cv.Model == "" || o.Model != "" {
			return nil, err
		}
		if model, err = cfg.ResolveModel(cv.Model); err != nil {
			return nil, err
		}
	}

	skills, w := prompt.LoadSkills(paths)
	a.Skills = skills
	a.Warnings = append(a.Warnings, w...)
	templates, w := prompt.LoadTemplates(paths)
	a.Templates = templates
	a.Warnings = append(a.Warnings, w...)

	a.Agent = agent.New(agent.Options{
		Config:    cfg,
		Paths:     paths,
		Session:   sess,
		Providers: a.Registry,
		Model:     model,
		Tools:     tools.Builtins(),
		Skills:    skills,
		Sink:      o.Sink,
	})
	if !o.NoExtensions {
		var builtin []string
		for _, t := range tools.Builtins() {
			builtin = append(builtin, t.Spec().Name)
		}
		a.Ext = ext.NewManager(ext.Options{
			Paths:   paths,
			Config:  cfg,
			Version: o.Version,
			Agent:   a.Agent,
			Builtin: builtin,
			Sink:    func(e events.Event) { a.emit(e) },
		})
	}
	if o.Model != "" && sess.BuildContext().Model != "" && sess.BuildContext().Model != model.ID {
		// An explicit --model wins over the session's recorded model.
		if err := a.Agent.SetModel(model.ID); err != nil {
			return nil, err
		}
	}
	return a, nil
}

// emit forwards an event to the agent's current sink.
func (a *App) emit(e events.Event) { a.Agent.Emit(e) }

// StartExtensions starts extensions with the given UI and registers their
// tools and hooks with the agent.
func (a *App) StartExtensions(ctx context.Context, ui ext.UI) {
	if a.Ext == nil {
		return
	}
	a.Ext.SetUI(ui)
	a.Ext.Start(ctx)
	a.refreshTools()
}

// ReloadExtensions restarts extensions.
func (a *App) ReloadExtensions(ctx context.Context) error {
	if a.Ext == nil {
		return nil
	}
	err := a.Ext.Reload(ctx)
	a.refreshTools()
	return err
}

// StopExtensions shuts extensions down.
func (a *App) StopExtensions() {
	if a.Ext != nil {
		a.Ext.Stop()
	}
}

func (a *App) refreshTools() {
	a.Agent.SetTools(append(tools.Builtins(), a.Ext.Tools()...))
	a.Agent.SetHooks(a.Ext)
}

// SetSetting changes a setting for this run and saves it to the global
// config. Per-model settings apply to the current model. saved is false
// when the value could only be changed for this session; the note says
// why, or that the project config overrides the saved value.
func (a *App) SetSetting(key, value string) (saved bool, note string, err error) {
	s, ok := config.LookupSetting(key)
	if !ok {
		return false, "", fmt.Errorf("unknown setting %q", key)
	}
	if a.Agent.Busy() {
		return false, "", agent.ErrBusy
	}
	model := a.Agent.Model().ID
	cfg, err := s.Apply(a.Agent.Config(), model, value)
	if err != nil {
		return false, "", err
	}
	path := a.Paths.GlobalFile()
	err = config.SaveSetting(path, s, model, s.Value(cfg, model))
	switch {
	case errors.Is(err, config.ErrModelNotInFile):
		note = "changed for this session only: model " + model + " has no [[models]] entry in " + path
	case err != nil:
		return false, "", err
	default:
		saved = true
	}
	switch {
	case saved && a.Paths.ProjectSets(s, model):
		if s.PerModel {
			note = "the project's .rho/config.toml defines model " + model + ", so its settings win on the next start"
		} else {
			note = "the project's .rho/config.toml also sets " + key + " and wins on the next start"
		}
	}
	a.Config = cfg
	a.Agent.SetConfig(cfg)
	return saved, note, nil
}

// SessionDir is the directory holding sessions for the working directory.
func (a *App) SessionDir() string { return session.Dir(a.Paths.Home, a.Paths.Cwd) }

// NewSession creates a fresh session for the working directory.
func (a *App) NewSession() *session.Session {
	return session.New(a.SessionDir(), a.Paths.Cwd, a.Version)
}

func (a *App) openSession(o Options) (*session.Session, error) {
	switch {
	case o.SessionFile != "":
		return session.Open(o.SessionFile)
	case o.Continue:
		latest, err := session.Latest(a.SessionDir())
		if err != nil {
			return nil, err
		}
		if latest == "" {
			return nil, errors.New("no previous session in this directory")
		}
		return session.Open(latest)
	}
	return a.NewSession(), nil
}

// Fork creates a new session file containing the path from the root to
// entry id of the current session, and switches to it.
func (a *App) Fork(id string) (*session.Session, error) {
	src := a.Agent.Session()
	path := src.PathTo(id)
	if len(path) == 0 {
		return nil, fmt.Errorf("unknown entry %q", id)
	}
	dst := a.NewSession()
	idMap := map[string]string{}
	for _, e := range path {
		ne := e
		ne.Parent = idMap[e.Parent]
		if e.Type == session.TypeCompaction {
			ne.FirstKept = idMap[e.FirstKept]
		}
		appended, err := dst.Append(ne)
		if err != nil {
			return nil, err
		}
		idMap[e.ID] = appended.ID
	}
	labels := src.Labels()
	for _, e := range path {
		if l := labels[e.ID]; l != "" {
			if _, err := dst.Append(session.Entry{Type: session.TypeLabel, Target: idMap[e.ID], Label: l}); err != nil {
				return nil, err
			}
		}
	}
	// Mark where the fork came from, unless that entry already has a label.
	if src.Path() != "" && labels[id] == "" {
		label := fmt.Sprintf("forked from %s", filepath.Base(src.Path()))
		if _, err := dst.Append(session.Entry{Type: session.TypeLabel, Target: idMap[id], Label: label}); err != nil {
			return nil, err
		}
	}
	return dst, nil
}
