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
	"strconv"
	"strings"

	"github.com/NDari/rho/core/jsonc"
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
	DefaultModel string `json:"default_model"`
	// Theme is a theme name, or "system" to use ThemeDark or ThemeLight
	// to match the system (the terminal's background in the TUI).
	Theme      string              `json:"theme"`
	ThemeDark  string              `json:"theme_dark"`
	ThemeLight string              `json:"theme_light"`
	Providers  map[string]Provider `json:"providers"`
	Models     []Model             `json:"models"`
	Bash       Bash                `json:"bash"`
	TUI        TUI                 `json:"tui"`
	Compaction Compaction          `json:"compaction"`
	Extensions Extensions          `json:"extensions"`
	Tools      Tools               `json:"tools"`
	// MaxRetries is the number of retries for transient provider errors.
	MaxRetries int `json:"max_retries"`
	// UpdateCheck lets Rho look for new releases once a day (default true).
	UpdateCheck *bool `json:"update_check"`
}

// UpdateCheckEnabled reports whether update checks are on.
func (c Config) UpdateCheckEnabled() bool { return c.UpdateCheck == nil || *c.UpdateCheck }

// Provider describes an API endpoint.
type Provider struct {
	// Type selects the adapter: "openai-compat" (Chat Completions, the
	// default) or "openai-responses" (Responses API).
	Type string `json:"type"`
	// BaseURL is the API root, e.g. "http://localhost:9931/v1".
	BaseURL string `json:"base_url"`
	// APIKeyEnv names the environment variable that holds the API key.
	APIKeyEnv string `json:"api_key_env"`
	// APIKey is a raw key. Allowed, but discouraged (a warning is emitted).
	APIKey string `json:"api_key"`
	// Flavor enables endpoint-specific handling. "gemini" is detected
	// automatically from the base URL.
	Flavor string `json:"flavor"`
	// Headers are extra HTTP headers sent with each request.
	Headers map[string]string `json:"headers"`
	// RequestsPerMinute and TokensPerMinute make Rho wait before sending
	// a request that would exceed them (0 means no limit).
	RequestsPerMinute int `json:"requests_per_minute"`
	TokensPerMinute   int `json:"tokens_per_minute"`
	// DebugLog is a file that receives one JSON line per request with the
	// exact payload and the response.
	DebugLog string `json:"debug_log"`
	// StreamIdleTimeout is how many seconds a response may go without
	// data (including the wait for the first data) before the request is
	// treated as stalled and retried. 0 means 300; negative disables it.
	StreamIdleTimeout int `json:"stream_idle_timeout"`

	// Defaults for this provider's models; see Model.
	ToolFormat     string `json:"tool_format"`
	SystemPromptIn string `json:"system_prompt_in"`
	DecodeEntities string `json:"decode_entities"`
	StopSequences  *bool  `json:"stop_sequences"`
}

// Tool formats.
const (
	ToolFormatNative = "native"
	ToolFormatXML    = "xml"
)

// Model describes a model available through a provider.
type Model struct {
	// ID is the name used inside Rho (e.g. with --model or /model).
	ID string `json:"id"`
	// Name is the model name sent to the API. Defaults to ID.
	Name             string   `json:"name"`
	Provider         string   `json:"provider"`
	ContextWindow    int      `json:"context_window"`
	MaxOutput        int      `json:"max_output"`
	SupportsImages   bool     `json:"supports_images"`
	SupportsThinking bool     `json:"supports_thinking"`
	ReasoningEffort  string   `json:"reasoning_effort"`
	Pricing          *Pricing `json:"pricing"`

	// ToolFormat is "native" (the API's tool calling, the default) or
	// "xml": tools are described in the prompt and the model calls them by
	// writing XML, for models or gateways without native tool calls.
	ToolFormat string `json:"tool_format"`
	// SystemPromptIn is "system" (default) or "user": put the system prompt
	// at the start of the first user message, for gateways that drop or
	// override system messages. Only used with tool_format "xml".
	SystemPromptIn string `json:"system_prompt_in"`
	// DecodeEntities is "off" (default), "auto" or "always": decode
	// &lt; &gt; &amp; &quot; &apos; in XML tool arguments, for models that
	// escape them. "auto" decodes only values with no raw markup.
	DecodeEntities string `json:"decode_entities"`
	// StopSequences sends a stop sequence so the server ends the response
	// before the model writes a tool result itself (default true). Turn it
	// off if a gateway rejects the "stop" field.
	StopSequences *bool `json:"stop_sequences"`
}

// XMLTools reports whether the model uses XML tool calling.
func (m Model) XMLTools() bool { return m.ToolFormat == ToolFormatXML }

// SendStopSequences reports whether to send XML stop sequences.
func (m Model) SendStopSequences() bool { return m.StopSequences == nil || *m.StopSequences }

// APIName returns the model name to send to the provider.
func (m Model) APIName() string {
	if m.Name != "" {
		return m.Name
	}
	return m.ID
}

// Pricing is in US dollars per million tokens.
type Pricing struct {
	Input      float64 `json:"input"`
	Output     float64 `json:"output"`
	CacheRead  float64 `json:"cache_read"`
	CacheWrite float64 `json:"cache_write"`
}

// Bash configures the bash tool.
type Bash struct {
	// Timeout is the default timeout in seconds.
	Timeout int `json:"timeout"`
	// MaxTimeout caps the timeout the model may request, in seconds.
	MaxTimeout int `json:"max_timeout"`
	// Shell overrides the shell executable (useful on Windows).
	Shell string `json:"shell"`
}

// TUI configures the terminal frontend.
type TUI struct {
	// SideBySideWidth is the minimum terminal width for side-by-side diffs.
	SideBySideWidth int `json:"side_by_side_width"`
	// Keybindings maps action names to key strings.
	Keybindings map[string]string `json:"keybindings"`
	// ShowThinking starts with thinking expanded (ctrl+t toggles it).
	ShowThinking bool `json:"show_thinking"`
	// MouseSelect lets a mouse drag select transcript text and copies it
	// on release (default true). When false, use the terminal's own
	// selection (usually shift+drag).
	MouseSelect *bool `json:"mouse_select"`
}

// MouseSelectEnabled reports whether mouse selection is on.
func (t TUI) MouseSelectEnabled() bool { return t.MouseSelect == nil || *t.MouseSelect }

// Compaction configures automatic compaction.
type Compaction struct {
	// Reserve is the number of tokens kept free below the context window.
	Reserve int `json:"reserve"`
	// KeepRecent is roughly how many recent tokens are kept verbatim.
	KeepRecent int `json:"keep_recent"`
	// Auto compacts before a turn that nears the context limit (default
	// true). When false, compaction runs only on /compact, or when the
	// server rejects a request as too long.
	Auto *bool `json:"auto"`
}

// AutoEnabled reports whether automatic compaction is on.
func (c Compaction) AutoEnabled() bool { return c.Auto == nil || *c.Auto }

// Extensions configures the extension manager.
type Extensions struct {
	// Timeout is the per-call timeout in seconds.
	Timeout int `json:"timeout"`
	// Disabled lists extension names that should not be started.
	Disabled []string `json:"disabled"`
}

// Tools selects the tools offered to the model.
type Tools struct {
	// Enabled lists the built-in tools to offer; nil means read, write,
	// edit and bash. grep, find and ls are added whenever bash is not
	// offered.
	Enabled []string `json:"enabled"`
	// Disabled lists tools, built-in or from extensions, never to offer.
	Disabled []string `json:"disabled"`
}

// Defaults returns the built-in configuration.
func Defaults() Config {
	return Config{
		Theme:      "system",
		ThemeDark:  "gruvbox-dark",
		ThemeLight: "github-light",
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
	// NoProject ignores every Rho file the project provides: its .rho
	// directory (config, SYSTEM.md, APPEND_SYSTEM.md, COMPACT.md, skills,
	// prompts, extensions) and its AGENTS.md files.
	NoProject bool
}

// Project returns the path of elem inside the project's .rho directory,
// or "" when project files are ignored or there is no project directory
// apart from Home.
func (p Paths) Project(elem ...string) string {
	if p.NoProject || p.ProjectDir == "" || p.ProjectDir == p.Home {
		return ""
	}
	return filepath.Join(append([]string{p.ProjectDir}, elem...)...)
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

// FileName is the name of the global and project config files.
const FileName = "config.jsonc"

// legacyFileName is the TOML config file Rho read before it used JSONC.
const legacyFileName = "config.toml"

// Load reads the global and project config files and merges them over the
// defaults. Warnings describe ignored or discouraged settings.
func Load(p Paths) (Config, []string, error) {
	cfg := Defaults()
	var warnings []string

	files := []string{p.GlobalFile()}
	if project := p.ProjectFile(); project != "" {
		files = append(files, project)
	}
	for i, f := range files {
		if w := ConvertLegacy(f); w != "" {
			warnings = append(warnings, w)
		}
		if err := mergeFile(&cfg, f, i > 0, &warnings); err != nil {
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
// repository can't redirect API keys, run commands or turn tools back on.
var restrictedProjectKeys = []string{"providers", "bash.shell", "tools.enabled"}

func mergeFile(cfg *Config, path string, project bool, warnings *[]string) error {
	data, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("read %s: %w", path, err)
	}
	var file Config
	md, err := jsonc.Decode(data, &file)
	if err != nil {
		return fmt.Errorf("parse %s: %w", path, err)
	}
	for _, k := range md.Undecoded() {
		*warnings = append(*warnings, fmt.Sprintf("%s: unknown key %q", path, k))
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
	if defined("theme_dark") {
		cfg.ThemeDark = file.ThemeDark
	}
	if defined("theme_light") {
		cfg.ThemeLight = file.ThemeLight
	}
	if defined("max_retries") {
		cfg.MaxRetries = file.MaxRetries
	}
	if defined("update_check") {
		cfg.UpdateCheck = file.UpdateCheck
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
	if defined("tui", "mouse_select") {
		cfg.TUI.MouseSelect = file.TUI.MouseSelect
	}
	if defined("tui", "show_thinking") {
		cfg.TUI.ShowThinking = file.TUI.ShowThinking
	}
	if defined("compaction", "auto") {
		cfg.Compaction.Auto = file.Compaction.Auto
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
	if defined("tools", "enabled") {
		cfg.Tools.Enabled = file.Tools.Enabled
		if cfg.Tools.Enabled == nil {
			cfg.Tools.Enabled = []string{}
		}
	}
	if defined("tools", "disabled") {
		cfg.Tools.Disabled = append(cfg.Tools.Disabled, file.Tools.Disabled...)
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
	if defined("requests_per_minute") {
		out.RequestsPerMinute = over.RequestsPerMinute
	}
	if defined("tokens_per_minute") {
		out.TokensPerMinute = over.TokensPerMinute
	}
	if defined("debug_log") {
		out.DebugLog = over.DebugLog
	}
	if defined("stream_idle_timeout") {
		out.StreamIdleTimeout = over.StreamIdleTimeout
	}
	if defined("tool_format") {
		out.ToolFormat = over.ToolFormat
	}
	if defined("system_prompt_in") {
		out.SystemPromptIn = over.SystemPromptIn
	}
	if defined("decode_entities") {
		out.DecodeEntities = over.DecodeEntities
	}
	if defined("stop_sequences") {
		out.StopSequences = over.StopSequences
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
		errs = append(errs, checkToolOptions("model "+strconv.Quote(m.ID), m.ToolFormat, m.SystemPromptIn, m.DecodeEntities)...)
	}
	for name, p := range c.Providers {
		if p.Type != ProviderOpenAICompat && p.Type != ProviderOpenAIResponses {
			errs = append(errs, fmt.Errorf("provider %q: unsupported type %q", name, p.Type))
		}
		if p.BaseURL == "" {
			errs = append(errs, fmt.Errorf("provider %q: base_url is required", name))
		}
		if p.RequestsPerMinute < 0 || p.TokensPerMinute < 0 {
			errs = append(errs, fmt.Errorf("provider %q: rate limits must not be negative", name))
		}
		errs = append(errs, checkToolOptions("provider "+strconv.Quote(name), p.ToolFormat, p.SystemPromptIn, p.DecodeEntities)...)
	}
	return errors.Join(errs...)
}

func checkToolOptions(what, format, systemIn, decode string) []error {
	var errs []error
	check := func(key, v string, allowed ...string) {
		if v != "" && !slices.Contains(allowed, v) {
			errs = append(errs, fmt.Errorf("%s: %s must be one of %s, not %q", what, key, strings.Join(allowed, ", "), v))
		}
	}
	check("tool_format", format, ToolFormatNative, ToolFormatXML)
	check("system_prompt_in", systemIn, "system", "user")
	check("decode_entities", decode, "off", "auto", "always")
	return errs
}

// inherit fills the model's unset tool options from its provider.
func (m Model) inherit(p Provider) Model {
	if m.ToolFormat == "" {
		m.ToolFormat = p.ToolFormat
	}
	if m.SystemPromptIn == "" {
		m.SystemPromptIn = p.SystemPromptIn
	}
	if m.DecodeEntities == "" {
		m.DecodeEntities = p.DecodeEntities
	}
	if m.StopSequences == nil {
		m.StopSequences = p.StopSequences
	}
	return m
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

// ErrNoModel is returned when no model is configured or given.
var ErrNoModel = errors.New("no model configured: set default_model or models in " + FileName + ", or pass --model provider/name")

// ResolveModel finds a model by id. A spec of the form "provider/name" that
// doesn't match a configured id creates an ad-hoc model with defaults.
func (c Config) ResolveModel(spec string) (Model, error) {
	if spec == "" {
		spec = c.DefaultModel
	}
	if spec == "" {
		if len(c.Models) > 0 {
			return c.Models[0].withDefaults().inherit(c.Providers[c.Models[0].Provider]), nil
		}
		return Model{}, ErrNoModel
	}
	for _, m := range c.Models {
		if m.ID == spec {
			return m.withDefaults().inherit(c.Providers[m.Provider]), nil
		}
	}
	prov, name, ok := strings.Cut(spec, "/")
	if ok {
		if pc, exists := c.Providers[prov]; exists {
			return Model{
				ID:            spec,
				Name:          name,
				Provider:      prov,
				ContextWindow: DefaultContextWindow,
				MaxOutput:     DefaultMaxOutput,
			}.inherit(pc), nil
		}
	}
	return Model{}, fmt.Errorf("unknown model %q (use a configured id or provider/name)", spec)
}
