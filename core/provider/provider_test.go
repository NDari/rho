package provider

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"
)

func TestEstimateTokensCountsOnlyThinkingSentBack(t *testing.T) {
	text := strings.Repeat("x", 4000)
	plain := Message{Role: RoleAssistant, Content: []Block{{Type: BlockThinking, Text: text}}}
	replayed := Message{Role: RoleAssistant, Content: []Block{{Type: BlockThinking, Text: text, Opaque: json.RawMessage(`{"id":"rs_1"}`)}}}
	if n := plain.EstimateTokens(); n > 10 {
		t.Errorf("plain thinking = %d tokens, want none counted", n)
	}
	if n := replayed.EstimateTokens(); n < 1000 {
		t.Errorf("replayed thinking = %d tokens, want about 1000", n)
	}
}

// TestLooksLikeContextOverflow uses the wording servers use. The llama.cpp
// line is recorded (see replay/recordings); the others are the servers'
// usual messages.
func TestLooksLikeContextOverflow(t *testing.T) {
	tests := []struct {
		msg  string
		want bool
	}{
		{"request (22900 tokens) exceeds the available context size (8192 tokens), try increasing it", true},
		{"This model's maximum context length is 128000 tokens. However, your messages resulted in 130512 tokens.", true},
		{"prompt is too long: 210000 tokens > 200000 maximum", true},
		{"The input token count (1200000) exceeds the maximum number of tokens allowed (1048576).", true},
		{"Your input exceeds the context window of this model.", true},
		{"context_length_exceeded", true},
		{"input length exceeds the context length", true},
		{"Input is too long for requested model.", true},
		{"Rate limit reached for requests", false},
		{"Incorrect API key provided", false},
		{"context canceled", false},
		{"context deadline exceeded", false},
		{"the stream ended before the response was complete", false},
		{"", false},
	}
	for _, tt := range tests {
		if got := LooksLikeContextOverflow(tt.msg); got != tt.want {
			t.Errorf("%q: got %v, want %v", tt.msg, got, tt.want)
		}
	}
}

func TestKindAndRetryAfterThroughWrapping(t *testing.T) {
	base := &Error{Kind: ErrTransient, StatusCode: 429, Message: "slow down", RetryAfter: 3 * time.Second}
	wrapped := fmt.Errorf("turn failed: %w", base)
	if KindOf(wrapped) != ErrTransient || RetryAfterOf(wrapped) != 3*time.Second {
		t.Errorf("kind %v, retry after %s", KindOf(wrapped), RetryAfterOf(wrapped))
	}
	plain := errors.New("boom")
	if KindOf(plain) != ErrOther || RetryAfterOf(plain) != 0 || KindOf(nil) != ErrOther {
		t.Error("unclassified errors must be ErrOther with no wait")
	}
	if got := base.Error(); got != "HTTP 429: slow down" {
		t.Errorf("Error() = %q", got)
	}
	inner := errors.New("dial tcp: connection refused")
	if e := (&Error{Kind: ErrTransient, Err: inner}); e.Error() != inner.Error() || !errors.Is(e, inner) {
		t.Errorf("Error() = %q, unwraps %v", e.Error(), errors.Is(e, inner))
	}
}
