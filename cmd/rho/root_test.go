package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/NDari/rho/core/config"
	"github.com/NDari/rho/core/docs"
	"github.com/NDari/rho/core/session"
	"github.com/NDari/rho/core/update"
)

// chatRequest is the part of a Chat Completions request the tests check.
type chatRequest struct {
	Model    string `json:"model"`
	Messages []struct {
		Role    string `json:"role"`
		Content any    `json:"content"`
	} `json:"messages"`
	Tools []struct {
		Function struct {
			Name string `json:"name"`
		} `json:"function"`
	} `json:"tools"`
}

// chatServer is a local Chat Completions server answering every request
// with reply. It records the requests.
type chatServer struct {
	URL  string
	mu   sync.Mutex
	reqs []chatRequest
}

func newChatServer(t *testing.T, reply string) *chatServer {
	t.Helper()
	cs := &chatServer{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		var req chatRequest
		if err := json.Unmarshal(body, &req); err != nil {
			t.Errorf("bad request body: %v", err)
		}
		cs.mu.Lock()
		cs.reqs = append(cs.reqs, req)
		cs.mu.Unlock()
		content, _ := json.Marshal(reply)
		w.Header().Set("Content-Type", "text/event-stream")
		for _, c := range []string{
			`{"id":"x","object":"chat.completion.chunk","created":1,"model":"m","choices":[{"index":0,"delta":{"role":"assistant","content":` + string(content) + `},"finish_reason":null}]}`,
			`{"id":"x","object":"chat.completion.chunk","created":1,"model":"m","choices":[{"index":0,"delta":{},"finish_reason":"stop"}],"usage":{"prompt_tokens":10,"completion_tokens":2,"total_tokens":12}}`,
		} {
			fmt.Fprintf(w, "data: %s\n\n", c)
		}
		fmt.Fprint(w, "data: [DONE]\n\n")
	}))
	t.Cleanup(srv.Close)
	cs.URL = srv.URL
	return cs
}

func (cs *chatServer) requests() []chatRequest {
	cs.mu.Lock()
	defer cs.mu.Unlock()
	return slices.Clone(cs.reqs)
}

// testEnv gives a test its own Rho home and working directory, and an
// empty regular file as stdin. It returns the home.
func testEnv(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("RHO_HOME", home)
	t.Chdir(t.TempDir())
	setStdin(t, "")
	return home
}

// setStdin replaces os.Stdin with a regular file holding content.
func setStdin(t *testing.T, content string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "stdin")
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	old := os.Stdin
	os.Stdin = f
	t.Cleanup(func() {
		os.Stdin = old
		_ = f.Close()
	})
}

// writeConfig points model "m" (the default) at the server.
func writeConfig(t *testing.T, home, url string) {
	t.Helper()
	cfg := fmt.Sprintf(`{
	"default_model": "m",
	"providers": {"test": {"base_url": %q}},
	"models": [{"id": "m", "provider": "test", "context_window": 32000}],
}`, url)
	if err := os.WriteFile(filepath.Join(home, config.FileName), []byte(cfg), 0o644); err != nil {
		t.Fatal(err)
	}
}

// execute runs the root command with args.
func execute(args ...string) (stdout, stderr string, err error) {
	var out, errOut bytes.Buffer
	cmd := newRootCmd()
	cmd.SetArgs(args)
	cmd.SetOut(&out)
	cmd.SetErr(&errOut)
	cmd.SetIn(strings.NewReader(""))
	err = cmd.Execute()
	return out.String(), errOut.String(), err
}

func TestRootFlagErrors(t *testing.T) {
	tests := []struct {
		name string
		args []string
		want string
	}{
		{"json without print", []string{"--json", "hi"}, "--json requires -p"},
		{"max steps without print", []string{"--max-steps", "2", "hi"}, "--max-steps requires -p"},
		{"negative max steps", []string{"-p", "--max-steps", "-1", "hi"}, "must not be negative"},
		{"unknown mode", []string{"--mode", "grpc"}, `unknown --mode "grpc"`},
		{"rpc with prompt", []string{"--mode", "rpc", "hi"}, "--mode rpc takes no prompt"},
		{"rpc with print", []string{"--mode", "rpc", "-p"}, "--mode rpc takes no prompt"},
		{"rpc with resume", []string{"--mode", "rpc", "-r"}, "--mode rpc takes no prompt"},
		{"print with resume", []string{"-p", "-r", "hi"}, "-r needs the interactive TUI"},
		{"print without prompt", []string{"-p"}, "no prompt given"},
		{"print with blank prompt", []string{"-p", "  "}, "no prompt given"},
		{"missing system file", []string{"-p", "--no-extensions", "--system-file", "nope.md", "hi"}, "prompt file"},
		{"unknown tool", []string{"-p", "--no-extensions", "--tools", "read,teleport", "hi"}, "teleport"},
		{"unknown flag", []string{"--bogus"}, "unknown flag"},
		{"tui without terminal", []string{"hi"}, "needs a terminal"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			home := testEnv(t)
			// A configured model, so errors come from the flags; the server
			// must never be reached.
			srv := newChatServer(t, "unexpected")
			writeConfig(t, home, srv.URL)
			_, _, err := execute(tc.args...)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Errorf("error = %v, want one containing %q", err, tc.want)
			}
			if n := len(srv.requests()); n != 0 {
				t.Errorf("server got %d requests", n)
			}
		})
	}
}

func TestPrintJSON(t *testing.T) {
	home := testEnv(t)
	srv := newChatServer(t, "hello")
	writeConfig(t, home, srv.URL)
	stdout, stderr, err := execute("-p", "--json", "--no-extensions", "say hi")
	if err != nil {
		t.Fatalf("%v (stderr %q)", err, stderr)
	}
	lines := strings.Split(strings.TrimRight(stdout, "\n"), "\n")
	var res struct {
		Type, Status, Answer, Model, Session string
		Steps                                int
	}
	if err := json.Unmarshal([]byte(lines[len(lines)-1]), &res); err != nil {
		t.Fatalf("last line %q: %v", lines[len(lines)-1], err)
	}
	if res.Type != "result" || res.Status != "done" || res.Answer != "hello" || res.Model != "m" || res.Steps != 1 {
		t.Errorf("result = %+v", res)
	}
	for _, l := range lines {
		if !json.Valid([]byte(l)) {
			t.Errorf("line is not JSON: %q", l)
		}
	}
	// The session is saved under the home, so -c can continue it.
	if res.Session == "" || !strings.HasPrefix(res.Session, home) {
		t.Errorf("session %q is not under %s", res.Session, home)
	}
	if _, err := os.Stat(res.Session); err != nil {
		t.Errorf("session file: %v", err)
	}
}

func TestPrintText(t *testing.T) {
	home := testEnv(t)
	srv := newChatServer(t, "hello")
	writeConfig(t, home, srv.URL)
	// Without --no-extensions: no extensions are installed, so none start.
	stdout, stderr, err := execute("-p", "say hi")
	if err != nil {
		t.Fatalf("%v (stderr %q)", err, stderr)
	}
	if stdout != "hello\n" {
		t.Errorf("stdout = %q, want %q", stdout, "hello\n")
	}
}

func lastUserText(t *testing.T, req chatRequest) string {
	t.Helper()
	for i := len(req.Messages) - 1; i >= 0; i-- {
		m := req.Messages[i]
		if m.Role != "user" {
			continue
		}
		switch c := m.Content.(type) {
		case string:
			return c
		default:
			data, _ := json.Marshal(c)
			return string(data)
		}
	}
	t.Fatal("no user message")
	return ""
}

func TestPrintWiring(t *testing.T) {
	tests := []struct {
		name  string
		stdin string
		args  []string
		check func(*testing.T, chatRequest)
	}{
		{
			name:  "stdin follows the prompt argument",
			stdin: "piped text\n",
			args:  []string{"summarize"},
			check: func(t *testing.T, r chatRequest) {
				if got := lastUserText(t, r); !strings.Contains(got, "summarize\n\npiped text") {
					t.Errorf("user text = %q", got)
				}
			},
		},
		{
			name:  "stdin alone",
			stdin: "only piped",
			check: func(t *testing.T, r chatRequest) {
				if got := lastUserText(t, r); !strings.Contains(got, "only piped") {
					t.Errorf("user text = %q", got)
				}
			},
		},
		{
			name: "model flag",
			args: []string{"-m", "test/other-model", "hi"},
			check: func(t *testing.T, r chatRequest) {
				if r.Model != "other-model" {
					t.Errorf("model = %q, want other-model", r.Model)
				}
			},
		},
		{
			name: "tools flag",
			args: []string{"--tools", "read, grep", "hi"},
			check: func(t *testing.T, r chatRequest) {
				var names []string
				for _, tl := range r.Tools {
					names = append(names, tl.Function.Name)
				}
				if slices.Contains(names, "bash") || slices.Contains(names, "write") || !slices.Contains(names, "read") || !slices.Contains(names, "grep") {
					t.Errorf("tools = %v, want read and grep without bash or write", names)
				}
			},
		},
		{
			name: "empty tools flag offers no write tools",
			args: []string{"--tools", "", "hi"},
			check: func(t *testing.T, r chatRequest) {
				for _, tl := range r.Tools {
					if n := tl.Function.Name; n == "bash" || n == "write" || n == "edit" {
						t.Errorf("tool %s offered with --tools \"\"", n)
					}
				}
			},
		},
		{
			name: "system file",
			args: []string{"--system-file", "sys.md", "hi"},
			check: func(t *testing.T, r chatRequest) {
				if len(r.Messages) == 0 || r.Messages[0].Role != "system" {
					t.Fatalf("no system message: %+v", r.Messages)
				}
				if s, _ := r.Messages[0].Content.(string); !strings.Contains(s, "CUSTOM SYSTEM PROMPT") {
					t.Errorf("system prompt = %q", s)
				}
			},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			home := testEnv(t)
			setStdin(t, tc.stdin)
			if err := os.WriteFile("sys.md", []byte("CUSTOM SYSTEM PROMPT\n"), 0o644); err != nil {
				t.Fatal(err)
			}
			srv := newChatServer(t, "ok")
			writeConfig(t, home, srv.URL)
			args := append([]string{"-p", "--no-extensions"}, tc.args...)
			if _, stderr, err := execute(args...); err != nil {
				t.Fatalf("%v (stderr %q)", err, stderr)
			}
			reqs := srv.requests()
			if len(reqs) != 1 {
				t.Fatalf("server got %d requests, want 1", len(reqs))
			}
			tc.check(t, reqs[0])
		})
	}
}

func TestPrintContinue(t *testing.T) {
	home := testEnv(t)
	srv := newChatServer(t, "hello")
	writeConfig(t, home, srv.URL)
	if _, _, err := execute("-p", "-c", "--no-extensions", "hi"); err == nil || !strings.Contains(err.Error(), "no previous session") {
		t.Fatalf("-c without a session: %v", err)
	}
	if _, _, err := execute("-p", "--no-extensions", "first question"); err != nil {
		t.Fatal(err)
	}
	if _, _, err := execute("-p", "-c", "--no-extensions", "second question"); err != nil {
		t.Fatal(err)
	}
	reqs := srv.requests()
	if len(reqs) != 2 {
		t.Fatalf("server got %d requests, want 2", len(reqs))
	}
	var users []string
	for _, m := range reqs[1].Messages {
		if m.Role == "user" {
			users = append(users, fmt.Sprint(m.Content))
		}
	}
	if len(users) != 2 || !strings.Contains(users[0], "first question") || !strings.Contains(users[1], "second question") {
		t.Errorf("continued request user messages = %q", users)
	}
}

func TestNoModel(t *testing.T) {
	home := testEnv(t)
	_, _, err := execute("-p", "--no-extensions", "hi")
	if !errors.Is(err, config.ErrNoModel) {
		t.Fatalf("error = %v, want config.ErrNoModel", err)
	}
	path := filepath.Join(home, config.FileName)
	hint := noModelHint()
	if !strings.Contains(hint, "rho init") || !strings.Contains(hint, path) || !strings.Contains(hint, indent(config.Example)) {
		t.Errorf("hint without a config file = %q", hint)
	}
	if err := os.WriteFile(path, []byte(`{"max_retries": 1}`), 0o644); err != nil {
		t.Fatal(err)
	}
	hint = noModelHint()
	if strings.Contains(hint, "rho init") || !strings.Contains(hint, "Add a model to "+path) {
		t.Errorf("hint with a config file = %q", hint)
	}
}

func TestInit(t *testing.T) {
	home := testEnv(t)
	path := filepath.Join(home, config.FileName)
	stdout, _, err := execute("init")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(stdout, "Wrote "+path+":\n\n") || !strings.Contains(stdout, `"default_model": "local"`) {
		t.Errorf("output = %q", stdout)
	}
	data, err := os.ReadFile(path)
	if err != nil || string(data) != config.Example {
		t.Fatalf("config file = %q, %v; want the example", data, err)
	}
	// The example must load and name a usable model.
	p, err := config.ResolvePaths(".")
	if err != nil {
		t.Fatal(err)
	}
	cfg, _, err := config.Load(p)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := cfg.ResolveModel(""); err != nil {
		t.Errorf("example config has no model: %v", err)
	}
	// A second init never replaces the file.
	if err := os.WriteFile(path, []byte("# mine\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, _, err := execute("init"); err == nil || !strings.Contains(err.Error(), "already exists") {
		t.Errorf("second init: %v", err)
	}
	if data, _ := os.ReadFile(path); string(data) != "# mine\n" {
		t.Errorf("second init changed the file to %q", data)
	}
	if _, _, err := execute("init", "extra"); err == nil {
		t.Error("init accepted an argument")
	}
}

func TestDocs(t *testing.T) {
	home := testEnv(t)
	stdout, _, err := execute("docs")
	if err != nil {
		t.Fatal(err)
	}
	for _, tp := range docs.Topics {
		if !strings.Contains(stdout, "  "+tp.Name) || !strings.Contains(stdout, tp.Summary) {
			t.Errorf("list has no topic %s", tp.Name)
		}
	}
	if ex := docs.Examples(); len(ex) == 0 || !strings.Contains(stdout, "  "+ex[0]+"\n") {
		t.Errorf("list has no examples: %q", stdout)
	}
	if !strings.Contains(stdout, "Installed copy: "+filepath.Join(home, "docs")) {
		t.Errorf("list does not name the installed copy under %s: %q", home, stdout)
	}

	want, err := docs.Read("configuration")
	if err != nil {
		t.Fatal(err)
	}
	if stdout, _, err = execute("docs", "configuration"); err != nil || stdout != string(want) {
		t.Errorf("docs configuration: %v, output matches %v", err, stdout == string(want))
	}
	if _, _, err := execute("docs", "no-such-topic"); err == nil || !strings.Contains(err.Error(), "no document") {
		t.Errorf("unknown topic: %v", err)
	}
	if _, _, err := execute("docs", "a", "b"); err == nil {
		t.Error("docs accepted two arguments")
	}
}

// newSession writes a session with one exchange under home for the
// current directory and returns its path.
func newSession(t *testing.T, home, user, reply string) string {
	t.Helper()
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	srv := newChatServer(t, reply)
	writeConfig(t, home, srv.URL)
	stdout, _, err := execute("-p", "--json", "--no-extensions", user)
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimRight(stdout, "\n"), "\n")
	var res struct{ Session string }
	if err := json.Unmarshal([]byte(lines[len(lines)-1]), &res); err != nil {
		t.Fatal(err)
	}
	if latest, _ := session.Latest(session.Dir(home, cwd)); latest != res.Session {
		t.Fatalf("latest session = %q, want %q", latest, res.Session)
	}
	return res.Session
}

func TestExport(t *testing.T) {
	home := testEnv(t)
	if _, _, err := execute("export"); err == nil || !strings.Contains(err.Error(), "no session in this directory") {
		t.Fatalf("export without a session: %v", err)
	}
	path := newSession(t, home, "what is rho", "a coding agent")
	sess, err := session.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	id := sess.ID()
	_ = sess.Close()

	isPage := func(t *testing.T, html string) {
		t.Helper()
		if !strings.Contains(html, "<html") || !strings.Contains(html, "what is rho") || !strings.Contains(html, "a coding agent") {
			t.Errorf("export is not a page with the conversation (%d bytes)", len(html))
		}
	}

	t.Run("stdout", func(t *testing.T) {
		stdout, stderr, err := execute("export", path, "-o", "-")
		if err != nil {
			t.Fatal(err)
		}
		isPage(t, stdout)
		if stderr != "" {
			t.Errorf("stderr = %q", stderr)
		}
	})
	t.Run("default name for the latest session", func(t *testing.T) {
		_, stderr, err := execute("export")
		if err != nil {
			t.Fatal(err)
		}
		name := "rho-" + id[:min(8, len(id))] + ".html"
		if stderr != "exported to "+name+"\n" {
			t.Errorf("stderr = %q, want it to name %s", stderr, name)
		}
		data, err := os.ReadFile(name)
		if err != nil {
			t.Fatal(err)
		}
		isPage(t, string(data))
	})
	t.Run("output file", func(t *testing.T) {
		out := filepath.Join(t.TempDir(), "page.html")
		if _, _, err := execute("export", path, "--output", out); err != nil {
			t.Fatal(err)
		}
		data, err := os.ReadFile(out)
		if err != nil {
			t.Fatal(err)
		}
		isPage(t, string(data))
	})
	t.Run("missing file", func(t *testing.T) {
		if _, _, err := execute("export", filepath.Join(t.TempDir(), "none.jsonl")); err == nil {
			t.Error("exporting a missing file succeeded")
		}
	})
}

func TestUpdateDevBuild(t *testing.T) {
	testEnv(t)
	// A build from source never looks for releases, so this needs no
	// network.
	if current.Method != update.Dev {
		t.Skip("not a development build")
	}
	if _, _, err := execute("update"); err == nil || !strings.Contains(err.Error(), "development build") {
		t.Errorf("update: %v", err)
	}
}

func TestVersionFlag(t *testing.T) {
	testEnv(t)
	stdout, _, err := execute("--version")
	if err != nil || !strings.Contains(stdout, version) {
		t.Errorf("--version = %q, %v", stdout, err)
	}
}

func TestIndent(t *testing.T) {
	tests := []struct{ in, want string }{
		{"a", "  a\n"},
		{"a\nb\n", "  a\n  b\n"},
		{"a\n\nb\n\n\n", "  a\n\n  b\n"},
		{"", "\n"},
	}
	for _, tt := range tests {
		if got := indent(tt.in); got != tt.want {
			t.Errorf("indent(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
}
