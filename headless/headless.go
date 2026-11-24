// Package headless runs Rho without a TUI: print mode prints the final
// answer, JSON mode streams events as JSON lines.
package headless

import (
	"context"
	"errors"
	"fmt"
	"io"
	"sync"
	"time"

	"github.com/NDari/rho/core/agent"
	"github.com/NDari/rho/core/app"
	"github.com/NDari/rho/core/events"
	"github.com/NDari/rho/core/provider"
)

// Options control a headless run.
type Options struct {
	JSON   bool
	Out    io.Writer
	ErrOut io.Writer
}

// Sink returns an event sink for the given mode.
func Sink(o Options) events.Sink {
	var mu sync.Mutex
	if o.JSON {
		return func(e events.Event) {
			data, err := events.Marshal(e)
			if err != nil {
				return
			}
			mu.Lock()
			defer mu.Unlock()
			_, _ = o.Out.Write(append(data, '\n'))
		}
	}
	return func(e events.Event) {
		mu.Lock()
		defer mu.Unlock()
		switch ev := e.(type) {
		case events.Notice:
			fmt.Fprintf(o.ErrOut, "%s: %s\n", ev.Level, ev.Text)
		case events.Retry:
			fmt.Fprintf(o.ErrOut, "retrying (%d/%d) in %s: %s\n", ev.Attempt, ev.Max, ev.Delay, ev.Error)
		case events.CompactionStart:
			fmt.Fprintf(o.ErrOut, "compacting context (%d tokens)...\n", ev.Tokens)
		case events.CompactionEnd:
			if ev.Error != "" {
				fmt.Fprintf(o.ErrOut, "compaction failed: %s\n", ev.Error)
			}
		}
	}
}

// Result is the last line of JSON mode, so scripts need not follow the
// event stream.
type Result struct {
	// Status is "done", "error", "cancelled", "blocked" (an extension
	// blocked the prompt), "max_steps" or "cut_off" (the last response
	// was cut off at the output token limit).
	Status string `json:"status"`
	Error  string `json:"error,omitempty"`
	// Answer is the last assistant text of the turn; when the turn was
	// cut off, the text of the cut-off response alone (often empty).
	Answer string `json:"answer"`
	Model  string `json:"model"`
	// Steps counts model responses; Usage and Cost add up every request
	// of the turn, compaction included (Cost is 0 without pricing).
	Steps   int            `json:"steps"`
	Usage   provider.Usage `json:"usage"`
	Cost    float64        `json:"cost"`
	Session string         `json:"session,omitempty"`
	// Duration is how long the turn took.
	Duration time.Duration `json:"duration_ns"`
}

// Type implements events.Event.
func (Result) Type() string { return "result" }

// Run sends one prompt and waits for the turn to finish. Print mode writes
// the final assistant text to Out; JSON mode ends with a Result line. The
// error is the one that ended the turn, so the exit status can report it.
func Run(ctx context.Context, a *app.App, text string, o Options) error {
	err := a.Agent.Run(ctx, provider.UserText(text))
	final := lastAssistantText(a, a.Agent.LastTurn().CutOff)
	if !o.JSON {
		if final != "" {
			fmt.Fprintln(o.Out, final)
		}
		return err
	}
	// Through the agent's sink, so it cannot interleave with other lines.
	a.Agent.Emit(TurnResult(a, err))
	return err
}

// TurnResult describes the last turn, which err ended (nil: it finished).
func TurnResult(a *app.App, err error) Result {
	stats := a.Agent.LastTurn()
	r := Result{
		Status: status(err), Answer: lastAssistantText(a, stats.CutOff), Model: a.Agent.Model().ID,
		Steps: stats.Steps, Usage: stats.Usage, Cost: stats.Cost, Session: a.Agent.Session().Path(),
		Duration: stats.Duration,
	}
	if err != nil {
		r.Error = err.Error()
	} else if stats.CutOff {
		r.Status = "cut_off"
	}
	return r
}

func status(err error) string {
	switch {
	case err == nil:
		return "done"
	case errors.Is(err, context.Canceled):
		return "cancelled"
	case errors.Is(err, agent.ErrBlocked):
		return "blocked"
	case errors.Is(err, agent.ErrMaxSteps):
		return "max_steps"
	default:
		return "error"
	}
}

// lastAssistantText returns the last assistant text of the turn. With
// cutOff, it returns the last response's text even when empty: an earlier
// "let me read..." is not the answer of a turn whose final response was
// cut off.
func lastAssistantText(a *app.App, cutOff bool) string {
	cv := a.Agent.Session().BuildContext()
	for i := len(cv.Messages) - 1; i >= 0; i-- {
		m := cv.Messages[i]
		if m.Role == provider.RoleAssistant {
			if t := m.TextContent(); t != "" || cutOff {
				return t
			}
		}
		if m.Role == provider.RoleUser {
			break
		}
	}
	return ""
}
