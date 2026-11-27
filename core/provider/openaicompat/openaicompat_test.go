package openaicompat

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/NDari/rho/core/provider"
	"github.com/NDari/rho/core/provider/streamutil"
)

// sseServer replies to every request with the given SSE data payloads and
// records the request bodies.
func sseServer(t *testing.T, chunks []string, bodies *[]map[string]any) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		var body map[string]any
		_ = json.Unmarshal(raw, &body)
		if bodies != nil {
			*bodies = append(*bodies, body)
		}
		w.Header().Set("Content-Type", "text/event-stream")
		for _, c := range chunks {
			fmt.Fprintf(w, "data: %s\n\n", c)
		}
		fmt.Fprint(w, "data: [DONE]\n\n")
	}))
}

func chunk(delta string, finish string) string {
	f := "null"
	if finish != "" {
		f = `"` + finish + `"`
	}
	return `{"id":"x","object":"chat.completion.chunk","created":1,"model":"m","choices":[{"index":0,"delta":` + delta + `,"finish_reason":` + f + `}]}`
}

func TestStreamTextReasoningAndToolCalls(t *testing.T) {
	var bodies []map[string]any
	srv := sseServer(t, []string{
		chunk(`{"role":"assistant","reasoning_content":"think "}`, ""),
		chunk(`{"content":"Hello"}`, ""),
		chunk(`{"content":" world"}`, ""),
		chunk(`{"tool_calls":[{"index":0,"id":"c1","type":"function","function":{"name":"read","arguments":"{\"pa"}, "extra_content":{"google":{"thought_signature":"sig"}}}]}`, ""),
		chunk(`{"tool_calls":[{"index":0,"function":{"arguments":"th\":\"a\"}"}}]}`, "tool_calls"),
		`{"id":"x","object":"chat.completion.chunk","created":1,"model":"m","choices":[],"usage":{"prompt_tokens":10,"completion_tokens":5,"total_tokens":15,"prompt_tokens_details":{"cached_tokens":4}}}`,
	}, &bodies)
	defer srv.Close()

	p := New(Options{Name: "test", BaseURL: srv.URL})
	var text, thinking strings.Builder
	resp, err := p.Stream(context.Background(), provider.Request{
		Model:    "m",
		System:   "sys",
		Messages: []provider.Message{provider.UserText("hi")},
		Tools:    []provider.ToolSpec{{Name: "read", Description: "d", Parameters: json.RawMessage(`{"type":"object"}`)}},
	}, func(e provider.StreamEvent) {
		switch e.Kind {
		case provider.EventTextDelta:
			text.WriteString(e.Text)
		case provider.EventThinkingDelta:
			thinking.WriteString(e.Text)
		}
	})
	if err != nil {
		t.Fatal(err)
	}
	if text.String() != "Hello world" || thinking.String() != "think " {
		t.Errorf("text=%q thinking=%q", text.String(), thinking.String())
	}
	calls := resp.Message.ToolCalls()
	if len(calls) != 1 || calls[0].Name != "read" || string(calls[0].Args) != `{"path":"a"}` {
		t.Fatalf("calls = %+v", calls)
	}
	if string(calls[0].Opaque) != `{"google":{"thought_signature":"sig"}}` {
		t.Errorf("opaque = %s", calls[0].Opaque)
	}
	if resp.StopReason != provider.StopToolUse {
		t.Errorf("stop = %s", resp.StopReason)
	}
	if resp.Usage.InputTokens != 10 || resp.Usage.OutputTokens != 5 || resp.Usage.CacheReadTokens != 4 {
		t.Errorf("usage = %+v", resp.Usage)
	}
	msgs := bodies[0]["messages"].([]any)
	if msgs[0].(map[string]any)["role"] != "system" {
		t.Errorf("first message should be system: %v", msgs[0])
	}
}

func TestToolCallRoundTripSendsOpaque(t *testing.T) {
	var bodies []map[string]any
	srv := sseServer(t, []string{chunk(`{"content":"ok"}`, "stop")}, &bodies)
	defer srv.Close()

	p := New(Options{Name: "g", BaseURL: srv.URL, Flavor: FlavorGemini})
	msgs := []provider.Message{
		provider.UserText("hi"),
		{Role: provider.RoleAssistant, Content: []provider.Block{
			{Type: provider.BlockToolCall, ID: "a", Name: "read", Args: json.RawMessage(`{}`), Provider: "g", Opaque: json.RawMessage(`{"google":{"thought_signature":"s1"}}`)},
			{Type: provider.BlockToolCall, ID: "b", Name: "read", Args: json.RawMessage(`{}`), Provider: "other", Opaque: json.RawMessage(`{"x":1}`)},
		}},
		{Role: provider.RoleToolResult, Content: []provider.Block{provider.ToolResult("a", "r1", false)}},
		{Role: provider.RoleToolResult, Content: []provider.Block{provider.ToolResult("b", "r2", false)}},
	}
	if _, err := p.Stream(context.Background(), provider.Request{Model: "m", Messages: msgs}, func(provider.StreamEvent) {}); err != nil {
		t.Fatal(err)
	}
	sent := bodies[0]["messages"].([]any)
	calls := sent[1].(map[string]any)["tool_calls"].([]any)
	sig := func(i int) string {
		ec := calls[i].(map[string]any)["extra_content"].(map[string]any)
		return ec["google"].(map[string]any)["thought_signature"].(string)
	}
	if sig(0) != "s1" {
		t.Errorf("own signature not sent back: %v", calls[0])
	}
	if sig(1) != geminiSkipSignature {
		t.Errorf("foreign call should get placeholder: %v", calls[1])
	}
	if sent[2].(map[string]any)["role"] != "tool" || sent[2].(map[string]any)["tool_call_id"] != "a" {
		t.Errorf("tool message = %v", sent[2])
	}
}

func TestErrorClassification(t *testing.T) {
	tests := []struct {
		status int
		body   string
		want   provider.ErrorKind
	}{
		{400, `{"error":{"code":400,"message":"the request exceeds the available context size, try increasing it","type":"exceed_context_size_error"}}`, provider.ErrContextOverflow},
		{429, `{"error":{"message":"slow down"}}`, provider.ErrTransient},
		{503, `{"error":{"message":"overloaded"}}`, provider.ErrTransient},
		{401, `{"error":{"message":"bad key"}}`, provider.ErrAuth},
		{400, `{"error":{"message":"bad request"}}`, provider.ErrOther},
	}
	for _, tt := range tests {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(tt.status)
			_, _ = io.WriteString(w, tt.body)
		}))
		p := New(Options{Name: "t", BaseURL: srv.URL})
		_, err := p.Stream(context.Background(), provider.Request{Model: "m", Messages: []provider.Message{provider.UserText("x")}}, func(provider.StreamEvent) {})
		srv.Close()
		if got := provider.KindOf(err); got != tt.want {
			t.Errorf("status %d: kind = %v, want %v (err %v)", tt.status, got, tt.want, err)
		}
	}
}

// TestIntegration runs against a real OpenAI-compatible server when
// RHO_TEST_BASE_URL is set (RHO_TEST_MODEL and RHO_TEST_API_KEY optional).
func TestIntegration(t *testing.T) {
	base := os.Getenv("RHO_TEST_BASE_URL")
	if base == "" {
		t.Skip("RHO_TEST_BASE_URL not set")
	}
	model := os.Getenv("RHO_TEST_MODEL")
	if model == "" {
		model = "default"
	}
	p := New(Options{Name: "it", BaseURL: base, APIKey: os.Getenv("RHO_TEST_API_KEY")})
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	resp, err := p.Stream(ctx, provider.Request{
		Model:     model,
		Messages:  []provider.Message{provider.UserText("Use the read tool to read the file named go.mod.")},
		Tools:     []provider.ToolSpec{{Name: "read", Description: "Read a file", Parameters: json.RawMessage(`{"type":"object","properties":{"path":{"type":"string"}},"required":["path"]}`)}},
		MaxTokens: 2048,
	}, func(provider.StreamEvent) {})
	if err != nil {
		t.Fatal(err)
	}
	calls := resp.Message.ToolCalls()
	if len(calls) == 0 {
		t.Fatalf("expected a tool call, got %+v", resp.Message)
	}
	if resp.Usage.InputTokens == 0 {
		t.Errorf("usage not reported: %+v", resp.Usage)
	}
}

// TestResponsesIntegration runs the Responses provider against a real
// server when RHO_TEST_RESPONSES_URL is set.
func TestResponsesIntegration(t *testing.T) {
	base := os.Getenv("RHO_TEST_RESPONSES_URL")
	if base == "" {
		t.Skip("RHO_TEST_RESPONSES_URL not set")
	}
	model := os.Getenv("RHO_TEST_MODEL")
	if model == "" {
		model = "default"
	}
	p := NewResponses(Options{Name: "it", BaseURL: base, APIKey: os.Getenv("RHO_TEST_API_KEY")})
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	tools := []provider.ToolSpec{{Name: "read", Description: "Read a file", Parameters: json.RawMessage(`{"type":"object","properties":{"path":{"type":"string"}},"required":["path"]}`)}}
	var text strings.Builder
	resp, err := p.Stream(ctx, provider.Request{
		Model:     model,
		System:    "You are a helpful assistant.",
		Messages:  []provider.Message{provider.UserText("Use the read tool to read the file named go.mod.")},
		Tools:     tools,
		MaxTokens: 4096,
	}, func(e provider.StreamEvent) {
		if e.Kind == provider.EventTextDelta {
			text.WriteString(e.Text)
		}
	})
	if err != nil {
		t.Fatal(err)
	}
	calls := resp.Message.ToolCalls()
	if len(calls) == 0 {
		t.Fatalf("expected a tool call, got %+v", resp.Message)
	}
	// Second round trip: send the tool result back.
	msgs := []provider.Message{
		provider.UserText("Use the read tool to read the file named go.mod."),
		resp.Message,
		{Role: provider.RoleToolResult, Content: []provider.Block{provider.ToolResult(calls[0].ID, "module example.com/x\n\ngo 1.24\n", false)}},
	}
	resp2, err := p.Stream(ctx, provider.Request{Model: model, Messages: msgs, Tools: tools, MaxTokens: 4096}, func(provider.StreamEvent) {})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(resp2.Message.TextContent(), "example.com/x") {
		t.Errorf("second response did not use the tool result: %q", resp2.Message.TextContent())
	}
}

func TestIsConnReset(t *testing.T) {
	tests := []struct {
		err  error
		want bool
	}{
		{io.EOF, true},
		{syscall.ECONNRESET, true},
		{fmt.Errorf("post: %w", net.ErrClosed), true},
		// As seen when a server closed an idle keep-alive connection mid-send.
		{errors.New(`Post "http://localhost:8080/v1/chat/completions": readfrom tcp 127.0.0.1:1->127.0.0.1:2: write tcp 127.0.0.1:1->127.0.0.1:2: use of closed network connection`), true},
		{errors.New("http2: server closed idle connection"), true},
		{errors.New("401 Unauthorized"), false},
		{errors.New("context length exceeded"), false},
	}
	for _, tc := range tests {
		if got := streamutil.IsConnReset(tc.err); got != tc.want {
			t.Errorf("isConnReset(%v) = %v, want %v", tc.err, got, tc.want)
		}
	}
}
