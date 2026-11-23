package integration

import (
	"net/http"
	"reflect"
	"strings"
	"testing"

	"github.com/NDari/rho/core/provider/replay"
)

// TestRecordedToolTurn replays a real tool-calling turn: the model's
// recorded call to read go.mod runs the real read tool on the project,
// its result goes back to the server, and the recorded answer ends it.
func TestRecordedToolTurn(t *testing.T) {
	e := newEnv(t, recordedToolCall, recordedText)
	r := e.run("What is the module name?", runOpts{})
	if r.err != nil || r.result["status"] != "done" {
		t.Fatalf("err %v, result %v", r.err, r.result)
	}
	if !strings.Contains(r.result["answer"].(string), "391") || r.result["steps"] != float64(2) {
		t.Errorf("result = %v", r.result)
	}
	ends := r.ofType("tool_end")
	if len(ends) != 1 || toolFailed(ends[0]) {
		t.Fatalf("tool_end = %v", ends)
	}
	// The second request carried the file's contents back to the model.
	msgs := e.sentMessages(1)
	last := msgs[len(msgs)-1].(map[string]any)
	if last["role"] != "tool" || !strings.Contains(last["content"].(string), "module example.com/demo") {
		t.Errorf("tool message sent = %v", last)
	}
	s := reopen(t, r.path)
	if got := roles(s); got != "user assistant(read) tool_result assistant" {
		t.Errorf("session = %s", got)
	}
}

// TestFlakyServer injects the failures seen with real servers and proxies
// before a good reply. Each must be retried, the user must get the full
// answer, and the session must hold exactly one answer: nothing from the
// failed attempts may leak into the conversation or into the requests.
func TestFlakyServer(t *testing.T) {
	good := recordedText
	llamaErrStart := replay.Events(good.Body)[:8]
	var midStreamErr []byte
	for _, ev := range llamaErrStart {
		midStreamErr = append(midStreamErr, ev...)
	}
	midStreamErr = append(midStreamErr, []byte(`data: {"error":{"code":500,"message":"Failed to decode the batch","type":"server_error"}}`+"\n\n")...)

	tests := []struct {
		name   string
		faults []replay.Reply
		extra  string
	}{
		{"connection dropped mid-stream", []replay.Reply{{Body: good.Body, CutAt: len(good.Body) * 2 / 5, Abort: true}}, ""},
		{"proxy closed the stream early", []replay.Reply{{Body: good.Body, CutAt: half(good)}}, ""},
		{"model still loading", []replay.Reply{{Status: http.StatusServiceUnavailable, Body: []byte(`{"error":{"code":503,"message":"Loading model","type":"unavailable_error"}}`)}}, ""},
		{"rate limited", []replay.Reply{{Status: http.StatusTooManyRequests, Header: map[string]string{"Retry-After-Ms": "30"}, Body: []byte(`{"error":{"message":"Rate limit reached","type":"requests","code":"rate_limit_exceeded"}}`)}}, ""},
		{"server error mid-stream", []replay.Reply{{Body: midStreamErr}}, ""},
		{"stalled mid-stream", []replay.Reply{{Body: good.Body, CutAt: half(good), Stall: true}}, "stream_idle_timeout = 1\n"},
		{"several in a row", []replay.Reply{
			{Body: good.Body, CutAt: len(good.Body) / 3, Abort: true},
			{Status: http.StatusBadGateway, Body: []byte(`<html><body><h1>502 Bad Gateway</h1></body></html>`)},
			{Body: good.Body, CutAt: half(good)},
		}, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			extra := ""
			if tt.extra != "" {
				// Per provider: append to the [providers.local] table.
				extra = "\n[providers.local]\n" + tt.extra
			}
			e := newEnvWithProviderExtra(t, extra, append(tt.faults, good)...)
			r := e.run("What is 17*23?", runOpts{})
			if r.err != nil || r.result["status"] != "done" {
				t.Fatalf("err %v, result %v\n%s", r.err, r.result, r.stderr)
			}
			if !strings.Contains(r.result["answer"].(string), "391") {
				t.Errorf("answer = %q", r.result["answer"])
			}
			retries := r.ofType("retry")
			if len(retries) != len(tt.faults) {
				t.Errorf("retries = %d, want %d: %v", len(retries), len(tt.faults), retries)
			}
			if r.result["steps"] != float64(1) {
				t.Errorf("steps = %v", r.result["steps"])
			}
			reqs := e.srv.Requests()
			if len(reqs) != len(tt.faults)+1 {
				t.Fatalf("requests = %d", len(reqs))
			}
			for i := 1; i < len(reqs); i++ {
				if !reflect.DeepEqual(reqs[i]["messages"], reqs[0]["messages"]) {
					t.Errorf("request %d differs from the first: a failed attempt leaked", i)
				}
			}
			if got := roles(reopen(t, r.path)); got != "user assistant" {
				t.Errorf("session = %s", got)
			}
		})
	}
}

// newEnvWithProviderExtra adds keys to the provider table. TOML does not
// allow a table twice, so the keys are spliced into the first one.
func newEnvWithProviderExtra(t *testing.T, extra string, replies ...replay.Reply) *env {
	t.Helper()
	e := newEnv(t, replies...)
	if extra == "" {
		return e
	}
	keys := strings.TrimPrefix(extra, "\n[providers.local]\n")
	cfg := readFile(t, e.home+"/config.toml")
	cfg = strings.Replace(cfg, "[providers.local]\n", "[providers.local]\n"+keys, 1)
	e.write(e.home+"/config.toml", cfg)
	return e
}

// TestDropBeforeAnyData: a connection that fails before any data arrives
// (a keep-alive connection the server already closed) is retried once
// silently by the provider, without a retry notice.
func TestDropBeforeAnyData(t *testing.T) {
	e := newEnv(t, replay.Reply{Body: recordedText.Body, CutAt: -1, Abort: true}, recordedText)
	r := e.run("What is 17*23?", runOpts{})
	if r.err != nil || len(r.ofType("retry")) != 0 || len(e.srv.Requests()) != 2 {
		t.Fatalf("err %v, retries %d, requests %d", r.err, len(r.ofType("retry")), len(e.srv.Requests()))
	}
}

// TestServerDownThenBack is the user's view of an unreachable server: the
// retries run out, the turn fails with the server's message, and the
// prompt is withdrawn so that sending it again does not stack a copy.
func TestServerDownThenBack(t *testing.T) {
	down := replay.Reply{Status: http.StatusServiceUnavailable, Body: []byte(`{"error":{"code":503,"message":"Loading model","type":"unavailable_error"}}`)}
	e := newEnv(t, down, down, down, down)
	r := e.run("What is 17*23?", runOpts{})
	if r.err == nil || r.result["status"] != "error" || !strings.Contains(r.result["error"].(string), "Loading model") {
		t.Fatalf("err %v, result %v", r.err, r.result)
	}
	if n := len(r.ofType("retry")); n != 3 {
		t.Errorf("retries = %d, want 3 (max_retries)", n)
	}
	ends := r.ofType("turn_end")
	if len(ends) != 1 || ends[0]["reason"] != "error" || ends[0]["unanswered"] != "What is 17*23?" {
		t.Errorf("turn_end = %v", ends)
	}
	if got := roles(reopen(t, r.path)); got != "" {
		t.Errorf("failed prompt still in the conversation: %s", got)
	}

	e.srv.Add(recordedText)
	r2 := e.run("What is 17*23?", runOpts{cont: true})
	if r2.err != nil || r2.path != r.path {
		t.Fatalf("second run: %v (session %s, first %s)", r2.err, r2.path, r.path)
	}
	if got := roles(reopen(t, r2.path)); got != "user assistant" {
		t.Errorf("session = %s", got)
	}
	if msgs := e.sentMessages(4); len(msgs) != 2 { // system + one user message
		t.Errorf("sent %d messages, want system and one prompt", len(msgs))
	}
}

func TestBadKeyIsNotRetried(t *testing.T) {
	e := newEnv(t, replay.Reply{Status: http.StatusUnauthorized, Body: []byte(`{"error":{"message":"Incorrect API key provided","type":"invalid_request_error","code":"invalid_api_key"}}`)})
	r := e.run("hi", runOpts{})
	if r.result["status"] != "error" || !strings.Contains(r.result["error"].(string), "Incorrect API key") {
		t.Fatalf("result = %v", r.result)
	}
	if len(e.srv.Requests()) != 1 || len(r.ofType("retry")) != 0 {
		t.Errorf("an auth error was retried")
	}
}

// TestContextOverflowCompactsAndRetries replays the real overflow error
// from llama-server in the middle of a conversation: Rho compacts the
// earlier turns and sends the prompt again.
func TestContextOverflowCompactsAndRetries(t *testing.T) {
	long := strings.Repeat("The build failed because the generated file was stale. ", 30)
	e := newEnv(t, textReply(long), textReply(long))
	for i, p := range []string{"Why did the build fail?", "And how do I fix it?"} {
		if r := e.run(p, runOpts{cont: i > 0}); r.err != nil {
			t.Fatal(r.err)
		}
	}
	summary := "## Goal\nFix the failing build.\n\n## Progress\nThe generated file was stale; regenerating it fixes the build."
	e.srv.Add(
		replay.Reply{Status: http.StatusBadRequest, Body: replay.Recording("llamacpp_chat_overflow.json")},
		textReply(summary),
		textReply("Run go generate, then build again."),
	)
	r := e.run("Summarize what to do.", runOpts{cont: true})
	if r.err != nil || r.result["status"] != "done" {
		t.Fatalf("err %v, result %v\n%s", r.err, r.result, r.stderr)
	}
	if !strings.Contains(r.notices("warn"), "context window exceeded") {
		t.Errorf("no overflow notice: %v", r.ofType("notice"))
	}
	if len(r.ofType("compaction_end")) != 1 {
		t.Errorf("compaction events: %v", r.ofType("compaction_end"))
	}
	s := reopen(t, r.path)
	cv := s.BuildContext()
	if cv.Compaction == nil || !strings.Contains(cv.Compaction.Summary, "regenerating it fixes the build") {
		t.Fatalf("no compaction in the session: %+v", cv.Compaction)
	}
	// The retried request is smaller than the one that overflowed.
	reqs := e.srv.Requests()
	if n, m := len(e.sentMessages(len(reqs)-1)), len(e.sentMessages(len(reqs)-3)); n >= m {
		t.Errorf("retry sent %d messages, the overflowing request %d", n, m)
	}
}

func readFile(t *testing.T, p string) string {
	t.Helper()
	b, err := readAll(p)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}
