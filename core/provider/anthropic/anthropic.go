// Package anthropic implements the provider interface for the Anthropic
// Messages API, and for servers that speak it (llama.cpp's llama-server
// serves it at /v1/messages too).
package anthropic

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"strings"
	"time"

	sdk "github.com/anthropics/anthropic-sdk-go/v2"
	"github.com/anthropics/anthropic-sdk-go/v2/option"

	"github.com/NDari/rho/core/provider"
	"github.com/NDari/rho/core/provider/streamutil"
)

// Options configures a Provider.
type Options struct {
	Name string
	// BaseURL is the API root, without /v1 (a trailing /v1 is removed).
	BaseURL    string
	APIKey     string
	Headers    map[string]string
	HTTPClient *http.Client
	// DebugLog, if set, is a file that receives one JSON line per request.
	DebugLog string
	// IdleTimeout ends a request whose stream sends no data for this long
	// (as a transient error, so it is retried). 0 means
	// streamutil.DefaultIdleTimeout; negative disables the check.
	IdleTimeout time.Duration
}

// defaultMaxTokens is sent when the request gives no output limit; the
// API requires one.
const defaultMaxTokens = 16384

// Provider talks to an Anthropic Messages endpoint.
type Provider struct {
	name   string
	client sdk.Client
	log    *streamutil.DebugLog
	idle   time.Duration
}

// New creates a provider.
func New(o Options) *Provider {
	base := strings.TrimSuffix(strings.TrimSuffix(o.BaseURL, "/"), "/v1")
	opts := []option.RequestOption{
		// Use only the configured key: never a key, token or sign-in the
		// SDK would otherwise find in the environment.
		option.WithoutEnvironmentDefaults(),
		option.WithBaseURL(base),
		option.WithMaxRetries(0), // the agent loop retries and reports progress
	}
	key := o.APIKey
	if key == "" {
		key = "none" // local servers usually ignore the key
	}
	opts = append(opts, option.WithAPIKey(key))
	for k, v := range o.Headers {
		opts = append(opts, option.WithHeader(k, v))
	}
	if o.HTTPClient != nil {
		opts = append(opts, option.WithHTTPClient(o.HTTPClient))
	}
	log := streamutil.NewDebugLog(o.DebugLog)
	if log != nil {
		opts = append(opts, option.WithMiddleware(func(req *http.Request, next option.MiddlewareNext) (*http.Response, error) {
			return log.Middleware(req, next)
		}))
	}
	idle := o.IdleTimeout
	if idle == 0 {
		idle = streamutil.DefaultIdleTimeout
	}
	return &Provider{name: o.Name, client: sdk.NewClient(opts...), log: log, idle: idle}
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
	ctx, capture := p.log.Start(ctx)
	for attempt := 0; attempt < 2; attempt++ {
		var gotData bool
		resp, gotData, err = p.streamOnce(ctx, params, onEvent)
		// A reused keep-alive connection that the server already closed
		// fails before any data arrives; retry that once, silently.
		if err == nil || gotData || ctx.Err() != nil || !streamutil.IsConnReset(err) {
			break
		}
	}
	p.log.Write(p.name, capture, resp, err)
	return resp, err
}

func (p *Provider) streamOnce(ctx context.Context, params sdk.BetaMessageNewParams, onEvent func(provider.StreamEvent)) (provider.Response, bool, error) {
	sctx, dog, stop := streamutil.Watch(ctx, p.idle)
	defer stop()
	stream := p.client.Beta.Messages.NewStreaming(sctx, params)
	defer stream.Close()

	acc := &accumulator{prov: p.name}
	gotData := false
	for stream.Next() {
		dog.Kick()
		gotData = true
		acc.add(stream.Current(), onEvent)
	}
	resp := acc.response()
	resp.Message.Meta.Model = params.Model
	err := stream.Err()
	switch {
	case ctx.Err() != nil:
		resp.StopReason = provider.StopCancelled
		resp.Message.Meta.StopReason = provider.StopCancelled
		return resp, gotData, ctx.Err()
	case streamutil.Stalled(sctx):
		return resp, gotData, streamutil.StalledError(p.idle)
	case err != nil:
		return resp, gotData, classify(err)
	case !acc.done:
		return resp, gotData, streamutil.ErrTruncated()
	case acc.stop == sdk.BetaStopReasonRefusal:
		return resp, gotData, refusal(acc.refusal)
	}
	return resp, gotData, nil
}

// refusal reports a response the model declined to give.
func refusal(d sdk.BetaRefusalStopDetails) error {
	msg := "the model declined to answer"
	if d.Category != "" {
		msg += " (" + string(d.Category) + ")"
	}
	if d.Explanation != "" {
		msg += ": " + d.Explanation
	}
	return &provider.Error{Kind: provider.ErrOther, Message: msg}
}

// The beta that lets a request drop thinking blocks whose conversation
// changed (after compaction, or a new system prompt) instead of failing.
const bindingBeta = sdk.AnthropicBetaThinkingBindingControls2026_08_01

func (p *Provider) buildParams(req provider.Request) (sdk.BetaMessageNewParams, error) {
	msgs, err := p.convertMessages(req)
	if err != nil {
		return sdk.BetaMessageNewParams{}, err
	}
	maxTokens := req.MaxTokens
	if maxTokens <= 0 {
		maxTokens = defaultMaxTokens
	}
	params := sdk.BetaMessageNewParams{
		Model:     req.Model,
		MaxTokens: int64(maxTokens),
		Messages:  msgs,
		// Cache everything up to the newest message; with the breakpoint
		// on the system prompt below, the tools and system prompt stay
		// cached even when the conversation changes.
		CacheControl:  sdk.NewBetaCacheControlEphemeralParam(),
		StopSequences: req.Stop,
	}
	if req.System != "" {
		params.System = []sdk.BetaTextBlockParam{{Text: req.System, CacheControl: sdk.NewBetaCacheControlEphemeralParam()}}
	}
	if req.Thinking {
		params.Thinking = sdk.BetaThinkingConfigParamUnion{OfAdaptive: &sdk.BetaThinkingConfigAdaptiveParam{
			Display:      sdk.BetaThinkingConfigAdaptiveDisplaySummarized,
			BlockBinding: sdk.BetaThinkingBlockBindingParam{PrefixMismatchBehavior: sdk.BetaThinkingPrefixMismatchBehaviorDropBlock},
		}}
		params.Betas = append(params.Betas, bindingBeta)
	}
	if req.ReasoningEffort != "" {
		params.OutputConfig.Effort = sdk.BetaOutputConfigEffort(req.ReasoningEffort)
	}
	for _, t := range req.Tools {
		tool, err := toolParam(t)
		if err != nil {
			return params, err
		}
		params.Tools = append(params.Tools, sdk.BetaToolUnionParam{OfTool: &tool})
	}
	return params, nil
}

// toolParam converts a tool's JSON schema.
func toolParam(t provider.ToolSpec) (sdk.BetaToolParam, error) {
	schema := map[string]any{}
	if len(t.Parameters) > 0 {
		if err := json.Unmarshal(t.Parameters, &schema); err != nil {
			return sdk.BetaToolParam{}, fmt.Errorf("tool %s: invalid parameter schema: %w", t.Name, err)
		}
	}
	in := sdk.BetaToolInputSchemaParam{Properties: schema["properties"]}
	if req, ok := schema["required"].([]any); ok {
		for _, r := range req {
			if s, ok := r.(string); ok {
				in.Required = append(in.Required, s)
			}
		}
	}
	delete(schema, "properties")
	delete(schema, "required")
	delete(schema, "type")
	if len(schema) > 0 {
		in.ExtraFields = schema
	}
	return sdk.BetaToolParam{Name: t.Name, Description: sdk.String(t.Description), InputSchema: in}, nil
}

// convertMessages turns the conversation into Messages API turns. Tool
// results are user content there, so a tool result message and the user
// message after it become one turn.
func (p *Provider) convertMessages(req provider.Request) ([]sdk.BetaMessageParam, error) {
	type turn struct {
		user   bool
		blocks []sdk.BetaContentBlockParamUnion
	}
	var turns []turn
	add := func(user bool, blocks []sdk.BetaContentBlockParamUnion) {
		if len(blocks) == 0 {
			return
		}
		if n := len(turns); n > 0 && turns[n-1].user == user {
			turns[n-1].blocks = append(turns[n-1].blocks, blocks...)
			return
		}
		turns = append(turns, turn{user, blocks})
	}
	for _, m := range req.Messages {
		switch m.Role {
		case provider.RoleUser:
			add(true, userBlocks(m.Content, req.SupportsImages))
		case provider.RoleToolResult:
			var blocks []sdk.BetaContentBlockParamUnion
			for _, b := range m.Content {
				if b.Type == provider.BlockToolResult {
					blocks = append(blocks, toolResult(b, req.SupportsImages))
				}
			}
			add(true, blocks)
		case provider.RoleAssistant:
			add(false, p.assistantBlocks(m))
		default:
			return nil, fmt.Errorf("unknown role %q", m.Role)
		}
	}
	out := make([]sdk.BetaMessageParam, len(turns))
	for i, t := range turns {
		if t.user {
			out[i] = sdk.NewBetaUserMessage(t.blocks...)
		} else {
			out[i] = sdk.BetaMessageParam{OfAssistant: &sdk.BetaAssistantMessageParam{Content: t.blocks}}
		}
	}
	return out, nil
}

const imageOmitted = "[image omitted: model does not support images]"

func userBlocks(content []provider.Block, images bool) []sdk.BetaContentBlockParamUnion {
	var out []sdk.BetaContentBlockParamUnion
	for _, b := range content {
		switch b.Type {
		case provider.BlockText:
			if b.Text != "" {
				out = append(out, sdk.NewBetaTextBlock(b.Text))
			}
		case provider.BlockImage:
			if images {
				out = append(out, sdk.BetaContentBlockParamUnion{OfImage: imageParam(b)})
			} else {
				out = append(out, sdk.NewBetaTextBlock(imageOmitted))
			}
		}
	}
	return out
}

func imageParam(b provider.Block) *sdk.BetaImageBlockParam {
	return &sdk.BetaImageBlockParam{Source: sdk.BetaImageBlockParamSourceUnion{OfBase64: &sdk.BetaBase64ImageSourceParam{
		Data:      base64.StdEncoding.EncodeToString(b.Data),
		MediaType: sdk.BetaBase64ImageSourceMediaType(b.MIME),
	}}}
}

func toolResult(b provider.Block, images bool) sdk.BetaContentBlockParamUnion {
	r := &sdk.BetaToolResultBlockParam{ToolUseID: b.CallID}
	if b.IsError {
		r.IsError = sdk.Bool(true)
	}
	for _, c := range b.Content {
		switch c.Type {
		case provider.BlockText:
			if c.Text != "" {
				r.Content = append(r.Content, sdk.BetaToolResultBlockParamContentUnion{OfText: &sdk.BetaTextBlockParam{Text: c.Text}})
			}
		case provider.BlockImage:
			if images {
				r.Content = append(r.Content, sdk.BetaToolResultBlockParamContentUnion{OfImage: imageParam(c)})
			} else {
				r.Content = append(r.Content, sdk.BetaToolResultBlockParamContentUnion{OfText: &sdk.BetaTextBlockParam{Text: imageOmitted}})
			}
		}
	}
	if len(r.Content) == 0 {
		r.Content = append(r.Content, sdk.BetaToolResultBlockParamContentUnion{OfText: &sdk.BetaTextBlockParam{Text: "(no output)"}})
	}
	return sdk.BetaContentBlockParamUnion{OfToolResult: r}
}

// opaque is the data a thinking block keeps for the API: its signature,
// or the encrypted data of a redacted block.
type opaque struct {
	Signature string `json:"signature,omitempty"`
	Redacted  string `json:"redacted,omitempty"`
}

// assistantBlocks returns a reply as it was received. Thinking goes back
// only to the provider that produced it, with its signature; the API
// drops blocks the model can't use.
func (p *Provider) assistantBlocks(m provider.Message) []sdk.BetaContentBlockParamUnion {
	var out []sdk.BetaContentBlockParamUnion
	for _, b := range m.Content {
		switch b.Type {
		case provider.BlockThinking:
			if b.Provider != p.name || len(b.Opaque) == 0 {
				continue
			}
			var o opaque
			if json.Unmarshal(b.Opaque, &o) != nil {
				continue
			}
			switch {
			case o.Redacted != "":
				out = append(out, sdk.NewBetaRedactedThinkingBlock(o.Redacted))
			case o.Signature != "":
				out = append(out, sdk.NewBetaThinkingBlock(o.Signature, b.Text))
			}
		case provider.BlockText:
			if b.Text != "" {
				out = append(out, sdk.NewBetaTextBlock(b.Text))
			}
		case provider.BlockToolCall:
			var input any = b.Args
			if !json.Valid(b.Args) || !strings.HasPrefix(strings.TrimSpace(string(b.Args)), "{") {
				input = map[string]any{} // arguments that did not parse were answered with an error
			}
			out = append(out, sdk.NewBetaToolUseBlock(b.ID, input, b.Name))
		}
	}
	return out
}

// accumulator assembles streamed events into a message.
type accumulator struct {
	prov    string
	blocks  []*block
	calls   int
	usage   provider.Usage
	stop    sdk.BetaStopReason
	refusal sdk.BetaRefusalStopDetails
	done    bool
}

type block struct {
	typ       string
	text      strings.Builder
	signature string
	data      string
	id, name  string
	input     strings.Builder
	call      int // index among the tool calls
}

func (a *accumulator) add(ev sdk.BetaRawMessageStreamEventUnion, onEvent func(provider.StreamEvent)) {
	switch ev.Type {
	case "message_start":
		u := ev.Message.Usage
		a.usage.InputTokens = int(u.InputTokens + u.CacheReadInputTokens + u.CacheCreationInputTokens)
		a.usage.CacheReadTokens = int(u.CacheReadInputTokens)
		a.usage.CacheWriteTokens = int(u.CacheCreationInputTokens)
		a.usage.OutputTokens = int(u.OutputTokens)
	case "content_block_start":
		cb := ev.ContentBlock
		b := &block{typ: cb.Type, id: cb.ID, name: cb.Name, data: cb.Data, signature: cb.Signature}
		b.text.WriteString(cb.Text)
		b.text.WriteString(cb.Thinking)
		if cb.Type == "tool_use" {
			b.call = a.calls
			a.calls++
			onEvent(provider.StreamEvent{Kind: provider.EventToolCallStart, Index: b.call, ID: cb.ID, Name: cb.Name})
		}
		a.blocks = append(a.blocks, b)
	case "content_block_delta":
		if ev.Index < 0 || int(ev.Index) >= len(a.blocks) {
			return
		}
		b := a.blocks[ev.Index]
		d := ev.Delta
		switch d.Type {
		case "text_delta":
			b.text.WriteString(d.Text)
			onEvent(provider.StreamEvent{Kind: provider.EventTextDelta, Text: d.Text})
		case "thinking_delta":
			b.text.WriteString(d.Thinking)
			onEvent(provider.StreamEvent{Kind: provider.EventThinkingDelta, Text: d.Thinking})
		case "signature_delta":
			b.signature += d.Signature
		case "input_json_delta":
			b.input.WriteString(d.PartialJSON)
			onEvent(provider.StreamEvent{Kind: provider.EventToolCallDelta, Index: b.call, Name: b.name, Text: d.PartialJSON})
		}
	case "message_delta":
		a.stop = ev.Delta.StopReason
		a.refusal = ev.Delta.StopDetails
		u := ev.Usage
		a.usage.OutputTokens = int(u.OutputTokens)
		if u.JSON.InputTokens.Valid() {
			a.usage.InputTokens = int(u.InputTokens + u.CacheReadInputTokens + u.CacheCreationInputTokens)
			a.usage.CacheReadTokens = int(u.CacheReadInputTokens)
			a.usage.CacheWriteTokens = int(u.CacheCreationInputTokens)
		}
	case "message_stop":
		a.done = true
	}
}

func (a *accumulator) response() provider.Response {
	msg := provider.Message{Role: provider.RoleAssistant, Meta: provider.Meta{Provider: a.prov}}
	for _, b := range a.blocks {
		switch b.typ {
		case "thinking", "redacted_thinking":
			o := opaque{Signature: b.signature, Redacted: b.data}
			blk := provider.Block{Type: provider.BlockThinking, Text: b.text.String(), Provider: a.prov}
			if o != (opaque{}) {
				blk.Opaque, _ = json.Marshal(o)
			}
			if blk.Text != "" || blk.Opaque != nil {
				msg.Content = append(msg.Content, blk)
			}
		case "text":
			if b.text.Len() > 0 {
				msg.Content = append(msg.Content, provider.Text(b.text.String()))
			}
		case "tool_use":
			args := json.RawMessage(b.input.String())
			if len(args) == 0 {
				args = json.RawMessage("{}")
			} else if !json.Valid(args) {
				// Keep the malformed text so the tool can report it to the model.
				args, _ = json.Marshal(b.input.String())
			}
			msg.Content = append(msg.Content, provider.Block{Type: provider.BlockToolCall, ID: b.id, Name: b.name, Args: args, Provider: a.prov})
		}
	}
	stop := provider.StopEnd
	switch {
	case len(msg.ToolCalls()) > 0:
		stop = provider.StopToolUse
	case a.stop == sdk.BetaStopReasonMaxTokens:
		stop = provider.StopMaxTokens
	}
	usage := a.usage
	msg.Meta.Usage = &usage
	msg.Meta.StopReason = stop
	return provider.Response{Message: msg, Usage: usage, StopReason: stop}
}

// classify converts SDK and network errors into provider errors.
func classify(err error) error {
	var apiErr *sdk.Error
	if errors.As(err, &apiErr) {
		msg := apiErr.Message
		if msg == "" {
			msg = http.StatusText(apiErr.StatusCode)
		}
		status, typ := apiErr.StatusCode, string(apiErr.Type())
		if status == http.StatusOK {
			// An error event inside the stream; its type says what happened.
			status = 0
			if typ == "api_error" {
				status = http.StatusInternalServerError
			}
		}
		pe := streamutil.ClassifyFailure(msg, "", typ, status)
		pe.Err = err
		if pe.Kind == provider.ErrTransient && apiErr.Response != nil && apiErr.StatusCode != http.StatusOK {
			pe.RetryAfter = streamutil.ParseRetryAfter(apiErr.Response.Header, time.Now())
		}
		return pe
	}
	var netErr net.Error
	if errors.As(err, &netErr) || streamutil.IsConnReset(err) {
		return &provider.Error{Kind: provider.ErrTransient, Err: err}
	}
	if provider.LooksLikeContextOverflow(err.Error()) {
		return &provider.Error{Kind: provider.ErrContextOverflow, Err: err}
	}
	return &provider.Error{Kind: provider.ErrOther, Err: err}
}
