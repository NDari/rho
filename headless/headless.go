// Package headless runs Rho without a TUI: print mode prints the final
// answer, JSON mode streams events as JSON lines.
package headless

import (
	"context"
	"fmt"
	"io"
	"sync"

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

// Run sends one prompt and waits for the turn to finish. In print mode the
// final assistant text is written to Out.
func Run(ctx context.Context, a *app.App, text string, o Options) error {
	err := a.Agent.Run(ctx, provider.UserText(text))
	if !o.JSON {
		if final := lastAssistantText(a); final != "" {
			fmt.Fprintln(o.Out, final)
		}
	}
	return err
}

func lastAssistantText(a *app.App) string {
	cv := a.Agent.Session().BuildContext()
	for i := len(cv.Messages) - 1; i >= 0; i-- {
		m := cv.Messages[i]
		if m.Role == provider.RoleAssistant {
			if t := m.TextContent(); t != "" {
				return t
			}
		}
		if m.Role == provider.RoleUser {
			break
		}
	}
	return ""
}
