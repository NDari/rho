// Package ratelimit keeps requests to a provider within requests-per-minute
// and tokens-per-minute limits over a rolling 60-second window. Requests
// wait until they fit instead of being rejected with a 429, and a 429 that
// still happens (for example because another process shares the key) is
// retried here with waits sized to the window, before the agent sees it.
package ratelimit

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"regexp"
	"sync"
	"time"

	"github.com/NDari/rho/core/provider"
)

// Window is the rolling window the limits apply to.
const Window = time.Minute

// MaxRetries is how many times a 429 is retried here.
const MaxRetries = 5

// Limits are per minute; 0 means no limit.
type Limits struct {
	Requests int
	Tokens   int
}

type entry struct {
	at       time.Time
	tokens   int
	rejected bool
}

// Limiter tracks what one process sent to one provider.
type Limiter struct {
	mu            sync.Mutex
	limits        Limits
	entries       []*entry
	tokensPerChar float64
	now           func() time.Time
	sleep         func(context.Context, time.Duration) error
}

// New creates a limiter.
func New(l Limits) *Limiter {
	return &Limiter{limits: l, tokensPerChar: 0.25, now: time.Now, sleep: sleepCtx}
}

func sleepCtx(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}

func (l *Limiter) prune() {
	cutoff := l.now().Add(-Window)
	i := 0
	for i < len(l.entries) && !l.entries[i].at.After(cutoff) {
		i++
	}
	l.entries = l.entries[i:]
}

// waitTime is how long until a request of tokens fits (0 if it fits now).
func (l *Limiter) waitTime(tokens int) time.Duration {
	l.prune()
	now := l.now()
	var wait time.Duration
	if n := l.limits.Requests; n > 0 && len(l.entries) >= n {
		// Enough of the oldest requests must leave to make room for one.
		wait = max(wait, l.entries[len(l.entries)-n].at.Add(Window).Sub(now))
	}
	if limit := l.limits.Tokens; limit > 0 {
		// A request larger than the whole budget goes once the window is empty.
		need := min(tokens, limit)
		used := 0
		for _, e := range l.entries {
			used += e.tokens
		}
		for _, e := range l.entries {
			if used+need <= limit {
				break
			}
			used -= e.tokens
			wait = max(wait, e.at.Add(Window).Sub(now))
		}
	}
	return max(wait, 0)
}

// Reservation is a request counted against the window.
type Reservation struct {
	l     *Limiter
	e     *entry
	chars int
}

// acquire waits until a request of chars characters fits, then counts it.
// onWait reports the remaining wait before each sleep, and 0 when done.
func (l *Limiter) acquire(ctx context.Context, chars int, onWait func(time.Duration)) (*Reservation, error) {
	waited := false
	for {
		l.mu.Lock()
		estimate := int(math.Ceil(float64(chars) * l.tokensPerChar))
		wait := l.waitTime(estimate)
		if wait == 0 {
			e := &entry{at: l.now(), tokens: estimate}
			l.entries = append(l.entries, e)
			l.mu.Unlock()
			if waited {
				onWait(0)
			}
			return &Reservation{l: l, e: e, chars: chars}, nil
		}
		l.mu.Unlock()
		waited = true
		onWait(wait)
		if err := l.sleep(ctx, min(wait, time.Second)); err != nil {
			onWait(0)
			return nil, err
		}
	}
}

// settle replaces the estimate with the reported usage and learns the
// characters-per-token ratio from it.
func (r *Reservation) settle(u provider.Usage) {
	r.l.mu.Lock()
	defer r.l.mu.Unlock()
	if t := u.Total(); t > 0 {
		r.e.tokens = t
	}
	if u.InputTokens > 0 && r.chars > 0 {
		ratio := min(1, max(0.05, float64(u.InputTokens)/float64(r.chars)))
		r.l.tokensPerChar = (r.l.tokensPerChar + ratio) / 2
	}
}

// reject keeps the request but not its tokens (the server refused it).
func (r *Reservation) reject() {
	r.l.mu.Lock()
	defer r.l.mu.Unlock()
	r.e.tokens = 0
	r.e.rejected = true
}

// retryDelay is how long to wait after a 429. If this process has accepted
// requests in the window, wait until the oldest leaves it; otherwise the
// quota is used elsewhere, so back off.
func (l *Limiter) retryDelay(attempt int) time.Duration {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.prune()
	backoff := min(5*time.Second<<attempt, Window)
	for _, e := range l.entries {
		if !e.rejected {
			return min(max(e.at.Add(Window).Sub(l.now()), time.Second), Window)
		}
	}
	return backoff
}

var quotaRe = regexp.MustCompile(`(?i)insufficient_quota|quota exceeded|out of budget|billing|usage limit reached`)

// isRateLimited reports a temporary rate limit (not an exhausted quota,
// which retrying won't fix).
func isRateLimited(err error) bool {
	var pe *provider.Error
	if !errors.As(err, &pe) || pe.StatusCode != 429 {
		return false
	}
	return !quotaRe.MatchString(err.Error())
}

// Provider wraps another provider with a limiter.
type Provider struct {
	inner provider.Provider
	l     *Limiter
}

// Wrap returns inner limited by l.
func Wrap(inner provider.Provider, l *Limiter) *Provider {
	return &Provider{inner: inner, l: l}
}

// Name implements provider.Provider.
func (p *Provider) Name() string { return p.inner.Name() }

// Stream implements provider.Provider.
func (p *Provider) Stream(ctx context.Context, req provider.Request, onEvent func(provider.StreamEvent)) (provider.Response, error) {
	chars := len(req.System)
	if js, err := json.Marshal(req.Messages); err == nil {
		chars += len(js)
	}
	status := func(why string) func(time.Duration) {
		return func(d time.Duration) {
			text := ""
			if d > 0 {
				text = fmt.Sprintf("%s, sending in %ds (Esc cancels)", why, int(math.Ceil(d.Seconds())))
			}
			onEvent(provider.StreamEvent{Kind: provider.EventStatus, Text: text})
		}
	}
	for attempt := 0; ; attempt++ {
		res, err := p.l.acquire(ctx, chars, status("rate limit"))
		if err != nil {
			return provider.Response{}, err
		}
		gotData := false
		resp, err := p.inner.Stream(ctx, req, func(e provider.StreamEvent) {
			gotData = true
			onEvent(e)
		})
		if err == nil || gotData || ctx.Err() != nil || !isRateLimited(err) || attempt >= MaxRetries {
			res.settle(resp.Usage)
			return resp, err
		}
		res.reject()
		delay := p.l.retryDelay(attempt)
		show := status("rate limited by the server")
		deadline := p.l.now().Add(delay)
		for left := delay; left > 0; left = deadline.Sub(p.l.now()) {
			show(left)
			if err := p.l.sleep(ctx, min(left, time.Second)); err != nil {
				show(0)
				return resp, err
			}
		}
		show(0)
	}
}
