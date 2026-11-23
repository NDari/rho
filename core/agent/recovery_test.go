package agent

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/NDari/rho/core/events"
	"github.com/NDari/rho/core/provider"
	"github.com/NDari/rho/core/provider/fake"
	"github.com/NDari/rho/core/session"
	"github.com/NDari/rho/core/tools"
)

func TestRetryWait(t *testing.T) {
	a := &Agent{retryDelay: 2 * time.Second}
	tests := []struct {
		attempt    int
		retryAfter time.Duration
		min, max   time.Duration
		ok         bool
	}{
		{0, 0, 2 * time.Second, 2400 * time.Millisecond, true},
		{1, 0, 4 * time.Second, 4800 * time.Millisecond, true},
		{3, 0, 16 * time.Second, 19200 * time.Millisecond, true},
		{4, 0, 30 * time.Second, 36 * time.Second, true},
		{40, 0, 30 * time.Second, 36 * time.Second, true}, // no overflow
		{0, 45 * time.Second, 45 * time.Second, 45 * time.Second, true},
		{3, time.Second, 16 * time.Second, 19200 * time.Millisecond, true},
		{0, maxRetryAfter, maxRetryAfter, maxRetryAfter, true},
		{0, maxRetryAfter + time.Second, 0, 0, false},
	}
	for _, tt := range tests {
		for range 50 { // jitter is random
			got, ok := a.retryWait(tt.attempt, tt.retryAfter)
			if ok != tt.ok || got < tt.min || got > tt.max {
				t.Fatalf("retryWait(%d, %s) = %s, %v; want %s..%s, %v", tt.attempt, tt.retryAfter, got, ok, tt.min, tt.max, tt.ok)
			}
		}
	}
}

func fastRetries(a *Agent) { a.retryDelay = time.Millisecond }

func TestRetryAfterStretchesTheWait(t *testing.T) {
	fp := fake.New("fake",
		fake.Step{Err: &provider.Error{Kind: provider.ErrTransient, StatusCode: 429, Message: "slow down", RetryAfter: 150 * time.Millisecond}},
		fake.Step{Text: "ok"},
	)
	a, rec, _ := newTestAgent(t, fp)
	fastRetries(a)
	start := time.Now()
	if err := a.Run(context.Background(), provider.UserText("hi")); err != nil {
		t.Fatal(err)
	}
	if took := time.Since(start); took < 150*time.Millisecond {
		t.Errorf("retried after %s, before the server's Retry-After", took)
	}
	var retry *events.Retry
	for _, e := range rec.events {
		if r, ok := e.(events.Retry); ok {
			retry = &r
		}
	}
	if retry == nil || retry.Delay != 150*time.Millisecond || retry.Attempt != 1 {
		t.Errorf("retry event = %+v", retry)
	}
}

func TestLongRetryAfterIsNotRetried(t *testing.T) {
	fp := fake.New("fake",
		fake.Step{Err: &provider.Error{Kind: provider.ErrTransient, StatusCode: 429, Message: "daily limit reached", RetryAfter: 3 * time.Hour}},
		fake.Step{Text: "never"},
	)
	a, _, _ := newTestAgent(t, fp)
	fastRetries(a)
	err := a.Run(context.Background(), provider.UserText("hi"))
	if err == nil || !strings.Contains(err.Error(), "asked to wait 3h0m0s") || provider.KindOf(err) != provider.ErrTransient {
		t.Fatalf("err = %v", err)
	}
	if len(fp.Requests) != 1 {
		t.Errorf("requests = %d, want 1", len(fp.Requests))
	}
}

// panicTool panics like a tool with a nil map or an index bug would.
type panicTool struct{}

func (panicTool) Spec() provider.ToolSpec {
	return provider.ToolSpec{Name: "boom", Description: "d", Parameters: json.RawMessage(`{"type":"object"}`)}
}
func (panicTool) ReadOnly() bool { return false }
func (panicTool) Execute(context.Context, *tools.Env, json.RawMessage, func(string)) tools.Result {
	panic("assignment to entry in nil map")
}

func TestPanickingToolIsReported(t *testing.T) {
	fp := fake.New("fake",
		fake.Step{Calls: []fake.Call{{Name: "boom", Args: map[string]string{}}}},
		fake.Step{Text: "the tool is broken", Check: func(r provider.Request) {
			res := r.Messages[len(r.Messages)-1].Content[0]
			if text := res.Content[0].Text; !res.IsError || !strings.Contains(text, "internal error") || !strings.Contains(text, "nil map") {
				t.Errorf("tool result = %+v", res)
			}
		}},
	)
	a, rec, _ := newTestAgent(t, fp)
	a.SetTools(append(a.Tools(), panicTool{}))
	if err := a.Run(context.Background(), provider.UserText("use boom")); err != nil {
		t.Fatal(err)
	}
	found := false
	for _, e := range rec.events {
		if n, ok := e.(events.Notice); ok && n.Level == "error" && strings.Contains(n.Text, "the boom tool crashed") && strings.Contains(n.Text, "goroutine") {
			found = true
		}
	}
	if !found {
		t.Error("no crash notice with a stack")
	}
}

// panicProvider panics in Stream once, then answers.
type panicProvider struct{ calls int }

func (p *panicProvider) Name() string { return "fake" }
func (p *panicProvider) Stream(context.Context, provider.Request, func(provider.StreamEvent)) (provider.Response, error) {
	p.calls++
	if p.calls == 1 {
		var s []int
		_ = s[3]
	}
	m := provider.Message{Role: provider.RoleAssistant, Content: []provider.Block{provider.Text("fine")}}
	return provider.Response{Message: m, StopReason: provider.StopEnd}, nil
}

func TestPanicInTurnEndsItAndAgentStaysUsable(t *testing.T) {
	a, rec, _ := newTestAgent(t, fake.New("fake"))
	pp := &panicProvider{}
	a.providers = providers{"fake": pp}
	err := a.Run(context.Background(), provider.UserText("hi"))
	if err == nil || !strings.Contains(err.Error(), "internal error") || !strings.Contains(err.Error(), "index out of range") {
		t.Fatalf("err = %v", err)
	}
	if a.Busy() {
		t.Fatal("agent still busy after a panic")
	}
	var end events.TurnEnd
	for _, e := range rec.events {
		if te, ok := e.(events.TurnEnd); ok {
			end = te
		}
	}
	if end.Reason != "error" || end.Unanswered != "hi" {
		t.Errorf("turn end = %+v", end)
	}
	if err := a.Run(context.Background(), provider.UserText("again")); err != nil {
		t.Fatalf("second turn: %v", err)
	}
}

func TestSetSessionReportsDamage(t *testing.T) {
	a, rec, dir := newTestAgent(t, fake.New("fake"))
	s := session.New(filepath.Join(dir, "sessions"), dir, "test")
	if _, err := s.AppendMessage(provider.UserText("hello")); err != nil {
		t.Fatal(err)
	}
	_ = s.Close()
	f, err := os.OpenFile(s.Path(), os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = f.WriteString(`{"id":"ab12","type":"message","message":{"role":"assis`)
	_ = f.Close()
	r, err := session.Open(s.Path())
	if err != nil {
		t.Fatal(err)
	}
	if err := a.SetSession(r); err != nil {
		t.Fatal(err)
	}
	var warn string
	for _, e := range rec.events {
		if n, ok := e.(events.Notice); ok && n.Level == "warn" {
			warn = n.Text
		}
	}
	if !strings.Contains(warn, "is damaged") || !strings.Contains(warn, "line 3: ignored an incomplete final entry") {
		t.Errorf("warning = %q", warn)
	}
	if msg := SessionProblems(s); msg != "" {
		t.Errorf("a healthy session reports %q", msg)
	}
}
