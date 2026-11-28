package bridge

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/NDari/rho/core/config"
	"github.com/NDari/rho/core/provider"
	"github.com/NDari/rho/core/provider/registry"
	"github.com/NDari/rho/core/theme"
	"github.com/NDari/rho/core/tools"
)

// KeyStatus says where a provider's API key comes from.
type KeyStatus struct {
	// Env is the variable the key is read from ("" when none is set up).
	Env string `json:"env,omitempty"`
	// Set reports whether Env is set in the GUI's environment.
	Set bool `json:"set,omitempty"`
	// Raw reports a key stored in the config file.
	Raw bool `json:"raw,omitempty"`
}

// ConfigForm is what the settings view shows: both config files, the
// merged result, and the fields it can edit.
type ConfigForm struct {
	config.View
	Sections       []string       `json:"sections"`
	Fields         []config.Field `json:"fields"`
	ProviderFields []config.Field `json:"provider_fields"`
	ModelFields    []config.Field `json:"model_fields"`
	// Themes are the theme names, for the theme settings.
	Themes []string `json:"themes"`
	// Keys says where each provider's key comes from.
	Keys map[string]KeyStatus `json:"keys"`
	// ProjectRoot is the open project's directory.
	ProjectRoot string `json:"project_root,omitempty"`
	// ProjectInGit and ProjectIgnored tell whether the project config file
	// is in a git repository and ignored by it.
	ProjectInGit   bool `json:"project_in_git,omitempty"`
	ProjectIgnored bool `json:"project_ignored,omitempty"`
	// Running is false on the start-up screen, when no project is open;
	// saved changes then apply on the next try.
	Running bool `json:"running"`
	// Model is the current model's ID.
	Model string `json:"model,omitempty"`
}

// paths returns the open project's paths, or those of the directory that
// failed to open.
func (b *Bridge) paths() (config.Paths, error) {
	if a := b.core(); a != nil {
		return a.Paths, nil
	}
	b.switching.Lock()
	dir := b.dir
	b.switching.Unlock()
	return config.ResolvePaths(dir)
}

// ConfigForm reads the config for the settings view. It works on the
// start-up screen too, so a config that doesn't load can be fixed.
func (b *Bridge) ConfigForm() (ConfigForm, error) {
	p, err := b.paths()
	if err != nil {
		return ConfigForm{}, err
	}
	f := ConfigForm{
		View:           config.LoadView(p),
		Sections:       config.Sections,
		Fields:         config.FileFields,
		ProviderFields: config.ProviderFields,
		ModelFields:    config.ModelFields,
		Keys:           map[string]KeyStatus{},
		Running:        b.core() != nil,
	}
	set, _ := theme.Load(p.Home)
	f.Themes = set.Names()
	provs, _ := f.Effective["providers"].(map[string]any)
	for name, pv := range provs {
		pc, _ := pv.(map[string]any)
		env, _ := pc["api_key_env"].(string)
		raw, _ := pc["api_key"].(string)
		f.Keys[name] = KeyStatus{Env: env, Set: env != "" && os.Getenv(env) != "", Raw: raw != ""}
	}
	if path := p.ProjectFile(); path != "" {
		f.ProjectRoot = p.ProjectRoot
		f.ProjectInGit, f.ProjectIgnored = tools.GitIgnored(path)
	}
	if a := b.core(); a != nil {
		f.Model = a.Agent.Model().ID
	}
	return f, nil
}

// SaveConfig makes the edits to the global or project config file and
// applies them to the open project. version is the file's version from
// ConfigForm. The note, when set, is worth showing.
func (b *Bridge) SaveConfig(project bool, version string, edits []config.Edit) (string, error) {
	a := b.core()
	if a == nil {
		p, err := b.paths()
		if err != nil {
			return "", err
		}
		return "", config.EditFile(p, project, version, edits)
	}
	before := slices.Clone(a.Agent.Config().Extensions.Disabled)
	note, err := a.EditConfig(project, version, edits)
	if err != nil {
		return "", err
	}
	if a.Ext != nil && !slices.Equal(before, a.Agent.Config().Extensions.Disabled) {
		note = strings.TrimPrefix(note+"; extensions turned on or off start or stop on /reload", "; ")
	}
	return note, nil
}

// TestProvider asks the server of provider name, with the settings in
// fields (unsaved ones included, over the built-in provider's), which
// models it offers.
func (b *Bridge) TestProvider(ctx context.Context, name string, fields map[string]any) ([]provider.ServerModel, error) {
	pc, err := config.ProviderFrom(name, fields)
	if err != nil {
		return nil, err
	}
	if pc.BaseURL == "" {
		return nil, errors.New("set the base URL first")
	}
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	models, err := registry.ListModels(ctx, name, pc)
	if errors.Is(ctx.Err(), context.DeadlineExceeded) {
		return nil, errors.New("the server did not answer within 15 seconds")
	}
	return models, err
}

// OpenConfigFile opens the global or project config file in the default
// editor, creating an empty project file first.
func (b *Bridge) OpenConfigFile(project bool) error {
	p, err := b.paths()
	if err != nil {
		return err
	}
	path := p.GlobalFile()
	if project {
		if path = p.ProjectFile(); path == "" {
			return errors.New("project files are ignored for this run")
		}
	}
	if _, err := os.Stat(path); errors.Is(err, os.ErrNotExist) {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			return err
		}
		if err := os.WriteFile(path, []byte("{\n}\n"), 0o644); err != nil {
			return err
		}
	}
	return b.host.OpenFile(path)
}
