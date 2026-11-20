package app

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/NDari/rho/core/events"
	"github.com/NDari/rho/core/provider"
	"github.com/NDari/rho/core/session"
)

// This file holds frontend-neutral views of the session, shared by the TUI
// and the GUI so that they only differ in presentation.

// Transcript item kinds.
const (
	ItemUser       = "user"
	ItemAssistant  = "assistant"
	ItemTool       = "tool"
	ItemCompaction = "compaction"
	ItemNote       = "note"
)

// TranscriptItem is one entry of the visible conversation.
type TranscriptItem struct {
	Kind string `json:"kind"`
	// Text is the user text (without attachments), the assistant text, or
	// the note.
	Text string `json:"text,omitempty"`
	// Attached lists the @file references attached to a user message.
	Attached []string `json:"attached,omitempty"`
	// Thinking is the assistant's reasoning text.
	Thinking string `json:"thinking,omitempty"`
	// Cancelled reports an assistant message cut short by the user.
	Cancelled bool `json:"cancelled,omitempty"`
	// Tool is set for tool items.
	Tool *ToolCall `json:"tool,omitempty"`
	// Summary and TokensBefore describe a compaction.
	Summary      string `json:"summary,omitempty"`
	TokensBefore int    `json:"tokens_before,omitempty"`
}

// ToolCall is a tool call and, once it finished, its result.
type ToolCall struct {
	ID      string          `json:"id"`
	Name    string          `json:"name"`
	Args    json.RawMessage `json:"args,omitempty"`
	Result  string          `json:"result,omitempty"`
	IsError bool            `json:"is_error,omitempty"`
	Display *events.Display `json:"display,omitempty"`
	// Done reports that a result was recorded.
	Done bool `json:"done,omitempty"`
}

// Transcript is the visible conversation on the current path.
type Transcript struct {
	Items []TranscriptItem `json:"items"`
	// Aside reports that the current position is in a side conversation.
	Aside bool `json:"aside,omitempty"`
}

// Transcript rebuilds the visible conversation from the session's current
// path, pairing tool calls with their results and display data.
func (a *App) Transcript() Transcript {
	sess := a.Agent.Session()
	cv := sess.BuildContext()
	t := Transcript{Aside: cv.Aside}
	tools := map[string]*ToolCall{}
	for i, msg := range cv.Messages {
		switch {
		case msg.Meta.Synthetic == session.SyntheticCompaction:
			it := TranscriptItem{Kind: ItemCompaction}
			if cv.Compaction != nil {
				it.Summary = cv.Compaction.Summary
				it.TokensBefore = cv.Compaction.TokensBefore
			}
			t.Items = append(t.Items, it)
		case msg.Meta.Synthetic == session.SyntheticAsideNote:
			t.Items = append(t.Items, TranscriptItem{Kind: ItemNote, Text: msg.TextContent()})
		case msg.Role == provider.RoleUser:
			text, attached := UserMessageText(msg)
			t.Items = append(t.Items, TranscriptItem{Kind: ItemUser, Text: text, Attached: attached})
		case msg.Role == provider.RoleAssistant:
			it := TranscriptItem{Kind: ItemAssistant, Text: msg.TextContent(), Cancelled: msg.Meta.StopReason == provider.StopCancelled}
			for _, b := range msg.Content {
				if b.Type == provider.BlockThinking {
					it.Thinking += b.Text
				}
			}
			if it.Text != "" || it.Thinking != "" || it.Cancelled {
				t.Items = append(t.Items, it)
			}
			for _, b := range msg.ToolCalls() {
				tc := &ToolCall{ID: b.ID, Name: b.Name, Args: b.Args}
				tools[b.ID] = tc
				t.Items = append(t.Items, TranscriptItem{Kind: ItemTool, Tool: tc})
			}
		case msg.Role == provider.RoleToolResult:
			var display *events.Display
			if id := cv.EntryIDs[i]; id != "" {
				if e, ok := sess.Get(id); ok && len(e.Display) > 0 {
					var d events.Display
					if json.Unmarshal(e.Display, &d) == nil {
						display = &d
					}
				}
			}
			for _, b := range msg.Content {
				if b.Type != provider.BlockToolResult {
					continue
				}
				if tc := tools[b.CallID]; tc != nil {
					tc.Result = ResultText(b)
					tc.IsError = b.IsError
					tc.Display = display
					tc.Done = true
				}
			}
		}
	}
	return t
}

// UserMessageText splits a user message into its text and the paths of
// attached @file references.
func UserMessageText(msg provider.Message) (text string, attached []string) {
	for i, b := range msg.Content {
		if b.Type != provider.BlockText {
			continue
		}
		if i == 0 {
			text = b.Text
			continue
		}
		if strings.HasPrefix(b.Text, "<file path=") {
			if j := strings.Index(b.Text, "\""); j >= 0 {
				if k := strings.Index(b.Text[j+1:], "\""); k >= 0 {
					attached = append(attached, b.Text[j+1:j+1+k])
				}
			}
			continue
		}
		text += "\n" + b.Text
	}
	return text, attached
}

// ResultText is the text content of a tool result block.
func ResultText(b provider.Block) string {
	var sb strings.Builder
	for _, c := range b.Content {
		if c.Type == provider.BlockText {
			sb.WriteString(c.Text)
		}
	}
	return sb.String()
}

var fileRefRE = regexp.MustCompile(`(?:^|\s)@([^\s]+)`)

// BuildUserMessage makes a user message from text and attaches the contents
// of @file references (text files up to 256 KB, relative to the working
// directory).
func (a *App) BuildUserMessage(text string) provider.Message {
	msg := provider.UserText(text)
	seen := map[string]bool{}
	for _, match := range fileRefRE.FindAllStringSubmatch(text, -1) {
		ref := strings.TrimRight(match[1], ".,;:!?)")
		if seen[ref] {
			continue
		}
		seen[ref] = true
		path := ref
		if !filepath.IsAbs(path) {
			path = filepath.Join(a.Paths.Cwd, ref)
		}
		info, err := os.Stat(path)
		if err != nil || info.IsDir() || info.Size() > 256*1024 {
			continue
		}
		data, err := os.ReadFile(path)
		if err != nil || strings.ContainsRune(string(data), 0) {
			continue
		}
		msg.Content = append(msg.Content, provider.Text(fmt.Sprintf("<file path=%q>\n%s\n</file>", ref, string(data))))
	}
	return msg
}

// MessageRef describes a message entry for pickers such as rewind and fork.
type MessageRef struct {
	ID   string        `json:"id"`
	Role provider.Role `json:"role"`
	Text string        `json:"text,omitempty"`
	// ToolName is the first tool call's name when the message has no text.
	ToolName string    `json:"tool_name,omitempty"`
	TS       time.Time `json:"ts"`
	Label    string    `json:"label,omitempty"`
}

// PathMessages lists the message entries on the current path with one of
// the given roles, newest first. Synthetic messages are left out.
func (a *App) PathMessages(roles ...provider.Role) []MessageRef {
	sess := a.Agent.Session()
	path := sess.PathTo(sess.Leaf())
	labels := sess.Labels()
	var out []MessageRef
	for i := len(path) - 1; i >= 0; i-- {
		e := path[i]
		if e.Type != session.TypeMessage || e.Message == nil || e.Message.Meta.Synthetic != "" {
			continue
		}
		if !slices.Contains(roles, e.Message.Role) {
			continue
		}
		r := MessageRef{ID: e.ID, Role: e.Message.Role, Text: e.Message.TextContent(), TS: e.TS, Label: labels[e.ID]}
		if calls := e.Message.ToolCalls(); r.Text == "" && len(calls) > 0 {
			r.ToolName = calls[0].Name
		}
		out = append(out, r)
	}
	return out
}

// Tree node kinds besides the message roles "user" and "assistant".
const (
	NodeCompaction  = "compaction"
	NodeAsideStart  = "aside_start"
	NodeAsideNote   = "aside_note"
	NodeModelChange = "model_change"
)

// TreeNode is one visible entry of the session tree.
type TreeNode struct {
	ID string `json:"id"`
	// Kind is "user", "assistant" or one of the Node constants.
	Kind string `json:"kind"`
	// Text is the message text, the aside note or the model name.
	Text string `json:"text,omitempty"`
	// Depth grows by one below each entry with more than one child.
	Depth   int       `json:"depth"`
	Current bool      `json:"current,omitempty"`
	OnPath  bool      `json:"on_path,omitempty"`
	Label   string    `json:"label,omitempty"`
	TS      time.Time `json:"ts"`
}

// Tree lists the session tree depth first. Entries without anything to
// show (tool results, assistant messages with only tool calls) are left
// out, but their children are kept.
func (a *App) Tree() []TreeNode {
	sess := a.Agent.Session()
	leaf := sess.Leaf()
	onPath := map[string]bool{}
	for _, e := range sess.PathTo(leaf) {
		onPath[e.ID] = true
	}
	labels := sess.Labels()
	var out []TreeNode
	var walk func(parent string, depth int)
	walk = func(parent string, depth int) {
		kids := sess.Children(parent)
		for _, e := range kids {
			d := depth
			if len(kids) > 1 {
				d = depth + 1
			}
			if kind, text, ok := treeKind(e); ok {
				out = append(out, TreeNode{
					ID: e.ID, Kind: kind, Text: text, Depth: d,
					Current: e.ID == leaf, OnPath: onPath[e.ID], Label: labels[e.ID], TS: e.TS,
				})
			}
			walk(e.ID, d)
		}
	}
	walk("", 0)
	return out
}

func treeKind(e session.Entry) (kind, text string, ok bool) {
	switch e.Type {
	case session.TypeMessage:
		if e.Message == nil {
			return "", "", false
		}
		switch e.Message.Role {
		case provider.RoleUser:
			return string(provider.RoleUser), e.Message.TextContent(), true
		case provider.RoleAssistant:
			t := e.Message.TextContent()
			return string(provider.RoleAssistant), t, strings.TrimSpace(t) != ""
		}
	case session.TypeCompaction:
		return NodeCompaction, "", true
	case session.TypeAsideStart:
		return NodeAsideStart, "", true
	case session.TypeAsideSummary:
		return NodeAsideNote, e.Note, true
	case session.TypeModelChange:
		return NodeModelChange, e.Model, true
	}
	return "", "", false
}

// CopyText returns the last assistant answer, or with all set the whole
// conversation as markdown (tool results left out). It is empty when there
// is nothing to copy.
func (a *App) CopyText(all bool) string {
	cv := a.Agent.Session().BuildContext()
	if all {
		var b strings.Builder
		for _, msg := range cv.Messages {
			if t := msg.TextContent(); t != "" && msg.Role != provider.RoleToolResult {
				fmt.Fprintf(&b, "## %s\n\n%s\n\n", msg.Role, t)
			}
		}
		return b.String()
	}
	for i := len(cv.Messages) - 1; i >= 0; i-- {
		if cv.Messages[i].Role == provider.RoleAssistant && cv.Messages[i].TextContent() != "" {
			return cv.Messages[i].TextContent()
		}
	}
	return ""
}

// GitBranch reads the current branch name from .git/HEAD.
func GitBranch(root string) string {
	data, err := os.ReadFile(filepath.Join(root, ".git", "HEAD"))
	if err != nil {
		return ""
	}
	s := strings.TrimSpace(string(data))
	if b, ok := strings.CutPrefix(s, "ref: refs/heads/"); ok {
		return b
	}
	if len(s) >= 7 {
		return s[:7]
	}
	return ""
}
