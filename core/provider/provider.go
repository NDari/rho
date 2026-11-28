package provider

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
)

// ToolSpec describes a tool offered to the model.
type ToolSpec struct {
	Name        string          `json:"name"`
	Description string          `json:"description"`
	Parameters  json.RawMessage `json:"parameters"`
}

// Request is one model call.
type Request struct {
	Model           string
	System          string
	Messages        []Message
	Tools           []ToolSpec
	MaxTokens       int
	ReasoningEffort string
	SupportsImages  bool
	// Thinking asks a provider that controls thinking per request
	// (Anthropic's) to turn it on; the model's supports_thinking.
	Thinking bool
	// Stop lists sequences that end the response early.
	Stop []string
}

// StreamEventKind identifies a streaming delta.
type StreamEventKind int

// Stream event kinds.
const (
	EventTextDelta StreamEventKind = iota
	EventThinkingDelta
	EventToolCallStart
	EventToolCallDelta
	// EventStatus carries a short status line (Text, empty to clear),
	// e.g. while a request waits for a rate limit.
	EventStatus
)

// StreamEvent is an incremental update while a response streams.
type StreamEvent struct {
	Kind StreamEventKind
	Text string
	// Tool call fields (EventToolCallStart / EventToolCallDelta).
	Index int
	ID    string
	Name  string
}

// Response is the completed result of a streamed request.
type Response struct {
	Message    Message
	Usage      Usage
	StopReason StopReason
}

// Provider streams model responses.
type Provider interface {
	// Name is the configured provider name (e.g. "openai").
	Name() string
	// Stream sends req and calls onEvent for each delta. It returns the
	// assembled message when the stream ends. On cancellation it returns the
	// partial response together with ctx.Err().
	Stream(ctx context.Context, req Request, onEvent func(StreamEvent)) (Response, error)
}

// ServerModel is a model a server offers (see ModelLister). Only ID is
// always known.
type ServerModel struct {
	ID          string `json:"id"`
	DisplayName string `json:"display_name,omitempty"`
	// ContextWindow and MaxOutput are 0 when the server doesn't say.
	ContextWindow int `json:"context_window,omitempty"`
	MaxOutput     int `json:"max_output,omitempty"`
	// Capabilities reports whether Thinking and Images are known.
	Capabilities bool `json:"capabilities,omitempty"`
	Thinking     bool `json:"thinking,omitempty"`
	Images       bool `json:"images,omitempty"`
}

// ModelLister is implemented by providers that can list the models their
// server offers.
type ModelLister interface {
	ListModels(ctx context.Context) ([]ServerModel, error)
}

// ErrorKind classifies provider errors.
type ErrorKind int

// Error kinds.
const (
	ErrOther ErrorKind = iota
	// ErrTransient errors (429, 5xx, network) may be retried.
	ErrTransient
	// ErrContextOverflow means the request exceeded the context window.
	ErrContextOverflow
	// ErrAuth means the credentials were rejected.
	ErrAuth
)

// Error is a classified provider error.
type Error struct {
	Kind       ErrorKind
	StatusCode int
	Message    string
	Err        error
	// RetryAfter is how long the server asked the client to wait before
	// retrying (from a Retry-After header), or 0.
	RetryAfter time.Duration
}

func (e *Error) Error() string {
	var b strings.Builder
	if e.StatusCode != 0 {
		fmt.Fprintf(&b, "HTTP %d: ", e.StatusCode)
	}
	if e.Message != "" {
		b.WriteString(e.Message)
	} else if e.Err != nil {
		b.WriteString(e.Err.Error())
	}
	return b.String()
}

func (e *Error) Unwrap() error { return e.Err }

// KindOf returns the classification of err.
func KindOf(err error) ErrorKind {
	var pe *Error
	if errors.As(err, &pe) {
		return pe.Kind
	}
	return ErrOther
}

// RetryAfterOf returns the wait the server asked for, or 0.
func RetryAfterOf(err error) time.Duration {
	var pe *Error
	if errors.As(err, &pe) {
		return pe.RetryAfter
	}
	return 0
}

// LooksLikeContextOverflow reports whether an error message from a provider
// describes a context-length problem. Servers phrase this differently.
func LooksLikeContextOverflow(msg string) bool {
	m := strings.ToLower(msg)
	for _, s := range []string{
		"context_length_exceeded",
		"exceed_context_size",
		"exceeds the available context",
		"maximum context length",
		"context window",
		"context length",
		"too many tokens",
		"prompt is too long",
		"exceeds the maximum number of tokens",
		"input is too long",
	} {
		if strings.Contains(m, s) {
			return true
		}
	}
	return false
}
