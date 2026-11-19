package ratelimit

import (
	"context"
	"testing"
	"time"

	"github.com/NDari/rho/core/provider"
	"github.com/NDari/rho/core/provider/fake"
)

// clock is a fake clock whose sleep advances time.
type clock struct{ t time.Time }

func (c *clock) now() time.Time { return c.t }
func (c *clock) sleep(ctx context.Context, d time.Duration) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	c.t = c.t.Add(d)
	return nil
}

func newTest(l Limits) (*Limiter, *clock) {
	c := &clock{t: time.Unix(1000, 0)}
	lim := New(l)
	lim.now, lim.sleep = c.now, c.sleep
	return lim, c
}

func TestRequestLimitWaitsForTheWindow(t *testing.T) {
	lim, c := newTest(Limits{Requests: 2})
	start := c.t
	ctx := context.Background()
	for range 2 {
		if _, err := lim.acquire(ctx, 10, func(time.Duration) {}); err != nil {
			t.Fatal(err)
		}
		c.t = c.t.Add(10 * time.Second)
	}
	var waits []time.Duration
	if _, err := lim.acquire(ctx, 10, func(d time.Duration) { waits = append(waits, d) }); err != nil {
		t.Fatal(err)
	}
	// The third request goes when the first leaves the window.
	if got := c.t.Sub(start); got != Window {
		t.Errorf("sent after %s, want %s", got, Window)
	}
	if len(waits) == 0 || waits[0] != 40*time.Second || waits[len(waits)-1] != 0 {
		t.Errorf("waits = %v", waits)
	}
}

func TestTokenLimitUsesReportedUsage(t *testing.T) {
	lim, c := newTest(Limits{Tokens: 1000})
	ctx := context.Background()
	r, _ := lim.acquire(ctx, 400, func(time.Duration) {}) // estimated 100 tokens
	r.settle(provider.Usage{InputTokens: 100, OutputTokens: 850})
	start := c.t
	if _, err := lim.acquire(ctx, 400, func(time.Duration) {}); err != nil {
		t.Fatal(err)
	}
	if c.t.Sub(start) != Window {
		t.Errorf("waited %s, want the full window (950 of 1000 tokens used)", c.t.Sub(start))
	}
}

func TestOversizedRequestGoesWhenWindowIsEmpty(t *testing.T) {
	lim, c := newTest(Limits{Tokens: 10})
	start := c.t
	if _, err := lim.acquire(context.Background(), 4000, func(time.Duration) {}); err != nil {
		t.Fatal(err)
	}
	if c.t != start {
		t.Error("an oversized request into an empty window waited")
	}
}

func TestWaitIsCancellable(t *testing.T) {
	lim, _ := newTest(Limits{Requests: 1})
	_, _ = lim.acquire(context.Background(), 1, func(time.Duration) {})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := lim.acquire(ctx, 1, func(time.Duration) {}); err == nil {
		t.Fatal("expected cancellation")
	}
}

func TestRetries429ThenSucceeds(t *testing.T) {
	lim, c := newTest(Limits{Requests: 100})
	rate := &provider.Error{Kind: provider.ErrTransient, StatusCode: 429, Message: "Too Many Requests"}
	fp := fake.New("fake", fake.Step{Err: rate}, fake.Step{Err: rate}, fake.Step{Text: "ok"})
	var statuses []string
	start := c.t
	resp, err := Wrap(fp, lim).Stream(context.Background(), provider.Request{}, func(e provider.StreamEvent) {
		if e.Kind == provider.EventStatus {
			statuses = append(statuses, e.Text)
		}
	})
	if err != nil || resp.Message.TextContent() != "ok" {
		t.Fatalf("resp = %+v, %v", resp, err)
	}
	if len(fp.Requests) != 3 || c.t.Sub(start) < 10*time.Second {
		t.Errorf("requests = %d, waited %s", len(fp.Requests), c.t.Sub(start))
	}
	if len(statuses) == 0 || statuses[len(statuses)-1] != "" {
		t.Errorf("status was not cleared: %q", statuses)
	}
}

func TestQuotaErrorsAreNotRetried(t *testing.T) {
	lim, _ := newTest(Limits{Requests: 100})
	quota := &provider.Error{Kind: provider.ErrTransient, StatusCode: 429, Message: "insufficient_quota: check your billing"}
	fp := fake.New("fake", fake.Step{Err: quota})
	if _, err := Wrap(fp, lim).Stream(context.Background(), provider.Request{}, func(provider.StreamEvent) {}); err == nil {
		t.Fatal("expected the quota error")
	}
	if len(fp.Requests) != 1 {
		t.Errorf("requests = %d, want 1", len(fp.Requests))
	}
}
