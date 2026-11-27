// Package streamutil holds the streaming, retry and logging helpers that
// the provider adapters share.
package streamutil

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/NDari/rho/core/provider"
)

// DefaultIdleTimeout is how long a stream may go without data before it is
// treated as stalled. It also bounds the wait for the first data, which
// includes the server reading the prompt.
const DefaultIdleTimeout = 5 * time.Minute

var errStalled = errors.New("stream stalled")

// Watchdog cancels a request whose stream stops sending data.
type Watchdog struct {
	t *time.Timer
	d time.Duration
}

// Watch derives a context that is cancelled when kick is not called for d.
// d <= 0 disables the watchdog. stop must be called when the stream ends.
func Watch(ctx context.Context, d time.Duration) (_ context.Context, _ *Watchdog, stop func()) {
	if d <= 0 {
		return ctx, nil, func() {}
	}
	ctx, cancel := context.WithCancelCause(ctx)
	w := &Watchdog{d: d, t: time.AfterFunc(d, func() { cancel(errStalled) })}
	return ctx, w, func() { w.t.Stop(); cancel(nil) }
}

// Kick records that data arrived.
func (w *Watchdog) Kick() {
	if w != nil {
		w.t.Reset(w.d)
	}
}

// Stalled reports whether the watchdog ended the request.
func Stalled(ctx context.Context) bool { return errors.Is(context.Cause(ctx), errStalled) }

// StalledError is the error for a stream the watchdog ended.
func StalledError(d time.Duration) error {
	return &provider.Error{Kind: provider.ErrTransient, Message: fmt.Sprintf("no data from the server for %s; the connection may have stalled", d)}
}

// ErrTruncated is returned when a stream ends cleanly but before the
// response was complete (no finish reason or final event), which happens
// when a proxy or the server drops the connection.
func ErrTruncated() error {
	return &provider.Error{Kind: provider.ErrTransient, Message: "the stream ended before the response was complete"}
}

// transientCodes are error codes and types that servers use for failures
// worth retrying.
var transientCodes = []string{
	"server_error", "internal_error", "rate_limit_exceeded", "rate_limit_error",
	"overloaded", "overloaded_error", "unavailable", "service_unavailable",
	"timeout", "resource_exhausted",
}

// ClassifyFailure classifies an error reported inside a stream (rather
// than as an HTTP status), from its message, code, type and numeric status.
func ClassifyFailure(msg, code, typ string, status int) *provider.Error {
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

// MaxRetryAfter caps the Retry-After value that is honored.
const MaxRetryAfter = time.Hour

// ParseRetryAfter reads retry-after-ms and Retry-After (seconds or an
// HTTP date) from response headers.
func ParseRetryAfter(h http.Header, now time.Time) time.Duration {
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
	return min(d, MaxRetryAfter)
}

// IsConnReset reports whether err is a connection the server closed, as
// happens to a reused keep-alive connection.
func IsConnReset(err error) bool {
	if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) || errors.Is(err, syscall.ECONNRESET) || errors.Is(err, net.ErrClosed) {
		return true
	}
	s := err.Error()
	return strings.Contains(s, "connection reset") || strings.Contains(s, "server closed idle connection") ||
		strings.Contains(s, "use of closed network connection") || strings.HasSuffix(s, ": EOF")
}
