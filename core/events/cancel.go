package events

import (
	"context"
	"errors"
)

// Who cancels a turn, for Cancelled.By. Extensions use "extension NAME".
const (
	ByUser      = "the user"
	ByRPCClient = "the RPC client"
	ByShutdown  = "Rho (shutting down)"
)

// Cancelled is the cause of a cancelled turn (see context.Cause): who or
// what cancelled it. It matches context.Canceled.
type Cancelled struct{ By string }

func (c *Cancelled) Error() string { return "cancelled by " + c.By }

// Is reports whether target is context.Canceled.
func (c *Cancelled) Is(target error) bool { return target == context.Canceled }

// CancelledBy names who cancelled ctx: the By of a Cancelled cause, or the
// user when the cause does not say.
func CancelledBy(ctx context.Context) string {
	var c *Cancelled
	if errors.As(context.Cause(ctx), &c) {
		return c.By
	}
	return ByUser
}
