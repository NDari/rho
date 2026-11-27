package jsonc

import (
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"
)

type inner struct {
	Timeout int `json:"timeout"`
}

type doc struct {
	Name   string           `json:"name"`
	Inner  inner            `json:"inner"`
	Items  []inner          `json:"items"`
	ByName map[string]inner `json:"by_name"`
	Flag   *bool            `json:"flag"`
}

func TestDecode(t *testing.T) {
	tests := []struct {
		name      string
		in        string
		want      doc
		defined   [][]string
		undefined [][]string
		unknown   []string
		err       string
	}{
		{name: "empty", in: " \n", undefined: [][]string{{"name"}}},
		{name: "comments and trailing commas",
			in:      "// top\n{\n\t\"name\": \"x\", // note\n\t/* block */ \"inner\": {\"timeout\": 3,},\n}\n",
			want:    doc{Name: "x", Inner: inner{Timeout: 3}},
			defined: [][]string{{"name"}, {"inner", "timeout"}}, undefined: [][]string{{"inner", "other"}, {"flag"}}},
		{name: "comment at the end without a newline", in: `{"name": "x"} // mine`, want: doc{Name: "x"}},
		{name: "null counts as defined", in: `{"flag": null}`, defined: [][]string{{"flag"}}},
		{name: "unknown keys",
			in:      `{"nam": 1, "inner": {"timeout": 1, "x": 2}, "items": [{"y": 1}, {"y": 2}], "by_name": {"a": {"z": 1}}}`,
			want:    doc{Inner: inner{Timeout: 1}, Items: []inner{{}, {}}, ByName: map[string]inner{"a": {}}},
			unknown: []string{"by_name.a.z", "inner.x", "items.y", "nam"}},
		{name: "keys are case sensitive", in: `{"Name": "x"}`, unknown: []string{"Name"}},
		{name: "syntax error", in: "{\n\"name\": ,\n}", err: "line 2"},
		{name: "wrong type", in: `{"inner": {"timeout": "x"}}`, err: "timeout"},
		{name: "not an object", in: `[1]`, err: "JSON object"},
		{name: "duplicate key", in: `{"name": "a", "name": "b"}`, err: "duplicate"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var got doc
			md, err := Decode([]byte(tt.in), &got)
			if tt.err != "" {
				if err == nil || !strings.Contains(err.Error(), tt.err) {
					t.Fatalf("err = %v, want %q", err, tt.err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("got %+v, want %+v", got, tt.want)
			}
			for _, k := range tt.defined {
				if !md.IsDefined(k...) {
					t.Errorf("IsDefined(%v) = false", k)
				}
			}
			for _, k := range tt.undefined {
				if md.IsDefined(k...) {
					t.Errorf("IsDefined(%v) = true", k)
				}
			}
			if !reflect.DeepEqual(md.Undecoded(), tt.unknown) {
				t.Errorf("Undecoded = %q, want %q", md.Undecoded(), tt.unknown)
			}
		})
	}
}

func TestSet(t *testing.T) {
	tests := []struct {
		name  string
		in    string
		path  []string
		value any
		want  string
	}{
		{name: "new file", in: "", path: []string{"theme"}, value: "nord",
			want: "{\n\t\"theme\": \"nord\"\n}\n"},
		{name: "new table", in: "{\n\t\"theme\": \"nord\",\n}\n", path: []string{"tui", "show_thinking"}, value: true,
			want: "{\n\t\"theme\": \"nord\",\n\t\"tui\": {\n\t\t\"show_thinking\": true\n\t}\n}\n"},
		{name: "replace keeps comments", in: "// mine\n{\n\t// the theme\n\t\"theme\": \"nord\", // dark\n}\n", path: []string{"theme"}, value: "dracula",
			want: "// mine\n{\n\t// the theme\n\t\"theme\": \"dracula\", // dark\n}\n"},
		{name: "remove", in: "{\n\t\"a\": 1,\n\t\"b\": 2,\n}\n", path: []string{"a"}, value: nil,
			want: "{\n\t\"b\": 2,\n}\n"},
		{name: "remove missing", in: "{\"a\": 1}", path: []string{"x", "y"}, value: nil, want: "{\"a\": 1}"},
		{name: "array element", in: "{\n\t\"models\": [\n\t\t{\n\t\t\t\"id\": \"a\",\n\t\t},\n\t],\n}\n", path: []string{"models", "0", "max_output"}, value: 5,
			want: "{\n\t\"models\": [\n\t\t{\n\t\t\t\"id\":         \"a\",\n\t\t\t\"max_output\": 5,\n\t\t},\n\t],\n}\n"},
		{name: "list", in: "{}", path: []string{"extensions", "disabled"}, value: []string{"x"},
			want: "{\"extensions\": {\n\t\"disabled\": [\"x\"]\n}}\n"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := Set([]byte(tt.in), tt.path, tt.value)
			if err != nil {
				t.Fatal(err)
			}
			if string(got) != tt.want {
				t.Errorf("got\n%s\nwant\n%s", got, tt.want)
			}
		})
	}
}

func TestFromTOML(t *testing.T) {
	in := `# comment
default_model = "local"
max_retries = 2

[providers.local]
base_url = "http://localhost:9931/v1"
headers = { X-A = "1" }

[[models]]
id = "local"
provider = "local"

[[models]]
id = "other"
provider = "local"

[tools]
enabled = ["read", "bash"]
`
	got, err := FromTOML([]byte(in))
	if err != nil {
		t.Fatal(err)
	}
	want := `{
	"default_model": "local",
	"max_retries":   2,
	"providers": {
		"local": {
			"base_url": "http://localhost:9931/v1",
			"headers": {
				"X-A": "1"
			}
		}
	},
	"models": [
		{
			"id":       "local",
			"provider": "local"
		},
		{
			"id":       "other",
			"provider": "local"
		}
	],
	"tools": {
		"enabled": ["read", "bash"]
	}
}
`
	if string(got) != want {
		t.Errorf("got\n%s\nwant\n%s", got, want)
	}
	if _, err := FromTOML([]byte("a = \n")); err == nil {
		t.Error("bad TOML: no error")
	}
}

func TestConvertTOML(t *testing.T) {
	dir := t.TempDir()
	src, dst := filepath.Join(dir, "config.toml"), filepath.Join(dir, "config.jsonc")
	if ok, err := ConvertTOML(src, dst); ok || err != nil {
		t.Fatalf("no source: %v, %v", ok, err)
	}
	if err := os.WriteFile(src, []byte("theme = \"nord\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if ok, err := ConvertTOML(src, dst); !ok || err != nil {
		t.Fatalf("convert: %v, %v", ok, err)
	}
	data, _ := os.ReadFile(dst)
	var got struct {
		Theme string `json:"theme"`
	}
	if _, err := Decode(data, &got); err != nil || got.Theme != "nord" {
		t.Fatalf("decode converted file: %v, %+v\n%s", err, got, data)
	}
	if !strings.HasPrefix(string(data), "// Converted from config.toml") {
		t.Errorf("no header:\n%s", data)
	}
	// Windows has no Unix permission bits to keep.
	if fi, _ := os.Stat(dst); runtime.GOOS != "windows" && fi.Mode().Perm() != 0o600 {
		t.Errorf("mode = %v, want 0600", fi.Mode().Perm())
	}
	if _, err := os.Stat(src); err != nil {
		t.Errorf("source removed: %v", err)
	}
	// An existing destination is never replaced.
	if err := os.WriteFile(src, []byte("theme = \"dracula\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if ok, err := ConvertTOML(src, dst); ok || err != nil {
		t.Fatalf("second convert: %v, %v", ok, err)
	}
	if again, _ := os.ReadFile(dst); string(again) != string(data) {
		t.Error("destination replaced")
	}
}
