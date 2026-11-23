// Package integration holds cross-cutting tests that run Rho the way it
// is used: a real config file, the real provider talking HTTP to a server
// that replays recorded model output with faults injected (see
// core/provider/replay), the real tools on a real project directory,
// session files on disk, and the JSON lines a script reads from
// `rho -p --json`. Some tests run the rho binary itself, to send it
// signals and kill it mid-turn.
package integration

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/NDari/rho/core/app"
	"github.com/NDari/rho/core/provider"
	"github.com/NDari/rho/core/provider/replay"
	"github.com/NDari/rho/core/session"
	"github.com/NDari/rho/headless"
)

// env is one user's machine: a Rho home with a config file, a project
// directory, and a model server.
type env struct {
	t    *testing.T
	home string
	cwd  string
	srv  *replay.Server
}

// newEnv writes a config for a local server like llama-server.
func newEnv(t *testing.T, replies ...replay.Reply) *env {
	t.Helper()
	e := &env{t: t, home: t.TempDir(), cwd: t.TempDir(), srv: replay.New(replies...)}
	t.Cleanup(e.srv.Close)
	cfg := fmt.Sprintf(`default_model = "local"
max_retries = 3
update_check = false

[providers.local]
base_url = %q

[[models]]
id = "local"
name = "qwen"
provider = "local"
context_window = 65536
max_output = 4096

[compaction]
reserve = 1000
keep_recent = 40
`, e.srv.URL+"/v1")
	e.write(filepath.Join(e.home, "config.toml"), cfg)
	e.write(filepath.Join(e.cwd, "go.mod"), "module example.com/demo\n\ngo 1.25\n")
	e.write(filepath.Join(e.cwd, "main.go"), "package main\n\nfunc main() {}\n")
	t.Setenv("RHO_HOME", e.home)
	t.Setenv("RHO_NO_UPDATE_CHECK", "1")
	return e
}

func (e *env) write(path, content string) {
	e.t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		e.t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		e.t.Fatal(err)
	}
}

// syncBuffer collects output written from the agent's goroutines.
type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

// run is one `rho -p --json` invocation, in process.
type run struct {
	lines  []map[string]any
	result map[string]any
	stderr string
	err    error
	path   string // session file
}

type runOpts struct {
	cont       bool
	extensions bool
}

func (e *env) run(prompt string, o runOpts) run {
	e.t.Helper()
	var out, errOut syncBuffer
	hopts := headless.Options{JSON: true, Out: &out, ErrOut: &errOut}
	a, err := app.New(app.Options{
		Cwd: e.cwd, Continue: o.cont, NoExtensions: !o.extensions,
		Sink: headless.Sink(hopts), RetryDelay: time.Millisecond, Version: "test",
	})
	if err != nil {
		e.t.Fatalf("app.New: %v", err)
	}
	for _, w := range a.Warnings {
		fmt.Fprintln(&errOut, "warning:", w)
	}
	ctx := context.Background()
	if o.extensions {
		a.StartExtensions(ctx, headless.UI{ErrOut: &errOut})
	}
	r := run{err: headless.Run(ctx, a, prompt, hopts), path: a.Agent.Session().Path()}
	if o.extensions {
		a.StopExtensions()
	}
	_ = a.Agent.Session().Close()
	r.lines = jsonLines(e.t, out.String())
	r.stderr = errOut.String()
	if n := len(r.lines); n > 0 && r.lines[n-1]["type"] == "result" {
		r.result = r.lines[n-1]
	} else {
		e.t.Fatalf("no result line in:\n%s", out.String())
	}
	return r
}

func jsonLines(t *testing.T, s string) []map[string]any {
	t.Helper()
	var out []map[string]any
	for _, l := range strings.Split(strings.TrimSpace(s), "\n") {
		if l == "" {
			continue
		}
		var m map[string]any
		if err := json.Unmarshal([]byte(l), &m); err != nil {
			t.Fatalf("not JSON: %q", l)
		}
		out = append(out, m)
	}
	return out
}

func (r run) ofType(typ string) []map[string]any {
	var out []map[string]any
	for _, l := range r.lines {
		if l["type"] == typ {
			out = append(out, l)
		}
	}
	return out
}

// toolFailed reports whether a tool_end event carries an error result.
func toolFailed(l map[string]any) bool {
	res, _ := l["result"].(map[string]any)
	return res["is_error"] == true
}

func (r run) notices(level string) string {
	var b strings.Builder
	for _, n := range r.ofType("notice") {
		if n["level"] == level {
			fmt.Fprintln(&b, n["text"])
		}
	}
	return b.String()
}

// reopen loads the session file fresh from disk, as the next start would,
// and fails on any damage.
func reopen(t *testing.T, path string) *session.Session {
	t.Helper()
	s, err := session.Open(path)
	if err != nil {
		t.Fatalf("reopen session: %v", err)
	}
	if p := s.Problems(); len(p) > 0 {
		t.Fatalf("session has problems: %q", p)
	}
	return s
}

// roles summarizes the conversation as the model will see it.
func roles(s *session.Session) string {
	var parts []string
	for _, m := range s.BuildContext().Messages {
		r := string(m.Role)
		if calls := m.ToolCalls(); len(calls) > 0 {
			r += "(" + calls[0].Name + ")"
		}
		if m.Meta.StopReason == provider.StopCancelled {
			r += "[cancelled]"
		}
		parts = append(parts, r)
	}
	return strings.Join(parts, " ")
}

// sentMessages are the messages of request i as the server received them.
func (e *env) sentMessages(i int) []any {
	e.t.Helper()
	reqs := e.srv.Requests()
	if i >= len(reqs) {
		e.t.Fatalf("only %d requests", len(reqs))
	}
	msgs, _ := reqs[i]["messages"].([]any)
	return msgs
}

// Recorded and synthesized model replies.
var (
	recordedToolCall = replay.Reply{Body: replay.Recording("llamacpp_chat_tool_call.sse")}
	recordedText     = replay.Reply{Body: replay.Recording("llamacpp_chat_text.sse")}
)

func textReply(text string) replay.Reply {
	return replay.Reply{Body: replay.Chat{Thinking: "Let me answer.", Text: text, InputTokens: 300, OutputTokens: 40}.SSE()}
}

func callReply(name, args string) replay.Reply {
	return replay.Reply{Body: replay.Chat{Thinking: "I will use a tool.", Calls: []replay.ChatCall{{ID: "call_" + name, Name: name, Args: args}}, InputTokens: 300, OutputTokens: 20}.SSE()}
}

// half cuts a reply at the event boundary nearest its middle.
func half(r replay.Reply) int {
	evs := replay.Events(r.Body)
	n := 0
	for _, ev := range evs[:len(evs)/2] {
		n += len(ev)
	}
	return n
}

func readAll(p string) ([]byte, error) { return os.ReadFile(p) }
