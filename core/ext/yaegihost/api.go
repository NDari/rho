// Package yaegihost runs Go source extensions with the Yaegi interpreter.
// Rho starts it as "rho ext-host <path>"; no Go toolchain is needed.
//
// Interpreted code imports the "rho" package:
//
//	package main
//
//	import "rho"
//
//	func main() {
//		rho.RegisterTool(rho.Tool{
//			Name:        "hello",
//			Description: "Say hello",
//			Parameters:  map[string]any{"type": "object", "properties": map[string]any{"name": map[string]any{"type": "string"}}},
//			Execute: func(ctx *rho.ToolContext, args map[string]any) (string, error) {
//				return "hello " + fmt.Sprint(args["name"]), nil
//			},
//		})
//	}
package yaegihost

import (
	"context"
	"fmt"
	"reflect"
	"sync"

	"github.com/traefik/yaegi/interp"

	"github.com/NDari/rho/core/ext"
)

// Tool is a tool implemented by an extension.
type Tool struct {
	Name        string
	Description string
	// Parameters is a JSON Schema object.
	Parameters map[string]any
	// ReadOnly tools are also offered in side conversations.
	ReadOnly bool
	Execute  func(ctx *ToolContext, args map[string]any) (string, error)
}

// ToolContext is passed to Tool.Execute.
type ToolContext struct {
	CallID string
	ctx    context.Context
}

// Progress streams a line of output to the user while the tool runs.
func (c *ToolContext) Progress(text string) {
	if h := current(); h != nil {
		_ = h.conn.Notify("tool/progress", ext.ToolProgressParams{CallID: c.CallID, Text: text})
	}
}

// Cancelled reports whether the user cancelled the call.
func (c *ToolContext) Cancelled() bool { return c.ctx.Err() != nil }

// Done is closed when the call is cancelled.
func (c *ToolContext) Done() <-chan struct{} { return c.ctx.Done() }

// Event is passed to hook handlers.
type Event struct {
	Name string
	// Tool, Args, Result and IsError are set for tool hooks.
	Tool    string
	Args    map[string]any
	Result  string
	IsError bool
	// Transcript is set for before_compaction.
	Transcript string
}

// Response is returned by hook handlers. The zero value changes nothing.
type Response struct {
	// Block stops a tool call (before_tool_call) with this reason.
	Block string
	// Args replaces the tool arguments (before_tool_call).
	Args map[string]any
	// Result replaces the tool result text (after_tool_call) when ReplaceResult is set.
	Result        string
	IsError       bool
	ReplaceResult bool
	// Summary replaces the compaction summary (before_compaction).
	Summary string
}

type command struct {
	name, desc string
	run        func(args string) error
}

type registry struct {
	mu       sync.Mutex
	name     string
	version  string
	tools    []Tool
	commands []command
	hooks    map[string][]func(Event) Response
}

var reg = &registry{hooks: map[string][]func(Event) Response{}}

func (r *registry) reset() {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.name, r.version = "", ""
	r.tools, r.commands = nil, nil
	r.hooks = map[string][]func(Event) Response{}
}

// RegisterTool adds a tool.
func RegisterTool(t Tool) {
	reg.mu.Lock()
	defer reg.mu.Unlock()
	reg.tools = append(reg.tools, t)
}

// RegisterCommand adds a slash command.
func RegisterCommand(name, description string, run func(args string) error) {
	reg.mu.Lock()
	defer reg.mu.Unlock()
	reg.commands = append(reg.commands, command{name, description, run})
}

// On registers a hook handler. Events: session_start, before_tool_call,
// after_tool_call, turn_end, before_compaction, session_end.
func On(event string, fn func(e Event) Response) {
	reg.mu.Lock()
	defer reg.mu.Unlock()
	reg.hooks[event] = append(reg.hooks[event], fn)
}

// Info sets the extension's name and version.
func Info(name, version string) {
	reg.mu.Lock()
	defer reg.mu.Unlock()
	reg.name, reg.version = name, version
}

// Log writes a line to the extension log (shown by /extensions).
func Log(format string, args ...any) {
	if h := current(); h != nil {
		_ = h.conn.Notify("log", ext.LogParams{Level: "info", Message: fmt.Sprintf(format, args...)})
	}
}

// AppendMessage sends a user message to the agent. With trigger set and
// the agent idle, a turn starts; otherwise it is queued as a follow-up.
func AppendMessage(text string, trigger bool) error {
	h := current()
	if h == nil {
		return fmt.Errorf("not connected")
	}
	return h.conn.Call(context.Background(), "session/append_message", ext.AppendMessageParams{Text: text, Trigger: trigger}, nil)
}

// UIAPI provides simple user interface requests.
type UIAPI struct{}

// UI is the user interface entry point.
var UI UIAPI

// Confirm asks a yes/no question.
func (UIAPI) Confirm(title, message string) bool {
	var r ext.ConfirmResult
	if h := current(); h != nil {
		_ = h.conn.Call(context.Background(), "ui/confirm", ext.ConfirmParams{Title: title, Message: message}, &r)
	}
	return r.Confirmed
}

// Select asks the user to choose; ok is false if cancelled.
func (UIAPI) Select(title string, options []string) (string, bool) {
	var r ext.ValueResult
	if h := current(); h != nil {
		if err := h.conn.Call(context.Background(), "ui/select", ext.SelectParams{Title: title, Options: options}, &r); err != nil {
			return "", false
		}
	}
	return r.Value, !r.Cancelled
}

// Input asks for a line of text; ok is false if cancelled.
func (UIAPI) Input(title, placeholder string) (string, bool) {
	var r ext.ValueResult
	if h := current(); h != nil {
		if err := h.conn.Call(context.Background(), "ui/input", ext.InputParams{Title: title, Placeholder: placeholder}, &r); err != nil {
			return "", false
		}
	}
	return r.Value, !r.Cancelled
}

// Notify shows a message to the user.
func (UIAPI) Notify(text string) {
	if h := current(); h != nil {
		_ = h.conn.Call(context.Background(), "ui/notify", ext.NotifyParams{Level: "info", Text: text}, nil)
	}
}

// Status sets this extension's status bar text ("" clears it).
func (UIAPI) Status(text string) {
	if h := current(); h != nil {
		_ = h.conn.Call(context.Background(), "ui/status", ext.StatusParams{Text: text}, nil)
	}
}

// Symbols exposes the rho package to interpreted code.
var Symbols = interp.Exports{
	"rho/rho": {
		"Tool":            reflect.ValueOf((*Tool)(nil)),
		"ToolContext":     reflect.ValueOf((*ToolContext)(nil)),
		"Event":           reflect.ValueOf((*Event)(nil)),
		"Response":        reflect.ValueOf((*Response)(nil)),
		"UIAPI":           reflect.ValueOf((*UIAPI)(nil)),
		"RegisterTool":    reflect.ValueOf(RegisterTool),
		"RegisterCommand": reflect.ValueOf(RegisterCommand),
		"On":              reflect.ValueOf(On),
		"Info":            reflect.ValueOf(Info),
		"Log":             reflect.ValueOf(Log),
		"AppendMessage":   reflect.ValueOf(AppendMessage),
		"UI":              reflect.ValueOf(&UI).Elem(),
	},
}
