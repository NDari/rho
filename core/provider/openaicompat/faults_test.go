package openaicompat

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/NDari/rho/core/provider"
	"github.com/NDari/rho/core/provider/replay"
	"github.com/NDari/rho/core/provider/streamutil"
)

// streamer is either provider type.
type streamer interface {
	Stream(ctx context.Context, req provider.Request, onEvent func(provider.StreamEvent)) (provider.Response, error)
}

var readTool = provider.ToolSpec{Name: "read", Description: "Read a file.", Parameters: json.RawMessage(`{"type":"object","properties":{"path":{"type":"string"}},"required":["path"]}`)}

func request() provider.Request {
	return provider.Request{Model: "m", Messages: []provider.Message{provider.UserText("hi")}, Tools: []provider.ToolSpec{readTool}}
}

type collected struct {
	text, thinking strings.Builder
	calls          int
}

func (c *collected) on(e provider.StreamEvent) {
	switch e.Kind {
	case provider.EventTextDelta:
		c.text.WriteString(e.Text)
	case provider.EventThinkingDelta:
		c.thinking.WriteString(e.Text)
	case provider.EventToolCallStart:
		c.calls++
	}
}

type recording struct {
	file      string
	responses bool
	// final is the index of the event after which the response is
	// complete (the finish reason, or the final Responses event).
	final func(events [][]byte) int
	check func(t *testing.T, resp provider.Response, c *collected)
}

func lastContaining(sub string) func([][]byte) int {
	return func(evs [][]byte) int {
		for i := len(evs) - 1; i >= 0; i-- {
			if bytes.Contains(evs[i], []byte(sub)) {
				return i
			}
		}
		return -1
	}
}

var recordings = []recording{
	{
		file:  "llamacpp_chat_text.sse",
		final: lastContaining(`"finish_reason":"stop"`),
		check: func(t *testing.T, resp provider.Response, c *collected) {
			if !strings.Contains(resp.Message.TextContent(), "391") || c.text.String() != resp.Message.TextContent() {
				t.Errorf("text = %q (streamed %q)", resp.Message.TextContent(), c.text.String())
			}
			if c.thinking.Len() == 0 || resp.StopReason != provider.StopEnd {
				t.Errorf("thinking %d bytes, stop %s", c.thinking.Len(), resp.StopReason)
			}
			if resp.Usage.InputTokens != 25 || resp.Usage.OutputTokens != 272 {
				t.Errorf("usage = %+v", resp.Usage)
			}
		},
	},
	{
		file:  "llamacpp_chat_tool_call.sse",
		final: lastContaining(`"finish_reason":"tool_calls"`),
		check: checkReadCall,
	},
	{
		file:      "llamacpp_responses_text.sse",
		responses: true,
		final:     lastContaining(`"type":"response.completed"`),
		check: func(t *testing.T, resp provider.Response, c *collected) {
			if !strings.Contains(resp.Message.TextContent(), "391") {
				t.Errorf("text = %q", resp.Message.TextContent())
			}
			if resp.Usage.InputTokens != 25 || resp.Usage.OutputTokens != 316 {
				t.Errorf("usage = %+v", resp.Usage)
			}
		},
	},
	{
		file:      "llamacpp_responses_tool_call.sse",
		responses: true,
		final:     lastContaining(`"type":"response.completed"`),
		check:     checkReadCall,
	},
}

func checkReadCall(t *testing.T, resp provider.Response, c *collected) {
	t.Helper()
	calls := resp.Message.ToolCalls()
	if len(calls) != 1 || calls[0].Name != "read" || calls[0].ID == "" {
		t.Fatalf("calls = %+v", calls)
	}
	var args struct{ Path string }
	if err := json.Unmarshal(calls[0].Args, &args); err != nil || args.Path != "go.mod" {
		t.Errorf("args = %s (%v)", calls[0].Args, err)
	}
	if resp.StopReason != provider.StopToolUse || c.calls != 1 {
		t.Errorf("stop = %s, call starts = %d", resp.StopReason, c.calls)
	}
}

func newStreamer(r recording, url string, idle time.Duration) streamer {
	o := Options{Name: "local", BaseURL: url, IdleTimeout: idle}
	if r.responses {
		return NewResponses(o)
	}
	return New(o)
}

// TestRecordedStreams replays real server output end to end.
func TestRecordedStreams(t *testing.T) {
	for _, r := range recordings {
		t.Run(r.file, func(t *testing.T) {
			srv := replay.New(replay.Reply{Body: replay.Recording(r.file)})
			defer srv.Close()
			var c collected
			resp, err := newStreamer(r, srv.URL, 0).Stream(context.Background(), request(), c.on)
			if err != nil {
				t.Fatal(err)
			}
			r.check(t, resp, &c)
		})
	}
}

// TestStreamCutAtEveryEvent cuts each recording after every event, as a
// proxy closing the stream early would. Before the response is complete
// that must be a transient error (so the agent retries), never a
// silently shortened answer; after it, a success.
func TestStreamCutAtEveryEvent(t *testing.T) {
	for _, r := range recordings {
		t.Run(r.file, func(t *testing.T) {
			body := replay.Recording(r.file)
			evs := replay.Events(body)
			final := r.final(evs)
			if final < 0 {
				t.Fatal("recording has no final event")
			}
			srv := replay.New()
			defer srv.Close()
			p := newStreamer(r, srv.URL, 0)
			cut := 0
			for i, ev := range evs {
				cut += len(ev)
				srv.Add(replay.Reply{Body: body, CutAt: cut})
				resp, err := p.Stream(context.Background(), request(), func(provider.StreamEvent) {})
				switch {
				case i < final && provider.KindOf(err) != provider.ErrTransient:
					t.Fatalf("cut after event %d of %d: err = %v, want transient (text %q)", i, len(evs), err, resp.Message.TextContent())
				case i < final && !strings.Contains(err.Error(), "ended before the response was complete"):
					t.Fatalf("cut after event %d: err = %v", i, err)
				case i >= final && err != nil:
					t.Fatalf("cut after event %d (final %d): %v", i, final, err)
				}
			}
		})
	}
}

// TestConnectionDropped drops the connection mid-stream at several
// points; each is a transient error.
func TestConnectionDropped(t *testing.T) {
	for _, r := range recordings {
		t.Run(r.file, func(t *testing.T) {
			body := replay.Recording(r.file)
			srv := replay.New()
			defer srv.Close()
			p := newStreamer(r, srv.URL, 0)
			for _, frac := range []float64{0.01, 0.3, 0.5, 0.9} {
				srv.Add(replay.Reply{Body: body, CutAt: int(float64(len(body)) * frac), Abort: true})
				_, err := p.Stream(context.Background(), request(), func(provider.StreamEvent) {})
				if provider.KindOf(err) != provider.ErrTransient {
					t.Errorf("dropped at %.0f%%: err = %v, want transient", frac*100, err)
				}
			}
		})
	}
}

// TestStalledStream covers a server that stops sending, mid-stream and
// before the first byte: the watchdog ends the request with a transient
// error instead of hanging.
func TestStalledStream(t *testing.T) {
	const idle = 150 * time.Millisecond
	for _, r := range recordings {
		for _, frac := range []float64{0, 0.5} {
			srv := replay.New()
			body := replay.Recording(r.file)
			cut := int(float64(len(body)) * frac)
			if cut == 0 {
				cut = -1 // nothing at all
			}
			srv.Add(replay.Reply{Body: body, CutAt: cut, Stall: true})
			start := time.Now()
			var c collected
			_, err := newStreamer(r, srv.URL, idle).Stream(context.Background(), request(), c.on)
			took := time.Since(start)
			srv.Close()
			if provider.KindOf(err) != provider.ErrTransient || !strings.Contains(err.Error(), "stalled") {
				t.Errorf("%s at %.0f%%: err = %v", r.file, frac*100, err)
			}
			if took > 5*idle {
				t.Errorf("%s at %.0f%%: took %s with idle %s", r.file, frac*100, took, idle)
			}
			if frac > 0 && c.thinking.Len() == 0 {
				t.Errorf("%s: nothing streamed before the stall", r.file)
			}
		}
	}
}

// TestSlowStreamIsNotStalled sends every event with a gap shorter than the
// idle timeout; the total is far longer, so the watchdog must reset.
func TestSlowStreamIsNotStalled(t *testing.T) {
	body := replay.Recording("llamacpp_chat_tool_call.sse")
	evs := replay.Events(body)
	srv := replay.New(replay.Reply{Body: body, Gap: 2 * time.Millisecond})
	defer srv.Close()
	idle := 60 * time.Millisecond
	if total := time.Duration(len(evs)) * 2 * time.Millisecond; total < 2*idle {
		t.Fatalf("recording too short for the test: %s", total)
	}
	var c collected
	resp, err := New(Options{Name: "local", BaseURL: srv.URL, IdleTimeout: idle}).Stream(context.Background(), request(), c.on)
	if err != nil {
		t.Fatal(err)
	}
	checkReadCall(t, resp, &c)
}

// TestCancelDuringStall reports a user cancel as cancellation, not a stall.
func TestCancelDuringStall(t *testing.T) {
	body := replay.Recording("llamacpp_chat_text.sse")
	srv := replay.New(replay.Reply{Body: body, CutAt: len(body) / 2, Stall: true})
	defer srv.Close()
	ctx, cancel := context.WithCancel(context.Background())
	time.AfterFunc(50*time.Millisecond, cancel)
	resp, err := New(Options{Name: "local", BaseURL: srv.URL, IdleTimeout: time.Minute}).Stream(ctx, request(), func(provider.StreamEvent) {})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v", err)
	}
	if resp.StopReason != provider.StopCancelled || len(resp.Message.Content) == 0 {
		t.Errorf("partial response = %+v", resp)
	}
}

// TestMidStreamErrors covers errors a server reports inside a stream
// after it started. These bodies are written by hand in the shapes the
// servers use (they are hard to provoke on demand).
func TestMidStreamErrors(t *testing.T) {
	start := replay.Events(replay.Recording("llamacpp_chat_text.sse"))[:5]
	tests := []struct {
		name  string
		event string
		want  provider.ErrorKind
	}{
		{"llama.cpp server error", `{"error":{"code":500,"message":"Failed to decode the batch","type":"server_error"}}`, provider.ErrTransient},
		{"OpenAI server error", `{"error":{"message":"The server had an error while processing your request. Sorry about that!","type":"server_error","param":null,"code":null}}`, provider.ErrTransient},
		{"rate limit", `{"error":{"message":"Rate limit reached for requests","type":"requests","code":"rate_limit_exceeded"}}`, provider.ErrTransient},
		{"overloaded", `{"error":{"message":"Overloaded","type":"overloaded_error"}}`, provider.ErrTransient},
		{"Gemini unavailable", `{"error":{"code":503,"message":"The model is overloaded. Please try again later.","status":"UNAVAILABLE"}}`, provider.ErrTransient},
		{"quota", `{"error":{"message":"You exceeded your current quota, please check your plan and billing details.","type":"insufficient_quota","code":"insufficient_quota"}}`, provider.ErrOther},
		{"context overflow", `{"error":{"code":400,"message":"the request exceeds the available context size, try increasing it","type":"exceed_context_size_error"}}`, provider.ErrContextOverflow},
		{"bad request", `{"error":{"code":400,"message":"Invalid tool schema","type":"invalid_request_error"}}`, provider.ErrOther},
		{"plain string", `{"error":"upstream connect error"}`, provider.ErrOther},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			body := append(bytes.Join(start, nil), []byte("data: "+tt.event+"\n\n")...)
			srv := replay.New(replay.Reply{Body: body})
			defer srv.Close()
			_, err := New(Options{Name: "local", BaseURL: srv.URL}).Stream(context.Background(), request(), func(provider.StreamEvent) {})
			if got := provider.KindOf(err); got != tt.want {
				t.Errorf("kind = %v, want %v (err %v)", got, tt.want, err)
			}
			var pe *provider.Error
			if errors.As(err, &pe) && strings.Contains(pe.Message, "{") {
				t.Errorf("message should be the server's text, not JSON: %q", pe.Message)
			}
		})
	}
}

// TestResponsesFailureEvents covers response.failed and error events.
func TestResponsesFailureEvents(t *testing.T) {
	start := replay.Events(replay.Recording("llamacpp_responses_text.sse"))[:6]
	tests := []struct {
		name  string
		event string
		want  provider.ErrorKind
	}{
		{"failed server_error", `{"type":"response.failed","response":{"status":"failed","error":{"code":"server_error","message":"An error occurred while processing your request."}}}`, provider.ErrTransient},
		{"failed rate limit", `{"type":"response.failed","response":{"status":"failed","error":{"code":"rate_limit_exceeded","message":"Rate limit reached"}}}`, provider.ErrTransient},
		{"failed overflow", `{"type":"response.failed","response":{"status":"failed","error":{"code":"context_length_exceeded","message":"Your input exceeds the context window of this model."}}}`, provider.ErrContextOverflow},
		{"failed invalid prompt", `{"type":"response.failed","response":{"status":"failed","error":{"code":"invalid_prompt","message":"Invalid prompt"}}}`, provider.ErrOther},
		{"failed without details", `{"type":"response.failed","response":{"status":"failed"}}`, provider.ErrOther},
		{"error event with numeric code", `{"type":"error","code":500,"message":"internal error"}`, provider.ErrTransient},
		{"error event", `{"type":"error","code":"server_error","message":"The server had an error","param":null}`, provider.ErrTransient},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			typ := strings.Split(strings.Split(tt.event, `"type":"`)[1], `"`)[0]
			body := append(bytes.Join(start, nil), []byte("event: "+typ+"\ndata: "+tt.event+"\n\n")...)
			srv := replay.New(replay.Reply{Body: body})
			defer srv.Close()
			_, err := NewResponses(Options{Name: "local", BaseURL: srv.URL}).Stream(context.Background(), request(), func(provider.StreamEvent) {})
			if got := provider.KindOf(err); got != tt.want {
				t.Errorf("kind = %v, want %v (err %v)", got, tt.want, err)
			}
		})
	}
}

func TestRecordedContextOverflow(t *testing.T) {
	srv := replay.New(replay.Reply{Status: http.StatusBadRequest, Body: replay.Recording("llamacpp_chat_overflow.json")})
	defer srv.Close()
	_, err := New(Options{Name: "local", BaseURL: srv.URL}).Stream(context.Background(), request(), func(provider.StreamEvent) {})
	if provider.KindOf(err) != provider.ErrContextOverflow || !strings.Contains(err.Error(), "22900 tokens") {
		t.Fatalf("err = %v", err)
	}
}

func TestRetryAfterFromErrorReplies(t *testing.T) {
	date := time.Now().Add(90 * time.Second).UTC().Format(http.TimeFormat)
	tests := []struct {
		name   string
		status int
		header map[string]string
		min    time.Duration
		max    time.Duration
	}{
		{"seconds", 429, map[string]string{"Retry-After": "7"}, 7 * time.Second, 7 * time.Second},
		{"milliseconds win", 429, map[string]string{"Retry-After": "7", "Retry-After-Ms": "1500"}, 1500 * time.Millisecond, 1500 * time.Millisecond},
		{"HTTP date", 503, map[string]string{"Retry-After": date}, 80 * time.Second, 91 * time.Second},
		{"none", 503, nil, 0, 0},
		{"garbage", 429, map[string]string{"Retry-After": "soon"}, 0, 0},
		{"capped", 429, map[string]string{"Retry-After": "86400"}, streamutil.MaxRetryAfter, streamutil.MaxRetryAfter},
		{"not for permanent errors", 400, map[string]string{"Retry-After": "7"}, 0, 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			srv := replay.New(replay.Reply{Status: tt.status, Header: tt.header, Body: []byte(`{"error":{"message":"busy"}}`)})
			defer srv.Close()
			_, err := New(Options{Name: "local", BaseURL: srv.URL}).Stream(context.Background(), request(), func(provider.StreamEvent) {})
			got := provider.RetryAfterOf(err)
			if got < tt.min || got > tt.max {
				t.Errorf("RetryAfter = %s, want %s..%s (err %v)", got, tt.min, tt.max, err)
			}
		})
	}
}
