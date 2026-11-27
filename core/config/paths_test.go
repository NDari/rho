package config

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func TestHomeDir(t *testing.T) {
	userHome := t.TempDir()
	rhoHome := t.TempDir()
	tests := []struct {
		name    string
		rhoHome string
		chdir   string
		want    string
	}{
		{"RHO_HOME absolute", rhoHome, "", rhoHome},
		{"RHO_HOME relative to the working directory", "rel", rhoHome, filepath.Join(rhoHome, "rel")},
		{"default under the user's home", "", "", filepath.Join(userHome, ".rho")},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("HOME", userHome)
			t.Setenv("USERPROFILE", userHome) // os.UserHomeDir on Windows
			t.Setenv("RHO_HOME", tc.rhoHome)
			if tc.chdir != "" {
				t.Chdir(tc.chdir)
			}
			got, err := HomeDir()
			if err != nil {
				t.Fatal(err)
			}
			if got != tc.want {
				t.Errorf("HomeDir() = %q, want %q", got, tc.want)
			}
		})
	}
}

// insideRepo reports whether dir or one of its ancestors has a .git entry,
// in which case FindProjectRoot cannot fall back to dir itself.
func insideRepo(dir string) bool {
	for d := dir; ; d = filepath.Dir(d) {
		if _, err := os.Stat(filepath.Join(d, ".git")); err == nil {
			return true
		}
		if filepath.Dir(d) == d {
			return false
		}
	}
}

func TestResolvePaths(t *testing.T) {
	home := t.TempDir()
	t.Setenv("RHO_HOME", home)

	repo := t.TempDir()
	if err := os.Mkdir(filepath.Join(repo, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	deep := filepath.Join(repo, "a", "b")
	if err := os.MkdirAll(deep, 0o755); err != nil {
		t.Fatal(err)
	}
	plain := t.TempDir()

	tests := []struct {
		name     string
		cwd      string
		chdir    string
		wantCwd  string
		wantRoot string
	}{
		{"repository root", repo, "", repo, repo},
		{"subdirectory of a repository", deep, "", deep, repo},
		{"relative cwd", filepath.Join("a", "b"), repo, deep, repo},
		{"no repository", plain, "", plain, plain},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if tc.wantRoot == plain && insideRepo(plain) {
				t.Skip("the temporary directory is inside a git repository")
			}
			if tc.chdir != "" {
				t.Chdir(tc.chdir)
			}
			got, err := ResolvePaths(tc.cwd)
			if err != nil {
				t.Fatal(err)
			}
			want := Paths{Home: home, Cwd: tc.wantCwd, ProjectRoot: tc.wantRoot, ProjectDir: filepath.Join(tc.wantRoot, ".rho")}
			if got != want {
				t.Errorf("ResolvePaths(%q) =\n %+v\nwant\n %+v", tc.cwd, got, want)
			}
			if got.GlobalFile() != filepath.Join(home, FileName) {
				t.Errorf("GlobalFile = %q", got.GlobalFile())
			}
		})
	}
}

// TestLoadMergesBuiltinProvider checks that a global config entry for a
// built-in provider changes only the keys it sets.
func TestLoadMergesBuiltinProvider(t *testing.T) {
	builtin := Defaults().Providers["openai"]
	no := false
	tests := []struct {
		name string
		file string
		want func(p *Provider)
	}{
		{"nothing set keeps the built-in values", `{
	"providers": {
		"openai": {}
	}
}
`, func(*Provider) {}},
		{"base_url only", `{
	"providers": {
		"openai": {
			"base_url": "http://localhost:9931/v1"
		}
	}
}
`, func(p *Provider) { p.BaseURL = "http://localhost:9931/v1" }},
		{"key and flavor", `{
	"providers": {
		"openai": {
			"api_key_env": "MY_KEY",
			"flavor":      "gemini"
		}
	}
}
`, func(p *Provider) { p.APIKeyEnv = "MY_KEY"; p.Flavor = "gemini" }},
		{"type", `{
	"providers": {
		"openai": {
			"type": "openai-responses"
		}
	}
}
`, func(p *Provider) { p.Type = ProviderOpenAIResponses }},
		{"limits, headers and debug log", `{
	"providers": {
		"openai": {
			"requests_per_minute": 10,
			"tokens_per_minute":   5000,
			"debug_log":           "/tmp/debug.jsonl",
			"headers": {
				"X-Test": "1"
			}
		}
	}
}
`, func(p *Provider) {
			p.RequestsPerMinute, p.TokensPerMinute, p.DebugLog = 10, 5000, "/tmp/debug.jsonl"
			p.Headers = map[string]string{"X-Test": "1"}
		}},
		{"xml tool defaults", `{
	"providers": {
		"openai": {
			"tool_format":      "xml",
			"system_prompt_in": "user",
			"decode_entities":  "auto",
			"stop_sequences":   false
		}
	}
}
`, func(p *Provider) {
			p.ToolFormat, p.SystemPromptIn, p.DecodeEntities, p.StopSequences = ToolFormatXML, "user", "auto", &no
		}},
		{"raw api key", `{
	"providers": {
		"openai": {
			"api_key": "sk-test"
		}
	}
}
`, func(p *Provider) { p.APIKey = "sk-test" }},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			home := t.TempDir()
			write(t, filepath.Join(home, FileName), tc.file)
			cfg, _, err := Load(Paths{Home: home})
			if err != nil {
				t.Fatalf("Load: %v", err)
			}
			want := builtin
			tc.want(&want)
			if got := cfg.Providers["openai"]; !reflect.DeepEqual(got, want) {
				t.Errorf("provider =\n %+v\nwant\n %+v", got, want)
			}
			if got, want := cfg.Providers["openrouter"], Defaults().Providers["openrouter"]; !reflect.DeepEqual(got, want) {
				t.Errorf("an untouched built-in provider changed: %+v", got)
			}
		})
	}
}

func TestLoadMouseSelect(t *testing.T) {
	tests := []struct {
		file string
		want bool
	}{
		{"", true},
		{`{"tui": {"mouse_select": true}}`, true},
		{`{"tui": {"mouse_select": false}}`, false},
	}
	for _, tc := range tests {
		home := t.TempDir()
		write(t, filepath.Join(home, FileName), tc.file)
		cfg, _, err := Load(Paths{Home: home})
		if err != nil {
			t.Fatal(err)
		}
		if got := cfg.TUI.MouseSelectEnabled(); got != tc.want {
			t.Errorf("config %q: MouseSelectEnabled = %v, want %v", tc.file, got, tc.want)
		}
	}
}

func TestProjectSetsAndDisables(t *testing.T) {
	setting := func(key string) Setting {
		s, ok := LookupSetting(key)
		if !ok {
			t.Fatalf("no setting %q", key)
		}
		return s
	}
	const project = `{
	"theme": "light",
	"compaction": {
		"reserve": 8000
	},
	"extensions": {
		"disabled": ["noisy"]
	},
	"providers": {
		"openai": {
			"models": [{"id": "local"}]
		}
	}
}
`
	tests := []struct {
		name      string
		project   string // "" writes no project config
		noProject bool
		key       string
		model     string
		wantSets  bool
		disables  string
		wantDis   bool
	}{
		{"top-level key", project, false, "theme", "local", true, "noisy", true},
		{"nested key", project, false, "compaction.reserve", "local", true, "other", false},
		{"key not in the project", project, false, "max_retries", "local", false, "noisy", true},
		{"per-model setting for a defined model", project, false, "reasoning_effort", "local", true, "", false},
		{"per-model setting for another model", project, false, "reasoning_effort", "remote", false, "", false},
		{"project files ignored", project, true, "theme", "local", false, "noisy", false},
		{"no project config", "", false, "theme", "local", false, "noisy", false},
		{"unparsable project config", `{"theme": }`, false, "theme", "local", false, "noisy", false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			p := Paths{Home: t.TempDir(), Cwd: root, ProjectRoot: root, ProjectDir: filepath.Join(root, ".rho"), NoProject: tc.noProject}
			if tc.project != "" {
				write(t, filepath.Join(p.ProjectDir, FileName), tc.project)
			}
			if got := p.ProjectSets(setting(tc.key), tc.model); got != tc.wantSets {
				t.Errorf("ProjectSets(%s, %s) = %v, want %v", tc.key, tc.model, got, tc.wantSets)
			}
			if tc.disables != "" {
				if got := p.ProjectDisables(tc.disables); got != tc.wantDis {
					t.Errorf("ProjectDisables(%s) = %v, want %v", tc.disables, got, tc.wantDis)
				}
			}
		})
	}
}
