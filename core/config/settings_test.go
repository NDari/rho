package config

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestSaveSetting(t *testing.T) {
	models := "{\n" +
		"\t\"default_model\": \"b\",\n" +
		"\t\"providers\": {\n" +
		"\t\t\"p\": {\n" +
		"\t\t\t\"base_url\": \"http://localhost:9931/v1\",\n" +
		"\t\t\t\"models\": [\n" +
		"\t\t\t\t{\"id\": \"a\", \"max_output\": 100},\n" +
		"\t\t\t\t// the second one\n" +
		"\t\t\t\t{\n" +
		"\t\t\t\t\t\"id\":               \"b\", // mine\n" +
		"\t\t\t\t\t\"max_output\":       200,\n" +
		"\t\t\t\t\t\"reasoning_effort\": \"low\",\n" +
		"\t\t\t\t},\n" +
		"\t\t\t],\n" +
		"\t\t},\n" +
		"\t},\n" +
		"}\n"
	tests := []struct {
		name, in, key, model, value string
		want                        string
		err                         error
	}{
		{name: "new file", key: "theme", value: "light",
			want: "{\n\t\"theme\": \"light\"\n}\n"},
		{name: "new file, table", key: "bash.timeout", value: "60",
			want: "{\n\t\"bash\": {\n\t\t\"timeout\": 60\n\t}\n}\n"},
		{name: "replace keeps comments",
			in:  "// top\n{\n\t\"theme\": \"dark\", // mine\n\t\"bash\": {\"timeout\": 5},\n}\n",
			key: "theme", value: "light",
			want: "// top\n{\n\t\"theme\": \"light\", // mine\n\t\"bash\":  {\"timeout\": 5},\n}\n"},
		{name: "same key in another table is left alone",
			in:  "{\n\t\"extensions\": {\"timeout\": 30},\n\t\"bash\": {\"timeout\": 5},\n}\n",
			key: "bash.timeout", value: "60",
			want: "{\n\t\"extensions\": {\"timeout\": 30},\n\t\"bash\":       {\"timeout\": 60},\n}\n"},
		{name: "add to existing table",
			in:  "{\n\t\"bash\": {\n\t\t\"timeout\": 5,\n\t},\n}\n",
			key: "bash.max_timeout", value: "600",
			want: "{\n\t\"bash\": {\n\t\t\"timeout\":     5,\n\t\t\"max_timeout\": 600,\n\t},\n}\n"},
		{name: "bool", in: "{}\n", key: "compaction.auto", value: "false",
			want: "{\"compaction\": {\n\t\"auto\": false\n}}\n"},
		{name: "replace per-model", in: models, key: "reasoning_effort", model: "b", value: "high",
			want: strings.Replace(models, `"low"`, `"high"`, 1)},
		{name: "add per-model", in: models, key: "reasoning_effort", model: "a", value: "medium",
			want: strings.Replace(models, `{"id": "a", "max_output": 100}`, `{"id": "a", "max_output": 100, "reasoning_effort": "medium"}`, 1)},
		{name: "remove per-model (optional, empty)", in: models, key: "reasoning_effort", model: "b", value: "",
			want: strings.Replace(strings.Replace(strings.Replace(models,
				"\t\t\t\t\t\"reasoning_effort\": \"low\",\n", "", 1),
				`"id":               "b"`, `"id":         "b"`, 1),
				`"max_output":       200`, `"max_output": 200`, 1)},
		{name: "model not in file", in: models, key: "reasoning_effort", model: "c", value: "high", err: ErrModelNotInFile},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), FileName)
			if tt.in != "" {
				if err := os.WriteFile(path, []byte(tt.in), 0o644); err != nil {
					t.Fatal(err)
				}
			}
			s, ok := LookupSetting(tt.key)
			if !ok {
				t.Fatalf("no setting %s", tt.key)
			}
			err := SaveSetting(path, s, tt.model, tt.value)
			if tt.err != nil {
				if !errors.Is(err, tt.err) {
					t.Fatalf("err = %v, want %v", err, tt.err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if got, _ := os.ReadFile(path); string(got) != tt.want {
				t.Errorf("got\n%s\nwant\n%s", got, tt.want)
			}
		})
	}
}

func TestSaveSettingRefusesBadFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), FileName)
	in := "{\"bash\": 5}\n"
	if err := os.WriteFile(path, []byte(in), 0o644); err != nil {
		t.Fatal(err)
	}
	s, _ := LookupSetting("bash.timeout")
	if err := SaveSetting(path, s, "", "60"); err == nil {
		t.Fatal("bash is not an object: expected an error")
	}
	if got, _ := os.ReadFile(path); string(got) != in {
		t.Errorf("file changed: %q", got)
	}
}

func TestNext(t *testing.T) {
	choices := []string{"30", "60", "120"}
	for _, tt := range []struct{ cur, want string }{
		{"30", "60"}, {"120", "30"}, {"45", "60"}, {"500", "30"}, {"x", "30"},
	} {
		if got := Next(choices, tt.cur, true); got != tt.want {
			t.Errorf("Next(%q) = %q, want %q", tt.cur, got, tt.want)
		}
	}
}

func TestSaveSettingRoundTrip(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, FileName)
	if err := os.WriteFile(path, []byte("// keep me\n{\n\t\"bash\": {\n\t\t\"timeout\": 5,\n\t},\n}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	s, _ := LookupSetting("bash.timeout")
	if err := SaveSetting(path, s, "", "300"); err != nil {
		t.Fatal(err)
	}
	cfg, _, err := Load(Paths{Home: dir})
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Bash.Timeout != 300 {
		t.Errorf("timeout = %d", cfg.Bash.Timeout)
	}
	// Windows has no Unix permission bits to keep.
	if fi, _ := os.Stat(path); runtime.GOOS != "windows" && fi.Mode().Perm() != 0o600 {
		t.Errorf("mode = %v, want 0600", fi.Mode().Perm())
	}
	data, _ := os.ReadFile(path)
	if string(data) != "// keep me\n{\n\t\"bash\": {\n\t\t\"timeout\": 300,\n\t},\n}\n" {
		t.Errorf("file = %q", data)
	}
}

func TestApplySetting(t *testing.T) {
	c := Defaults()
	c.Models = []Model{{ID: "m", Provider: "openai"}}
	s, _ := LookupSetting("max_retries")
	if _, err := s.Apply(c, "", "-1"); err == nil {
		t.Error("negative: expected an error")
	}
	got, err := s.Apply(c, "", " 7 ")
	if err != nil || got.MaxRetries != 7 || c.MaxRetries == 7 {
		t.Errorf("apply: %v, %d", err, got.MaxRetries)
	}
	d, _ := LookupSetting("default_model")
	if _, err := d.Apply(c, "", "nope"); err == nil {
		t.Error("unknown model: expected an error")
	}
	if got, err := d.Apply(c, "", "m"); err != nil || got.DefaultModel != "m" {
		t.Errorf("default_model: %v", err)
	}
}

func TestApplyPerModelAndBool(t *testing.T) {
	c := Defaults()
	c.Models = []Model{{ID: "m", Provider: "openai"}}
	r, _ := LookupSetting("reasoning_effort")
	got, err := r.Apply(c, "m", "high")
	if err != nil || got.Models[0].ReasoningEffort != "high" || c.Models[0].ReasoningEffort != "" {
		t.Fatalf("apply: %v %+v", err, got.Models)
	}
	if v := r.Value(got, "m"); v != "high" {
		t.Errorf("value = %q", v)
	}
	if got, _ := r.Apply(got, "m", "default"); got.Models[0].ReasoningEffort != "" {
		t.Error("default did not clear")
	}
	adhoc, err := r.Apply(c, "openai/x", "low")
	if err != nil || r.Value(adhoc, "openai/x") != "low" {
		t.Errorf("ad-hoc model: %v", err)
	}
	if m, _ := adhoc.ResolveModel("openai/x"); m.APIName() != "x" {
		t.Errorf("ad-hoc api name = %q", m.APIName())
	}

	a, _ := LookupSetting("compaction.auto")
	if a.Value(c, "") != "true" {
		t.Error("auto should default to true")
	}
	off, err := a.Apply(c, "", "off")
	if err != nil || off.Compaction.AutoEnabled() {
		t.Errorf("off: %v", err)
	}
	if _, err := a.Apply(c, "", "maybe"); err == nil {
		t.Error("expected an error for a non-bool")
	}
}

func TestSetExtensionDisabled(t *testing.T) {
	tests := []struct {
		name     string
		in       string
		ext      string
		disabled bool
		want     string
	}{
		{"new file", "", "a", true, "{\n\t\"extensions\": {\n\t\t\"disabled\": [\"a\"]\n\t}\n}\n"},
		{"add to list", "{\n\t\"extensions\": {\n\t\t\"timeout\":  30,\n\t\t\"disabled\": [\"a\"], // off\n\t},\n}\n", "b", true,
			"{\n\t\"extensions\": {\n\t\t\"timeout\":  30,\n\t\t\"disabled\": [\"a\", \"b\"], // off\n\t},\n}\n"},
		{"already there", "{\"extensions\": {\"disabled\": [\"a\"]}}", "a", true, "{\"extensions\": {\"disabled\": [\"a\"]}}"},
		{"remove last", "{\"extensions\": {\"disabled\": [\"a\"]}}\n", "a", false, "{\"extensions\": {\"disabled\": []}}\n"},
		{"remove missing", "{\"theme\": \"dark\"}", "a", false, "{\"theme\": \"dark\"}"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), FileName)
			if tt.in != "" {
				if err := os.WriteFile(path, []byte(tt.in), 0o644); err != nil {
					t.Fatal(err)
				}
			}
			if err := SetExtensionDisabled(path, tt.ext, tt.disabled); err != nil {
				t.Fatal(err)
			}
			if got, _ := os.ReadFile(path); string(got) != tt.want {
				t.Errorf("got\n%q\nwant\n%q", got, tt.want)
			}
		})
	}
}
