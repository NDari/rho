package yaegihost

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/traefik/yaegi/interp"
	"github.com/traefik/yaegi/stdlib"

	"github.com/NDari/rho/core/ext"
	"github.com/NDari/rho/core/ext/rpc"
	"github.com/NDari/rho/core/provider"
)

type host struct {
	conn    *rpc.Conn
	ready   chan struct{}
	loadErr error
	calls   sync.Map // call id -> context.CancelFunc
	// notes runs notification hooks in order, off the connection's read
	// loop, so a handler may call back into Rho.
	notes chan func()
	mu    sync.Mutex
	init  ext.InitializeParams // what Rho sent in initialize
}

var (
	curMu   sync.Mutex
	curHost *host
)

func current() *host {
	curMu.Lock()
	defer curMu.Unlock()
	return curHost
}

// Main runs the host for the extension at path, speaking the protocol on
// stdin/stdout. Output written to os.Stdout by the extension is redirected
// to stderr so it can't corrupt the protocol.
func Main(path string) error {
	out := os.Stdout
	os.Stdout = os.Stderr
	return Serve(path, os.Stdin, out, os.Stderr)
}

// Serve runs the host on the given streams.
func Serve(path string, in io.Reader, out io.Writer, logw io.Writer) error {
	h := &host{ready: make(chan struct{})}
	curMu.Lock()
	curHost = h
	curMu.Unlock()
	reg.reset()
	h.notes = make(chan func(), 1024)
	h.conn = rpc.NewConn(in, out, h.handle)
	go func() {
		for {
			select {
			case f := <-h.notes:
				f()
			case <-h.conn.Done():
				return
			}
		}
	}()

	func() {
		defer func() {
			if r := recover(); r != nil {
				h.loadErr = fmt.Errorf("panic while loading: %v", r)
			}
		}()
		// The "rho" build tag lets extension files carry //go:build rho, so
		// go build and gopls skip them in a Go module while Rho runs them.
		opts := interp.Options{Stdout: logw, Stderr: logw, BuildTags: []string{"rho"}}
		// Yaegi loads packages from a GOPATH layout; build one, for a
		// single file too, since only package loading applies BuildTags.
		files := []string{path}
		if info, err := os.Stat(path); err == nil && info.IsDir() {
			if files, err = filepath.Glob(filepath.Join(path, "*.go")); err != nil {
				h.loadErr = err
				return
			}
		}
		gopath, err := stage(files)
		if err != nil {
			h.loadErr = err
			return
		}
		defer os.RemoveAll(gopath)
		opts.GoPath = gopath
		target := stagedPkg
		i := interp.New(opts)
		if err := i.Use(stdlib.Symbols); err != nil {
			h.loadErr = err
			return
		}
		if err := i.Use(Symbols); err != nil {
			h.loadErr = err
			return
		}
		if _, err := i.EvalPath(target); err != nil {
			h.loadErr = err
			return
		}
	}()
	close(h.ready)
	<-h.conn.Done()
	if errors.Is(h.conn.Err(), io.EOF) {
		return nil
	}
	return h.conn.Err()
}

func (h *host) handle(ctx context.Context, method string, params json.RawMessage) (any, error) {
	<-h.ready
	switch method {
	case "initialize":
		if h.loadErr != nil {
			return nil, h.loadErr
		}
		var p ext.InitializeParams
		_ = json.Unmarshal(params, &p)
		h.mu.Lock()
		h.init = p
		h.mu.Unlock()
		return h.initialize(), nil
	case "shutdown":
		return struct{}{}, nil
	case "tool/execute":
		var p ext.ToolExecuteParams
		if err := json.Unmarshal(params, &p); err != nil {
			return nil, err
		}
		return h.execute(ctx, p), nil
	case "tool/cancel":
		var p struct {
			CallID string `json:"call_id"`
		}
		_ = json.Unmarshal(params, &p)
		if c, ok := h.calls.Load(p.CallID); ok {
			c.(context.CancelFunc)()
		}
		return nil, nil
	case "command/run":
		var p ext.CommandRunParams
		if err := json.Unmarshal(params, &p); err != nil {
			return nil, err
		}
		return struct{}{}, h.runCommand(p)
	}
	if hook, ok := strings.CutPrefix(method, "hook/"); ok {
		var p ext.HookParams
		if err := json.Unmarshal(params, &p); err != nil {
			return nil, err
		}
		if hook == ext.HookMessageEnd || hook == ext.HookToolEnd {
			select {
			case h.notes <- func() { h.hook(hook, p) }:
			default: // the extension is not keeping up; drop it
			}
			return nil, nil
		}
		return h.hook(hook, p), nil
	}
	return nil, &rpc.Error{Code: rpc.CodeMethodNotFound, Message: "unknown method " + method}
}

func (h *host) initialize() ext.InitializeResult {
	reg.mu.Lock()
	defer reg.mu.Unlock()
	res := ext.InitializeResult{ProtocolVersion: ext.ProtocolVersion, Name: reg.name, Version: reg.version}
	for _, t := range reg.tools {
		params := []byte(`{"type":"object","properties":{}}`)
		if t.Parameters != nil {
			if b, err := json.Marshal(t.Parameters); err == nil {
				params = b
			}
		}
		res.Tools = append(res.Tools, ext.ToolInfo{Name: t.Name, Description: t.Description, Parameters: params, ReadOnly: t.ReadOnly})
	}
	for _, c := range reg.commands {
		res.Commands = append(res.Commands, ext.CommandInfo{Name: c.name, Description: c.desc})
	}
	for name := range reg.hooks {
		res.Hooks = append(res.Hooks, name)
	}
	return res
}

func safeCall(f func()) (err error) {
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("panic: %v", r)
		}
	}()
	f()
	return nil
}

func (h *host) execute(ctx context.Context, p ext.ToolExecuteParams) ext.ToolResult {
	reg.mu.Lock()
	var tool *Tool
	for i := range reg.tools {
		if reg.tools[i].Name == p.Name {
			tool = &reg.tools[i]
		}
	}
	reg.mu.Unlock()
	if tool == nil || tool.Execute == nil {
		return errResult("unknown tool " + p.Name)
	}
	var args map[string]any
	if len(p.Args) > 0 {
		if err := json.Unmarshal(p.Args, &args); err != nil {
			return errResult("invalid arguments: " + err.Error())
		}
	}
	if args == nil {
		args = map[string]any{}
	}
	cctx, cancel := context.WithCancel(ctx)
	defer cancel()
	h.calls.Store(p.CallID, cancel)
	defer h.calls.Delete(p.CallID)

	var out string
	var callErr error
	if err := safeCall(func() { out, callErr = tool.Execute(&ToolContext{CallID: p.CallID, ctx: cctx}, args) }); err != nil {
		return errResult(err.Error())
	}
	if callErr != nil {
		text := callErr.Error()
		if out != "" {
			text = out + "\n" + text
		}
		return errResult(text)
	}
	return ext.ToolResult{Content: []ext.ContentBlock{{Type: "text", Text: out}}}
}

func errResult(s string) ext.ToolResult {
	return ext.ToolResult{Content: []ext.ContentBlock{{Type: "text", Text: s}}, IsError: true}
}

func (h *host) runCommand(p ext.CommandRunParams) error {
	reg.mu.Lock()
	var run func(string) error
	for _, c := range reg.commands {
		if c.name == p.Name {
			run = c.run
		}
	}
	reg.mu.Unlock()
	if run == nil {
		return fmt.Errorf("unknown command %s", p.Name)
	}
	var err error
	if perr := safeCall(func() { err = run(p.Args) }); perr != nil {
		return perr
	}
	return err
}

func (h *host) hook(name string, p ext.HookParams) ext.HookResult {
	reg.mu.Lock()
	fns := append([]func(Event) Response(nil), reg.hooks[name]...)
	reg.mu.Unlock()
	ev := Event{Name: name, Tool: p.Tool, Transcript: p.Transcript, Text: p.Text, System: p.System, Aside: p.Aside, EntryID: p.EntryID, Tools: p.Tools}
	if p.Message != nil {
		if b, err := json.Marshal(p.Message); err == nil {
			_ = json.Unmarshal(b, &ev.Message)
		}
	}
	if len(p.Args) > 0 {
		_ = json.Unmarshal(p.Args, &ev.Args)
	}
	if len(p.Messages) > 0 {
		if b, err := json.Marshal(p.Messages); err == nil {
			_ = json.Unmarshal(b, &ev.Messages)
		}
	}
	if p.Result != nil {
		ev.IsError = p.Result.IsError
		var b strings.Builder
		for _, c := range p.Result.Content {
			b.WriteString(c.Text)
		}
		ev.Result = b.String()
	}
	var out ext.HookResult
	for _, fn := range fns {
		var r Response
		if err := safeCall(func() { r = fn(ev) }); err != nil {
			continue
		}
		if r.Block != "" {
			out.Block = r.Block
			return out
		}
		if r.Args != nil {
			if b, err := json.Marshal(r.Args); err == nil {
				out.Args = b
				ev.Args = r.Args
			}
		}
		if r.ReplaceResult {
			out.Result = &ext.ToolResult{Content: []ext.ContentBlock{{Type: "text", Text: r.Result}}, IsError: r.IsError}
			ev.Result, ev.IsError = r.Result, r.IsError
		}
		if r.Summary != "" {
			out.Summary = r.Summary
		}
		if r.Text != "" {
			out.Text, ev.Text = r.Text, r.Text
		}
		if r.System != "" {
			out.System, ev.System = r.System, r.System
		}
		if len(r.Tools) > 0 {
			out.Tools, ev.Tools = r.Tools, r.Tools
		}
		if r.Messages != nil {
			var msgs []provider.Message
			if b, err := json.Marshal(r.Messages); err == nil && json.Unmarshal(b, &msgs) == nil {
				out.Messages, ev.Messages = msgs, r.Messages
			}
		}
	}
	return out
}

const stagedPkg = "rhoextension"

// stage copies Go files (not tests) into a package in a temporary GOPATH.
func stage(files []string) (string, error) {
	gopath, err := os.MkdirTemp("", "rho-ext-*")
	if err != nil {
		return "", err
	}
	dst := filepath.Join(gopath, "src", stagedPkg)
	if err := os.MkdirAll(dst, 0o700); err != nil {
		return "", err
	}
	for _, f := range files {
		if strings.HasSuffix(f, "_test.go") {
			continue
		}
		data, err := os.ReadFile(f)
		if err != nil {
			return "", err
		}
		if err := os.WriteFile(filepath.Join(dst, filepath.Base(f)), data, 0o600); err != nil {
			return "", err
		}
	}
	return gopath, nil
}
