package openaicompat

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"strings"
	"time"

	"github.com/openai/openai-go/v3"
	"github.com/openai/openai-go/v3/option"
	"github.com/openai/openai-go/v3/responses"

	"github.com/NDari/rho/core/provider"
	"github.com/NDari/rho/core/provider/streamutil"
)

// ResponsesProvider talks to an endpoint implementing the OpenAI Responses
// API (OpenAI itself, and some local servers). Requests are stateless
// (store=false); encrypted reasoning is carried in the session instead.
type ResponsesProvider struct {
	name   string
	client openai.Client
	log    *streamutil.DebugLog
	idle   time.Duration
}

// NewResponses creates a Responses API provider.
func NewResponses(o Options) *ResponsesProvider {
	p := New(o)
	return &ResponsesProvider{name: o.Name, client: p.client, log: p.log, idle: p.idle}
}

// Name implements provider.Provider.
func (p *ResponsesProvider) Name() string { return p.name }

// ListModels implements provider.ModelLister.
func (p *ResponsesProvider) ListModels(ctx context.Context) ([]provider.ServerModel, error) {
	return listModels(ctx, p.client)
}

// Stream implements provider.Provider.
func (p *ResponsesProvider) Stream(ctx context.Context, req provider.Request, onEvent func(provider.StreamEvent)) (provider.Response, error) {
	var resp provider.Response
	var err error
	ctx, capture := p.log.Start(ctx)
	for attempt := 0; attempt < 2; attempt++ {
		var gotData bool
		resp, gotData, err = p.streamOnce(ctx, req, onEvent)
		if err == nil || gotData || ctx.Err() != nil || !streamutil.IsConnReset(err) {
			break
		}
	}
	p.log.Write(p.name, capture, resp, err)
	return resp, err
}

func (p *ResponsesProvider) streamOnce(ctx context.Context, req provider.Request, onEvent func(provider.StreamEvent)) (provider.Response, bool, error) {
	params := responses.ResponseNewParams{
		Model: req.Model,
		Store: openai.Bool(false),
	}
	if req.System != "" {
		params.Instructions = openai.String(req.System)
	}
	if req.MaxTokens > 0 {
		params.MaxOutputTokens = openai.Int(int64(req.MaxTokens))
	}
	opts := []option.RequestOption{
		option.WithJSONSet("input", p.inputItems(req)),
		option.WithJSONSet("include", []string{"reasoning.encrypted_content"}),
	}
	if len(req.Tools) > 0 {
		var ts []map[string]any
		for _, t := range req.Tools {
			var schema any
			_ = json.Unmarshal(t.Parameters, &schema)
			ts = append(ts, map[string]any{"type": "function", "name": t.Name, "description": t.Description, "parameters": schema})
		}
		opts = append(opts, option.WithJSONSet("tools", ts))
	}
	reasoning := map[string]any{"summary": "auto"}
	if req.ReasoningEffort != "" {
		reasoning["effort"] = req.ReasoningEffort
	}
	opts = append(opts, option.WithJSONSet("reasoning", reasoning))

	sctx, dog, stop := streamutil.Watch(ctx, p.idle)
	defer stop()
	stream := p.client.Responses.NewStreaming(sctx, params, opts...)
	defer stream.Close()

	acc := &responsesAcc{prov: p.name}
	gotData := false
	for stream.Next() {
		dog.Kick()
		gotData = true
		acc.add(stream.Current().RawJSON(), onEvent)
	}
	resp := acc.response(req.Model)
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
	case acc.failed:
		return resp, gotData, streamutil.ClassifyFailure(acc.failure, acc.failCode, acc.failType, 0)
	case acc.final == nil:
		return resp, gotData, streamutil.ErrTruncated()
	}
	return resp, gotData, nil
}

// inputItems converts the conversation to Responses API input items.
func (p *ResponsesProvider) inputItems(req provider.Request) []any {
	var items []any
	for _, m := range req.Messages {
		switch m.Role {
		case provider.RoleUser:
			var content []map[string]any
			for _, b := range m.Content {
				switch b.Type {
				case provider.BlockText:
					content = append(content, map[string]any{"type": "input_text", "text": b.Text})
				case provider.BlockImage:
					if req.SupportsImages {
						content = append(content, map[string]any{"type": "input_image", "image_url": "data:" + b.MIME + ";base64," + base64.StdEncoding.EncodeToString(b.Data)})
					} else {
						content = append(content, map[string]any{"type": "input_text", "text": "[image omitted: model does not support images]"})
					}
				}
			}
			items = append(items, map[string]any{"type": "message", "role": "user", "content": content})
		case provider.RoleAssistant:
			for _, b := range m.Content {
				switch b.Type {
				case provider.BlockThinking:
					// Only encrypted reasoning from this provider can be replayed.
					if b.Provider == p.name && len(b.Opaque) > 0 {
						items = append(items, b.Opaque)
					}
				case provider.BlockText:
					if b.Text != "" {
						items = append(items, map[string]any{"type": "message", "role": "assistant", "content": []map[string]any{{"type": "output_text", "text": b.Text}}})
					}
				case provider.BlockToolCall:
					args := string(b.Args)
					if args == "" {
						args = "{}"
					}
					items = append(items, map[string]any{"type": "function_call", "call_id": b.ID, "name": b.Name, "arguments": args})
				}
			}
		case provider.RoleToolResult:
			var images []map[string]any
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
							text.WriteString("[image attached below]")
							images = append(images, map[string]any{"type": "input_image", "image_url": "data:" + c.MIME + ";base64," + base64.StdEncoding.EncodeToString(c.Data)})
						}
					}
				}
				out := text.String()
				if out == "" {
					out = "(no output)"
				}
				items = append(items, map[string]any{"type": "function_call_output", "call_id": b.CallID, "output": out})
			}
			if len(images) > 0 {
				content := append([]map[string]any{{"type": "input_text", "text": "Images returned by the tool call above:"}}, images...)
				items = append(items, map[string]any{"type": "message", "role": "user", "content": content})
			}
		}
	}
	return items
}

type responsesAcc struct {
	prov     string
	text     strings.Builder
	thinking strings.Builder
	calls    map[int]string // output index -> tool name (for deltas)
	final    []json.RawMessage
	usage    provider.Usage
	stopMax  bool
	// failed is set by a response.failed or error event.
	failed   bool
	failure  string
	failCode string
	failType string
}

type rEvent struct {
	Type        string          `json:"type"`
	Delta       string          `json:"delta"`
	OutputIndex int             `json:"output_index"`
	Item        json.RawMessage `json:"item"`
	Message     string          `json:"message"`
	Code        flexString      `json:"code"`
	Response    *struct {
		Output []json.RawMessage `json:"output"`
		Usage  *struct {
			InputTokens        int `json:"input_tokens"`
			OutputTokens       int `json:"output_tokens"`
			InputTokensDetails struct {
				CachedTokens int `json:"cached_tokens"`
			} `json:"input_tokens_details"`
		} `json:"usage"`
		Error *struct {
			Message string     `json:"message"`
			Code    flexString `json:"code"`
			Type    string     `json:"type"`
		} `json:"error"`
		IncompleteDetails *struct {
			Reason string `json:"reason"`
		} `json:"incomplete_details"`
	} `json:"response"`
}

// flexString accepts a JSON string, number or null (servers differ in how
// they send error codes).
type flexString string

func (f *flexString) UnmarshalJSON(b []byte) error {
	var s string
	if json.Unmarshal(b, &s) == nil {
		*f = flexString(s)
		return nil
	}
	*f = flexString(strings.Trim(string(b), `"`))
	if *f == "null" {
		*f = ""
	}
	return nil
}

type rItem struct {
	Type      string `json:"type"`
	Name      string `json:"name"`
	CallID    string `json:"call_id"`
	Arguments string `json:"arguments"`
	Content   []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	} `json:"content"`
	Summary []struct {
		Text string `json:"text"`
	} `json:"summary"`
	EncryptedContent string `json:"encrypted_content"`
}

func (a *responsesAcc) add(raw string, onEvent func(provider.StreamEvent)) {
	var ev rEvent
	if json.Unmarshal([]byte(raw), &ev) != nil {
		return
	}
	switch ev.Type {
	case "response.output_text.delta":
		a.text.WriteString(ev.Delta)
		onEvent(provider.StreamEvent{Kind: provider.EventTextDelta, Text: ev.Delta})
	case "response.reasoning_summary_text.delta", "response.reasoning_text.delta":
		a.thinking.WriteString(ev.Delta)
		onEvent(provider.StreamEvent{Kind: provider.EventThinkingDelta, Text: ev.Delta})
	case "response.output_item.added":
		var it rItem
		if json.Unmarshal(ev.Item, &it) == nil && it.Type == "function_call" {
			if a.calls == nil {
				a.calls = map[int]string{}
			}
			a.calls[ev.OutputIndex] = it.Name
			onEvent(provider.StreamEvent{Kind: provider.EventToolCallStart, Index: ev.OutputIndex, ID: it.CallID, Name: it.Name})
		}
	case "response.function_call_arguments.delta":
		onEvent(provider.StreamEvent{Kind: provider.EventToolCallDelta, Index: ev.OutputIndex, Name: a.calls[ev.OutputIndex], Text: ev.Delta})
	case "response.completed", "response.incomplete":
		if ev.Response != nil {
			a.final = ev.Response.Output
			if u := ev.Response.Usage; u != nil {
				a.usage = provider.Usage{InputTokens: u.InputTokens, OutputTokens: u.OutputTokens, CacheReadTokens: u.InputTokensDetails.CachedTokens}
			}
			if d := ev.Response.IncompleteDetails; d != nil && d.Reason == "max_output_tokens" {
				a.stopMax = true
			}
		}
	case "response.failed":
		a.failed = true
		a.failure = "the response failed"
		if ev.Response != nil && ev.Response.Error != nil {
			e := ev.Response.Error
			if e.Message != "" {
				a.failure = e.Message
			}
			a.failCode, a.failType = string(e.Code), e.Type
		}
	case "error":
		a.failed = true
		a.failure, a.failCode = ev.Message, string(ev.Code)
	}
}

func (a *responsesAcc) response(model string) provider.Response {
	msg := provider.Message{Role: provider.RoleAssistant, Meta: provider.Meta{Provider: a.prov, Model: model}}
	if a.final == nil {
		// No completed response (e.g. cancelled): keep what streamed.
		if a.thinking.Len() > 0 {
			msg.Content = append(msg.Content, provider.Block{Type: provider.BlockThinking, Text: a.thinking.String(), Provider: a.prov})
		}
		if a.text.Len() > 0 {
			msg.Content = append(msg.Content, provider.Text(a.text.String()))
		}
	}
	for _, raw := range a.final {
		var it rItem
		if json.Unmarshal(raw, &it) != nil {
			continue
		}
		switch it.Type {
		case "reasoning":
			var text strings.Builder
			for _, s := range it.Summary {
				text.WriteString(s.Text)
			}
			if text.Len() == 0 {
				for _, c := range it.Content {
					text.WriteString(c.Text)
				}
			}
			b := provider.Block{Type: provider.BlockThinking, Text: text.String(), Provider: a.prov}
			if it.EncryptedContent != "" {
				b.Opaque = append(json.RawMessage(nil), raw...)
			}
			msg.Content = append(msg.Content, b)
		case "message":
			var text strings.Builder
			for _, c := range it.Content {
				if c.Type == "output_text" {
					text.WriteString(c.Text)
				}
			}
			if text.Len() > 0 {
				msg.Content = append(msg.Content, provider.Text(text.String()))
			}
		case "function_call":
			args := json.RawMessage(it.Arguments)
			if len(args) == 0 {
				args = json.RawMessage("{}")
			} else if !json.Valid(args) {
				args, _ = json.Marshal(it.Arguments)
			}
			id := it.CallID
			if id == "" {
				id = "call_" + randomID()
			}
			msg.Content = append(msg.Content, provider.Block{Type: provider.BlockToolCall, ID: id, Name: it.Name, Args: args, Provider: a.prov})
		}
	}
	stop := provider.StopEnd
	switch {
	case len(msg.ToolCalls()) > 0:
		stop = provider.StopToolUse
	case a.stopMax:
		stop = provider.StopMaxTokens
	}
	u := a.usage
	msg.Meta.Usage = &u
	msg.Meta.StopReason = stop
	return provider.Response{Message: msg, Usage: u, StopReason: stop}
}
