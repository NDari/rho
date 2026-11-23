// Package app wires configuration, sessions, providers, tools and the
// agent together. Frontends create an App and drive its Agent.
package app

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/NDari/rho/core/agent"
	"github.com/NDari/rho/core/config"
	"github.com/NDari/rho/core/events"
	"github.com/NDari/rho/core/ext"
	"github.com/NDari/rho/core/prompt"
	"github.com/NDari/rho/core/provider/registry"
	"github.com/NDari/rho/core/session"
	"github.com/NDari/rho/core/theme"
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
	// Tools, when not nil, replaces the tools.enabled setting for this run.
	Tools []string
	// NoProjectContext ignores every Rho file the project provides (see
	// config.Paths.NoProject).
	NoProjectContext bool
	// SystemFile replaces the system prompt; AppendSystemFiles are added
	// after it. Relative paths are relative to the process's working
	// directory, not Cwd.
	SystemFile        string
	AppendSystemFiles []string
	// MaxSteps, if positive, limits each turn to that many model
	// responses (see agent.Options).
	MaxSteps int
	// ExtHost is the program that runs Go source extensions (see
	// ext.Options); empty means the running executable.
	ExtHost string
	// RetryDelay is the first wait before retrying a transient provider
	// error (see agent.Options); 0 means the default. Tests set it low.
	RetryDelay time.Duration
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
	// builtin are the built-in tools offered; autoTools names those added
	// only because bash is not offered, which extension tools may replace.
	builtin   []tools.Tool
	autoTools []string
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
	paths.NoProject = o.NoProjectContext
	systemFile, appendFiles, err := promptFiles(o.SystemFile, o.AppendSystemFiles)
	if err != nil {
		return nil, err
	}
	cfg, warnings, err := config.Load(paths)
	if err != nil {
		return nil, err
	}
	if o.Tools != nil {
		cfg.Tools.Enabled = o.Tools
	}
	builtin, auto, err := tools.Select(cfg.Tools.Enabled, cfg.Tools.Disabled)
	if err != nil {
		return nil, err
	}
	a := &App{Config: cfg, Paths: paths, Registry: registry.New(cfg), Warnings: warnings, Version: o.Version, builtin: builtin, autoTools: auto}

	sess, err := a.openSession(o)
	if err != nil {
		return nil, err
	}
	if msg := agent.SessionProblems(sess); msg != "" {
		a.Warnings = append(a.Warnings, msg)
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
		Tools:     builtin,
		Skills:    skills,
		Sink:      o.Sink,

		SystemFile:        systemFile,
		AppendSystemFiles: appendFiles,
		MaxSteps:          o.MaxSteps,
		RetryDelay:        o.RetryDelay,
	})
	if !o.NoExtensions {
		// Extension tools may not take the name of a built-in tool that is
		// offered, except one added automatically.
		names := slices.DeleteFunc(tools.Names(builtin), func(n string) bool { return slices.Contains(auto, n) })
		a.Ext = ext.NewManager(ext.Options{
			Paths:   paths,
			Config:  cfg,
			Version: o.Version,
			Agent:   agentControl{Agent: a.Agent, cwd: paths.Cwd},
			Builtin: names,
			ExtHost: o.ExtHost,
			Sink:    func(e events.Event) { a.emit(e) },
			// A restarted extension's tools are new objects.
			OnRestart: func() { a.toolNotices(a.refreshTools()) },
		})
	}
	if a.Ext == nil {
		a.Warnings = append(a.Warnings, a.unknownDisabled()...)
	}
	if o.Model != "" && sess.BuildContext().Model != "" && sess.BuildContext().Model != model.ID {
		// An explicit --model wins over the session's recorded model.
		if err := a.Agent.SetModel(model.ID); err != nil {
			return nil, err
		}
	}
	return a, nil
}

// promptFiles makes the system prompt files absolute and checks that they
// can be read, so a typo fails at start rather than on the first request.
func promptFiles(system string, appends []string) (string, []string, error) {
	check := func(p string) (string, error) {
		abs, err := filepath.Abs(p)
		if err != nil {
			return "", err
		}
		if _, err := os.ReadFile(abs); err != nil {
			return "", fmt.Errorf("prompt file: %w", err)
		}
		return abs, nil
	}
	var err error
	if system != "" {
		if system, err = check(system); err != nil {
			return "", nil, err
		}
	}
	var out []string
	for _, p := range appends {
		abs, err := check(p)
		if err != nil {
			return "", nil, err
		}
		out = append(out, abs)
	}
	return system, out, nil
}

// agentControl is what extensions may see and do with the agent.
type agentControl struct {
	*agent.Agent
	cwd string
}

// Info implements ext.AgentControl.
func (c agentControl) Info() ext.SessionInfo { return sessionInfo(c.Agent, c.cwd) }

// Messages implements ext.AgentControl.
func (c agentControl) Messages() []ext.SessionEntry { return sessionMessages(c.Agent) }

// SessionInfo describes the current session (as extensions see it).
func (a *App) SessionInfo() ext.SessionInfo { return sessionInfo(a.Agent, a.Paths.Cwd) }

// SessionMessages returns the current branch's messages with their entry
// ids (as extensions see them).
func (a *App) SessionMessages() []ext.SessionEntry { return sessionMessages(a.Agent) }

func sessionInfo(ag *agent.Agent, cwd string) ext.SessionInfo {
	sess := ag.Session()
	cv := sess.BuildContext()
	m := ag.Model()
	return ext.SessionInfo{
		ID: sess.ID(), File: sess.Path(), Cwd: cwd, Model: m.ID, Busy: ag.Busy(), Aside: cv.Aside,
		ContextTokens: ag.EstimateContext(cv), ContextWindow: m.ContextWindow, Cost: ag.Cost(),
		Tools: ag.ActiveTools(),
	}
}

func sessionMessages(ag *agent.Agent) []ext.SessionEntry {
	cv := ag.Session().BuildContext()
	out := make([]ext.SessionEntry, len(cv.Messages))
	for i, m := range cv.Messages {
		out[i] = ext.SessionEntry{Message: m}
		if i < len(cv.EntryIDs) {
			out[i].ID = cv.EntryIDs[i]
		}
	}
	return out
}

// ParseToolList splits a comma-separated list of tool names, as given to
// --tools. The result is never nil, so an empty list offers no tools
// beyond those added automatically.
func ParseToolList(s string) []string {
	out := []string{}
	for _, n := range strings.Split(s, ",") {
		if n = strings.TrimSpace(n); n != "" {
			out = append(out, n)
		}
	}
	return out
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
	a.toolNotices(a.refreshTools())
}

// ReloadExtensions restarts extensions.
func (a *App) ReloadExtensions(ctx context.Context) error {
	if a.Ext == nil {
		return nil
	}
	err := a.Ext.Reload(ctx)
	a.toolNotices(a.refreshTools())
	return err
}

// StopExtensions shuts extensions down.
func (a *App) StopExtensions() {
	if a.Ext != nil {
		a.Ext.Stop()
	}
}

// refreshTools offers the built-in tools and the extension tools that are
// not disabled. An extension tool replaces a built-in tool of the same name
// that was added automatically; replaced names those.
func (a *App) refreshTools() (replaced []string) {
	extTools := tools.Without(a.Ext.Tools(), a.Agent.Config().Tools.Disabled)
	extNames := tools.Names(extTools)
	var ts []tools.Tool
	for _, t := range a.builtin {
		if n := t.Spec().Name; slices.Contains(a.autoTools, n) && slices.Contains(extNames, n) {
			replaced = append(replaced, n)
			continue
		}
		ts = append(ts, t)
	}
	a.Agent.SetTools(append(ts, extTools...))
	a.Agent.SetHooks(a.Ext)
	return replaced
}

// toolNotices reports built-in tools replaced by extension tools and
// tools.disabled entries that match no tool.
func (a *App) toolNotices(replaced []string) {
	for _, n := range replaced {
		a.emit(events.Notice{Level: "info", Text: fmt.Sprintf("an extension's %s tool replaces the built-in %s", n, n)})
	}
	for _, w := range a.unknownDisabled() {
		a.emit(events.Notice{Level: "warn", Text: w})
	}
}

// unknownDisabled describes tools.disabled entries that name neither a
// built-in tool nor a loaded extension tool, which is usually a typo.
func (a *App) unknownDisabled() []string {
	known := tools.Names(tools.Builtins())
	if a.Ext != nil {
		known = append(known, tools.Names(a.Ext.Tools())...)
	}
	var out []string
	for _, n := range a.Agent.Config().Tools.Disabled {
		if !slices.Contains(known, n) {
			out = append(out, fmt.Sprintf("tools.disabled: no tool named %q is loaded (built-in tools: %s)", n, strings.Join(tools.Names(tools.Builtins()), ", ")))
		}
	}
	return out
}

// SetSetting changes a setting for this run and saves it to the global
// config. Per-model settings apply to the current model. saved is false
// when the value could only be changed for this session; the note says
// checkTheme rejects a theme setting that names no theme. Only theme
// accepts "system".
func (a *App) checkTheme(key, value string) error {
	if key != "theme" && key != "theme_dark" && key != "theme_light" {
		return nil
	}
	set, _ := theme.Load(a.Paths.Home)
	value = strings.TrimSpace(value)
	if _, ok := set.Get(value); ok || key == "theme" && value == theme.System {
		return nil
	}
	return fmt.Errorf("unknown theme %q (one of: %s)", value, strings.Join(set.Names(), ", "))
}

// why, or that the project config overrides the saved value.
func (a *App) SetSetting(key, value string) (saved bool, note string, err error) {
	s, ok := config.LookupSetting(key)
	if !ok {
		return false, "", fmt.Errorf("unknown setting %q", key)
	}
	if a.Agent.Busy() {
		return false, "", agent.ErrBusy
	}
	if err := a.checkTheme(key, value); err != nil {
		return false, "", err
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

// SetExtensionEnabled starts or stops one extension and saves the choice
// in extensions.disabled in the global config. The note is set when the
// project config keeps the extension off on the next start.
func (a *App) SetExtensionEnabled(ctx context.Context, id string, on bool) (note string, err error) {
	if a.Ext == nil {
		return "", errors.New("extensions are turned off for this run")
	}
	if a.Agent.Busy() {
		return "", agent.ErrBusy
	}
	if !slices.ContainsFunc(a.Ext.Extensions(), func(e *ext.Extension) bool { return e.ID == id }) {
		return "", fmt.Errorf("no extension named %q", id)
	}
	if err := config.SetExtensionDisabled(a.Paths.GlobalFile(), id, !on); err != nil {
		return "", err
	}
	cfg := a.Agent.Config()
	cfg.Extensions.Disabled = slices.DeleteFunc(slices.Clone(cfg.Extensions.Disabled), func(n string) bool { return n == id })
	if !on {
		cfg.Extensions.Disabled = append(cfg.Extensions.Disabled, id)
	}
	a.Config = cfg
	a.Agent.SetConfig(cfg)
	if on && a.Paths.ProjectDisables(id) {
		note = "the project's .rho/config.toml also disables " + id + ", so it stays off on the next start"
	}
	err = a.Ext.SetEnabled(ctx, id, on)
	a.refreshTools()
	return note, err
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
