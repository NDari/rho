// Package fake provides a scripted provider for tests.
package fake

import (
	"context"
	"encoding/json"
	"errors"
	"sync"

	"github.com/NDari/rho/core/provider"
)

// Step is one scripted response. Exactly one of the fields is used:
// Err returns an error; otherwise Text and Calls form the reply.
type Step struct {
	Text     string
	Thinking string
	Calls    []Call
	Err      error
	Usage    provider.Usage
	// Stop, if set, replaces the stop reason.
	Stop provider.StopReason
	// Wait blocks until the context is cancelled (for cancel tests).
	Wait bool
	// Check, if set, inspects the request.
	Check func(provider.Request)
}

// Call is a scripted tool call.
type Call struct {
	ID   string
	Name string
	Args any
}

// Provider replays steps in order.
type Provider struct {
	mu       sync.Mutex
	name     string
	steps    []Step
	Requests []provider.Request
}

// New creates a fake provider.
func New(name string, steps ...Step) *Provider {
	return &Provider{name: name, steps: steps}
}

// Add appends more steps.
func (p *Provider) Add(steps ...Step) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.steps = append(p.steps, steps...)
}

// Name implements provider.Provider.
func (p *Provider) Name() string { return p.name }

// ErrExhausted is returned when no steps remain.
var ErrExhausted = errors.New("fake provider: no more scripted steps")

// Stream implements provider.Provider.
func (p *Provider) Stream(ctx context.Context, req provider.Request, onEvent func(provider.StreamEvent)) (provider.Response, error) {
	p.mu.Lock()
	p.Requests = append(p.Requests, req)
	if len(p.steps) == 0 {
		p.mu.Unlock()
		return provider.Response{}, ErrExhausted
	}
	s := p.steps[0]
	p.steps = p.steps[1:]
	p.mu.Unlock()

	if s.Check != nil {
		s.Check(req)
	}
	if s.Err != nil {
		return provider.Response{}, s.Err
	}
	msg := provider.Message{Role: provider.RoleAssistant, Meta: provider.Meta{Provider: p.name, Model: req.Model}}
	if s.Thinking != "" {
		onEvent(provider.StreamEvent{Kind: provider.EventThinkingDelta, Text: s.Thinking})
		msg.Content = append(msg.Content, provider.Block{Type: provider.BlockThinking, Text: s.Thinking, Provider: p.name})
	}
	if s.Text != "" {
		onEvent(provider.StreamEvent{Kind: provider.EventTextDelta, Text: s.Text})
		msg.Content = append(msg.Content, provider.Text(s.Text))
	}
	if s.Wait {
		<-ctx.Done()
		msg.Meta.StopReason = provider.StopCancelled
		return provider.Response{Message: msg, StopReason: provider.StopCancelled}, ctx.Err()
	}
	for i, c := range s.Calls {
		args, _ := json.Marshal(c.Args)
		id := c.ID
		if id == "" {
			id = "call_" + string(rune('a'+i))
		}
		onEvent(provider.StreamEvent{Kind: provider.EventToolCallStart, Index: i, ID: id, Name: c.Name})
		msg.Content = append(msg.Content, provider.Block{Type: provider.BlockToolCall, ID: id, Name: c.Name, Args: args, Provider: p.name})
	}
	stop := provider.StopEnd
	if len(s.Calls) > 0 {
		stop = provider.StopToolUse
	}
	if s.Stop != "" {
		stop = s.Stop
	}
	usage := s.Usage
	msg.Meta.Usage = &usage
	msg.Meta.StopReason = stop
	return provider.Response{Message: msg, Usage: usage, StopReason: stop}, nil
}
