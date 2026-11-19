// Package app wires configuration, sessions, providers, tools and the
// agent together. Frontends create an App and drive its Agent.
package app

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/NDari/rho/core/agent"
	"github.com/NDari/rho/core/config"
	"github.com/NDari/rho/core/events"
	"github.com/NDari/rho/core/prompt"
	"github.com/NDari/rho/core/provider/registry"
	"github.com/NDari/rho/core/session"
	"github.com/NDari/rho/core/tools"
)

// Options configure App creation.
type Options struct {
	Cwd         string
	Model       string
	Continue    bool
	SessionFile string
	Version     string
	Sink        events.Sink
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
	if o.Model != "" && sess.BuildContext().Model != "" && sess.BuildContext().Model != model.ID {
		// An explicit --model wins over the session's recorded model.
		if err := a.Agent.SetModel(model.ID); err != nil {
			return nil, err
		}
	}
	return a, nil
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
	if src.Path() != "" {
		label := fmt.Sprintf("forked from %s", filepath.Base(src.Path()))
		if _, err := dst.Append(session.Entry{Type: session.TypeLabel, Target: idMap[id], Label: label}); err != nil {
			return nil, err
		}
	}
	return dst, nil
}
