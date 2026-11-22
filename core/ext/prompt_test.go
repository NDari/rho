package ext_test

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/NDari/rho/core/config"
	"github.com/NDari/rho/core/events"
	"github.com/NDari/rho/core/ext"
)

// gateExtension asks before every bash command, as guard.go does, except
// for commands starting with "slow", where it works for 3 seconds instead.
const gateExtension = `package main

import (
	"strings"
	"time"

	"rho"
)

func main() {
	rho.Info("gate", "0.1.0")
	rho.On("before_tool_call", func(e rho.Event) rho.Response {
		cmd, _ := e.Args["command"].(string)
		if strings.HasPrefix(cmd, "slow") {
			time.Sleep(3 * time.Second)
			return rho.Response{Block: "slow"}
		}
		if rho.UI.Confirm("Allow?", cmd) {
			return rho.Response{}
		}
		return rho.Response{Block: "declined"}
	})
}
`

// promptUI answers Confirm with confirm, which gets the prompt's context.
type promptUI struct {
	fakeUI
	confirm func(ctx context.Context) (bool, error)
}

func (u *promptUI) Confirm(ctx context.Context, _, _ string) (bool, error) { return u.confirm(ctx) }

func startGate(t *testing.T, ui ext.UI) *ext.Manager {
	t.Helper()
	home := t.TempDir()
	dir := filepath.Join(home, "extensions", "gate")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "gate.go"), []byte(gateExtension), 0o644); err != nil {
		t.Fatal(err)
	}
	m := ext.NewManager(ext.Options{
		Paths:  config.Paths{Home: home, Cwd: home, ProjectRoot: home, ProjectDir: filepath.Join(t.TempDir(), ".rho")},
		Config: config.Defaults(),
		UI:     ui,
		Launch: inProcess,
	})
	// before_tool_call gets 10 times this: one second.
	m.SetTimeout(100 * time.Millisecond)
	m.Start(context.Background())
	t.Cleanup(m.Stop)
	if exts := m.Extensions(); len(exts) != 1 || exts[0].Status != ext.StatusRunning {
		t.Fatalf("extension not running: %s", m.Describe())
	}
	return m
}

// TestPromptTimeDoesNotCountAgainstHookTimeout answers a prompt after the
// hook's timeout has passed: the answer must still decide the call.
func TestPromptTimeDoesNotCountAgainstHookTimeout(t *testing.T) {
	m := startGate(t, &promptUI{confirm: func(ctx context.Context) (bool, error) {
		select {
		case <-time.After(1500 * time.Millisecond):
			return false, nil
		case <-ctx.Done():
			return false, ctx.Err()
		}
	}})
	_, reason := m.BeforeToolCall(context.Background(), "bash", json.RawMessage(`{"command":"rm -rf build"}`))
	if reason != "declined (blocked by extension gate)" {
		t.Errorf("reason = %q, want the user's answer to block the call", reason)
	}
}

// TestHookTimeoutStillApplies checks that a hook that works too long
// without asking the user still times out.
func TestHookTimeoutStillApplies(t *testing.T) {
	m := startGate(t, &promptUI{confirm: func(context.Context) (bool, error) { return true, nil }})
	start := time.Now()
	m.BeforeToolCall(context.Background(), "bash", json.RawMessage(`{"command":"slow"}`))
	if d := time.Since(start); d > 2500*time.Millisecond {
		t.Errorf("hook took %s, want it cut off after about a second", d)
	}
}

// TestPromptClosesWhenHookEnds closes an open prompt in the ways Rho can
// and checks that the prompt sees why.
func TestPromptClosesWhenHookEnds(t *testing.T) {
	tests := []struct {
		name  string
		close func(m *ext.Manager, cancel context.CancelCauseFunc)
		want  string
	}{
		{"turn cancelled", func(_ *ext.Manager, cancel context.CancelCauseFunc) {
			cancel(&events.Cancelled{By: events.ByRPCClient})
		}, "cancelled by the RPC client"},
		{"extension stopped", func(m *ext.Manager, _ context.CancelCauseFunc) { m.Stop() }, "extension gate stopped"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			opened := make(chan struct{})
			closed := make(chan string, 1)
			m := startGate(t, &promptUI{confirm: func(ctx context.Context) (bool, error) {
				close(opened)
				<-ctx.Done()
				closed <- context.Cause(ctx).Error()
				return false, ctx.Err()
			}})
			ctx, cancel := context.WithCancelCause(context.Background())
			go func() {
				<-opened
				tt.close(m, cancel)
			}()
			m.BeforeToolCall(ctx, "bash", json.RawMessage(`{"command":"rm -rf build"}`))
			select {
			case got := <-closed:
				if got != tt.want {
					t.Errorf("close reason = %q, want %q", got, tt.want)
				}
			case <-time.After(2 * time.Second):
				t.Error("the prompt stayed open after the hook call ended")
			}
		})
	}
}

// TestCallWaitsForClosedPrompt checks that a hook call that closes a
// prompt returns only after the frontend took the prompt down, so the
// close comes before what follows the call, and that a frontend ignoring
// the close delays it by at most about a second.
func TestCallWaitsForClosedPrompt(t *testing.T) {
	tests := []struct {
		name    string
		confirm func(ctx context.Context, closed chan<- time.Time) (bool, error)
		max     time.Duration
	}{
		{"frontend takes the prompt down", func(ctx context.Context, closed chan<- time.Time) (bool, error) {
			<-ctx.Done()
			time.Sleep(200 * time.Millisecond) // e.g. writing ui/closed
			closed <- time.Now()
			return false, ctx.Err()
		}, 900 * time.Millisecond},
		{"frontend ignores the close", func(_ context.Context, closed chan<- time.Time) (bool, error) {
			time.Sleep(5 * time.Second)
			closed <- time.Now()
			return false, nil
		}, 1700 * time.Millisecond},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			opened := make(chan struct{})
			closed := make(chan time.Time, 1)
			m := startGate(t, &promptUI{confirm: func(ctx context.Context) (bool, error) {
				close(opened)
				return tt.confirm(ctx, closed)
			}})
			ctx, cancel := context.WithCancelCause(context.Background())
			var cancelledAt time.Time
			go func() {
				<-opened
				cancelledAt = time.Now()
				cancel(&events.Cancelled{By: events.ByRPCClient})
			}()
			m.BeforeToolCall(ctx, "bash", json.RawMessage(`{"command":"rm -rf build"}`))
			returned := time.Now()
			if d := returned.Sub(cancelledAt); d > tt.max {
				t.Errorf("the call returned %s after the cancel, want at most %s", d, tt.max)
			}
			select {
			case at := <-closed:
				if at.After(returned) {
					t.Error("the call returned before the frontend took the prompt down")
				}
			default:
				if tt.max < time.Second {
					t.Error("the call returned before the frontend took the prompt down")
				}
			}
		})
	}
}
