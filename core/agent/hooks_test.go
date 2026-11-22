package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/NDari/rho/core/events"
	"github.com/NDari/rho/core/provider"
	"github.com/NDari/rho/core/provider/fake"
	"github.com/NDari/rho/core/tools"
)

// testHooks implements Hooks with functions for the two message hooks.
type testHooks struct {
	user    func(text string) (string, string)
	request func(system string, msgs []provider.Message) (string, []provider.Message)
	before  func(name string, args json.RawMessage) (json.RawMessage, string)
	message func(id string, m provider.Message)
	toolEnd func(name string, args json.RawMessage, res tools.Result)
}

func (h testHooks) BeforeToolCall(_ context.Context, name string, args json.RawMessage) (json.RawMessage, string) {
	if h.before == nil {
		return args, ""
	}
	return h.before(name, args)
}
func (testHooks) AfterToolCall(_ context.Context, _ string, _ json.RawMessage, res tools.Result) tools.Result {
	return res
}
func (testHooks) TurnEnd(context.Context) {}
func (testHooks) BeforeCompaction(context.Context, []provider.Message) (string, bool) {
	return "", false
}
func (h testHooks) MessageEnd(id string, m provider.Message) {
	if h.message != nil {
		h.message(id, m)
	}
}
func (h testHooks) ToolEnd(name string, args json.RawMessage, res tools.Result) {
	if h.toolEnd != nil {
		h.toolEnd(name, args, res)
	}
}
func (h testHooks) UserMessage(_ context.Context, text string) (string, string) {
	if h.user == nil {
		return text, ""
	}
	return h.user(text)
}
func (h testHooks) BeforeRequest(_ context.Context, system string, msgs []provider.Message, _ bool) (string, []provider.Message) {
	if h.request == nil {
		return system, msgs
	}
	return h.request(system, msgs)
}

func TestUserMessageHookRewritesAndKeepsAttachments(t *testing.T) {
	fp := fake.New("fake", fake.Step{Text: "ok", Check: func(r provider.Request) {
		if got := r.Messages[0].TextContent(); got != "HELLO" {
			t.Errorf("model saw %q, want the rewritten text", got)
		}
	}})
	a, _, _ := newTestAgent(t, fp)
	a.SetHooks(testHooks{user: func(text string) (string, string) { return strings.ToUpper(text), "" }})
	msg := provider.Message{Role: provider.RoleUser, Content: []provider.Block{
		provider.Text("hello"), {Type: provider.BlockImage, MIME: "image/png", Data: []byte{1}},
	}}
	if err := a.Run(context.Background(), msg); err != nil {
		t.Fatal(err)
	}
	saved := a.Session().BuildContext().Messages[0]
	if saved.TextContent() != "HELLO" || len(saved.Content) != 2 || saved.Content[1].Type != provider.BlockImage {
		t.Errorf("saved message = %+v", saved)
	}
}

func TestUserMessageHookBlocks(t *testing.T) {
	fp := fake.New("fake") // any request would fail the turn
	a, rec, _ := newTestAgent(t, fp)
	a.SetHooks(testHooks{user: func(string) (string, string) { return "", "no secrets (blocked by extension guard)" }})
	if err := a.Run(context.Background(), provider.UserText("my password is hunter2")); !errors.Is(err, ErrBlocked) {
		t.Fatalf("err = %v, want ErrBlocked", err)
	}
	if n := len(a.Session().BuildContext().Messages); n != 0 {
		t.Errorf("a blocked message was saved (%d messages)", n)
	}
	found, reason := false, ""
	rec.mu.Lock()
	defer rec.mu.Unlock()
	for _, e := range rec.events {
		if n, ok := e.(events.Notice); ok && strings.Contains(n.Text, "message not sent: no secrets") {
			found = true
		}
		if te, ok := e.(events.TurnEnd); ok {
			reason = te.Reason
		}
		if _, ok := e.(events.Error); ok {
			t.Errorf("a blocked message should not be reported as an error: %+v", e)
		}
	}
	if reason != "blocked" {
		t.Errorf("turn end reason = %q, want blocked", reason)
	}
	if !found {
		t.Errorf("no notice about the blocked message: %v", rec.events)
	}
}

func TestBeforeRequestHookChangesOnlyTheRequest(t *testing.T) {
	const retrieved = "Retrieved: the answer is 42."
	fp := fake.New("fake",
		fake.Step{Text: "first", Check: func(r provider.Request) {
			if !strings.HasSuffix(r.System, retrieved) {
				t.Errorf("system prompt not changed: ...%s", r.System[max(0, len(r.System)-80):])
			}
			if len(r.Messages) != 2 || r.Messages[0].TextContent() != "note: be brief" {
				t.Errorf("messages not replaced: %+v", r.Messages)
			}
		}},
	)
	a, _, _ := newTestAgent(t, fp)
	a.SetHooks(testHooks{request: func(system string, msgs []provider.Message) (string, []provider.Message) {
		return system + "\n\n" + retrieved, append([]provider.Message{provider.UserText("note: be brief")}, msgs...)
	}})
	if err := a.Run(context.Background(), provider.UserText("question")); err != nil {
		t.Fatal(err)
	}
	cv := a.Session().BuildContext()
	if len(cv.Messages) != 2 || cv.Messages[0].TextContent() != "question" {
		t.Errorf("the session changed: %+v", cv.Messages)
	}
}

func TestBeforeRequestHookCountsTowardOutputLimit(t *testing.T) {
	big := strings.Repeat("x", 4*90000) // about 90000 tokens in a 100000 window
	var limit int
	fp := fake.New("fake", fake.Step{Text: "ok", Check: func(r provider.Request) { limit = r.MaxTokens }})
	a, _, _ := newTestAgent(t, fp)
	m := a.Model()
	m.MaxOutput = 16000
	a.mu.Lock()
	a.model = m
	a.mu.Unlock()
	a.SetHooks(testHooks{request: func(system string, msgs []provider.Message) (string, []provider.Message) {
		return system + big, msgs
	}})
	if err := a.Run(context.Background(), provider.UserText("q")); err != nil {
		t.Fatal(err)
	}
	if limit >= 16000 {
		t.Errorf("output limit %d ignores the %d tokens the hook added", limit, len(big)/4)
	}
}

func TestUserMessageHookBlocksAFollowUp(t *testing.T) {
	fp := fake.New("fake", fake.Step{Text: "only answer"}) // a second request would fail
	a, _, _ := newTestAgent(t, fp)
	queued := false
	a.SetHooks(testHooks{
		user: func(text string) (string, string) {
			if strings.Contains(text, "secret") {
				return "", "blocked"
			}
			return text, ""
		},
		request: func(system string, msgs []provider.Message) (string, []provider.Message) {
			if !queued {
				queued = true
				if err := a.FollowUp("a secret follow-up"); err != nil {
					t.Error(err)
				}
			}
			return system, msgs
		},
	})
	if err := a.Run(context.Background(), provider.UserText("question")); err != nil {
		t.Fatal(err)
	}
	if n := len(a.Session().BuildContext().Messages); n != 2 {
		t.Errorf("messages = %d, want the question and one answer", n)
	}
}

func TestMaxStepsAndTurnStats(t *testing.T) {
	call := fake.Step{Calls: []fake.Call{{Name: "read", Args: map[string]string{"path": "missing.txt"}}}, Usage: provider.Usage{InputTokens: 100, OutputTokens: 10}}
	fp := fake.New("fake", call, call, call)
	a, _, _ := newTestAgent(t, fp)
	a.maxSteps = 2
	err := a.Run(context.Background(), provider.UserText("loop"))
	if !errors.Is(err, ErrMaxSteps) {
		t.Fatalf("err = %v, want ErrMaxSteps", err)
	}
	st := a.LastTurn()
	if st.Steps != 2 || st.Usage.InputTokens != 200 || st.Usage.OutputTokens != 20 {
		t.Errorf("stats = %+v, want 2 steps and the usage of both", st)
	}

	fp = fake.New("fake", fake.Step{Text: "done", Usage: provider.Usage{InputTokens: 7, OutputTokens: 3}})
	a, _, _ = newTestAgent(t, fp)
	a.maxSteps = 1
	if err := a.Run(context.Background(), provider.UserText("one answer")); err != nil {
		t.Fatalf("a final answer at the limit is not an error: %v", err)
	}
	if st := a.LastTurn(); st.Steps != 1 || st.Usage.InputTokens != 7 {
		t.Errorf("stats = %+v; a new turn must start from zero", st)
	}
}

func TestMessageEndAndToolEndHooks(t *testing.T) {
	fp := fake.New("fake",
		fake.Step{Calls: []fake.Call{
			{ID: "c1", Name: "bash", Args: map[string]string{"command": "rm -rf /"}},
			{ID: "c2", Name: "read", Args: map[string]string{"path": "a.txt"}},
		}},
		fake.Step{Text: "done"},
	)
	a, _, _ := newTestAgent(t, fp)
	var roles []string
	var ends []string
	a.SetHooks(testHooks{
		before: func(name string, args json.RawMessage) (json.RawMessage, string) {
			if name == "bash" {
				return nil, "no"
			}
			return json.RawMessage(`{"path":"b.txt"}`), ""
		},
		message: func(id string, m provider.Message) {
			if id == "" {
				t.Errorf("message_end without an entry id: %+v", m)
			}
			roles = append(roles, string(m.Role))
		},
		toolEnd: func(name string, args json.RawMessage, res tools.Result) {
			ends = append(ends, fmt.Sprintf("%s %s error=%v", name, args, res.IsError))
		},
	})
	if err := a.Run(context.Background(), provider.UserText("go")); err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(roles, ","); got != "user,assistant,tool_result,tool_result,assistant" {
		t.Errorf("message_end roles = %s", got)
	}
	want := []string{`bash {"command":"rm -rf /"} error=true`, `read {"path":"b.txt"} error=true`}
	if strings.Join(ends, "|") != strings.Join(want, "|") {
		t.Errorf("tool_end = %q, want %q (the blocked call too, and the rewritten arguments)", ends, want)
	}
}
