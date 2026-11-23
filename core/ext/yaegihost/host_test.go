package yaegihost

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/NDari/rho/core/ext"
	"github.com/NDari/rho/core/ext/rpc"
	"github.com/NDari/rho/core/provider"
)

// rhoSide plays Rho's end of the protocol: it answers the extension's
// requests and records its notifications.
type rhoSide struct {
	conn *rpc.Conn

	mu       sync.Mutex
	calls    []string // methods the extension called, in order
	notifies []ext.NotifyParams
	logs     []string
	progress []string
	appended []ext.AppendMessageParams
	models   []string
	tools    [][]string
	changed  chan struct{} // signalled on every recorded message
}

func (r *rhoSide) record(f func()) {
	r.mu.Lock()
	f()
	r.mu.Unlock()
	select {
	case r.changed <- struct{}{}:
	default:
	}
}

func (r *rhoSide) handle(_ context.Context, method string, params json.RawMessage) (any, error) {
	r.record(func() { r.calls = append(r.calls, method) })
	switch method {
	case "log":
		var p ext.LogParams
		_ = json.Unmarshal(params, &p)
		r.record(func() { r.logs = append(r.logs, p.Message) })
	case "tool/progress":
		var p ext.ToolProgressParams
		_ = json.Unmarshal(params, &p)
		r.record(func() { r.progress = append(r.progress, p.CallID+":"+p.Text) })
	case "ui/notify":
		var p ext.NotifyParams
		_ = json.Unmarshal(params, &p)
		r.record(func() { r.notifies = append(r.notifies, p) })
		return struct{}{}, nil
	case "ui/status":
		return struct{}{}, nil
	case "ui/confirm":
		var p ext.ConfirmParams
		_ = json.Unmarshal(params, &p)
		return ext.ConfirmResult{Confirmed: p.Title == "yes?"}, nil
	case "ui/select":
		var p ext.SelectParams
		_ = json.Unmarshal(params, &p)
		if len(p.Options) == 0 {
			return nil, errors.New("no options")
		}
		return ext.ValueResult{Value: p.Options[len(p.Options)-1]}, nil
	case "ui/input":
		return ext.ValueResult{Cancelled: true}, nil
	case "session/info":
		return ext.SessionInfo{ID: "s1", Cwd: "/work", Model: "test-model", ContextWindow: 1000, Tools: []string{"read"}}, nil
	case "session/messages":
		return ext.SessionMessagesResult{Messages: []ext.SessionEntry{
			{ID: "e1", Message: provider.UserText("hello")},
		}}, nil
	case "session/append_message":
		var p ext.AppendMessageParams
		_ = json.Unmarshal(params, &p)
		r.record(func() { r.appended = append(r.appended, p) })
		return struct{}{}, nil
	case "agent/set_model":
		var p ext.SetModelParams
		_ = json.Unmarshal(params, &p)
		if p.Model == "missing" {
			return nil, &rpc.Error{Code: rpc.CodeInvalidParams, Message: "unknown model missing"}
		}
		r.record(func() { r.models = append(r.models, p.Model) })
		return struct{}{}, nil
	case "agent/set_tools":
		var p ext.SetToolsParams
		_ = json.Unmarshal(params, &p)
		r.record(func() { r.tools = append(r.tools, p.Tools) })
		return struct{}{}, nil
	case "agent/cancel", "agent/compact":
		return struct{}{}, nil
	}
	return nil, &rpc.Error{Code: rpc.CodeMethodNotFound, Message: "unknown method " + method}
}

// waitFor polls cond until it holds or the test times out.
func (r *rhoSide) waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.After(10 * time.Second)
	for {
		r.mu.Lock()
		ok := cond()
		r.mu.Unlock()
		if ok {
			return
		}
		select {
		case <-r.changed:
		case <-time.After(10 * time.Millisecond):
		case <-deadline:
			t.Fatalf("timed out waiting for %s", what)
		}
	}
}

// serve writes files into a temporary extension directory and runs the
// host on it; a lone "ext.go" is run as a single file.
func serve(t *testing.T, files map[string]string) *rhoSide {
	t.Helper()
	dir := t.TempDir()
	for name, src := range files {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(src), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	path := dir
	if len(files) == 1 {
		if _, ok := files["ext.go"]; ok {
			path = filepath.Join(dir, "ext.go")
		}
	}
	return serveAt(t, path)
}

// serveAt runs the host on path over pipes. The host stops when the test
// ends; Serve must then return nil.
func serveAt(t *testing.T, path string) *rhoSide {
	t.Helper()
	hostIn, rhoOut := io.Pipe()
	rhoIn, hostOut := io.Pipe()
	r := &rhoSide{changed: make(chan struct{}, 1)}
	r.conn = rpc.NewConn(rhoIn, rhoOut, r.handle)
	done := make(chan error, 1)
	go func() { done <- Serve(path, hostIn, hostOut, io.Discard) }()
	t.Cleanup(func() {
		_ = rhoOut.Close() // the host sees EOF and stops
		select {
		case err := <-done:
			if err != nil {
				t.Errorf("Serve = %v, want nil after EOF", err)
			}
		case <-time.After(10 * time.Second):
			t.Error("Serve did not return after its input closed")
		}
		_ = hostOut.Close()
		<-r.conn.Done()
	})
	return r
}

func (r *rhoSide) initialize(t *testing.T, p ext.InitializeParams) (ext.InitializeResult, error) {
	t.Helper()
	var res ext.InitializeResult
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	err := r.conn.Call(ctx, "initialize", p, &res)
	return res, err
}

func (r *rhoSide) mustInit(t *testing.T) ext.InitializeResult {
	t.Helper()
	res, err := r.initialize(t, ext.InitializeParams{ProtocolVersion: ext.ProtocolVersion, Hooks: []string{"before_request"}, Methods: []string{"agent/set_tools"}})
	if err != nil {
		t.Fatalf("initialize: %v", err)
	}
	return res
}

func (r *rhoSide) execute(t *testing.T, callID, name string, args string) ext.ToolResult {
	t.Helper()
	var res ext.ToolResult
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	p := ext.ToolExecuteParams{CallID: callID, Name: name}
	if args != "" {
		p.Args = json.RawMessage(args)
	}
	if err := r.conn.Call(ctx, "tool/execute", p, &res); err != nil {
		t.Fatalf("tool/execute %s: %v", name, err)
	}
	return res
}

func (r *rhoSide) hook(t *testing.T, name string, p ext.HookParams) ext.HookResult {
	t.Helper()
	var res ext.HookResult
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := r.conn.Call(ctx, "hook/"+name, p, &res); err != nil {
		t.Fatalf("hook/%s: %v", name, err)
	}
	return res
}

func resultText(res ext.ToolResult) string {
	var b strings.Builder
	for _, c := range res.Content {
		b.WriteString(c.Text)
	}
	return b.String()
}

const toolsExt = `package main

import (
	"errors"
	"fmt"
	"strings"

	"rho"
)

func main() {
	rho.Info("probe", "1.2.3")
	rho.RegisterTool(rho.Tool{
		Name:        "shout",
		Description: "Upper-case text",
		Parameters:  map[string]any{"type": "object", "properties": map[string]any{"text": map[string]any{"type": "string"}}},
		ReadOnly:    true,
		Execute: func(ctx *rho.ToolContext, args map[string]any) (string, error) {
			ctx.Progress("working")
			return strings.ToUpper(fmt.Sprint(args["text"])), nil
		},
	})
	rho.RegisterTool(rho.Tool{
		Name: "fail",
		Execute: func(ctx *rho.ToolContext, args map[string]any) (string, error) {
			return "partial output", errors.New("it broke")
		},
	})
	rho.RegisterTool(rho.Tool{
		Name: "boom",
		Execute: func(ctx *rho.ToolContext, args map[string]any) (string, error) {
			var m map[string]int
			m["x"] = 1 // nil map write panics
			return "unreachable", nil
		},
	})
	rho.RegisterTool(rho.Tool{
		Name: "explode",
		Execute: func(ctx *rho.ToolContext, args map[string]any) (string, error) {
			panic("tool exploded")
		},
	})
	rho.RegisterTool(rho.Tool{
		Name: "nargs",
		Execute: func(ctx *rho.ToolContext, args map[string]any) (string, error) {
			return fmt.Sprintf("%d args, nil=%v", len(args), args == nil), nil
		},
	})
	rho.RegisterTool(rho.Tool{
		Name: "wait",
		Execute: func(ctx *rho.ToolContext, args map[string]any) (string, error) {
			ctx.Progress("waiting")
			<-ctx.Done()
			return fmt.Sprintf("cancelled=%v", ctx.Cancelled()), nil
		},
	})
	rho.RegisterTool(rho.Tool{Name: "noexec"})
}
`

func TestInitializeReportsRegistrations(t *testing.T) {
	r := serve(t, map[string]string{"ext.go": toolsExt + `
func init() {
	rho.RegisterCommand("hi", "Say hi", func(string) error { return nil })
	rho.On("turn_end", func(rho.Event) rho.Response { return rho.Response{} })
}
`})
	res := r.mustInit(t)
	if res.ProtocolVersion != ext.ProtocolVersion || res.Name != "probe" || res.Version != "1.2.3" {
		t.Errorf("initialize = protocol %d, name %q, version %q", res.ProtocolVersion, res.Name, res.Version)
	}
	var names []string
	for _, ti := range res.Tools {
		names = append(names, ti.Name)
	}
	if want := []string{"shout", "fail", "boom", "explode", "nargs", "wait", "noexec"}; !slices.Equal(names, want) {
		t.Errorf("tools = %v, want %v", names, want)
	}
	shout := res.Tools[0]
	if !shout.ReadOnly || shout.Description != "Upper-case text" {
		t.Errorf("shout = %+v", shout)
	}
	var schema map[string]any
	if err := json.Unmarshal(shout.Parameters, &schema); err != nil || schema["type"] != "object" || schema["properties"] == nil {
		t.Errorf("shout parameters = %s (%v)", shout.Parameters, err)
	}
	// A tool without parameters still offers an object schema.
	if got := string(res.Tools[1].Parameters); got != `{"type":"object","properties":{}}` {
		t.Errorf("default parameters = %s", got)
	}
	if len(res.Commands) != 1 || res.Commands[0] != (ext.CommandInfo{Name: "hi", Description: "Say hi"}) {
		t.Errorf("commands = %+v", res.Commands)
	}
	if !slices.Equal(res.Hooks, []string{"turn_end"}) {
		t.Errorf("hooks = %v", res.Hooks)
	}
}

func TestToolExecute(t *testing.T) {
	r := serve(t, map[string]string{"ext.go": toolsExt})
	r.mustInit(t)
	tests := []struct {
		name     string
		tool     string
		args     string
		want     string // substring of the result text
		wantErr  bool
		wantFull string // exact text, when set
	}{
		{name: "success", tool: "shout", args: `{"text":"hi"}`, wantFull: "HI"},
		{name: "error keeps output", tool: "fail", wantErr: true, wantFull: "partial output\nit broke"},
		{name: "runtime panic is recovered", tool: "boom", wantErr: true, want: "panic"},
		{name: "explicit panic is recovered", tool: "explode", wantErr: true, want: "tool exploded"},
		{name: "missing args are an empty map", tool: "nargs", wantFull: "0 args, nil=false"},
		{name: "null args are an empty map", tool: "nargs", args: `null`, wantFull: "0 args, nil=false"},
		{name: "invalid args", tool: "nargs", args: `[1,2]`, wantErr: true, want: "invalid arguments"},
		{name: "unknown tool", tool: "nope", wantErr: true, wantFull: "unknown tool nope"},
		{name: "tool without Execute", tool: "noexec", wantErr: true, wantFull: "unknown tool noexec"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			res := r.execute(t, "c-"+tt.tool, tt.tool, tt.args)
			got := resultText(res)
			if res.IsError != tt.wantErr {
				t.Errorf("IsError = %v, want %v (text %q)", res.IsError, tt.wantErr, got)
			}
			if tt.wantFull != "" && got != tt.wantFull {
				t.Errorf("text = %q, want %q", got, tt.wantFull)
			}
			if tt.want != "" && !strings.Contains(got, tt.want) {
				t.Errorf("text = %q, want it to contain %q", got, tt.want)
			}
		})
	}
	// The host keeps serving after a panicking tool.
	if got := resultText(r.execute(t, "again", "shout", `{"text":"still here"}`)); got != "STILL HERE" {
		t.Errorf("after panics, shout = %q", got)
	}
	// Progress is a notification on the read loop, so it arrives before
	// the result.
	r.mu.Lock()
	progress := slices.Clone(r.progress)
	r.mu.Unlock()
	if !slices.Contains(progress, "c-shout:working") {
		t.Errorf("progress = %v, want c-shout:working", progress)
	}
}

func TestToolCancel(t *testing.T) {
	r := serve(t, map[string]string{"ext.go": toolsExt})
	r.mustInit(t)
	got := make(chan ext.ToolResult, 1)
	go func() {
		var res ext.ToolResult
		_ = r.conn.Call(context.Background(), "tool/execute", ext.ToolExecuteParams{CallID: "w1", Name: "wait"}, &res)
		got <- res
	}()
	r.waitFor(t, "the tool to start", func() bool { return slices.Contains(r.progress, "w1:waiting") })
	// Cancelling another call does nothing.
	if err := r.conn.Call(context.Background(), "tool/cancel", map[string]string{"call_id": "other"}, nil); err != nil {
		t.Fatal(err)
	}
	if err := r.conn.Call(context.Background(), "tool/cancel", map[string]string{"call_id": "w1"}, nil); err != nil {
		t.Fatal(err)
	}
	select {
	case res := <-got:
		if text := resultText(res); res.IsError || text != "cancelled=true" {
			t.Errorf("cancelled tool = %q (error %v)", text, res.IsError)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("tool did not return after tool/cancel")
	}
}

func TestLoadFailures(t *testing.T) {
	tests := []struct {
		name  string
		files map[string]string
		want  string
	}{
		{
			name:  "panic in main",
			files: map[string]string{"ext.go": "package main\n\nfunc main() { panic(\"bad extension\") }\n"},
			want:  "panic while loading: bad extension",
		},
		{
			name:  "panic in init",
			files: map[string]string{"ext.go": "package main\n\nfunc init() { panic(\"init failed\") }\n\nfunc main() {}\n"},
			want:  "panic while loading: init failed",
		},
		{
			name:  "compile error",
			files: map[string]string{"ext.go": "package main\n\nfunc main() { undefinedThing() }\n"},
			want:  "undefined: undefinedThing",
		},
		{name: "missing file", want: "missing.go"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var r *rhoSide
			if tt.files == nil {
				r = serveAt(t, filepath.Join(t.TempDir(), "missing.go"))
			} else {
				r = serve(t, tt.files)
			}
			_, err := r.initialize(t, ext.InitializeParams{ProtocolVersion: ext.ProtocolVersion})
			if err == nil {
				t.Fatal("initialize succeeded, want the load error")
			}
			if tt.want != "" && !strings.Contains(err.Error(), tt.want) {
				t.Errorf("initialize error = %v, want it to mention %q", err, tt.want)
			}
			// The host still answers other requests after a failed load.
			if err := r.conn.Call(context.Background(), "shutdown", struct{}{}, nil); err != nil {
				t.Errorf("shutdown after failed load: %v", err)
			}
		})
	}
}

func TestDirectoryExtension(t *testing.T) {
	r := serve(t, map[string]string{
		"main.go": "//go:build rho\n\npackage main\n\nimport \"rho\"\n\nfunc main() { rho.Info(name(), \"0.0.1\") }\n",
		"name.go": "package main\n\nfunc name() string { return \"multi\" }\n",
		// Test files are not loaded, so this one cannot break the build.
		"main_test.go": "package main\n\nfunc broken( {\n",
		"notes.txt":    "not Go",
	})
	if res := r.mustInit(t); res.Name != "multi" {
		t.Errorf("name = %q, want multi", res.Name)
	}
}

func TestCommands(t *testing.T) {
	r := serve(t, map[string]string{"ext.go": `package main

import (
	"errors"

	"rho"
)

func main() {
	rho.RegisterCommand("greet", "Say hello", func(args string) error {
		rho.UI.Notify("hello " + args)
		return nil
	})
	rho.RegisterCommand("refuse", "", func(args string) error { return errors.New("not today") })
	rho.RegisterCommand("crash", "", func(string) error { panic("command crashed") })
}
`})
	r.mustInit(t)
	run := func(name, args string) error {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		return r.conn.Call(ctx, "command/run", ext.CommandRunParams{Name: name, Args: args}, nil)
	}
	tests := []struct {
		name, args string
		want       string // error substring, "" for success
	}{
		{"greet", "world", ""},
		{"refuse", "", "not today"},
		{"crash", "", "panic: command crashed"},
		{"nope", "", "unknown command nope"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := run(tt.name, tt.args)
			switch {
			case tt.want == "" && err != nil:
				t.Errorf("run = %v", err)
			case tt.want != "" && (err == nil || !strings.Contains(err.Error(), tt.want)):
				t.Errorf("run = %v, want error containing %q", err, tt.want)
			}
		})
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.notifies) != 1 || r.notifies[0] != (ext.NotifyParams{Level: "info", Text: "hello world"}) {
		t.Errorf("notifies = %+v", r.notifies)
	}
}

const hooksExt = `package main

import (
	"fmt"
	"strings"

	"rho"
)

func main() {
	rho.On("before_tool_call", func(e rho.Event) rho.Response {
		panic("first handler panics")
	})
	rho.On("before_tool_call", func(e rho.Event) rho.Response {
		if cmd, _ := e.Args["command"].(string); strings.Contains(cmd, "rm -rf") {
			return rho.Response{Block: "dangerous"}
		}
		if e.Tool == "read" {
			return rho.Response{Args: map[string]any{"path": "safe.txt"}}
		}
		return rho.Response{}
	})
	rho.On("before_tool_call", func(e rho.Event) rho.Response {
		// Sees the arguments the previous handler replaced.
		if e.Args["path"] == "safe.txt" {
			return rho.Response{Args: map[string]any{"path": "safe.txt", "seen": true}}
		}
		return rho.Response{}
	})
	rho.On("after_tool_call", func(e rho.Event) rho.Response {
		return rho.Response{ReplaceResult: true, Result: fmt.Sprintf("%s! (error=%v)", e.Result, e.IsError), IsError: !e.IsError}
	})
	rho.On("user_message", func(e rho.Event) rho.Response {
		if e.Text == "stop" {
			return rho.Response{Block: "no"}
		}
		return rho.Response{Text: "[" + e.Text + "]"}
	})
	rho.On("before_compaction", func(e rho.Event) rho.Response {
		return rho.Response{Summary: "summary of " + fmt.Sprint(len(e.Transcript)) + " bytes"}
	})
	rho.On("before_request", func(e rho.Event) rho.Response {
		msgs := append(e.Messages, map[string]any{"role": "user", "content": []any{map[string]any{"type": "text", "text": "extra"}}})
		return rho.Response{System: e.System + " +ext", Messages: msgs, Tools: e.Tools[:1]}
	})
	rho.On("message_end", func(e rho.Event) rho.Response {
		rho.Log("saved %s with role %v", e.EntryID, e.Message["role"])
		return rho.Response{}
	})
}
`

func TestHooks(t *testing.T) {
	r := serve(t, map[string]string{"ext.go": hooksExt})
	res := r.mustInit(t)
	slices.Sort(res.Hooks)
	if want := []string{"after_tool_call", "before_compaction", "before_request", "before_tool_call", "message_end", "user_message"}; !slices.Equal(res.Hooks, want) {
		t.Errorf("hooks = %v, want %v", res.Hooks, want)
	}

	t.Run("block despite a panicking handler", func(t *testing.T) {
		got := r.hook(t, "before_tool_call", ext.HookParams{Tool: "bash", Args: json.RawMessage(`{"command":"rm -rf /"}`)})
		if got.Block != "dangerous" {
			t.Errorf("block = %q, want dangerous", got.Block)
		}
	})
	t.Run("args are replaced in order", func(t *testing.T) {
		got := r.hook(t, "before_tool_call", ext.HookParams{Tool: "read", Args: json.RawMessage(`{"path":"secret.txt"}`)})
		var args map[string]any
		if err := json.Unmarshal(got.Args, &args); err != nil {
			t.Fatal(err)
		}
		if args["path"] != "safe.txt" || args["seen"] != true || got.Block != "" {
			t.Errorf("hook result = %+v, args %v", got, args)
		}
	})
	t.Run("no change", func(t *testing.T) {
		got := r.hook(t, "before_tool_call", ext.HookParams{Tool: "ls"})
		if got.Block != "" || got.Args != nil || got.Result != nil {
			t.Errorf("hook result = %+v, want zero", got)
		}
	})
	t.Run("result replaced", func(t *testing.T) {
		got := r.hook(t, "after_tool_call", ext.HookParams{Tool: "ls", Result: &ext.ToolResult{Content: []ext.ContentBlock{{Type: "text", Text: "a"}, {Type: "text", Text: "b"}}}})
		if got.Result == nil || resultText(*got.Result) != "ab! (error=false)" || !got.Result.IsError {
			t.Errorf("result = %+v", got.Result)
		}
	})
	t.Run("user message", func(t *testing.T) {
		if got := r.hook(t, "user_message", ext.HookParams{Text: "hi"}); got.Text != "[hi]" {
			t.Errorf("text = %q", got.Text)
		}
		if got := r.hook(t, "user_message", ext.HookParams{Text: "stop"}); got.Block != "no" || got.Text != "" {
			t.Errorf("stop = %+v", got)
		}
	})
	t.Run("compaction summary", func(t *testing.T) {
		if got := r.hook(t, "before_compaction", ext.HookParams{Transcript: "12345"}); got.Summary != "summary of 5 bytes" {
			t.Errorf("summary = %q", got.Summary)
		}
	})
	t.Run("before request", func(t *testing.T) {
		got := r.hook(t, "before_request", ext.HookParams{
			System:   "sys",
			Messages: []provider.Message{provider.UserText("question")},
			Tools:    []string{"read", "bash"},
		})
		if got.System != "sys +ext" || !slices.Equal(got.Tools, []string{"read"}) {
			t.Errorf("system %q, tools %v", got.System, got.Tools)
		}
		if len(got.Messages) != 2 || got.Messages[0].TextContent() != "question" || got.Messages[1].TextContent() != "extra" || got.Messages[1].Role != provider.RoleUser {
			t.Errorf("messages = %+v", got.Messages)
		}
	})
	t.Run("unregistered hook", func(t *testing.T) {
		if got := r.hook(t, "session_start", ext.HookParams{}); got.Block != "" || got.Text != "" {
			t.Errorf("session_start = %+v", got)
		}
	})
	t.Run("notification hook runs and may call back", func(t *testing.T) {
		msg := provider.UserText("x")
		if err := r.conn.Notify("hook/message_end", ext.HookParams{EntryID: "e9", Message: &msg}); err != nil {
			t.Fatal(err)
		}
		r.waitFor(t, "the message_end log line", func() bool { return slices.Contains(r.logs, "saved e9 with role user") })
	})
}

func TestUnknownMethod(t *testing.T) {
	r := serve(t, map[string]string{"ext.go": "package main\n\nfunc main() {}\n"})
	r.mustInit(t)
	err := r.conn.Call(context.Background(), "nope/method", struct{}{}, nil)
	var re *rpc.Error
	if !errors.As(err, &re) || re.Code != rpc.CodeMethodNotFound {
		t.Errorf("unknown method error = %v, want code %d", err, rpc.CodeMethodNotFound)
	}
}

func TestAPICallsBackIntoRho(t *testing.T) {
	r := serve(t, map[string]string{"ext.go": `package main

import (
	"fmt"
	"strings"

	"rho"
)

func main() {
	rho.RegisterTool(rho.Tool{
		Name: "probe",
		Execute: func(ctx *rho.ToolContext, args map[string]any) (string, error) {
			var out []string
			add := func(format string, a ...any) { out = append(out, fmt.Sprintf(format, a...)) }
			info, err := rho.Session.Info()
			add("info %s %s %s %d %v %v", info.ID, info.Model, info.Cwd, info.ContextWindow, info.Tools, err)
			msgs, err := rho.Session.Messages()
			add("messages %d %v %v", len(msgs), msgs[0]["id"], err)
			add("set_model %v", rho.Agent.SetModel("other"))
			add("set_model_bad %v", rho.Agent.SetModel("missing"))
			add("set_tools %v", rho.Agent.SetTools([]string{"read", "grep"}))
			add("cancel %v compact %v", rho.Agent.Cancel(), rho.Agent.Compact())
			add("append %v", rho.AppendMessage("follow up", true))
			add("confirm %v %v", rho.UI.Confirm("yes?", "m"), rho.UI.Confirm("no?", "m"))
			v, ok := rho.UI.Select("pick", []string{"a", "b"})
			add("select %s %v", v, ok)
			v, ok = rho.UI.Select("pick", nil)
			add("select_err %q %v", v, ok)
			v, ok = rho.UI.Input("name", "")
			add("input %q %v", v, ok)
			rho.UI.Status("busy")
			add("supports %v %v %v", rho.Supports("before_request"), rho.Supports("agent/set_tools"), rho.Supports("hook/nope"))
			rho.Log("probe ran")
			return strings.Join(out, "\n"), nil
		},
	})
}
`})
	r.mustInit(t)
	res := r.execute(t, "p1", "probe", "")
	if res.IsError {
		t.Fatalf("probe failed: %s", resultText(res))
	}
	lines := strings.Split(resultText(res), "\n")
	want := []string{
		"info s1 test-model /work 1000 [read] <nil>",
		"messages 1 e1 <nil>",
		"set_model <nil>",
		"set_model_bad unknown model missing",
		"set_tools <nil>",
		"cancel <nil> compact <nil>",
		"append <nil>",
		"confirm true false",
		"select b true",
		`select_err "" false`,
		`input "" false`,
		"supports true true false",
	}
	if !slices.Equal(lines, want) {
		t.Errorf("probe output:\n%s\nwant:\n%s", strings.Join(lines, "\n"), strings.Join(want, "\n"))
	}
	r.waitFor(t, "the log line", func() bool { return slices.Contains(r.logs, "probe ran") })
	r.mu.Lock()
	defer r.mu.Unlock()
	if !slices.Equal(r.models, []string{"other"}) {
		t.Errorf("models = %v", r.models)
	}
	if len(r.tools) != 1 || !slices.Equal(r.tools[0], []string{"read", "grep"}) {
		t.Errorf("tools = %v", r.tools)
	}
	if len(r.appended) != 1 || r.appended[0] != (ext.AppendMessageParams{Text: "follow up", Trigger: true}) {
		t.Errorf("appended = %+v", r.appended)
	}
	for _, m := range []string{"agent/cancel", "agent/compact", "ui/status", "ui/input"} {
		if !slices.Contains(r.calls, m) {
			t.Errorf("extension did not call %s (calls %v)", m, r.calls)
		}
	}
}

// TestAPIWithoutHost checks the API functions fail cleanly when no host is
// connected, as they would if called outside Serve.
func TestAPIWithoutHost(t *testing.T) {
	curMu.Lock()
	saved := curHost
	curHost = nil
	curMu.Unlock()
	t.Cleanup(func() {
		curMu.Lock()
		curHost = saved
		curMu.Unlock()
	})
	if err := AppendMessage("x", false); err == nil {
		t.Error("AppendMessage succeeded without a host")
	}
	if _, err := Session.Info(); err == nil {
		t.Error("Session.Info succeeded without a host")
	}
	if _, err := Session.Messages(); err == nil {
		t.Error("Session.Messages succeeded without a host")
	}
	for name, f := range map[string]func() error{
		"SetModel": func() error { return Agent.SetModel("m") },
		"SetTools": func() error { return Agent.SetTools(nil) },
		"Cancel":   Agent.Cancel,
		"Compact":  Agent.Compact,
	} {
		if err := f(); err == nil {
			t.Errorf("Agent.%s succeeded without a host", name)
		}
	}
	if Supports("before_request") {
		t.Error("Supports is true without a host")
	}
	if UI.Confirm("t", "m") {
		t.Error("Confirm is true without a host")
	}
	// Without a host there is nobody to cancel, so Select and Input report
	// no cancellation and an empty value.
	if v, ok := UI.Select("t", []string{"a"}); v != "" || !ok {
		t.Errorf("Select = %q, %v", v, ok)
	}
	if v, ok := UI.Input("t", ""); v != "" || !ok {
		t.Errorf("Input = %q, %v", v, ok)
	}
	// These must not panic.
	UI.Notify("x")
	UI.Status("x")
	Log("x %d", 1)
	(&ToolContext{CallID: "c", ctx: context.Background()}).Progress("x")
}
