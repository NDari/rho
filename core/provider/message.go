// Package provider defines Rho's neutral message format and the interface
// implemented by model providers.
package provider

import (
	"encoding/json"
	"strings"
)

// Role is the author of a message.
type Role string

// Message roles.
const (
	RoleUser       Role = "user"
	RoleAssistant  Role = "assistant"
	RoleToolResult Role = "tool_result"
)

// BlockType identifies the kind of content block.
type BlockType string

// Block types.
const (
	BlockText       BlockType = "text"
	BlockThinking   BlockType = "thinking"
	BlockImage      BlockType = "image"
	BlockToolCall   BlockType = "tool_call"
	BlockToolResult BlockType = "tool_result"
)

// Block is one piece of message content. Which fields are meaningful
// depends on Type; a single struct keeps the JSONL encoding simple.
type Block struct {
	Type BlockType `json:"type"`

	// Text and Thinking.
	Text string `json:"text,omitempty"`

	// Thinking and ToolCall: provider-specific data that must be sent back
	// verbatim to the provider that produced it (e.g. signatures).
	Provider string          `json:"provider,omitempty"`
	Opaque   json.RawMessage `json:"opaque,omitempty"`

	// Image.
	MIME string `json:"mime,omitempty"`
	Data []byte `json:"data,omitempty"`
	Path string `json:"path,omitempty"`

	// ToolCall.
	ID   string          `json:"id,omitempty"`
	Name string          `json:"name,omitempty"`
	Args json.RawMessage `json:"args,omitempty"`
	// ArgsError explains why a tool call could not be parsed. The call is
	// answered with this error instead of being run.
	ArgsError string `json:"args_error,omitempty"`

	// ToolResult.
	CallID  string  `json:"call_id,omitempty"`
	Content []Block `json:"content,omitempty"`
	IsError bool    `json:"is_error,omitempty"`
}

// Text returns a text block.
func Text(s string) Block { return Block{Type: BlockText, Text: s} }

// ToolResult returns a tool result block with text content.
func ToolResult(callID, text string, isError bool) Block {
	return Block{Type: BlockToolResult, CallID: callID, Content: []Block{Text(text)}, IsError: isError}
}

// Usage reports token counts for one request.
type Usage struct {
	InputTokens      int `json:"input_tokens"`
	OutputTokens     int `json:"output_tokens"`
	CacheReadTokens  int `json:"cache_read_tokens,omitempty"`
	CacheWriteTokens int `json:"cache_write_tokens,omitempty"`
}

// Total is the context size implied by this request: everything sent plus
// everything generated.
func (u Usage) Total() int { return u.InputTokens + u.OutputTokens }

// StopReason explains why the model stopped.
type StopReason string

// Stop reasons.
const (
	StopEnd       StopReason = "end"
	StopToolUse   StopReason = "tool_use"
	StopMaxTokens StopReason = "max_tokens"
	StopCancelled StopReason = "cancelled"
	StopError     StopReason = "error"
)

// Meta carries information about how a message was produced.
type Meta struct {
	Provider   string     `json:"provider,omitempty"`
	Model      string     `json:"model,omitempty"`
	Usage      *Usage     `json:"usage,omitempty"`
	StopReason StopReason `json:"stop_reason,omitempty"`
	// Synthetic marks messages created by Rho rather than typed by the user
	// (e.g. compaction summaries, aside notes).
	Synthetic string `json:"synthetic,omitempty"`
}

// Message is the neutral message format stored in sessions.
type Message struct {
	Role    Role    `json:"role"`
	Content []Block `json:"content"`
	Meta    Meta    `json:"meta,omitzero"`
}

// UserText returns a user message with a single text block.
func UserText(s string) Message {
	return Message{Role: RoleUser, Content: []Block{Text(s)}}
}

// TextContent concatenates the text blocks of a message.
func (m Message) TextContent() string {
	var b strings.Builder
	for _, c := range m.Content {
		if c.Type == BlockText {
			b.WriteString(c.Text)
		}
	}
	return b.String()
}

// ToolCalls returns the tool call blocks of a message.
func (m Message) ToolCalls() []Block {
	var out []Block
	for _, c := range m.Content {
		if c.Type == BlockToolCall {
			out = append(out, c)
		}
	}
	return out
}

// EstimateTokens returns a rough token estimate (characters / 4).
func (m Message) EstimateTokens() int {
	n := 0
	for _, c := range m.Content {
		n += c.estimateChars()
	}
	return n/4 + 4
}

func (b Block) estimateChars() int {
	switch b.Type {
	case BlockImage:
		return 4 * 1500 // images cost roughly a fixed amount
	case BlockToolCall:
		return len(b.Name) + len(b.Args)
	case BlockToolResult:
		n := 0
		for _, c := range b.Content {
			n += c.estimateChars()
		}
		return n
	default:
		return len(b.Text)
	}
}
