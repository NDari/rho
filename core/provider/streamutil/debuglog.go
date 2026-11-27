package streamutil

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"sync"
	"time"

	"github.com/NDari/rho/core/provider"
)

// DebugLog appends one JSON line per request to a file: the exact payload
// sent and what came back. It holds whole conversations, so it is off by
// default. Auth headers are never written. A nil *DebugLog logs nothing.
type DebugLog struct {
	path string
	mu   sync.Mutex
}

// NewDebugLog returns a log that writes to path, or nil when path is "".
func NewDebugLog(path string) *DebugLog {
	if path == "" {
		return nil
	}
	return &DebugLog{path: path}
}

type captureKey struct{}

// Capture holds the request body of one logged request.
type Capture struct{ body []byte }

// Middleware records the body of a request made with a context from
// Start, then sends it on with next. SDKs take it as their HTTP middleware.
func (d *DebugLog) Middleware(req *http.Request, next func(*http.Request) (*http.Response, error)) (*http.Response, error) {
	if c, ok := req.Context().Value(captureKey{}).(*Capture); ok && req.Body != nil {
		body, err := io.ReadAll(req.Body)
		_ = req.Body.Close()
		if err != nil {
			return nil, err
		}
		c.body = body
		req.Body = io.NopCloser(bytes.NewReader(body))
	}
	return next(req)
}

// Start returns a context that captures the request body.
func (d *DebugLog) Start(ctx context.Context) (context.Context, *Capture) {
	if d == nil {
		return ctx, nil
	}
	c := &Capture{}
	return context.WithValue(ctx, captureKey{}, c), c
}

// Write appends the record. Errors are ignored: logging must never break
// a request.
func (d *DebugLog) Write(prov string, c *Capture, resp provider.Response, err error) {
	if d == nil || c == nil {
		return
	}
	type call struct {
		Name string          `json:"name"`
		Args json.RawMessage `json:"args,omitempty"`
	}
	rec := struct {
		Time       time.Time       `json:"time"`
		Provider   string          `json:"provider"`
		StopReason string          `json:"stop_reason,omitempty"`
		Error      string          `json:"error,omitempty"`
		Usage      provider.Usage  `json:"usage"`
		Text       string          `json:"text,omitempty"`
		Thinking   string          `json:"thinking,omitempty"`
		ToolCalls  []call          `json:"tool_calls,omitempty"`
		Request    json.RawMessage `json:"request,omitempty"`
	}{Time: time.Now().UTC(), Provider: prov, StopReason: string(resp.StopReason), Usage: resp.Usage, Text: resp.Message.TextContent()}
	if err != nil {
		rec.Error = err.Error()
		rec.StopReason = ""
	}
	for _, b := range resp.Message.Content {
		switch b.Type {
		case provider.BlockThinking:
			rec.Thinking += b.Text
		case provider.BlockToolCall:
			rec.ToolCalls = append(rec.ToolCalls, call{b.Name, b.Args})
		}
	}
	if json.Valid(c.body) {
		rec.Request = c.body
	}
	line, merr := json.Marshal(rec)
	if merr != nil {
		return
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	f, ferr := os.OpenFile(d.path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if ferr != nil {
		return
	}
	_, _ = f.Write(append(line, '\n'))
	_ = f.Close()
}
