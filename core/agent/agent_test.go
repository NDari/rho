package agent

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/NDari/rho/core/config"
	"github.com/NDari/rho/core/events"
	"github.com/NDari/rho/core/provider"
	"github.com/NDari/rho/core/provider/fake"
	"github.com/NDari/rho/core/session"
	"github.com/NDari/rho/core/tools"
)

type providers map[string]provider.Provider

func (p providers) Get(name string) (provider.Provider, error) {
	if pr, ok := p[name]; ok {
		return pr, nil
	}
	return nil, errors.New("unknown provider " + name)
}

type recorder struct {
	mu     sync.Mutex
	events []events.Event
}

func (r *recorder) sink(e events.Event) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.events = append(r.events, e)
}

func (r *recorder) types() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	var out []string
	for _, e := range r.events {
		out = append(out, e.Type())
	}
	return out
}

func newTestAgent(t *testing.T, fp *fake.Provider) (*Agent, *recorder, string) {
	t.Helper()
	dir := t.TempDir()
	cfg := config.Defaults()
	cfg.Providers["fake"] = config.Provider{Type: config.ProviderOpenAICompat, BaseURL: "http://x"}
	model := config.Model{ID: "m", Provider: "fake", ContextWindow: 100000, MaxOutput: 1000}
	cfg.Models = []config.Model{model}
	cfg.MaxRetries = 2
	rec := &recorder{}
	a := New(Options{
		Config:    cfg,
		Paths:     config.Paths{Home: filepath.Join(dir, "home"), Cwd: dir, ProjectRoot: dir, ProjectDir: filepath.Join(dir, ".rho")},
		Session:   session.New("", dir, "test"),
		Providers: providers{"fake": fp},
		Model:     model,
		Tools:     tools.Builtins(),
		Sink:      rec.sink,
	})
	return a, rec, dir
}

func TestTurnWithToolCall(t *testing.T) {
	fp := fake.New("fake",
		fake.Step{Calls: []fake.Call{{Name: "write", Args: map[string]string{"path": "a.txt", "content": "hi\n"}}}},
		fake.Step{Text: "done", Check: func(r provider.Request) {
			last := r.Messages[len(r.Messages)-1]
			if last.Role != provider.RoleToolResult || last.Content[0].IsError {
				t.Errorf("expected successful tool result, got %+v", last)
			}
		}},
	)
	a, rec, dir := newTestAgent(t, fp)
	if err := a.Run(context.Background(), provider.UserText("write a file")); err != nil {
		t.Fatal(err)
	}
	if data, _ := os.ReadFile(filepath.Join(dir, "a.txt")); string(data) != "hi\n" {
		t.Errorf("file = %q", data)
	}
	got := strings.Join(rec.types(), ",")
	for _, want := range []string{"turn_start", "tool_start", "tool_end", "turn_end"} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %s in %s", want, got)
		}
	}
	cv := a.Session().BuildContext()
	if len(cv.Messages) != 4 {
		t.Errorf("messages = %d, want 4", len(cv.Messages))
	}
}

// TestTurnDuration checks that the turn's time is measured from start to
// end and reported in turn_end and the turn's stats.
func TestTurnDuration(t *testing.T) {
	const pause = 50 * time.Millisecond
	fp := fake.New("fake", fake.Step{Text: "ok", Check: func(provider.Request) { time.Sleep(pause) }})
	a, rec, _ := newTestAgent(t, fp)
	if err := a.Run(context.Background(), provider.UserText("hi")); err != nil {
		t.Fatal(err)
	}
	var end events.TurnEnd
	for _, e := range rec.events {
		if te, ok := e.(events.TurnEnd); ok {
			end = te
		}
	}
	if end.Duration < pause || end.Duration > 10*time.Second {
		t.Errorf("turn_end duration = %s, want at least %s", end.Duration, pause)
	}
	if got := a.LastTurn().Duration; got != end.Duration {
		t.Errorf("LastTurn().Duration = %s, turn_end has %s", got, end.Duration)
	}
}

func TestRetryOnTransientError(t *testing.T) {
	fp := fake.New("fake",
		fake.Step{Err: &provider.Error{Kind: provider.ErrTransient, StatusCode: 503, Message: "busy"}},
		fake.Step{Text: "ok"},
	)
	a, rec, _ := newTestAgent(t, fp)
	start := time.Now()
	if err := a.Run(context.Background(), provider.UserText("hi")); err != nil {
		t.Fatal(err)
	}
	if time.Since(start) < time.Second {
		t.Error("expected a backoff delay")
	}
	if !strings.Contains(strings.Join(rec.types(), ","), "retry") {
		t.Error("no retry event")
	}
}

func TestNonTransientErrorEndsTurn(t *testing.T) {
	fp := fake.New("fake", fake.Step{Err: &provider.Error{Kind: provider.ErrAuth, StatusCode: 401, Message: "bad key"}})
	a, _, _ := newTestAgent(t, fp)
	err := a.Run(context.Background(), provider.UserText("hi"))
	if err == nil || !strings.Contains(err.Error(), "bad key") {
		t.Fatalf("err = %v", err)
	}
	if a.Busy() {
		t.Error("agent still busy")
	}
}

func TestCancelDuringStream(t *testing.T) {
	fp := fake.New("fake", fake.Step{Text: "partial", Wait: true})
	a, _, _ := newTestAgent(t, fp)
	if err := a.Prompt(provider.UserText("hi")); err != nil {
		t.Fatal(err)
	}
	time.Sleep(50 * time.Millisecond)
	a.Cancel()
	a.Wait()
	cv := a.Session().BuildContext()
	last := cv.Messages[len(cv.Messages)-1]
	if last.Role != provider.RoleAssistant || last.TextContent() != "partial" || last.Meta.StopReason != provider.StopCancelled {
		t.Errorf("partial message not recorded: %+v", last)
	}
}

func TestSteeringSkipsRemainingTools(t *testing.T) {
	fp := fake.New("fake",
		fake.Step{Calls: []fake.Call{
			{ID: "1", Name: "bash", Args: map[string]string{"command": "sleep 0.3"}},
			{ID: "2", Name: "bash", Args: map[string]string{"command": "echo never"}},
		}},
		fake.Step{Text: "ok", Check: func(r provider.Request) {
			n := len(r.Messages)
			if r.Messages[n-1].TextContent() != "change of plan" {
				t.Errorf("steering message not last: %+v", r.Messages[n-1])
			}
			skipped := r.Messages[n-2].Content[0]
			if !skipped.IsError || !strings.Contains(skipped.Content[0].Text, "Skipped") {
				t.Errorf("second tool not skipped: %+v", skipped)
			}
		}},
	)
	a, _, _ := newTestAgent(t, fp)
	if err := a.Prompt(provider.UserText("go")); err != nil {
		t.Fatal(err)
	}
	time.Sleep(100 * time.Millisecond)
	if err := a.Steer("change of plan"); err != nil {
		t.Fatal(err)
	}
	a.Wait()
}

func TestFollowUpDeliveredAfterTurn(t *testing.T) {
	fp := fake.New("fake",
		fake.Step{Calls: []fake.Call{{Name: "bash", Args: map[string]string{"command": "sleep 0.2"}}}},
		fake.Step{Text: "first done"},
		fake.Step{Text: "second done", Check: func(r provider.Request) {
			if r.Messages[len(r.Messages)-1].TextContent() != "and then this" {
				t.Errorf("follow-up not delivered last")
			}
		}},
	)
	a, _, _ := newTestAgent(t, fp)
	if err := a.Prompt(provider.UserText("go")); err != nil {
		t.Fatal(err)
	}
	time.Sleep(50 * time.Millisecond)
	if err := a.FollowUp("and then this"); err != nil {
		t.Fatal(err)
	}
	a.Wait()
	if len(fp.Requests) != 3 {
		t.Errorf("requests = %d, want 3", len(fp.Requests))
	}
}

func TestOverflowCompactsAndRetries(t *testing.T) {
	fp := fake.New("fake",
		fake.Step{Text: "first"},
		fake.Step{Text: "second"},
		fake.Step{Err: &provider.Error{Kind: provider.ErrContextOverflow, Message: "context length exceeded"}},
		fake.Step{Text: "SUMMARY"}, // compaction
		fake.Step{Text: "after", Check: func(r provider.Request) {
			if !strings.Contains(r.Messages[0].TextContent(), "SUMMARY") {
				t.Errorf("summary not sent first: %+v", r.Messages[0])
			}
		}},
	)
	a, _, _ := newTestAgent(t, fp)
	ctx := context.Background()
	for _, p := range []string{"one", "two", "three"} {
		if err := a.Run(ctx, provider.UserText(p)); err != nil {
			t.Fatalf("%s: %v", p, err)
		}
	}
	cv := a.Session().BuildContext()
	if cv.Compaction == nil {
		t.Fatal("no compaction entry")
	}
}

// smallWindow sets a window too small for the model's whole output limit,
// but large enough that the first turns do not compact.
func smallWindow(a *Agent) {
	cfg := a.Config()
	cfg.Models[0].ContextWindow = 10000
	cfg.Models[0].MaxOutput = 8000
	a.SetConfig(cfg)
}

func TestOutputLimitLoweredNearWindow(t *testing.T) {
	var sent []int
	check := func(r provider.Request) { sent = append(sent, r.MaxTokens) }
	fp := fake.New("fake", fake.Step{Text: "a", Check: check})
	a, _, _ := newTestAgent(t, fp)
	if err := a.Run(context.Background(), provider.UserText("one")); err != nil {
		t.Fatal(err)
	}
	fp.Add(fake.Step{Text: "b", Check: check})
	smallWindow(a)
	if err := a.Run(context.Background(), provider.UserText("two")); err != nil {
		t.Fatal(err)
	}
	if sent[0] != 1000 {
		t.Errorf("large window: max tokens = %d, want the model's 1000", sent[0])
	}
	if sent[1] >= 8000 || sent[1] < 4096 {
		t.Errorf("small window: max tokens = %d, want lowered to fit", sent[1])
	}
}

func TestThinkingNotSentLeavesOutputLimit(t *testing.T) {
	// A long thinking turn: its output tokens are mostly thinking, which
	// the next request does not send, so the next request has the room.
	var limit int
	fp := fake.New("fake",
		fake.Step{Thinking: strings.Repeat("x", 80000), Text: "a", Usage: provider.Usage{InputTokens: 2000, OutputTokens: 20000}},
		fake.Step{Text: "b", Check: func(r provider.Request) { limit = r.MaxTokens }},
	)
	a, _, _ := newTestAgent(t, fp)
	cfg := a.Config()
	cfg.Models[0].ContextWindow = 40000
	cfg.Models[0].MaxOutput = 16000
	a.SetConfig(cfg)
	ctx := context.Background()
	for _, p := range []string{"one", "two"} {
		if err := a.Run(ctx, provider.UserText(p)); err != nil {
			t.Fatalf("%s: %v", p, err)
		}
	}
	if limit != 16000 {
		t.Errorf("max tokens = %d, want the model's 16000", limit)
	}
	if got := a.EstimateContext(a.Session().BuildContext()); got > 2100 {
		t.Errorf("context estimate = %d, want about 2000 (thinking not counted)", got)
	}
}

func TestCutOffAtLoweredLimitCompactsAndRetries(t *testing.T) {
	fp := fake.New("fake",
		fake.Step{Text: strings.Repeat("x", 80000)}, // about 20k tokens
		fake.Step{Text: "partial", Stop: provider.StopMaxTokens},
		fake.Step{Text: "SUMMARY"}, // compaction
		fake.Step{Text: "after"},
	)
	a, rec, _ := newTestAgent(t, fp)
	// Below the compaction trigger (24k), but too full for the whole
	// output limit; compaction would make room for it.
	cfg := a.Config()
	cfg.Models[0].ContextWindow = 40000
	cfg.Models[0].MaxOutput = 30000
	a.SetConfig(cfg)
	ctx := context.Background()
	for _, p := range []string{"one", "two"} {
		if err := a.Run(ctx, provider.UserText(p)); err != nil {
			t.Fatalf("%s: %v", p, err)
		}
	}
	cv := a.Session().BuildContext()
	if cv.Compaction == nil {
		t.Fatal("no compaction entry")
	}
	for _, m := range cv.Messages {
		if m.TextContent() == "partial" {
			t.Error("the cut-off response was kept")
		}
	}
	if got := cv.Messages[len(cv.Messages)-1].TextContent(); got != "after" {
		t.Errorf("last answer = %q", got)
	}
	if !strings.Contains(strings.Join(rec.types(), ","), "compaction_start") {
		t.Error("no compaction event")
	}
}

func TestCutOffInSmallContextIsKept(t *testing.T) {
	// The limit is lowered because the window is small, not because the
	// context is full, so compacting would not help.
	fp := fake.New("fake",
		fake.Step{Text: "first"},
		fake.Step{Text: "partial", Stop: provider.StopMaxTokens},
	)
	a, _, _ := newTestAgent(t, fp)
	smallWindow(a)
	ctx := context.Background()
	for _, p := range []string{"one", "two"} {
		if err := a.Run(ctx, provider.UserText(p)); err != nil {
			t.Fatalf("%s: %v", p, err)
		}
	}
	cv := a.Session().BuildContext()
	if cv.Compaction != nil {
		t.Error("compacted a context that compaction cannot shrink")
	}
	if got := cv.Messages[len(cv.Messages)-1].TextContent(); got != "partial" {
		t.Errorf("last answer = %q", got)
	}
}

func TestCutOffAtModelLimitIsKept(t *testing.T) {
	fp := fake.New("fake", fake.Step{Text: "partial", Stop: provider.StopMaxTokens})
	a, rec, _ := newTestAgent(t, fp)
	if err := a.Run(context.Background(), provider.UserText("one")); err != nil {
		t.Fatal(err)
	}
	cv := a.Session().BuildContext()
	if cv.Compaction != nil {
		t.Error("compacted after a cut-off at the model's own limit")
	}
	if got := cv.Messages[len(cv.Messages)-1].TextContent(); got != "partial" {
		t.Errorf("last answer = %q", got)
	}
	rec.mu.Lock()
	defer rec.mu.Unlock()
	warned := false
	for _, e := range rec.events {
		if n, ok := e.(events.Notice); ok && strings.Contains(n.Text, "cut off at the output token limit (1000 tokens)") {
			warned = true
		}
	}
	if !warned {
		t.Error("no cut-off warning")
	}
}

func TestRepairToolPairs(t *testing.T) {
	msgs := []provider.Message{
		provider.UserText("hi"),
		{Role: provider.RoleAssistant, Content: []provider.Block{{Type: provider.BlockToolCall, ID: "a", Name: "read"}}},
		provider.UserText("interrupted"),
		{Role: provider.RoleToolResult, Content: []provider.Block{provider.ToolResult("orphan", "x", false)}},
	}
	out := RepairToolPairs(msgs)
	if len(out) != 4 || out[2].Role != provider.RoleToolResult || out[2].Content[0].CallID != "a" {
		t.Fatalf("repair failed: %+v", out)
	}
	if out[3].Role != provider.RoleUser {
		t.Errorf("orphan result not dropped: %+v", out[3])
	}
}

func TestSetModelRecordsChange(t *testing.T) {
	fp := fake.New("fake")
	a, _, _ := newTestAgent(t, fp)
	a.cfg.Models = append(a.cfg.Models, config.Model{ID: "m2", Provider: "fake"})
	if err := a.SetModel("m2"); err != nil {
		t.Fatal(err)
	}
	if a.Model().ID != "m2" {
		t.Errorf("model = %s", a.Model().ID)
	}
	if cv := a.Session().BuildContext(); cv.Model != "m2" {
		t.Errorf("session model = %q", cv.Model)
	}
}

func TestAsideUsesReadOnlyTools(t *testing.T) {
	fp := fake.New("fake",
		fake.Step{Text: "main"},
		fake.Step{Calls: []fake.Call{{Name: "bash", Args: map[string]string{"command": "echo hi"}}}, Check: func(r provider.Request) {
			var names []string
			for _, tl := range r.Tools {
				names = append(names, tl.Name)
			}
			if got := strings.Join(names, ","); got != "read,grep,find,ls" {
				t.Errorf("aside tools = %s, want read,grep,find,ls", got)
			}
		}},
		fake.Step{Text: "aside done"},
	)
	a, _, _ := newTestAgent(t, fp)
	ctx := context.Background()
	if err := a.Run(ctx, provider.UserText("main q")); err != nil {
		t.Fatal(err)
	}
	if err := a.StartAside(); err != nil {
		t.Fatal(err)
	}
	if err := a.Run(ctx, provider.UserText("aside q")); err != nil {
		t.Fatal(err)
	}
	cv := a.Session().BuildContext()
	res := cv.Messages[len(cv.Messages)-2]
	if !res.Content[0].IsError {
		t.Errorf("bash should be refused in aside: %+v", res)
	}
	if err := a.EndAside(""); err != nil {
		t.Fatal(err)
	}
	cv = a.Session().BuildContext()
	if cv.Aside || len(cv.Messages) != 2 {
		t.Errorf("back on main: aside=%v messages=%d", cv.Aside, len(cv.Messages))
	}
}

func TestCompactionReusesConversationPrefix(t *testing.T) {
	var turn provider.Request
	fp := fake.New("fake",
		fake.Step{Text: "first", Check: func(r provider.Request) { turn = r }},
		fake.Step{Text: "second"},
		fake.Step{Text: "SUMMARY", Check: func(r provider.Request) {
			if r.System != turn.System || len(r.Tools) != len(turn.Tools) {
				t.Errorf("summary request does not reuse the system prompt and tools")
			}
			if len(r.Messages) < 2 || r.Messages[0].TextContent() != "one" {
				t.Errorf("summary request does not start with the conversation: %+v", r.Messages)
			}
			if last := r.Messages[len(r.Messages)-1]; !strings.Contains(last.TextContent(), "Do not call any tools") {
				t.Errorf("last message is not the instruction: %q", last.TextContent())
			}
		}},
	)
	a, rec, _ := newTestAgent(t, fp)
	ctx := context.Background()
	for _, p := range []string{"one", "two"} {
		if err := a.Run(ctx, provider.UserText(p)); err != nil {
			t.Fatal(err)
		}
	}
	if err := a.Compact(ctx); err != nil {
		t.Fatal(err)
	}
	if cv := a.Session().BuildContext(); cv.Compaction == nil || cv.Compaction.Summary != "SUMMARY" {
		t.Fatalf("compaction = %+v", cv.Compaction)
	}
	if strings.Contains(strings.Join(rec.types(), ","), "notice") {
		t.Error("unexpected fallback notice")
	}
}

// TestSystemPromptIsKeptUntilThePrefixChanges checks that an edit to
// AGENTS.md, here by the model itself, leaves the system prompt alone (so
// the provider's prompt cache and thinking blocks stay valid) until
// /reload or compaction.
func TestSystemPromptIsKeptUntilThePrefixChanges(t *testing.T) {
	has := func(want string) func(provider.Request) {
		return func(r provider.Request) {
			t.Helper()
			if !strings.Contains(r.System, want) {
				t.Errorf("system prompt lacks %q", want)
			}
		}
	}
	fp := fake.New("fake",
		fake.Step{Calls: []fake.Call{{Name: "write", Args: map[string]string{"path": "AGENTS.md", "content": "rule two\n"}}}, Check: has("rule one")},
		fake.Step{Text: "done", Check: has("rule one")},
		fake.Step{Text: "again", Check: has("rule one")},
		fake.Step{Text: "reloaded", Check: has("rule two")},
		fake.Step{Text: "SUMMARY", Check: has("rule two")},
		fake.Step{Text: "compacted", Check: has("rule three")},
	)
	a, _, dir := newTestAgent(t, fp)
	agents := filepath.Join(dir, "AGENTS.md")
	if err := os.WriteFile(agents, []byte("rule one\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	for _, p := range []string{"edit it", "next"} {
		if err := a.Run(ctx, provider.UserText(p)); err != nil {
			t.Fatal(err)
		}
	}
	a.SetSkills(nil) // what /reload does
	if err := a.Run(ctx, provider.UserText("after reload")); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(agents, []byte("rule three\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := a.Compact(ctx); err != nil {
		t.Fatal(err)
	}
	if err := a.Run(ctx, provider.UserText("after compaction")); err != nil {
		t.Fatal(err)
	}
}

func TestCompactionFallsBackToTranscript(t *testing.T) {
	for name, step := range map[string]fake.Step{
		"tool call": {Calls: []fake.Call{{Name: "read", Args: map[string]string{"path": "x"}}}},
		"empty":     {Text: ""},
		"overflow":  {Err: &provider.Error{Kind: provider.ErrContextOverflow, Message: "context length exceeded"}},
	} {
		t.Run(name, func(t *testing.T) {
			fp := fake.New("fake",
				fake.Step{Text: "first"},
				fake.Step{Text: "second"},
				step,
				fake.Step{Text: "SUMMARY", Check: func(r provider.Request) {
					if len(r.Tools) != 0 || !strings.Contains(r.Messages[0].TextContent(), "<conversation>") {
						t.Errorf("fallback did not send a transcript: %+v", r)
					}
				}},
			)
			a, rec, _ := newTestAgent(t, fp)
			ctx := context.Background()
			for _, p := range []string{"one", "two"} {
				if err := a.Run(ctx, provider.UserText(p)); err != nil {
					t.Fatal(err)
				}
			}
			if err := a.Compact(ctx); err != nil {
				t.Fatal(err)
			}
			if cv := a.Session().BuildContext(); cv.Compaction == nil || cv.Compaction.Summary != "SUMMARY" {
				t.Fatalf("compaction = %+v", cv.Compaction)
			}
			if !strings.Contains(strings.Join(rec.types(), ","), "notice") {
				t.Error("no fallback notice")
			}
		})
	}
}

func TestCompactionDoesNotFallBackOnOtherErrors(t *testing.T) {
	fp := fake.New("fake",
		fake.Step{Text: "first"},
		fake.Step{Text: "second"},
		fake.Step{Err: &provider.Error{Kind: provider.ErrAuth, Message: "bad key"}},
	)
	a, _, _ := newTestAgent(t, fp)
	ctx := context.Background()
	for _, p := range []string{"one", "two"} {
		if err := a.Run(ctx, provider.UserText(p)); err != nil {
			t.Fatal(err)
		}
	}
	if err := a.Compact(ctx); err == nil {
		t.Fatal("expected an error")
	}
	if len(fp.Requests) != 3 {
		t.Errorf("requests = %d, want 3 (no transcript retry)", len(fp.Requests))
	}
}

func TestXMLToolCalling(t *testing.T) {
	fp := fake.New("fake",
		fake.Step{Text: "Writing.\n<tool_call name=\"write\">\n<parameter name=\"path\">a.txt</parameter>\n<parameter name=\"content\">\nif a < b && c {\n}\n</parameter>\n</tool_call>"},
		fake.Step{Text: "<tool_call>no name here</tool_call>", Check: func(r provider.Request) {
			if len(r.Tools) != 0 || !strings.Contains(r.System, "# Tool use") {
				t.Errorf("tools were not moved into the prompt")
			}
			last := r.Messages[len(r.Messages)-1].TextContent()
			if !strings.HasPrefix(last, "<tool_result name=\"write\" status=\"ok\">") {
				t.Errorf("tool result not replayed as XML: %q", last)
			}
		}},
		fake.Step{Text: "done", Check: func(r provider.Request) {
			last := r.Messages[len(r.Messages)-1].TextContent()
			if !strings.Contains(last, "status=\"error\"") || !strings.Contains(last, "no name") {
				t.Errorf("malformed call not answered with an error: %q", last)
			}
		}},
	)
	a, _, dir := newTestAgent(t, fp)
	m := a.Model()
	m.ToolFormat = config.ToolFormatXML
	a.mu.Lock()
	a.model = m
	a.mu.Unlock()
	if err := a.Run(context.Background(), provider.UserText("write a file")); err != nil {
		t.Fatal(err)
	}
	if data, _ := os.ReadFile(filepath.Join(dir, "a.txt")); string(data) != "if a < b && c {\n}" {
		t.Errorf("file = %q", data)
	}
}

func TestEmptyReplyWarns(t *testing.T) {
	fp := fake.New("fake", fake.Step{Thinking: "thinking only"})
	a, rec, _ := newTestAgent(t, fp)
	if err := a.Run(context.Background(), provider.UserText("hi")); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(strings.Join(rec.types(), ","), "notice") {
		t.Error("no warning for an empty reply")
	}
}

func TestAutoCompactionOff(t *testing.T) {
	fp := fake.New("fake", fake.Step{Text: "a"}, fake.Step{Text: "b"}, fake.Step{Text: "c"})
	a, _, _ := newTestAgent(t, fp)
	cfg := a.Config()
	off := false
	cfg.Compaction.Auto = &off
	cfg.Models[0].ContextWindow = 1000 // always over the limit
	a.SetConfig(cfg)
	if a.Model().ContextWindow != 1000 {
		t.Fatal("SetConfig did not refresh the current model")
	}
	ctx := context.Background()
	for _, p := range []string{"one", "two", "three"} {
		if err := a.Run(ctx, provider.UserText(p)); err != nil {
			t.Fatal(err)
		}
	}
	cv := a.Session().BuildContext()
	if cv.Compaction != nil {
		t.Error("compacted with compaction.auto = false")
	}
	if got := cv.Messages[len(cv.Messages)-1].TextContent(); got != "c" {
		t.Errorf("last answer = %q", got)
	}
}

// TestUnansweredPromptIsWithdrawn checks that a prompt that got no reply
// is taken off the conversation so the next message replaces it, while a
// prompt with any saved reply stays.
func TestUnansweredPromptIsWithdrawn(t *testing.T) {
	authErr := &provider.Error{Kind: provider.ErrAuth, StatusCode: 401, Message: "bad key"}
	tests := []struct {
		name      string
		steps     []fake.Step
		cancel    bool
		withdrawn bool
	}{
		{name: "error before reply", steps: []fake.Step{{Err: authErr}}, withdrawn: true},
		{name: "cancel before reply", steps: []fake.Step{{Wait: true}}, cancel: true, withdrawn: true},
		{name: "cancel after partial text", steps: []fake.Step{{Text: "partial", Wait: true}}, cancel: true},
		{name: "error after a tool call", steps: []fake.Step{
			{Calls: []fake.Call{{Name: "ls", Args: map[string]string{}}}},
			{Err: authErr},
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			steps := append(tt.steps, fake.Step{Text: "ok", Check: func(r provider.Request) {
				var users []string
				for _, m := range r.Messages {
					if m.Role == provider.RoleUser {
						users = append(users, m.TextContent())
					}
				}
				want := []string{"first", "second"}
				if tt.withdrawn {
					want = []string{"second"}
				}
				if strings.Join(users, "|") != strings.Join(want, "|") {
					t.Errorf("user messages = %q, want %q", users, want)
				}
			}})
			a, rec, _ := newTestAgent(t, fake.New("fake", steps...))
			if err := a.Prompt(provider.UserText("first")); err != nil {
				t.Fatal(err)
			}
			if tt.cancel {
				time.Sleep(50 * time.Millisecond)
				a.Cancel()
			}
			a.Wait()
			var end events.TurnEnd
			for _, e := range rec.events {
				if te, ok := e.(events.TurnEnd); ok {
					end = te
				}
			}
			want := ""
			if tt.withdrawn {
				want = "first"
			}
			if end.Unanswered != want {
				t.Errorf("unanswered = %q, want %q", end.Unanswered, want)
			}
			if err := a.Run(context.Background(), provider.UserText("second")); err != nil {
				t.Fatal(err)
			}
			n := 0
			for _, e := range a.Session().Entries() {
				if e.Message != nil && e.Message.TextContent() == "first" {
					n++
				}
			}
			if n != 1 {
				t.Errorf("first prompt saved %d times, want 1 (the file keeps it)", n)
			}
		})
	}
}
