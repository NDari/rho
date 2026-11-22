// Package rpcmode lets another program drive Rho over JSON-RPC 2.0 on
// stdin and stdout (rho --mode rpc): it sends commands as requests and
// receives every event as a notification. See docs/rpc.md.
package rpcmode

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"sync"

	"github.com/NDari/rho/core/app"
	"github.com/NDari/rho/core/events"
	"github.com/NDari/rho/core/ext"
	"github.com/NDari/rho/core/ext/rpc"
	"github.com/NDari/rho/headless"
)

// ProtocolVersion is the RPC mode protocol version.
const ProtocolVersion = 1

// Methods are the requests RPC mode serves, sent in initialize.
var Methods = []string{
	"initialize", "prompt", "steer", "follow_up", "cancel", "wait",
	"session/info", "session/messages", "session/new", "set_model", "set_tools", "compact", "shutdown", "exit",
}

// Params and results of the client's requests.
type (
	// InitializeResult is the reply to initialize.
	InitializeResult struct {
		ProtocolVersion int             `json:"protocol_version"`
		RhoVersion      string          `json:"rho_version"`
		Session         ext.SessionInfo `json:"session"`
		// Methods are the requests this Rho serves.
		Methods []string `json:"methods"`
	}
	// TextParams carries a message for prompt, steer and follow_up.
	TextParams struct {
		Text string `json:"text"`
	}
	// ModelParams for set_model.
	ModelParams struct {
		Model string `json:"model"`
	}
	// ToolsParams for set_tools; a missing or null tools goes back to
	// the configured set.
	ToolsParams struct {
		Tools []string `json:"tools"`
	}
	// MessagesResult is the reply to session/messages.
	MessagesResult struct {
		Messages []ext.SessionEntry `json:"messages"`
	}
)

// Run creates the app with o and serves requests from in, writing
// replies, events and UI requests to out, until the client sends exit,
// closes in, or ctx ends.
func Run(ctx context.Context, o app.Options, in io.Reader, out io.Writer) error {
	s := &server{ready: make(chan struct{}), quit: make(chan struct{})}
	s.conn = rpc.NewConn(in, out, s.handle)
	o.Sink = s.sink
	a, err := app.New(o)
	if err != nil {
		return err
	}
	s.app = a
	defer a.Agent.Session().Close()
	for _, w := range a.Warnings {
		s.sink(events.Notice{Level: "warn", Text: w})
	}
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	a.StartExtensions(ctx, clientUI{s.conn})
	close(s.ready)
	select {
	case <-s.conn.Done():
	case <-s.quit:
	case <-ctx.Done():
	}
	s.stop()
	return nil
}

// stop cancels the turn and stops the extensions, once.
func (s *server) stop() {
	s.stopOnce.Do(func() {
		s.app.Agent.Cancel()
		s.app.Agent.Wait()
		s.app.StopExtensions()
	})
}

type server struct {
	conn     *rpc.Conn
	app      *app.App
	ready    chan struct{}
	quit     chan struct{}
	quitOnce sync.Once
	stopOnce sync.Once
}

// sink sends an event to the client as an "event" notification.
func (s *server) sink(e events.Event) {
	data, err := events.Marshal(e)
	if err != nil {
		return
	}
	_ = s.conn.Notify("event", json.RawMessage(data))
}

func (s *server) handle(ctx context.Context, method string, params json.RawMessage) (any, error) {
	select {
	case <-s.ready:
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	a := s.app
	text := func() (string, error) {
		var p TextParams
		if err := json.Unmarshal(params, &p); err != nil || p.Text == "" {
			return "", &rpc.Error{Code: rpc.CodeInvalidParams, Message: "text is required"}
		}
		return p.Text, nil
	}
	switch method {
	case "initialize":
		return InitializeResult{ProtocolVersion: ProtocolVersion, RhoVersion: a.Version, Session: a.SessionInfo(), Methods: Methods}, nil
	case "prompt":
		t, err := text()
		if err != nil {
			return nil, err
		}
		return struct{}{}, a.Agent.Prompt(a.BuildUserMessage(t))
	case "steer":
		t, err := text()
		if err != nil {
			return nil, err
		}
		return struct{}{}, a.Agent.Steer(t)
	case "follow_up":
		t, err := text()
		if err != nil {
			return nil, err
		}
		return struct{}{}, a.Agent.FollowUp(t)
	case "cancel":
		a.Agent.Cancel()
		return struct{}{}, nil
	case "wait":
		a.Agent.Wait()
		return headless.TurnResult(a, a.Agent.LastError()), nil
	case "session/info":
		return a.SessionInfo(), nil
	case "session/messages":
		return MessagesResult{Messages: a.SessionMessages()}, nil
	case "session/new":
		if err := a.Agent.SetSession(a.NewSession()); err != nil {
			return nil, err
		}
		return a.SessionInfo(), nil
	case "set_model":
		var p ModelParams
		if err := json.Unmarshal(params, &p); err != nil || p.Model == "" {
			return nil, &rpc.Error{Code: rpc.CodeInvalidParams, Message: "model is required"}
		}
		return struct{}{}, a.Agent.SetModel(p.Model)
	case "set_tools":
		var p ToolsParams
		if len(params) > 0 {
			if err := json.Unmarshal(params, &p); err != nil {
				return nil, &rpc.Error{Code: rpc.CodeInvalidParams, Message: "tools must be a list of names"}
			}
		}
		return struct{}{}, a.Agent.SetActiveTools(p.Tools)
	case "compact":
		return struct{}{}, a.Agent.Compact(ctx)
	case "shutdown":
		// Like LSP: stop, reply, and leave on exit or when stdin closes,
		// so the reply is always written.
		s.stop()
		return struct{}{}, nil
	case "exit":
		s.quitOnce.Do(func() { close(s.quit) })
		return nil, nil
	}
	return nil, &rpc.Error{Code: rpc.CodeMethodNotFound, Message: "unknown method " + method}
}

// clientUI forwards extensions' UI requests to the client. A client that
// does not answer them gets the headless defaults: no, or cancelled.
type clientUI struct{ conn *rpc.Conn }

func (u clientUI) Confirm(ctx context.Context, title, message string) (bool, error) {
	var r ext.ConfirmResult
	err := u.conn.Call(ctx, "ui/confirm", ext.ConfirmParams{Title: title, Message: message}, &r)
	return r.Confirmed && err == nil, nil
}

func (u clientUI) Select(ctx context.Context, title string, options []string) (string, error) {
	var r ext.ValueResult
	if err := u.conn.Call(ctx, "ui/select", ext.SelectParams{Title: title, Options: options}, &r); err != nil {
		return "", err
	}
	if r.Cancelled {
		return "", errors.New("cancelled")
	}
	return r.Value, nil
}

func (u clientUI) Input(ctx context.Context, title, placeholder string) (string, error) {
	var r ext.ValueResult
	if err := u.conn.Call(ctx, "ui/input", ext.InputParams{Title: title, Placeholder: placeholder}, &r); err != nil {
		return "", err
	}
	if r.Cancelled {
		return "", errors.New("cancelled")
	}
	return r.Value, nil
}

func (u clientUI) Notify(level, text string) {
	_ = u.conn.Notify("ui/notify", ext.NotifyParams{Level: level, Text: text})
}

func (u clientUI) SetStatus(key, text string) {
	_ = u.conn.Notify("ui/status", ext.StatusParams{Key: key, Text: text})
}
