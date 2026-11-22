// Package ext runs out-of-process extensions that speak JSON-RPC over
// stdio. See docs/extensions.md for the protocol.
package ext

import (
	"context"
	"encoding/json"

	"github.com/NDari/rho/core/provider"
)

// ProtocolVersion is the extension protocol version.
const ProtocolVersion = 1

// Hook names.
const (
	HookSessionStart     = "session_start"
	HookBeforeToolCall   = "before_tool_call"
	HookAfterToolCall    = "after_tool_call"
	HookTurnEnd          = "turn_end"
	HookBeforeCompaction = "before_compaction"
	HookSessionEnd       = "session_end"
	HookUserMessage      = "user_message"
	HookBeforeRequest    = "before_request"
	// HookMessageEnd and HookToolEnd are notifications: Rho does not wait
	// for the extension, and ignores any reply.
	HookMessageEnd = "message_end"
	HookToolEnd    = "tool_end"
)

// InitializeParams is sent by Rho first.
type InitializeParams struct {
	ProtocolVersion int    `json:"protocol_version"`
	RhoVersion      string `json:"rho_version"`
	Cwd             string `json:"cwd"`
}

// InitializeResult is the extension's reply.
type InitializeResult struct {
	ProtocolVersion int           `json:"protocol_version"`
	Name            string        `json:"name"`
	Version         string        `json:"version"`
	Tools           []ToolInfo    `json:"tools,omitempty"`
	Commands        []CommandInfo `json:"commands,omitempty"`
	Hooks           []string      `json:"hooks,omitempty"`
}

// ToolInfo registers a tool.
type ToolInfo struct {
	Name        string          `json:"name"`
	Description string          `json:"description"`
	Parameters  json.RawMessage `json:"parameters"`
	// ReadOnly tools are offered in side conversations.
	ReadOnly bool `json:"read_only,omitempty"`
}

// CommandInfo registers a slash command.
type CommandInfo struct {
	Name        string `json:"name"`
	Description string `json:"description"`
}

// ToolExecuteParams is sent with tool/execute.
type ToolExecuteParams struct {
	CallID string          `json:"call_id"`
	Name   string          `json:"name"`
	Args   json.RawMessage `json:"args"`
}

// ToolProgressParams is sent by an extension as a tool/progress
// notification while a tool runs.
type ToolProgressParams struct {
	CallID string `json:"call_id"`
	Text   string `json:"text"`
}

// ContentBlock is a piece of tool output.
type ContentBlock struct {
	Type string `json:"type"` // "text" or "image"
	Text string `json:"text,omitempty"`
	MIME string `json:"mime,omitempty"`
	Data []byte `json:"data,omitempty"`
}

// ToolResult is a tool's output.
type ToolResult struct {
	Content []ContentBlock `json:"content"`
	IsError bool           `json:"is_error,omitempty"`
}

// CommandRunParams is sent with command/run.
type CommandRunParams struct {
	Name string `json:"name"`
	Args string `json:"args"`
}

// HookParams is sent with hook/<name>.
type HookParams struct {
	SessionID string          `json:"session_id,omitempty"`
	Tool      string          `json:"tool,omitempty"`
	Args      json.RawMessage `json:"args,omitempty"`
	Result    *ToolResult     `json:"result,omitempty"`
	// Transcript is a plain-text rendering of the messages to compact.
	Transcript string `json:"transcript,omitempty"`
	// Text (user_message) is the text of the user's message.
	Text string `json:"text,omitempty"`
	// System and Messages (before_request) are the request about to be
	// sent, messages in the session file's format; Aside marks a side
	// conversation.
	System   string             `json:"system,omitempty"`
	Messages []provider.Message `json:"messages,omitempty"`
	Aside    bool               `json:"aside,omitempty"`
	// EntryID and Message (message_end) are a message just saved to the
	// session: the user's, the model's, or a tool result.
	EntryID string            `json:"entry_id,omitempty"`
	Message *provider.Message `json:"message,omitempty"`
}

// HookResult is an extension's reply to a hook.
type HookResult struct {
	// Block (before_tool_call, user_message) stops the call or message
	// with this reason.
	Block string `json:"block,omitempty"`
	// Text (user_message) replaces the message's text.
	Text string `json:"text,omitempty"`
	// System and Messages (before_request) replace the request's system
	// prompt and messages, for this request only.
	System   string             `json:"system,omitempty"`
	Messages []provider.Message `json:"messages,omitempty"`
	// Args (before_tool_call) replaces the arguments.
	Args json.RawMessage `json:"args,omitempty"`
	// Result (after_tool_call) replaces the result.
	Result *ToolResult `json:"result,omitempty"`
	// Summary (before_compaction) replaces the generated summary.
	Summary string `json:"summary,omitempty"`
}

// UI requests from extensions.
type (
	// ConfirmParams for ui/confirm.
	ConfirmParams struct {
		Title   string `json:"title"`
		Message string `json:"message"`
	}
	// ConfirmResult for ui/confirm.
	ConfirmResult struct {
		Confirmed bool `json:"confirmed"`
	}
	// SelectParams for ui/select.
	SelectParams struct {
		Title   string   `json:"title"`
		Options []string `json:"options"`
	}
	// InputParams for ui/input.
	InputParams struct {
		Title       string `json:"title"`
		Placeholder string `json:"placeholder"`
	}
	// ValueResult for ui/select and ui/input.
	ValueResult struct {
		Value     string `json:"value"`
		Cancelled bool   `json:"cancelled"`
	}
	// NotifyParams for ui/notify.
	NotifyParams struct {
		Level string `json:"level"`
		Text  string `json:"text"`
	}
	// StatusParams for ui/status.
	StatusParams struct {
		Key  string `json:"key"`
		Text string `json:"text"`
	}
	// AppendMessageParams for session/append_message.
	AppendMessageParams struct {
		Text string `json:"text"`
		// Trigger starts a turn when the agent is idle; otherwise the
		// message is queued as a follow-up.
		Trigger bool `json:"trigger"`
	}
	// LogParams for log.
	LogParams struct {
		Level   string `json:"level"`
		Message string `json:"message"`
	}
	// SessionInfo is the result of session/info.
	SessionInfo struct {
		ID            string  `json:"id"`
		File          string  `json:"file,omitempty"`
		Cwd           string  `json:"cwd"`
		Model         string  `json:"model"`
		Busy          bool    `json:"busy"`
		Aside         bool    `json:"aside,omitempty"`
		ContextTokens int     `json:"context_tokens"`
		ContextWindow int     `json:"context_window"`
		Cost          float64 `json:"cost"`
	}
	// SessionEntry is one message of session/messages.
	SessionEntry struct {
		// ID is the session entry ("" for synthetic messages).
		ID      string           `json:"id"`
		Message provider.Message `json:"message"`
	}
	// SessionMessagesResult is the result of session/messages.
	SessionMessagesResult struct {
		Messages []SessionEntry `json:"messages"`
	}
	// SetModelParams for agent/set_model.
	SetModelParams struct {
		Model string `json:"model"`
	}
)

// UI is implemented by frontends to answer extension UI requests.
type UI interface {
	Confirm(ctx context.Context, title, message string) (bool, error)
	Select(ctx context.Context, title string, options []string) (string, error)
	Input(ctx context.Context, title, placeholder string) (string, error)
	Notify(level, text string)
	SetStatus(key, text string)
}
