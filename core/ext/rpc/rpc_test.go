package rpc

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"testing"
	"time"
)

// TestCallWithdrawable cancels a call the other side is still answering
// and checks that it is told which request was withdrawn and why.
func TestCallWithdrawable(t *testing.T) {
	aR, bW := io.Pipe()
	bR, aW := io.Pipe()
	got := make(chan Withdrawn, 1)
	asked := make(chan struct{})
	b := NewConn(bR, bW, func(ctx context.Context, method string, params json.RawMessage) (any, error) {
		switch method {
		case "ask":
			close(asked)
			<-ctx.Done()
			return nil, ctx.Err()
		case "closed":
			var w Withdrawn
			_ = json.Unmarshal(params, &w)
			got <- w
		}
		return nil, nil
	})
	a := NewConn(aR, aW, func(context.Context, string, json.RawMessage) (any, error) { return nil, nil })
	defer func() {
		_ = aW.Close()
		_ = bW.Close()
		<-a.Done()
		<-b.Done()
	}()

	ctx, cancel := context.WithCancelCause(context.Background())
	go func() {
		<-asked
		cancel(errors.New("cancelled by the RPC client"))
	}()
	if err := a.CallWithdrawable(ctx, "ask", struct{}{}, nil, "closed"); !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", err)
	}
	select {
	case w := <-got:
		if w.ID != 1 || w.Reason != "cancelled by the RPC client" {
			t.Errorf("withdrawn = %+v", w)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("no withdraw notification")
	}
}
