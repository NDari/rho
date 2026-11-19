// Package xmltools gives tool calling to models without native tool calls.
// Tools are described in the prompt, the model calls them by writing XML
// blocks, and the blocks are parsed out of the streamed text into ordinary
// tool calls. Earlier calls and results are replayed in the same XML form.
// String values are written raw, without JSON escaping, which also makes
// code edits more reliable for some models that do support native tools.
package xmltools

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"strings"
	"unicode"

	"github.com/NDari/rho/core/provider"
)

// Options configure the wrapper.
type Options struct {
	// SystemInUser puts the system prompt at the start of the first user
	// message instead of a system message.
	SystemInUser bool
	// Decode is DecodeOff, DecodeAuto or DecodeAlways.
	Decode string
	// StopSequences asks the server to stop before an invented tool result.
	StopSequences bool
}

// Provider wraps another provider with XML tool calling.
type Provider struct {
	inner provider.Provider
	opts  Options
}

// Wrap returns inner with XML tool calling.
func Wrap(inner provider.Provider, o Options) *Provider {
	return &Provider{inner: inner, opts: o}
}

// Name implements provider.Provider.
func (p *Provider) Name() string { return p.inner.Name() }

// Stream implements provider.Provider.
func (p *Provider) Stream(ctx context.Context, req provider.Request, onEvent func(provider.StreamEvent)) (provider.Response, error) {
	schemas := map[string]*object{}
	for _, t := range req.Tools {
		schemas[t.Name] = schemaOf(t)
	}
	system := req.System
	if len(req.Tools) > 0 {
		system = strings.TrimSpace(strings.Join([]string{system, Instructions(req.Tools)}, "\n\n"))
	}
	inner := req
	inner.Tools = nil
	inner.Messages = rewriteMessages(req.Messages, req.SupportsImages)
	if p.opts.SystemInUser {
		inner.System = ""
		inner.Messages = prependToFirstUser(inner.Messages, system)
	} else {
		inner.System = system
	}
	if p.opts.StopSequences && len(req.Tools) > 0 {
		inner.Stop = append(append([]string(nil), req.Stop...), haltMarkers[0])
	}

	ictx, cancel := context.WithCancel(ctx)
	defer cancel()
	b := &builder{onEvent: onEvent, schemas: schemas, decode: p.opts.Decode}
	sp := &splitter{}
	halted := false
	resp, err := p.inner.Stream(ictx, inner, func(e provider.StreamEvent) {
		switch e.Kind {
		case provider.EventTextDelta:
			if halted {
				return
			}
			b.handle(sp.push(e.Text))
			if sp.halted {
				// The model began writing a tool result itself; everything
				// needed is parsed, so stop the request.
				halted = true
				cancel()
			}
		case provider.EventThinkingDelta, provider.EventStatus:
			onEvent(e)
		}
	})
	switch {
	case halted && ctx.Err() == nil:
		err = nil
	case err != nil && ctx.Err() == nil && !errors.Is(err, context.Canceled):
		return resp, err
	}
	b.handle(sp.finish())
	b.closeText()

	msg := resp.Message
	msg.Role = provider.RoleAssistant
	var content []provider.Block
	for _, blk := range resp.Message.Content {
		if blk.Type == provider.BlockThinking {
			content = append(content, blk)
		}
	}
	msg.Content = append(content, b.content...)
	stop := resp.StopReason
	switch {
	case ctx.Err() != nil:
		stop = provider.StopCancelled
		// Like the adapters, keep only what streamed on cancel.
	case len(msg.ToolCalls()) > 0:
		stop = provider.StopToolUse
	case stop == provider.StopToolUse || stop == provider.StopCancelled || stop == "":
		stop = provider.StopEnd
	}
	msg.Meta.StopReason = stop
	resp.Message = msg
	resp.StopReason = stop
	if ctx.Err() != nil {
		return resp, ctx.Err()
	}
	return resp, err
}

// builder assembles the response content from splitter events and streams
// the matching provider events.
type builder struct {
	onEvent func(provider.StreamEvent)
	schemas map[string]*object
	decode  string
	content []provider.Block
	text    *strings.Builder
	call    int // index in content of the open tool call, or -1
	calls   int
	open    bool
}

func (b *builder) handle(events []splitEvent) {
	for _, ev := range events {
		switch ev.kind {
		case splitText:
			b.appendText(ev.text)
		case splitToolStart:
			b.closeText()
			b.content = append(b.content, provider.Block{Type: provider.BlockToolCall, ID: "call_" + randomID(), Name: ev.name})
			b.call, b.open = len(b.content)-1, true
			b.onEvent(provider.StreamEvent{Kind: provider.EventToolCallStart, Index: b.calls, ID: b.content[b.call].ID, Name: ev.name})
		case splitToolDelta:
			if !b.open {
				continue
			}
			blk := &b.content[b.call]
			if blk.Name == "" {
				blk.Name = findNameInBody(ev.body)
			}
			b.onEvent(provider.StreamEvent{Kind: provider.EventToolCallDelta, Index: b.calls, Name: blk.Name, Text: ev.text})
		case splitToolEnd:
			b.finishCall(ev.name, ev.body, ev.complete)
		}
	}
}

func (b *builder) appendText(t string) {
	if b.text == nil {
		// Don't start a text block with whitespace alone (e.g. the newlines
		// between two tool calls).
		t = strings.TrimLeftFunc(t, unicode.IsSpace)
		if t == "" {
			return
		}
		b.text = &strings.Builder{}
	}
	b.text.WriteString(t)
	b.onEvent(provider.StreamEvent{Kind: provider.EventTextDelta, Text: t})
}

func (b *builder) closeText() {
	if b.text == nil {
		return
	}
	if t := strings.TrimRightFunc(b.text.String(), unicode.IsSpace); t != "" {
		b.content = append(b.content, provider.Text(t))
	}
	b.text = nil
}

func (b *builder) finishCall(name, body string, complete bool) {
	if !b.open {
		return
	}
	blk := &b.content[b.call]
	if name == "" {
		name = findNameInBody(body)
	}
	if name == "" {
		name = blk.Name
	}
	blk.Name = name
	args, perr := parseArguments(body, b.schemas[name], false, b.decode)
	js, err := marshal(args)
	if err != nil {
		js = []byte("{}")
	}
	blk.Args = js
	var problems []string
	if name == "" {
		problems = append(problems, `The tool call has no name. Write <tool_call name="TOOL_NAME">.`)
	}
	if !complete {
		problems = append(problems, "The tool call was cut off before </tool_call>.")
	}
	if perr != "" {
		problems = append(problems, perr)
	}
	if len(problems) > 0 {
		received := body
		if len(received) > 2000 {
			received = received[:2000]
		}
		blk.ArgsError = strings.Join(problems, " ") + " Received:\n" + received
	}
	b.open = false
	b.calls++
}

func randomID() string {
	var buf [8]byte
	_, _ = rand.Read(buf[:])
	return hex.EncodeToString(buf[:])
}
