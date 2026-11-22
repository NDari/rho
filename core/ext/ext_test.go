package ext_test

import (
	"context"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/NDari/rho/core/config"
	"github.com/NDari/rho/core/ext"
	"github.com/NDari/rho/core/ext/yaegihost"
	"github.com/NDari/rho/core/provider"
	"github.com/NDari/rho/core/tools"
)

// inProcess launches Go extensions in this process via the Yaegi host.
func inProcess(c ext.Candidate) (io.ReadCloser, io.WriteCloser, io.ReadCloser, func(), error) {
	inR, inW := io.Pipe()
	outR, outW := io.Pipe()
	go func() {
		_ = yaegihost.Serve(c.Path, inR, outW, io.Discard)
		_ = outW.Close()
	}()
	return outR, inW, nil, func() { _ = inW.Close() }, nil
}

type fakeUI struct {
	mu       sync.Mutex
	notified []string
	confirm  bool
}

func (u *fakeUI) Confirm(context.Context, string, string) (bool, error) { return u.confirm, nil }
func (u *fakeUI) Select(context.Context, string, []string) (string, error) {
	return "", nil
}
func (u *fakeUI) Input(context.Context, string, string) (string, error) { return "", nil }
func (u *fakeUI) Notify(_, text string) {
	u.mu.Lock()
	defer u.mu.Unlock()
	u.notified = append(u.notified, text)
}
func (u *fakeUI) SetStatus(string, string) {}

func TestGoExtensionEndToEnd(t *testing.T) {
	home := t.TempDir()
	extDir := filepath.Join(home, "extensions", "hello")
	if err := os.MkdirAll(extDir, 0o755); err != nil {
		t.Fatal(err)
	}
	src, err := os.ReadFile(filepath.Join("testdata", "hello", "hello.go"))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(extDir, "hello.go"), src, 0o644); err != nil {
		t.Fatal(err)
	}
	ui := &fakeUI{}
	m := ext.NewManager(ext.Options{
		Paths:   config.Paths{Home: home, Cwd: home, ProjectRoot: home, ProjectDir: filepath.Join(t.TempDir(), ".rho")},
		Config:  config.Defaults(),
		UI:      ui,
		Builtin: []string{"read", "write", "edit", "bash"},
		Launch:  inProcess,
	})
	ctx := context.Background()
	m.Start(ctx)
	defer m.Stop()

	exts := m.Extensions()
	if len(exts) != 1 || exts[0].Status != ext.StatusRunning {
		t.Fatalf("extension not running: %s", m.Describe())
	}
	ts := m.Tools()
	if len(ts) != 1 || ts[0].Spec().Name != "shout" || !ts[0].ReadOnly() {
		t.Fatalf("tools = %+v", ts)
	}
	var pmu sync.Mutex
	var progress []string
	res := ts[0].Execute(ctx, &tools.Env{}, json.RawMessage(`{"text":"hi"}`), func(s string) {
		pmu.Lock()
		defer pmu.Unlock()
		progress = append(progress, s)
	})
	if res.IsError || res.Content[0].Text != "HI" {
		t.Fatalf("execute = %+v", res)
	}
	res = m.AfterToolCall(ctx, "shout", nil, res)
	if res.Content[0].Text != "HI!" {
		t.Errorf("after hook = %q", res.Content[0].Text)
	}
	if _, reason := m.BeforeToolCall(ctx, "bash", json.RawMessage(`{"command":"rm -rf /"}`)); !strings.Contains(reason, "dangerous") {
		t.Errorf("before hook did not block: %q", reason)
	}
	if args, reason := m.BeforeToolCall(ctx, "bash", json.RawMessage(`{"command":"ls"}`)); reason != "" || string(args) != `{"command":"ls"}` {
		t.Errorf("before hook changed a safe call: %s %q", args, reason)
	}
	if err := m.RunCommand(ctx, "greet", "world"); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		ui.mu.Lock()
		n := len(ui.notified)
		ui.mu.Unlock()
		if n > 0 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	ui.mu.Lock()
	if len(ui.notified) != 1 || ui.notified[0] != "hello world" {
		t.Errorf("notified = %v", ui.notified)
	}
	ui.mu.Unlock()
	pmu.Lock()
	defer pmu.Unlock()
	if len(progress) != 1 || progress[0] != "shouting" {
		t.Errorf("progress = %v", progress)
	}
}

func TestProjectExtensionNeedsApproval(t *testing.T) {
	home := t.TempDir()
	proj := t.TempDir()
	extFile := filepath.Join(proj, ".rho", "extensions", "x.go")
	if err := os.MkdirAll(filepath.Dir(extFile), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(extFile, []byte("package main\n\nfunc main() {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	paths := config.Paths{Home: home, Cwd: proj, ProjectRoot: proj, ProjectDir: filepath.Join(proj, ".rho")}
	launched := 0
	launch := func(c ext.Candidate) (io.ReadCloser, io.WriteCloser, io.ReadCloser, func(), error) {
		launched++
		return inProcess(c)
	}
	ui := &fakeUI{confirm: false}
	m := ext.NewManager(ext.Options{Paths: paths, Config: config.Defaults(), UI: ui, Launch: launch})
	m.Start(context.Background())
	if launched != 0 || m.Extensions()[0].Status != ext.StatusNotApproved {
		t.Fatalf("unapproved extension was started: %s", m.Describe())
	}
	m.Stop()

	ui.confirm = true
	m = ext.NewManager(ext.Options{Paths: paths, Config: config.Defaults(), UI: ui, Launch: launch})
	m.Start(context.Background())
	m.Stop()
	if launched != 1 {
		t.Fatalf("approved extension not started")
	}
	// Approval is remembered until the content changes.
	ui.confirm = false
	m = ext.NewManager(ext.Options{Paths: paths, Config: config.Defaults(), UI: ui, Launch: launch})
	m.Start(context.Background())
	m.Stop()
	if launched != 2 {
		t.Fatalf("trusted extension not started")
	}
	if err := os.WriteFile(extFile, []byte("package main\n\nfunc main() { println() }\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	m = ext.NewManager(ext.Options{Paths: paths, Config: config.Defaults(), UI: ui, Launch: launch})
	m.Start(context.Background())
	m.Stop()
	if launched != 2 {
		t.Fatalf("changed extension started without re-approval")
	}
}

func TestDiscoverManifest(t *testing.T) {
	home := t.TempDir()
	dir := filepath.Join(home, "extensions", "py")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, ext.ManifestName), []byte("name = \"pyext\"\ncommand = \"python3\"\nargs = [\"${dir}/main.py\"]\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	m := ext.NewManager(ext.Options{Paths: config.Paths{Home: home, ProjectDir: filepath.Join(home, "nope")}, Config: config.Defaults()})
	cands, warnings := m.Discover()
	if len(warnings) != 0 || len(cands) != 1 {
		t.Fatalf("cands=%+v warnings=%v", cands, warnings)
	}
	c := cands[0]
	if c.Name != "pyext" || c.Kind != ext.KindExec || c.Command != "python3" || c.Args[0] != filepath.Join(dir, "main.py") && c.Args[0] != dir+"/main.py" {
		t.Errorf("candidate = %+v", c)
	}
}

func TestSetEnabled(t *testing.T) {
	home := t.TempDir()
	extDir := filepath.Join(home, "extensions", "hello")
	if err := os.MkdirAll(extDir, 0o755); err != nil {
		t.Fatal(err)
	}
	src, err := os.ReadFile(filepath.Join("testdata", "hello", "hello.go"))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(extDir, "hello.go"), src, 0o644); err != nil {
		t.Fatal(err)
	}
	cfg := config.Defaults()
	cfg.Extensions.Disabled = []string{"hello"}
	m := ext.NewManager(ext.Options{
		Paths:   config.Paths{Home: home, Cwd: home, ProjectRoot: home, ProjectDir: filepath.Join(t.TempDir(), ".rho")},
		Config:  cfg,
		UI:      &fakeUI{},
		Builtin: []string{"read", "write", "edit", "bash"},
		Launch:  inProcess,
	})
	ctx := context.Background()
	m.Start(ctx)
	defer m.Stop()
	status := func() string {
		x := m.Extensions()[0]
		x.Lock()
		defer x.Unlock()
		return x.Status
	}
	if status() != ext.StatusDisabled || len(m.Tools()) != 0 {
		t.Fatalf("not disabled at start: %s", m.Describe())
	}
	if err := m.SetEnabled(ctx, "hello", true); err != nil {
		t.Fatal(err)
	}
	if status() != ext.StatusRunning || len(m.Tools()) != 1 || len(m.Commands()) != 1 {
		t.Fatalf("not running after enable: %s", m.Describe())
	}
	if err := m.SetEnabled(ctx, "hello", false); err != nil {
		t.Fatal(err)
	}
	if status() != ext.StatusDisabled || len(m.Tools()) != 0 || len(m.Commands()) != 0 {
		t.Fatalf("still registered after disable: %s", m.Describe())
	}
	// The choice holds across a restart.
	if err := m.Reload(ctx); err != nil {
		t.Fatal(err)
	}
	if status() != ext.StatusDisabled {
		t.Errorf("enabled again after reload: %s", m.Describe())
	}
	if err := m.SetEnabled(ctx, "nope", true); err == nil {
		t.Error("expected an error for an unknown extension")
	}
}

const messageHooksExtension = `package main

import (
	"strings"

	"rho"
)

func main() {
	rho.Info("msg", "0.1.0")
	rho.On("user_message", func(e rho.Event) rho.Response {
		if strings.Contains(e.Text, "password") {
			return rho.Response{Block: "looks like a secret"}
		}
		return rho.Response{Text: strings.ReplaceAll(e.Text, "TBD", "the parser")}
	})
	rho.On("before_request", func(e rho.Event) rho.Response {
		note := map[string]any{"role": "user", "content": []any{map[string]any{"type": "text", "text": "context: the docs say 42"}}}
		return rho.Response{System: e.System + "\nBe brief.", Messages: append([]map[string]any{note}, e.Messages...)}
	})
}
`

func TestMessageHooksEndToEnd(t *testing.T) {
	home := t.TempDir()
	extDir := filepath.Join(home, "extensions", "msg")
	if err := os.MkdirAll(extDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(extDir, "msg.go"), []byte(messageHooksExtension), 0o644); err != nil {
		t.Fatal(err)
	}
	m := ext.NewManager(ext.Options{
		Paths:  config.Paths{Home: home, Cwd: home, ProjectRoot: home, ProjectDir: filepath.Join(t.TempDir(), ".rho")},
		Config: config.Defaults(),
		UI:     &fakeUI{},
		Launch: inProcess,
	})
	ctx := context.Background()
	m.Start(ctx)
	defer m.Stop()
	if exts := m.Extensions(); len(exts) != 1 || exts[0].Status != ext.StatusRunning {
		t.Fatalf("extension not running: %s", m.Describe())
	}

	if text, reason := m.UserMessage(ctx, "fix TBD"); text != "fix the parser" || reason != "" {
		t.Errorf("user_message rewrite = %q, %q", text, reason)
	}
	if _, reason := m.UserMessage(ctx, "my password is hunter2"); reason != "looks like a secret (blocked by extension msg)" {
		t.Errorf("user_message block = %q", reason)
	}

	sys, msgs := m.BeforeRequest(ctx, "system", []provider.Message{provider.UserText("question")}, false)
	if sys != "system\nBe brief." {
		t.Errorf("system = %q", sys)
	}
	if len(msgs) != 2 || msgs[0].TextContent() != "context: the docs say 42" || msgs[1].TextContent() != "question" || msgs[0].Role != provider.RoleUser {
		t.Errorf("messages = %+v", msgs)
	}
}

// The example in the repository carries //go:build rho, which keeps it out
// of go build; the extension host must still run it.
func TestTaggedExampleRuns(t *testing.T) {
	home := t.TempDir()
	src, err := os.ReadFile(filepath.Join("..", "..", "examples", "extensions", "guard.go"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(string(src), "//go:build rho") {
		t.Fatal("the example lost its build tag")
	}
	if err := os.MkdirAll(filepath.Join(home, "extensions"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, "extensions", "guard.go"), src, 0o644); err != nil {
		t.Fatal(err)
	}
	m := ext.NewManager(ext.Options{
		Paths:  config.Paths{Home: home, Cwd: home, ProjectRoot: home, ProjectDir: filepath.Join(t.TempDir(), ".rho")},
		Config: config.Defaults(),
		UI:     &fakeUI{},
		Launch: inProcess,
	})
	m.Start(context.Background())
	defer m.Stop()
	if !strings.Contains(m.Describe(), "hooks: before_tool_call") {
		t.Errorf("the tagged example did not register its hook:\n%s", m.Describe())
	}
}
