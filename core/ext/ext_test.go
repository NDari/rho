package ext_test

import (
	"context"
	"encoding/json"
	"errors"
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
	if err := os.WriteFile(filepath.Join(dir, ext.ManifestName), []byte(`{"name": "pyext", "command": "python3", "args": ["${dir}/main.py"]}`), 0o644); err != nil {
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

func TestDiscoverConvertsLegacyManifest(t *testing.T) {
	home := t.TempDir()
	dir := filepath.Join(home, "extensions", "py")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "rho-extension.toml"), []byte("name = \"pyext\"\ncommand = \"python3\"\nargs = [\"main.py\"]\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	m := ext.NewManager(ext.Options{Paths: config.Paths{Home: home, ProjectDir: filepath.Join(home, "nope")}, Config: config.Defaults()})
	cands, warnings := m.Discover()
	if len(cands) != 1 || cands[0].Name != "pyext" || len(cands[0].Args) != 1 {
		t.Fatalf("cands=%+v", cands)
	}
	if len(warnings) != 1 || !strings.Contains(warnings[0], "converted") {
		t.Errorf("warnings = %q", warnings)
	}
	if _, err := os.Stat(filepath.Join(dir, ext.ManifestName)); err != nil {
		t.Error(err)
	}
	if _, warnings = m.Discover(); len(warnings) != 0 {
		t.Errorf("second discovery warned again: %q", warnings)
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

	sys, msgs, keep, _ := m.BeforeRequest(ctx, "system", []provider.Message{provider.UserText("question")}, []string{"read", "bash"}, false)
	if keep != nil {
		t.Errorf("tools changed without being asked: %v", keep)
	}
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

const agentControlExtension = `package main

import (
	"fmt"

	"rho"
)

func main() {
	rho.Info("ctl", "0.1.0")
	rho.On("message_end", func(e rho.Event) rho.Response {
		info, err := rho.Session.Info()
		msgs, err2 := rho.Session.Messages()
		if err != nil || err2 != nil {
			rho.UI.Notify(fmt.Sprint("error ", err, err2))
			return rho.Response{}
		}
		role := e.Message["role"]
		rho.UI.Notify(fmt.Sprintf("%s %v %s %d %s", e.EntryID, role, info.Model, len(msgs), msgs[0]["id"]))
		return rho.Response{}
	})
	rho.On("tool_end", func(e rho.Event) rho.Response {
		rho.UI.Notify(fmt.Sprintf("tool %s %v %s %v", e.Tool, e.Args["path"], e.Result, e.IsError))
		return rho.Response{}
	})
	rho.RegisterCommand("ctl", "drive the agent", func(args string) error {
		switch args {
		case "cancel":
			return rho.Agent.Cancel()
		case "compact":
			return rho.Agent.Compact()
		}
		return rho.Agent.SetModel(args)
	})
}
`

type fakeAgent struct {
	mu    sync.Mutex
	calls []string
}

func (a *fakeAgent) record(s string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.calls = append(a.calls, s)
}
func (a *fakeAgent) Busy() bool                    { return false }
func (a *fakeAgent) Prompt(provider.Message) error { return nil }
func (a *fakeAgent) FollowUp(string) error         { return nil }
func (a *fakeAgent) SetModel(id string) error {
	a.record("set_model " + id)
	if id == "missing" {
		return errors.New("unknown model")
	}
	return nil
}
func (a *fakeAgent) SetActiveTools(names []string) error {
	a.record("set_tools " + strings.Join(names, "+"))
	return nil
}
func (a *fakeAgent) CancelBy(by string)            { a.record("cancel by " + by) }
func (a *fakeAgent) Compact(context.Context) error { a.record("compact"); return nil }
func (a *fakeAgent) Info() ext.SessionInfo         { return ext.SessionInfo{ID: "s1", Model: "m1"} }
func (a *fakeAgent) Messages() []ext.SessionEntry {
	return []ext.SessionEntry{{ID: "e1", Message: provider.UserText("q")}, {ID: "e2", Message: provider.UserText("a")}}
}

func TestNotificationsAndAgentControl(t *testing.T) {
	home := t.TempDir()
	extDir := filepath.Join(home, "extensions", "ctl")
	if err := os.MkdirAll(extDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(extDir, "ctl.go"), []byte(agentControlExtension), 0o644); err != nil {
		t.Fatal(err)
	}
	ui := &fakeUI{}
	ag := &fakeAgent{}
	m := ext.NewManager(ext.Options{
		Paths:  config.Paths{Home: home, Cwd: home, ProjectRoot: home, ProjectDir: filepath.Join(t.TempDir(), ".rho")},
		Config: config.Defaults(),
		UI:     ui,
		Agent:  ag,
		Launch: inProcess,
	})
	ctx := context.Background()
	m.Start(ctx)
	defer m.Stop()
	if exts := m.Extensions(); len(exts) != 1 || exts[0].Status != ext.StatusRunning {
		t.Fatalf("extension not running: %s", m.Describe())
	}

	// The handlers call back into Rho, which must not deadlock.
	m.MessageEnd("e7", provider.UserText("hello"))
	m.ToolEnd("read", json.RawMessage(`{"path":"a.txt"}`), tools.Result{Content: []provider.Block{provider.Text("contents")}})
	want := []string{"e7 user m1 2 e1", "tool read a.txt contents false"}
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		ui.mu.Lock()
		n := len(ui.notified)
		ui.mu.Unlock()
		if n >= len(want) {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	ui.mu.Lock()
	if strings.Join(ui.notified, "|") != strings.Join(want, "|") {
		t.Errorf("notified = %q, want %q (in order)", ui.notified, want)
	}
	ui.mu.Unlock()

	for _, args := range []string{"big", "cancel", "compact"} {
		if err := m.RunCommand(ctx, "ctl", args); err != nil {
			t.Fatalf("/ctl %s: %v", args, err)
		}
	}
	if err := m.RunCommand(ctx, "ctl", "missing"); err == nil || !strings.Contains(err.Error(), "unknown model") {
		t.Errorf("a failed agent call should reach the extension: %v", err)
	}
	ag.mu.Lock()
	defer ag.mu.Unlock()
	if got := strings.Join(ag.calls, ","); got != "set_model big,cancel by extension ctl,compact,set_model missing" {
		t.Errorf("agent calls = %s", got)
	}
}

const toolControlExtension = `//go:build rho

package main

import (
	"fmt"

	"rho"
)

func main() {
	rho.Info("plan", "0.1.0")
	rho.On("before_request", func(e rho.Event) rho.Response {
		return rho.Response{Tools: []string{"read"}}
	})
	rho.RegisterCommand("plan", "plan mode", func(args string) error {
		rho.UI.Notify(fmt.Sprint(rho.Supports("agent/set_tools"), rho.Supports("before_request"), rho.Supports("no/such")))
		return rho.Agent.SetTools([]string{"read", "grep"})
	})
}
`

func TestToolControlAndCapabilities(t *testing.T) {
	home := t.TempDir()
	if err := os.MkdirAll(filepath.Join(home, "extensions"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, "extensions", "plan.go"), []byte(toolControlExtension), 0o644); err != nil {
		t.Fatal(err)
	}
	ui := &fakeUI{}
	ag := &fakeAgent{}
	m := ext.NewManager(ext.Options{
		Paths:  config.Paths{Home: home, Cwd: home, ProjectRoot: home, ProjectDir: filepath.Join(t.TempDir(), ".rho")},
		Config: config.Defaults(),
		UI:     ui,
		Agent:  ag,
		Launch: inProcess,
	})
	ctx := context.Background()
	m.Start(ctx)
	defer m.Stop()

	_, _, keep, _ := m.BeforeRequest(ctx, "s", []provider.Message{provider.UserText("q")}, []string{"read", "write", "bash"}, false)
	if strings.Join(keep, ",") != "read" {
		t.Errorf("narrowed tools = %v", keep)
	}
	if err := m.RunCommand(ctx, "plan", ""); err != nil {
		t.Fatal(err)
	}
	ag.mu.Lock()
	calls := strings.Join(ag.calls, ",")
	ag.mu.Unlock()
	if calls != "set_tools read+grep" {
		t.Errorf("agent calls = %s", calls)
	}
	ui.mu.Lock()
	defer ui.mu.Unlock()
	if strings.Join(ui.notified, "|") != "true true false" {
		t.Errorf("Supports answers = %q", ui.notified)
	}
}
