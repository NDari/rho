package headless

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/NDari/rho/core/agent"
	"github.com/NDari/rho/core/events"
	"github.com/NDari/rho/core/provider"
)

func TestStatus(t *testing.T) {
	tests := []struct {
		err  error
		want string
	}{
		{nil, "done"},
		{context.Canceled, "cancelled"},
		{agent.ErrBlocked, "blocked"},
		{fmt.Errorf("%w: stopped after 3 model responses", agent.ErrMaxSteps), "max_steps"},
		{errors.New("provider failed"), "error"},
	}
	for _, tt := range tests {
		if got := status(tt.err); got != tt.want {
			t.Errorf("status(%v) = %q, want %q", tt.err, got, tt.want)
		}
	}
}

func TestResultJSON(t *testing.T) {
	data, err := events.Marshal(Result{Status: "done", Answer: "ok", Model: "m", Steps: 2, Usage: provider.Usage{InputTokens: 5, OutputTokens: 1}, Duration: 2500 * time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	const want = `{"type":"result","status":"done","answer":"ok","model":"m","steps":2,"usage":{"input_tokens":5,"output_tokens":1},"cost":0,"duration_ns":2500000000}`
	if got := string(data); got != want || strings.Contains(got, "\n") {
		t.Errorf("got  %s\nwant %s", got, want)
	}
}
