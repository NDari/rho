// Package events defines the typed event stream that the core emits to
// frontends (TUI, headless, GUI).
package events

import (
	"encoding/json"
	"time"

	"github.com/NDari/rho/core/provider"
)

// Event is any core event.
type Event interface {
	// Type is a stable, snake_case name used in JSON output.
	Type() string
}

// Sink receives events. Implementations must not block for long.
type Sink func(Event)

// TurnStart is emitted when the agent starts working on user input.
type TurnStart struct{}

// TurnEnd is emitted when control returns to the user.
type TurnEnd struct {
	// Reason is "done", "cancelled", "blocked" (an extension blocked the
	// message) or "error".
	Reason string `json:"reason"`
	// Unanswered is set when the turn was cancelled or failed before
	// anything was saved after the prompt. The conversation was moved back
	// to before the prompt, so the next message replaces it; it holds the
	// prompt text for frontends to drop the prompt from the transcript and
	// put it back in the editor.
	Unanswered string `json:"unanswered,omitempty"`
	// CancelledBy names who or what cancelled the turn when Reason is
	// "cancelled": "the user", "the RPC client", "extension NAME" or
	// "Rho (shutting down)".
	CancelledBy string `json:"cancelled_by,omitempty"`
	// Duration is the time from turn_start to turn_end.
	Duration time.Duration `json:"duration_ns"`
}

// MessageStart begins a streamed assistant message.
type MessageStart struct {
	Model string `json:"model"`
}

// TextDelta is streamed assistant text.
type TextDelta struct {
	Text string `json:"text"`
}

// ThinkingDelta is streamed reasoning text.
type ThinkingDelta struct {
	Text string `json:"text"`
}

// ToolCallDelta reports that the model is composing a tool call.
type ToolCallDelta struct {
	Index int    `json:"index"`
	Name  string `json:"name,omitempty"`
}

// MessageEnd carries a completed message that was added to the session.
// It is emitted for user, assistant and tool result messages.
type MessageEnd struct {
	EntryID string           `json:"entry_id"`
	Message provider.Message `json:"message"`
}

// ToolStart is emitted before a tool executes.
type ToolStart struct {
	ID   string          `json:"id"`
	Name string          `json:"name"`
	Args json.RawMessage `json:"args"`
}

// ToolOutput is live output from a running tool (e.g. bash).
type ToolOutput struct {
	ID   string `json:"id"`
	Text string `json:"text"`
}

// ToolEnd is emitted after a tool finishes.
type ToolEnd struct {
	ID       string         `json:"id"`
	Name     string         `json:"name"`
	Result   provider.Block `json:"result"`
	Display  *Display       `json:"display,omitempty"`
	Duration time.Duration  `json:"duration_ns"`
}

// Usage reports context and cost after each model response.
type Usage struct {
	Usage         provider.Usage `json:"usage"`
	ContextTokens int            `json:"context_tokens"`
	ContextWindow int            `json:"context_window"`
	// SessionCost is the accumulated cost in US dollars, if pricing is known.
	SessionCost float64 `json:"session_cost"`
}

// Retry reports a transient error that will be retried.
type Retry struct {
	Attempt int           `json:"attempt"`
	Max     int           `json:"max"`
	Delay   time.Duration `json:"delay_ns"`
	Error   string        `json:"error"`
}

// Error reports an error that ended or interrupted the turn.
type Error struct {
	Message string `json:"message"`
}

// CompactionStart is emitted when compaction begins.
type CompactionStart struct {
	Reason string `json:"reason"` // "auto", "manual" or "overflow"
	Tokens int    `json:"tokens"`
}

// CompactionEnd is emitted when compaction finishes.
type CompactionEnd struct {
	EntryID      string `json:"entry_id,omitempty"`
	TokensBefore int    `json:"tokens_before"`
	TokensAfter  int    `json:"tokens_after"`
	Error        string `json:"error,omitempty"`
}

// Notice is an informational or warning message for the user.
type Notice struct {
	Level string `json:"level"` // "info", "warn" or "error"
	Text  string `json:"text"`
}

// Status sets a short status line under Key (empty Text clears it), e.g.
// a rate-limit countdown.
type Status struct {
	Key  string `json:"key"`
	Text string `json:"text"`
}

// Queue reports pending steering and follow-up messages.
type Queue struct {
	Steering  []string `json:"steering"`
	FollowUps []string `json:"follow_ups"`
}

// ModelChanged is emitted when the active model changes.
type ModelChanged struct {
	Model string `json:"model"`
}

// SessionChanged is emitted when the session or position changes in a way
// that requires frontends to reload the transcript (resume, rewind, aside).
type SessionChanged struct {
	SessionID string `json:"session_id"`
	Path      string `json:"path"`
	Aside     bool   `json:"aside"`
}

func (TurnStart) Type() string       { return "turn_start" }
func (TurnEnd) Type() string         { return "turn_end" }
func (MessageStart) Type() string    { return "message_start" }
func (TextDelta) Type() string       { return "text_delta" }
func (ThinkingDelta) Type() string   { return "thinking_delta" }
func (ToolCallDelta) Type() string   { return "tool_call_delta" }
func (MessageEnd) Type() string      { return "message_end" }
func (ToolStart) Type() string       { return "tool_start" }
func (ToolOutput) Type() string      { return "tool_output" }
func (ToolEnd) Type() string         { return "tool_end" }
func (Usage) Type() string           { return "usage" }
func (Retry) Type() string           { return "retry" }
func (Error) Type() string           { return "error" }
func (CompactionStart) Type() string { return "compaction_start" }
func (CompactionEnd) Type() string   { return "compaction_end" }
func (Notice) Type() string          { return "notice" }
func (Queue) Type() string           { return "queue" }
func (Status) Type() string          { return "status" }
func (ModelChanged) Type() string    { return "model_changed" }
func (SessionChanged) Type() string  { return "session_changed" }

// Marshal encodes an event as a JSON object with a "type" field.
func Marshal(e Event) ([]byte, error) {
	body, err := json.Marshal(e)
	if err != nil {
		return nil, err
	}
	typ, _ := json.Marshal(e.Type())
	out := make([]byte, 0, len(body)+len(typ)+10)
	out = append(out, `{"type":`...)
	out = append(out, typ...)
	if len(body) > 2 {
		out = append(out, ',')
		out = append(out, body[1:]...)
	} else {
		out = append(out, '}')
	}
	return out, nil
}
