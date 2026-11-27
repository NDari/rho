package anthropic

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/NDari/rho/core/provider"
	"github.com/NDari/rho/core/provider/replay"
)

var readTool = provider.ToolSpec{Name: "read", Description: "Read a file.", Parameters: json.RawMessage(`{"type":"object","properties":{"path":{"type":"string"}},"required":["path"],"additionalProperties":false}`)}

func request() provider.Request {
	return provider.Request{Model: "m", Messages: []provider.Message{provider.UserText("hi")}, Tools: []provider.ToolSpec{readTool}}
}

type collected struct {
	text, thinking, args strings.Builder
	calls                int
}

func (c *collected) on(e provider.StreamEvent) {
	switch e.Kind {
	case provider.EventTextDelta:
		c.text.WriteString(e.Text)
	case provider.EventThinkingDelta:
		c.thinking.WriteString(e.Text)
	case provider.EventToolCallStart:
		c.calls++
	case provider.EventToolCallDelta:
		c.args.WriteString(e.Text)
	}
}

func TestRecordedStreams(t *testing.T) {
	tests := []struct {
		file  string
		check func(t *testing.T, resp provider.Response, c *collected)
	}{
		{"llamacpp_messages_text.sse", func(t *testing.T, resp provider.Response, c *collected) {
			if !strings.Contains(resp.Message.TextContent(), "391") || c.text.String() != resp.Message.TextContent() {
				t.Errorf("text = %q (streamed %q)", resp.Message.TextContent(), c.text.String())
			}
			th := resp.Message.Content[0]
			if th.Type != provider.BlockThinking || th.Text == "" || th.Text != c.thinking.String() {
				t.Errorf("thinking = %+v (streamed %q)", th, c.thinking.String())
			}
			// llama-server signs nothing, so there is nothing to send back.
			if th.Replayed() || resp.StopReason != provider.StopEnd {
				t.Errorf("replayed %v, stop %s", th.Replayed(), resp.StopReason)
			}
			if resp.Usage.InputTokens != 24 || resp.Usage.OutputTokens != 361 {
				t.Errorf("usage = %+v", resp.Usage)
			}
		}},
		{"llamacpp_messages_tool_call.sse", func(t *testing.T, resp provider.Response, c *collected) {
			calls := resp.Message.ToolCalls()
			if len(calls) != 1 || calls[0].Name != "read" || string(calls[0].Args) != `{"path":"go.mod"}` || calls[0].ID == "" {
				t.Fatalf("calls = %+v", calls)
			}
			if c.calls != 1 || c.args.String() != `{"path":"go.mod"}` || resp.StopReason != provider.StopToolUse {
				t.Errorf("streamed %d calls, args %q, stop %s", c.calls, c.args.String(), resp.StopReason)
			}
		}},
	}
	for _, tt := range tests {
		t.Run(tt.file, func(t *testing.T) {
			srv := replay.New(replay.Reply{Body: replay.Recording(tt.file)})
			defer srv.Close()
			var c collected
			resp, err := New(Options{Name: "local", BaseURL: srv.URL}).Stream(context.Background(), request(), c.on)
			if err != nil {
				t.Fatal(err)
			}
			tt.check(t, resp, &c)
		})
	}
}

// sse builds an event stream from data lines.
func sse(events ...string) []byte {
	var b strings.Builder
	for _, e := range events {
		var typ struct{ Type string }
		_ = json.Unmarshal([]byte(e), &typ)
		fmt.Fprintf(&b, "event: %s\ndata: %s\n\n", typ.Type, e)
	}
	return []byte(b.String())
}

// signedReply is a Claude reply with summarized thinking, a redacted
// block, text and a tool call, using prompt caching.
var signedReply = sse(
	`{"type":"message_start","message":{"id":"msg_1","type":"message","role":"assistant","content":[],"model":"m","usage":{"input_tokens":10,"cache_read_input_tokens":900,"cache_creation_input_tokens":90,"output_tokens":1}}}`,
	`{"type":"content_block_start","index":0,"content_block":{"type":"thinking","thinking":"","signature":""}}`,
	`{"type":"content_block_delta","index":0,"delta":{"type":"thinking_delta","thinking":"Check the file."}}`,
	`{"type":"content_block_delta","index":0,"delta":{"type":"signature_delta","signature":"sig-1"}}`,
	`{"type":"content_block_stop","index":0}`,
	`{"type":"content_block_start","index":1,"content_block":{"type":"redacted_thinking","data":"enc-1"}}`,
	`{"type":"content_block_stop","index":1}`,
	`{"type":"content_block_start","index":2,"content_block":{"type":"text","text":""}}`,
	`{"type":"content_block_delta","index":2,"delta":{"type":"text_delta","text":"Reading it."}}`,
	`{"type":"content_block_stop","index":2}`,
	`{"type":"content_block_start","index":3,"content_block":{"type":"tool_use","id":"toolu_1","name":"read","input":{}}}`,
	`{"type":"content_block_delta","index":3,"delta":{"type":"input_json_delta","partial_json":"{\"path\":"}}`,
	`{"type":"content_block_delta","index":3,"delta":{"type":"input_json_delta","partial_json":"\"go.mod\"}"}}`,
	`{"type":"content_block_stop","index":3}`,
	`{"type":"message_delta","delta":{"stop_reason":"tool_use","stop_sequence":null},"usage":{"output_tokens":42}}`,
	`{"type":"message_stop"}`,
)

func TestSignedReplyRoundTrip(t *testing.T) {
	srv := replay.New(replay.Reply{Body: signedReply}, replay.Reply{Body: sse(
		`{"type":"message_start","message":{"id":"msg_2","type":"message","role":"assistant","content":[],"model":"m","usage":{"input_tokens":5,"output_tokens":1}}}`,
		`{"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`,
		`{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"Done."}}`,
		`{"type":"content_block_stop","index":0}`,
		`{"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":2}}`,
		`{"type":"message_stop"}`,
	)})
	defer srv.Close()
	p := New(Options{Name: "claude", BaseURL: srv.URL + "/v1"}) // a trailing /v1 is removed
	req := request()
	resp, err := p.Stream(context.Background(), req, func(provider.StreamEvent) {})
	if err != nil {
		t.Fatal(err)
	}
	if got := resp.Usage; got.InputTokens != 1000 || got.CacheReadTokens != 900 || got.CacheWriteTokens != 90 || got.OutputTokens != 42 {
		t.Errorf("usage = %+v", got)
	}
	c := resp.Message.Content
	if len(c) != 4 || c[0].Text != "Check the file." || !c[0].Replayed() || !c[1].Replayed() || c[2].Text != "Reading it." || c[3].ID != "toolu_1" {
		t.Fatalf("content = %+v", c)
	}

	// Send it back with the tool result, a reply from another provider
	// whose thinking must not go to Claude, and a user message.
	other := provider.Message{Role: provider.RoleAssistant, Content: []provider.Block{
		{Type: provider.BlockThinking, Text: "theirs", Provider: "gpt", Opaque: json.RawMessage(`{"signature":"x"}`)},
		provider.Text("From another model."),
		{Type: provider.BlockToolCall, ID: "call_2", Name: "read", Args: json.RawMessage(`"{broken"`)},
	}}
	req.Messages = append(req.Messages, resp.Message,
		provider.Message{Role: provider.RoleToolResult, Content: []provider.Block{provider.ToolResult("toolu_1", "module x", false)}},
		other,
		provider.Message{Role: provider.RoleToolResult, Content: []provider.Block{{Type: provider.BlockToolResult, CallID: "call_2", IsError: true}}},
		provider.UserText("thanks"),
	)
	req.System = "Be brief."
	req.Thinking = true
	req.ReasoningEffort = "high"
	req.MaxTokens = 0
	if _, err := p.Stream(context.Background(), req, func(provider.StreamEvent) {}); err != nil {
		t.Fatal(err)
	}
	sent, _ := json.Marshal(srv.Requests()[1])
	want := []string{
		`"system":[{"cache_control":{"type":"ephemeral"},"text":"Be brief.","type":"text"}]`,
		`"cache_control":{"type":"ephemeral"}`,
		`"thinking":{"block_binding":{"prefix_mismatch_behavior":"drop_block"},"display":"summarized","type":"adaptive"}`,
		`"output_config":{"effort":"high"}`,
		fmt.Sprintf(`"max_tokens":%d`, defaultMaxTokens),
		// The reply goes back as it came, signatures included.
		`{"content":[{"signature":"sig-1","thinking":"Check the file.","type":"thinking"},{"data":"enc-1","type":"redacted_thinking"},{"text":"Reading it.","type":"text"},{"id":"toolu_1","input":{"path":"go.mod"},"name":"read","type":"tool_use"}],"role":"assistant"}`,
		`{"content":[{"text":"From another model.","type":"text"},{"id":"call_2","input":{},"name":"read","type":"tool_use"}],"role":"assistant"}`,
		// Tool results and the next user text share one user turn.
		`{"content":[{"content":[{"text":"module x","type":"text"}],"tool_use_id":"toolu_1","type":"tool_result"}],"role":"user"}`,
		`{"content":[{"content":[{"text":"(no output)","type":"text"}],"is_error":true,"tool_use_id":"call_2","type":"tool_result"},{"text":"thanks","type":"text"}],"role":"user"}`,
		`"input_schema":{"additionalProperties":false,"properties":{"path":{"type":"string"}},"required":["path"],"type":"object"}`,
	}
	for _, w := range want {
		if !strings.Contains(string(sent), w) {
			t.Errorf("request lacks %s\n%s", w, sent)
		}
	}
	if strings.Contains(string(sent), "theirs") {
		t.Errorf("another provider's thinking was sent:\n%s", sent)
	}
}

func TestRequestHeaders(t *testing.T) {
	var got http.Header
	var path string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got, path = r.Header.Clone(), r.URL.Path
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write(signedReply)
	}))
	defer srv.Close()
	t.Setenv("ANTHROPIC_API_KEY", "from-the-environment")
	req := request()
	req.Thinking = true
	if _, err := New(Options{Name: "claude", BaseURL: srv.URL, APIKey: "sk-test", Headers: map[string]string{"X-Extra": "1"}}).Stream(context.Background(), req, func(provider.StreamEvent) {}); err != nil {
		t.Fatal(err)
	}
	if path != "/v1/messages" {
		t.Errorf("path = %s", path)
	}
	auth := got.Get("X-Api-Key") + got.Get("Authorization")
	if !strings.Contains(auth, "sk-test") || strings.Contains(auth, "from-the-environment") {
		t.Errorf("credentials sent: %q", auth)
	}
	if got.Get("X-Extra") != "1" || !strings.Contains(got.Get("Anthropic-Beta"), bindingBeta) {
		t.Errorf("headers = %v", got)
	}
}

func TestErrors(t *testing.T) {
	body := func(typ, msg string) []byte {
		return []byte(fmt.Sprintf(`{"type":"error","error":{"type":%q,"message":%q}}`, typ, msg))
	}
	start := strings.Index(string(signedReply), "event: content_block_delta")
	midStreamError := append([]byte(string(signedReply[:start])), sse(string(body("overloaded_error", "Overloaded")))...)
	tests := []struct {
		name  string
		reply replay.Reply
		kind  provider.ErrorKind
		retry time.Duration
		text  string
	}{
		{"overloaded", replay.Reply{Status: 529, Header: map[string]string{"Retry-After": "3"}, Body: body("overloaded_error", "Overloaded")}, provider.ErrTransient, 3 * time.Second, "Overloaded"},
		{"rate limited", replay.Reply{Status: 429, Body: body("rate_limit_error", "slow down")}, provider.ErrTransient, 0, "slow down"},
		{"prompt too long", replay.Reply{Status: 400, Body: body("invalid_request_error", "prompt is too long: 210000 tokens > 200000 maximum")}, provider.ErrContextOverflow, 0, "210000"},
		{"bad key", replay.Reply{Status: 401, Body: body("authentication_error", "invalid x-api-key")}, provider.ErrAuth, 0, "invalid x-api-key"},
		{"bad request", replay.Reply{Status: 400, Body: body("invalid_request_error", "messages: bad")}, provider.ErrOther, 0, "messages: bad"},
		{"error event mid-stream", replay.Reply{Body: midStreamError}, provider.ErrTransient, 0, "Overloaded"},
		{"stream cut short", replay.Reply{Body: signedReply, CutAt: len(signedReply) / 2}, provider.ErrTransient, 0, "ended before"},
		{"refusal", replay.Reply{Body: sse(
			`{"type":"message_start","message":{"id":"m","type":"message","role":"assistant","content":[],"model":"m","usage":{"input_tokens":5,"output_tokens":0}}}`,
			`{"type":"message_delta","delta":{"stop_reason":"refusal","stop_details":{"type":"refusal","category":"cyber","explanation":"not this"}},"usage":{"output_tokens":0}}`,
			`{"type":"message_stop"}`,
		)}, provider.ErrOther, 0, "declined to answer (cyber): not this"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			srv := replay.New(tt.reply)
			defer srv.Close()
			_, err := New(Options{Name: "claude", BaseURL: srv.URL}).Stream(context.Background(), request(), func(provider.StreamEvent) {})
			var pe *provider.Error
			if !errors.As(err, &pe) || pe.Kind != tt.kind || pe.RetryAfter != tt.retry || !strings.Contains(err.Error(), tt.text) {
				t.Fatalf("err = %v (%+v), want kind %d, retry %s, text %q", err, pe, tt.kind, tt.retry, tt.text)
			}
		})
	}
}

func TestCancelKeepsPartialReply(t *testing.T) {
	srv := replay.New(replay.Reply{Body: signedReply, CutAt: strings.Index(string(signedReply), "content_block_stop"), Stall: true})
	defer srv.Close()
	ctx, cancel := context.WithCancel(context.Background())
	resp, err := New(Options{Name: "claude", BaseURL: srv.URL}).Stream(ctx, request(), func(e provider.StreamEvent) {
		if e.Kind == provider.EventThinkingDelta {
			cancel()
		}
	})
	if !errors.Is(err, context.Canceled) || resp.StopReason != provider.StopCancelled || len(resp.Message.Content) == 0 {
		t.Fatalf("err %v, resp %+v", err, resp)
	}
}
