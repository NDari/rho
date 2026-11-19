package headless

import (
	"context"
	"errors"
	"fmt"
	"io"
)

// errNoUI is returned for prompts that need a user.
var errNoUI = errors.New("not available in headless mode")

// UI answers extension UI requests without a user: confirmations are
// declined, selections and input fail, notifications go to stderr.
type UI struct {
	ErrOut io.Writer
}

// Confirm implements ext.UI.
func (UI) Confirm(context.Context, string, string) (bool, error) { return false, nil }

// Select implements ext.UI.
func (UI) Select(context.Context, string, []string) (string, error) { return "", errNoUI }

// Input implements ext.UI.
func (UI) Input(context.Context, string, string) (string, error) { return "", errNoUI }

// Notify implements ext.UI.
func (u UI) Notify(level, text string) {
	if u.ErrOut != nil {
		fmt.Fprintf(u.ErrOut, "%s: %s\n", level, text)
	}
}

// SetStatus implements ext.UI.
func (UI) SetStatus(string, string) {}
