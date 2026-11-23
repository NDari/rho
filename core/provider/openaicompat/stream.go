package openaicompat

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/openai/openai-go/v3/packages/ssestream"

	"github.com/NDari/rho/core/provider"
)

// DefaultIdleTimeout is how long a stream may go without data before it is
// treated as stalled. It also bounds the wait for the first data, which
// includes the server reading the prompt.
const DefaultIdleTimeout = 5 * time.Minute

var errStalled = errors.New("stream stalled")

// watchdog cancels a request whose stream stops sending data.
type watchdog struct {
	t *time.Timer
	d time.Duration
}

// watch derives a context that is cancelled when kick is not called for d.
// d <= 0 disables the watchdog. stop must be called when the stream ends.
func watch(ctx context.Context, d time.Duration) (_ context.Context, _ *watchdog, stop func()) {
	if d <= 0 {
		return ctx, nil, func() {}
	}
	ctx, cancel := context.WithCancelCause(ctx)
	w := &watchdog{d: d, t: time.AfterFunc(d, func() { cancel(errStalled) })}
	return ctx, w, func() { w.t.Stop(); cancel(nil) }
}

// kick records that data arrived.
func (w *watchdog) kick() {
	if w != nil {
		w.t.Reset(w.d)
	}
}

// stalled reports whether the watchdog ended the request.
func stalled(ctx context.Context) bool { return errors.Is(context.Cause(ctx), errStalled) }

func stalledError(d time.Duration) error {
	return &provider.Error{Kind: provider.ErrTransient, Message: fmt.Sprintf("no data from the server for %s; the connection may have stalled", d)}
}

// errTruncated is returned when a stream ends cleanly but before the
// response was complete (no finish reason or final event), which happens
// when a proxy or the server drops the connection.
func errTruncated() error {
	return &provider.Error{Kind: provider.ErrTransient, Message: "the stream ended before the response was complete"}
}

// transientCodes are error codes and types that servers use for failures
// worth retrying.
var transientCodes = []string{
	"server_error", "internal_error", "rate_limit_exceeded", "rate_limit_error",
	"overloaded", "overloaded_error", "unavailable", "service_unavailable",
	"timeout", "resource_exhausted",
}

// classifyFailure classifies an error reported inside a stream (rather
// than as an HTTP status), from its message, code, type and numeric status.
func classifyFailure(msg, code, typ string, status int) *provider.Error {
	if msg == "" {
		msg = strings.TrimSpace(code + " " + typ)
	}
	if msg == "" {
		msg = "the server reported an error while streaming"
	}
	if n, err := strconv.Atoi(code); err == nil && status == 0 {
		status = n
	}
	kind := provider.ErrOther
	lc, lt := strings.ToLower(code), strings.ToLower(typ)
	switch {
	case provider.LooksLikeContextOverflow(msg + " " + code + " " + typ):
		kind = provider.ErrContextOverflow
	case strings.Contains(strings.ToLower(msg), "quota") || lc == "insufficient_quota":
		// Quota and billing problems do not go away by retrying.
	case status == http.StatusTooManyRequests || status >= 500,
		containsAny(transientCodes, lc, lt),
		strings.Contains(strings.ToLower(msg), "overloaded"):
		kind = provider.ErrTransient
	case status == http.StatusUnauthorized || status == http.StatusForbidden:
		kind = provider.ErrAuth
	}
	return &provider.Error{Kind: kind, StatusCode: status, Message: msg}
}

func containsAny(set []string, vals ...string) bool {
	for _, v := range vals {
		for _, s := range set {
			if v != "" && v == s {
				return true
			}
		}
	}
	return false
}

// classifyStreamError handles the SDK's error for a `data: {"error": ...}`
// event in a Chat Completions stream, which servers send when generation
// fails after the response started.
func classifyStreamError(se *ssestream.StreamError) error {
	var body struct {
		Error json.RawMessage `json:"error"`
	}
	if json.Unmarshal(se.Event.Data, &body) != nil || len(body.Error) == 0 {
		return &provider.Error{Kind: provider.ErrOther, Message: se.Message, Err: se}
	}
	var text string
	if json.Unmarshal(body.Error, &text) == nil {
		pe := classifyFailure(text, "", "", 0)
		pe.Err = se
		return pe
	}
	var obj struct {
		Message string          `json:"message"`
		Code    json.RawMessage `json:"code"`
		Type    string          `json:"type"`
		Status  string          `json:"status"`
	}
	_ = json.Unmarshal(body.Error, &obj)
	code, status := "", 0
	if n, err := strconv.Atoi(string(obj.Code)); err == nil {
		status = n
	} else {
		_ = json.Unmarshal(obj.Code, &code)
	}
	if code == "" {
		code = obj.Status // Gemini: "UNAVAILABLE", "RESOURCE_EXHAUSTED"
	}
	pe := classifyFailure(obj.Message, code, obj.Type, status)
	pe.Err = se
	return pe
}

// maxRetryAfter caps the Retry-After value that is honored.
const maxRetryAfter = time.Hour

// parseRetryAfter reads retry-after-ms and Retry-After (seconds or an
// HTTP date) from response headers.
func parseRetryAfter(h http.Header, now time.Time) time.Duration {
	if h == nil {
		return 0
	}
	var d time.Duration
	if v := h.Get("Retry-After-Ms"); v != "" {
		if ms, err := strconv.ParseFloat(v, 64); err == nil && ms > 0 {
			d = time.Duration(ms * float64(time.Millisecond))
		}
	} else if v := h.Get("Retry-After"); v != "" {
		if s, err := strconv.ParseFloat(v, 64); err == nil && s > 0 {
			d = time.Duration(s * float64(time.Second))
		} else if t, err := http.ParseTime(v); err == nil {
			d = t.Sub(now)
		}
	}
	if d <= 0 {
		return 0
	}
	return min(d, maxRetryAfter)
}
