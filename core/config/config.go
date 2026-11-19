// Package config loads and merges Rho's global and project configuration.
package config

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"

	"github.com/BurntSushi/toml"
)

// Provider types understood by the provider registry.
const (
	// ProviderOpenAICompat uses the Chat Completions API.
	ProviderOpenAICompat = "openai-compat"
	// ProviderOpenAIResponses uses the Responses API.
	ProviderOpenAIResponses = "openai-responses"
)

// Config is the merged configuration.
type Config struct {
	DefaultModel string              `toml:"default_model"`
	Theme        string              `toml:"theme"`
	Providers    map[string]Provider `toml:"providers"`
	Models       []Model             `toml:"models"`
	Bash         Bash                `toml:"bash"`
	TUI          TUI                 `toml:"tui"`
	Compaction   Compaction          `toml:"compaction"`
	Extensions   Extensions          `toml:"extensions"`
	// MaxRetries is the number of retries for transient provider errors.
	MaxRetries int `toml:"max_retries"`
}

// Provider describes an API endpoint.
type Provider struct {
	// Type selects the adapter: "openai-compat" (Chat Completions, the
	// default) or "openai-responses" (Responses API).
	Type string `toml:"type"`
	// BaseURL is the API root, e.g. "http://localhost:8080/v1".
	BaseURL string `toml:"base_url"`
	// APIKeyEnv names the environment variable that holds the API key.
	APIKeyEnv string `toml:"api_key_env"`
	// APIKey is a raw key. Allowed, but discouraged (a warning is emitted).
	APIKey string `toml:"api_key"`
	// Flavor enables endpoint-specific handling. "gemini" is detected
	// automatically from the base URL.
	Flavor string `toml:"flavor"`
	// Headers are extra HTTP headers sent with each request.
	Headers map[string]string `toml:"headers"`
}

// Model describes a model available through a provider.
type Model struct {
	// ID is the name used inside Rho (e.g. with --model or /model).
	ID string `toml:"id"`
	// Name is the model name sent to the API. Defaults to ID.
	Name             string   `toml:"name"`
	Provider         string   `toml:"provider"`
	ContextWindow    int      `toml:"context_window"`
	MaxOutput        int      `toml:"max_output"`
	SupportsImages   bool     `toml:"supports_images"`
	SupportsThinking bool     `toml:"supports_thinking"`
	ReasoningEffort  string   `toml:"reasoning_effort"`
	Pricing          *Pricing `toml:"pricing"`
}

// APIName returns the model name to send to the provider.
func (m Model) APIName() string {
	if m.Name != "" {
		return m.Name
	}
	return m.ID
}

// Pricing is in US dollars per million tokens.
type Pricing struct {
	Input      float64 `toml:"input"`
	Output     float64 `toml:"output"`
	CacheRead  float64 `toml:"cache_read"`
	CacheWrite float64 `toml:"cache_write"`
}

// Bash configures the bash tool.
type Bash struct {
	// Timeout is the default timeout in seconds.
	Timeout int `toml:"timeout"`
	// MaxTimeout caps the timeout the model may request, in seconds.
	MaxTimeout int `toml:"max_timeout"`
	// Shell overrides the shell executable (useful on Windows).
	Shell string `toml:"shell"`
}

// TUI configures the terminal frontend.
type TUI struct {
	// SideBySideWidth is the minimum terminal width for side-by-side diffs.
	SideBySideWidth int `toml:"side_by_side_width"`
	// Keybindings maps action names to key strings.
	Keybindings map[string]string `toml:"keybindings"`
}

// Compaction configures automatic compaction.
type Compaction struct {
	// Reserve is the number of tokens kept free below the context window.
	Reserve int `toml:"reserve"`
	// KeepRecent is roughly how many recent tokens are kept verbatim.
	KeepRecent int `toml:"keep_recent"`
}

// Extensions configures the extension manager.
type Extensions struct {
	// Timeout is the per-call timeout in seconds.
	Timeout int `toml:"timeout"`
	// Disabled lists extension names that should not be started.
	Disabled []string `toml:"disabled"`
}

// Defaults returns the built-in configuration.
func Defaults() Config {
	return Config{
		Theme: "dark",
		Providers: map[string]Provider{
			"openai": {
				Type:      ProviderOpenAICompat,
				BaseURL:   "https://api.openai.com/v1",
				APIKeyEnv: "OPENAI_API_KEY",
			},
			"openrouter": {
				Type:      ProviderOpenAICompat,
				BaseURL:   "https://openrouter.ai/api/v1",
				APIKeyEnv: "OPENROUTER_API_KEY",
			},
			"gemini": {
				Type:      ProviderOpenAICompat,
				BaseURL:   "https://generativelanguage.googleapis.com/v1beta/openai/",
				APIKeyEnv: "GEMINI_API_KEY",
				Flavor:    "gemini",
			},
		},
		Bash:       Bash{Timeout: 120, MaxTimeout: 600},
		TUI:        TUI{SideBySideWidth: 160},
		Compaction: Compaction{Reserve: 16000, KeepRecent: 20000},
		Extensions: Extensions{Timeout: 30},
		MaxRetries: 3,
	}
}

// Paths holds the resolved locations used by Rho.
type Paths struct {
	// Home is the user-facing Rho directory (default ~/.rho, or $RHO_HOME).
	Home string
	// Cwd is the session working directory.
	Cwd string
	// ProjectRoot is the nearest ancestor of Cwd containing .git, or Cwd.
	ProjectRoot string
	// ProjectDir is ProjectRoot/.rho.
	ProjectDir string
}

// ResolvePaths computes the standard paths for cwd.
func ResolvePaths(cwd string) (Paths, error) {
	home, err := HomeDir()
	if err != nil {
		return Paths{}, err
	}
	abs, err := filepath.Abs(cwd)
	if err != nil {
		return Paths{}, err
	}
	root := FindProjectRoot(abs)
	return Paths{Home: home, Cwd: abs, ProjectRoot: root, ProjectDir: filepath.Join(root, ".rho")}, nil
}

// HomeDir returns $RHO_HOME or ~/.rho.
func HomeDir() (string, error) {
	if h := os.Getenv("RHO_HOME"); h != "" {
		return filepath.Abs(h)
	}
	uh, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("locate home directory: %w", err)
	}
	return filepath.Join(uh, ".rho"), nil
}

// FindProjectRoot walks up from dir looking for a .git entry. It returns dir
// itself when none is found.
func FindProjectRoot(dir string) string {
	d := dir
	for {
		if _, err := os.Stat(filepath.Join(d, ".git")); err == nil {
			return d
		}
		parent := filepath.Dir(d)
		if parent == d {
			return dir
		}
		d = parent
	}
}

// Load reads the global and project config files and merges them over the
// defaults. Warnings describe ignored or discouraged settings.
func Load(p Paths) (Config, []string, error) {
	cfg := Defaults()
	var warnings []string

	global := filepath.Join(p.Home, "config.toml")
	if err := mergeFile(&cfg, global, false, &warnings); err != nil {
		return cfg, warnings, err
	}
	if p.ProjectDir != "" && p.ProjectDir != p.Home {
		project := filepath.Join(p.ProjectDir, "config.toml")
		if err := mergeFile(&cfg, project, true, &warnings); err != nil {
			return cfg, warnings, err
		}
	}
	for name, prov := range cfg.Providers {
		if prov.APIKey != "" {
			warnings = append(warnings, fmt.Sprintf("provider %q stores a raw api_key in config; prefer api_key_env", name))
		}
	}
	return cfg, warnings, cfg.Validate()
}

// restrictedProjectKeys may only be set in the global config, so a cloned
// repository can't redirect API keys or run commands.
var restrictedProjectKeys = []string{"providers", "bash.shell"}

func mergeFile(cfg *Config, path string, project bool, warnings *[]string) error {
	data, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("read %s: %w", path, err)
	}
	var file Config
	md, err := toml.Decode(string(data), &file)
	if err != nil {
		return fmt.Errorf("parse %s: %w", path, err)
	}
	for _, k := range md.Undecoded() {
		*warnings = append(*warnings, fmt.Sprintf("%s: unknown key %q", path, k.String()))
	}
	defined := func(key ...string) bool {
		if !md.IsDefined(key...) {
			return false
		}
		if project && slices.Contains(restrictedProjectKeys, strings.Join(key, ".")) {
			*warnings = append(*warnings, fmt.Sprintf("%s: %q can only be set in the global config; ignored", path, strings.Join(key, ".")))
			return false
		}
		return true
	}

	if defined("default_model") {
		cfg.DefaultModel = file.DefaultModel
	}
	if defined("theme") {
		cfg.Theme = file.Theme
	}
	if defined("max_retries") {
		cfg.MaxRetries = file.MaxRetries
	}
	if defined("providers") {
		names := make([]string, 0, len(file.Providers))
		for n := range file.Providers {
			names = append(names, n)
		}
		sort.Strings(names)
		for _, n := range names {
			np := file.Providers[n]
			if base, ok := cfg.Providers[n]; ok {
				np = mergeProvider(base, np, func(k string) bool { return md.IsDefined("providers", n, k) })
			}
			if np.Type == "" {
				np.Type = ProviderOpenAICompat
			}
			cfg.Providers[n] = np
		}
	}
	if defined("models") {
		for _, m := range file.Models {
			cfg.Models = upsertModel(cfg.Models, m)
		}
	}
	if defined("bash", "timeout") {
		cfg.Bash.Timeout = file.Bash.Timeout
	}
	if defined("bash", "max_timeout") {
		cfg.Bash.MaxTimeout = file.Bash.MaxTimeout
	}
	if defined("bash", "shell") {
		cfg.Bash.Shell = file.Bash.Shell
	}
	if defined("tui", "side_by_side_width") {
		cfg.TUI.SideBySideWidth = file.TUI.SideBySideWidth
	}
	if defined("tui", "keybindings") {
		if cfg.TUI.Keybindings == nil {
			cfg.TUI.Keybindings = map[string]string{}
		}
		for k, v := range file.TUI.Keybindings {
			cfg.TUI.Keybindings[k] = v
		}
	}
	if defined("compaction", "reserve") {
		cfg.Compaction.Reserve = file.Compaction.Reserve
	}
	if defined("compaction", "keep_recent") {
		cfg.Compaction.KeepRecent = file.Compaction.KeepRecent
	}
	if defined("extensions", "timeout") {
		cfg.Extensions.Timeout = file.Extensions.Timeout
	}
	if defined("extensions", "disabled") {
		cfg.Extensions.Disabled = append(cfg.Extensions.Disabled, file.Extensions.Disabled...)
	}
	return nil
}

func mergeProvider(base, over Provider, defined func(string) bool) Provider {
	out := base
	if defined("type") {
		out.Type = over.Type
	}
	if defined("base_url") {
		out.BaseURL = over.BaseURL
	}
	if defined("api_key_env") {
		out.APIKeyEnv = over.APIKeyEnv
	}
	if defined("api_key") {
		out.APIKey = over.APIKey
	}
	if defined("flavor") {
		out.Flavor = over.Flavor
	}
	if defined("headers") {
		out.Headers = over.Headers
	}
	return out
}

func upsertModel(models []Model, m Model) []Model {
	for i := range models {
		if models[i].ID == m.ID {
			models[i] = m
			return models
		}
	}
	return append(models, m)
}

// Validate checks internal consistency.
func (c Config) Validate() error {
	var errs []error
	seen := map[string]bool{}
	for _, m := range c.Models {
		if m.ID == "" {
			errs = append(errs, errors.New("model with empty id"))
			continue
		}
		if seen[m.ID] {
			errs = append(errs, fmt.Errorf("model %q defined twice", m.ID))
		}
		seen[m.ID] = true
		if _, ok := c.Providers[m.Provider]; !ok {
			errs = append(errs, fmt.Errorf("model %q: unknown provider %q", m.ID, m.Provider))
		}
	}
	for name, p := range c.Providers {
		if p.Type != ProviderOpenAICompat && p.Type != ProviderOpenAIResponses {
			errs = append(errs, fmt.Errorf("provider %q: unsupported type %q", name, p.Type))
		}
		if p.BaseURL == "" {
			errs = append(errs, fmt.Errorf("provider %q: base_url is required", name))
		}
	}
	return errors.Join(errs...)
}

// Default values for models given only as "provider/name".
const (
	DefaultContextWindow = 128000
	DefaultMaxOutput     = 16384
)

func (m Model) withDefaults() Model {
	if m.ContextWindow <= 0 {
		m.ContextWindow = DefaultContextWindow
	}
	if m.MaxOutput <= 0 {
		m.MaxOutput = DefaultMaxOutput
	}
	return m
}

// ResolveModel finds a model by id. A spec of the form "provider/name" that
// doesn't match a configured id creates an ad-hoc model with defaults.
func (c Config) ResolveModel(spec string) (Model, error) {
	if spec == "" {
		spec = c.DefaultModel
	}
	if spec == "" {
		if len(c.Models) > 0 {
			return c.Models[0].withDefaults(), nil
		}
		return Model{}, errors.New("no model configured: set default_model or [[models]] in config.toml, or pass --model provider/name")
	}
	for _, m := range c.Models {
		if m.ID == spec {
			return m.withDefaults(), nil
		}
	}
	prov, name, ok := strings.Cut(spec, "/")
	if ok {
		if _, exists := c.Providers[prov]; exists {
			return Model{
				ID:            spec,
				Name:          name,
				Provider:      prov,
				ContextWindow: DefaultContextWindow,
				MaxOutput:     DefaultMaxOutput,
			}, nil
		}
	}
	return Model{}, fmt.Errorf("unknown model %q (use a configured id or provider/name)", spec)
}
