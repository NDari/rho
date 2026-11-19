// Package openaicompat implements the provider interface for the OpenAI
// Chat Completions API and the many servers that speak it (llama.cpp,
// Ollama, LM Studio, vLLM, OpenRouter, Gemini's compatibility endpoint).
package openaicompat

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"syscall"

	"github.com/openai/openai-go/v3"
	"github.com/openai/openai-go/v3/option"
	"github.com/openai/openai-go/v3/shared"

	"github.com/NDari/rho/core/provider"
)

// Flavors enable endpoint-specific behavior.
const (
	FlavorGemini = "gemini"
)

// geminiSkipSignature is Google's documented placeholder for tool calls that
// have no thought signature (e.g. produced by another model).
const geminiSkipSignature = "skip_thought_signature_validator"

// Options configures a Provider.
type Options struct {
	Name       string
	BaseURL    string
	APIKey     string
	Headers    map[string]string
	Flavor     string
	HTTPClient *http.Client
}

// Provider talks to an OpenAI-compatible Chat Completions endpoint.
type Provider struct {
	name   string
	flavor string
	openai bool
	client openai.Client
}

// New creates a provider.
func New(o Options) *Provider {
	opts := []option.RequestOption{
		option.WithBaseURL(o.BaseURL),
		option.WithMaxRetries(0), // the agent loop retries and reports progress
	}
	key := o.APIKey
	if key == "" {
		key = "none" // local servers usually ignore the key, but the header must be valid
	}
	opts = append(opts, option.WithAPIKey(key))
	for k, v := range o.Headers {
		opts = append(opts, option.WithHeader(k, v))
	}
	if o.HTTPClient != nil {
		opts = append(opts, option.WithHTTPClient(o.HTTPClient))
	}
	flavor := o.Flavor
	if flavor == "" && strings.Contains(o.BaseURL, "generativelanguage.googleapis.com") {
		flavor = FlavorGemini
	}
	return &Provider{
		name:   o.Name,
		flavor: flavor,
		openai: strings.Contains(o.BaseURL, "api.openai.com"),
		client: openai.NewClient(opts...),
	}
}

// Name implements provider.Provider.
func (p *Provider) Name() string { return p.name }

// Stream implements provider.Provider.
func (p *Provider) Stream(ctx context.Context, req provider.Request, onEvent func(provider.StreamEvent)) (provider.Response, error) {
	params, err := p.buildParams(req)
	if err != nil {
		return provider.Response{}, err
	}
	var resp provider.Response
	for attempt := 0; attempt < 2; attempt++ {
		var gotData bool
		resp, gotData, err = p.streamOnce(ctx, params, onEvent)
		// A reused keep-alive connection that the server already closed
		// fails before any data arrives; retry that once, silently.
		if err == nil || gotData || ctx.Err() != nil || !isConnReset(err) {
			break
		}
	}
	return resp, err
}

func isConnReset(err error) bool {
	if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) || errors.Is(err, syscall.ECONNRESET) {
		return true
	}
	s := err.Error()
	return strings.Contains(s, "connection reset") || strings.Contains(s, "server closed idle connection") || strings.HasSuffix(s, ": EOF")
}

func (p *Provider) streamOnce(ctx context.Context, params openai.ChatCompletionNewParams, onEvent func(provider.StreamEvent)) (provider.Response, bool, error) {
	stream := p.client.Chat.Completions.NewStreaming(ctx, params)
	defer stream.Close()

	acc := newAccumulator(p.name)
	gotData := false
	for stream.Next() {
		gotData = true
		acc.add(stream.Current(), onEvent)
	}
	resp := acc.response()
	resp.Message.Meta.Model = params.Model
	if err := stream.Err(); err != nil {
		if ctx.Err() != nil {
			resp.StopReason = provider.StopCancelled
			resp.Message.Meta.StopReason = provider.StopCancelled
			return resp, gotData, ctx.Err()
		}
		return resp, gotData, classify(err)
	}
	if ctx.Err() != nil {
		resp.StopReason = provider.StopCancelled
		resp.Message.Meta.StopReason = provider.StopCancelled
		return resp, gotData, ctx.Err()
	}
	return resp, gotData, nil
}

func (p *Provider) buildParams(req provider.Request) (openai.ChatCompletionNewParams, error) {
	msgs, err := p.convertMessages(req)
	if err != nil {
		return openai.ChatCompletionNewParams{}, err
	}
	params := openai.ChatCompletionNewParams{
		Model:         req.Model,
		Messages:      msgs,
		StreamOptions: openai.ChatCompletionStreamOptionsParam{IncludeUsage: openai.Bool(true)},
	}
	if req.MaxTokens > 0 {
		if p.openai {
			params.MaxCompletionTokens = openai.Int(int64(req.MaxTokens))
		} else {
			params.MaxTokens = openai.Int(int64(req.MaxTokens))
		}
	}
	if req.ReasoningEffort != "" {
		params.ReasoningEffort = shared.ReasoningEffort(req.ReasoningEffort)
	}
	for _, t := range req.Tools {
		var schema map[string]any
		if len(t.Parameters) > 0 {
			if err := json.Unmarshal(t.Parameters, &schema); err != nil {
				return params, fmt.Errorf("tool %s: invalid parameter schema: %w", t.Name, err)
			}
		}
		params.Tools = append(params.Tools, openai.ChatCompletionFunctionTool(shared.FunctionDefinitionParam{
			Name:        t.Name,
			Description: openai.String(t.Description),
			Parameters:  shared.FunctionParameters(schema),
		}))
	}
	return params, nil
}

func (p *Provider) convertMessages(req provider.Request) ([]openai.ChatCompletionMessageParamUnion, error) {
	var out []openai.ChatCompletionMessageParamUnion
	if req.System != "" {
		out = append(out, openai.SystemMessage(req.System))
	}
	// Chat Completions can't put images in tool messages, so images returned
	// by tools are collected and sent in a user message after the results.
	var pendingImages []provider.Block
	flushImages := func() {
		if len(pendingImages) == 0 {
			return
		}
		parts := []openai.ChatCompletionContentPartUnionParam{openai.TextContentPart("Images returned by the tool calls above:")}
		for _, img := range pendingImages {
			parts = append(parts, imagePart(img))
		}
		out = append(out, openai.UserMessage(parts))
		pendingImages = nil
	}

	for _, m := range req.Messages {
		if m.Role != provider.RoleToolResult {
			flushImages()
		}
		switch m.Role {
		case provider.RoleUser:
			out = append(out, p.userMessage(m, req.SupportsImages))
		case provider.RoleAssistant:
			if msg, ok := p.assistantMessage(m); ok {
				out = append(out, msg)
			}
		case provider.RoleToolResult:
			for _, b := range m.Content {
				if b.Type != provider.BlockToolResult {
					continue
				}
				var text strings.Builder
				for _, c := range b.Content {
					switch c.Type {
					case provider.BlockText:
						text.WriteString(c.Text)
					case provider.BlockImage:
						if req.SupportsImages {
							pendingImages = append(pendingImages, c)
							text.WriteString("[image attached below]")
						} else {
							text.WriteString("[image omitted: model does not support images]")
						}
					}
				}
				s := text.String()
				if s == "" {
					s = "(no output)"
				}
				out = append(out, openai.ToolMessage(s, b.CallID))
			}
		default:
			return nil, fmt.Errorf("unknown role %q", m.Role)
		}
	}
	flushImages()
	return out, nil
}

func (p *Provider) userMessage(m provider.Message, images bool) openai.ChatCompletionMessageParamUnion {
	hasImage := false
	for _, b := range m.Content {
		if b.Type == provider.BlockImage {
			hasImage = true
		}
	}
	if !hasImage {
		return openai.UserMessage(m.TextContent())
	}
	var parts []openai.ChatCompletionContentPartUnionParam
	for _, b := range m.Content {
		switch b.Type {
		case provider.BlockText:
			parts = append(parts, openai.TextContentPart(b.Text))
		case provider.BlockImage:
			if images {
				parts = append(parts, imagePart(b))
			} else {
				parts = append(parts, openai.TextContentPart("[image omitted: model does not support images]"))
			}
		}
	}
	return openai.UserMessage(parts)
}

func imagePart(b provider.Block) openai.ChatCompletionContentPartUnionParam {
	url := "data:" + b.MIME + ";base64," + base64.StdEncoding.EncodeToString(b.Data)
	return openai.ImageContentPart(openai.ChatCompletionContentPartImageImageURLParam{URL: url})
}

func (p *Provider) assistantMessage(m provider.Message) (openai.ChatCompletionMessageParamUnion, bool) {
	var asst openai.ChatCompletionAssistantMessageParam
	text := m.TextContent()
	if text != "" {
		asst.Content.OfString = openai.String(text)
	}
	for _, b := range m.Content {
		if b.Type != provider.BlockToolCall {
			continue
		}
		args := string(b.Args)
		if args == "" {
			args = "{}"
		}
		call := &openai.ChatCompletionMessageFunctionToolCallParam{
			ID: b.ID,
			Function: openai.ChatCompletionMessageFunctionToolCallFunctionParam{
				Name:      b.Name,
				Arguments: args,
			},
		}
		if extra := p.toolCallExtra(b); extra != nil {
			call.SetExtraFields(map[string]any{"extra_content": extra})
		}
		asst.ToolCalls = append(asst.ToolCalls, openai.ChatCompletionMessageToolCallUnionParam{OfFunction: call})
	}
	if text == "" && len(asst.ToolCalls) == 0 {
		return openai.ChatCompletionMessageParamUnion{}, false
	}
	return openai.ChatCompletionMessageParamUnion{OfAssistant: &asst}, true
}

// toolCallExtra returns the extra_content object to send with a tool call:
// the provider's own opaque data, or for Gemini a placeholder signature.
func (p *Provider) toolCallExtra(b provider.Block) any {
	if len(b.Opaque) > 0 && b.Provider == p.name {
		return b.Opaque
	}
	if p.flavor == FlavorGemini {
		return map[string]any{"google": map[string]any{"thought_signature": geminiSkipSignature}}
	}
	return nil
}

// accumulator assembles streamed chunks into a message.
type accumulator struct {
	provName string
	text     strings.Builder
	thinking strings.Builder
	calls    []*toolCall
	byIndex  map[int64]*toolCall
	usage    provider.Usage
	finish   string
}

type toolCall struct {
	id     string
	name   string
	args   strings.Builder
	opaque json.RawMessage
}

func newAccumulator(name string) *accumulator {
	return &accumulator{provName: name, byIndex: map[int64]*toolCall{}}
}

func (a *accumulator) add(chunk openai.ChatCompletionChunk, onEvent func(provider.StreamEvent)) {
	if chunk.Usage.PromptTokens > 0 || chunk.Usage.CompletionTokens > 0 {
		cached := int(chunk.Usage.PromptTokensDetails.CachedTokens)
		a.usage = provider.Usage{
			InputTokens:     int(chunk.Usage.PromptTokens),
			OutputTokens:    int(chunk.Usage.CompletionTokens),
			CacheReadTokens: cached,
		}
	}
	for _, ch := range chunk.Choices {
		d := ch.Delta
		if r := reasoningText(d); r != "" {
			a.thinking.WriteString(r)
			onEvent(provider.StreamEvent{Kind: provider.EventThinkingDelta, Text: r})
		}
		if d.Content != "" {
			a.text.WriteString(d.Content)
			onEvent(provider.StreamEvent{Kind: provider.EventTextDelta, Text: d.Content})
		}
		for _, tc := range d.ToolCalls {
			call, ok := a.byIndex[tc.Index]
			if !ok || (tc.ID != "" && call.id != "" && tc.ID != call.id) {
				call = &toolCall{id: tc.ID}
				a.byIndex[tc.Index] = call
				a.calls = append(a.calls, call)
				onEvent(provider.StreamEvent{Kind: provider.EventToolCallStart, Index: len(a.calls) - 1, ID: tc.ID, Name: tc.Function.Name})
			}
			if call.id == "" {
				call.id = tc.ID
			}
			if tc.Function.Name != "" && call.name == "" {
				call.name = tc.Function.Name
			}
			if tc.Function.Arguments != "" {
				call.args.WriteString(tc.Function.Arguments)
				onEvent(provider.StreamEvent{Kind: provider.EventToolCallDelta, Index: len(a.calls) - 1, Name: call.name, Text: tc.Function.Arguments})
			}
			if f, ok := tc.JSON.ExtraFields["extra_content"]; ok && f.Raw() != "" && f.Raw() != "null" {
				call.opaque = json.RawMessage(f.Raw())
			}
		}
		if ch.FinishReason != "" {
			a.finish = ch.FinishReason
		}
	}
}

// reasoningText extracts reasoning deltas, which servers report under
// different non-standard field names.
func reasoningText(d openai.ChatCompletionChunkChoiceDelta) string {
	for _, k := range []string{"reasoning_content", "reasoning"} {
		f, ok := d.JSON.ExtraFields[k]
		if !ok {
			continue
		}
		var s string
		if json.Unmarshal([]byte(f.Raw()), &s) == nil && s != "" {
			return s
		}
	}
	return ""
}

func (a *accumulator) response() provider.Response {
	msg := provider.Message{Role: provider.RoleAssistant, Meta: provider.Meta{Provider: a.provName}}
	if a.thinking.Len() > 0 {
		msg.Content = append(msg.Content, provider.Block{Type: provider.BlockThinking, Text: a.thinking.String(), Provider: a.provName})
	}
	if a.text.Len() > 0 {
		msg.Content = append(msg.Content, provider.Text(a.text.String()))
	}
	for _, c := range a.calls {
		if c.name == "" {
			continue
		}
		id := c.id
		if id == "" {
			id = "call_" + randomID()
		}
		args := json.RawMessage(c.args.String())
		if len(args) == 0 {
			args = json.RawMessage("{}")
		} else if !json.Valid(args) {
			// Keep the malformed text so the tool can report it to the model.
			quoted, _ := json.Marshal(c.args.String())
			args = quoted
		}
		msg.Content = append(msg.Content, provider.Block{
			Type: provider.BlockToolCall, ID: id, Name: c.name, Args: args,
			Provider: a.provName, Opaque: c.opaque,
		})
	}
	stop := provider.StopEnd
	switch {
	case len(msg.ToolCalls()) > 0:
		stop = provider.StopToolUse
	case a.finish == "length":
		stop = provider.StopMaxTokens
	}
	usage := a.usage
	msg.Meta.Usage = &usage
	msg.Meta.StopReason = stop
	return provider.Response{Message: msg, Usage: usage, StopReason: stop}
}

func randomID() string {
	var b [8]byte
	_, _ = rand.Read(b[:])
	return hex.EncodeToString(b[:])
}

// classify converts SDK and network errors into provider errors.
func classify(err error) error {
	var apiErr *openai.Error
	if errors.As(err, &apiErr) {
		msg := apiErr.Message
		if msg == "" {
			msg = apiErr.RawJSON()
		}
		if msg == "" {
			msg = http.StatusText(apiErr.StatusCode)
		}
		kind := provider.ErrOther
		switch {
		case provider.LooksLikeContextOverflow(msg + " " + apiErr.Code + " " + apiErr.Type):
			kind = provider.ErrContextOverflow
		case apiErr.StatusCode == http.StatusTooManyRequests || apiErr.StatusCode >= 500:
			kind = provider.ErrTransient
		case apiErr.StatusCode == http.StatusUnauthorized || apiErr.StatusCode == http.StatusForbidden:
			kind = provider.ErrAuth
		}
		return &provider.Error{Kind: kind, StatusCode: apiErr.StatusCode, Message: msg, Err: err}
	}
	var netErr net.Error
	if errors.As(err, &netErr) || errors.Is(err, io.ErrUnexpectedEOF) || errors.Is(err, io.EOF) {
		return &provider.Error{Kind: provider.ErrTransient, Err: err}
	}
	if provider.LooksLikeContextOverflow(err.Error()) {
		return &provider.Error{Kind: provider.ErrContextOverflow, Err: err}
	}
	return &provider.Error{Kind: provider.ErrOther, Err: err}
}
