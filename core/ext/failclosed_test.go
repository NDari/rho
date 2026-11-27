package ext_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/NDari/rho/core/config"
	"github.com/NDari/rho/core/ext"
	"github.com/NDari/rho/core/ext/rpc"
	"github.com/NDari/rho/core/provider"
	"github.com/NDari/rho/core/tools"
)

// failHooks are the hooks the scripted extension registers.
var failHooks = []string{
	ext.HookUserMessage, ext.HookBeforeRequest, ext.HookBeforeToolCall,
	ext.HookAfterToolCall, ext.HookBeforeCompaction, ext.HookTurnEnd,
}

// script answers an extension's requests other than shutdown. n counts the
// processes started (1 for the first), and crash makes this one exit.
type script func(n int, method string, crash func()) (any, error)

// initialized is the initialize reply of the scripted extension.
func initialized() (any, error) {
	return ext.InitializeResult{ProtocolVersion: ext.ProtocolVersion, Name: "fx", Hooks: failHooks}, nil
}

// startScripted starts a manager with one extension, "fx", run in this
// process by s.
func startScripted(t *testing.T, s script) (*ext.Manager, *noticeLog) {
	t.Helper()
	home := t.TempDir()
	dir := filepath.Join(home, "extensions", "fx")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, ext.ManifestName), []byte(`{"command": "unused"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	var started atomic.Int32
	launch := func(ext.Candidate) (io.ReadCloser, io.WriteCloser, io.ReadCloser, func(), error) {
		n := int(started.Add(1))
		inR, inW := io.Pipe()
		outR, outW := io.Pipe()
		var once sync.Once
		crash := func() {
			once.Do(func() {
				_ = outW.Close()
				_ = inR.Close()
			})
		}
		rpc.NewConn(inR, outW, func(_ context.Context, method string, _ json.RawMessage) (any, error) {
			if method == "shutdown" {
				return struct{}{}, nil
			}
			return s(n, method, crash)
		})
		return outR, inW, nil, crash, nil
	}
	var notices noticeLog
	m := ext.NewManager(ext.Options{
		Paths:  config.Paths{Home: home, Cwd: home, ProjectDir: filepath.Join(home, "none")},
		Config: config.Defaults(),
		Launch: launch,
		Sink:   notices.sink,
	})
	// Hooks get this (after_tool_call) or 10 times this.
	m.SetTimeout(50 * time.Millisecond)
	m.SetRestartDelay(100 * time.Millisecond)
	m.Start(context.Background())
	t.Cleanup(m.Stop)
	if exts := m.Extensions(); len(exts) != 1 || exts[0].Status != ext.StatusRunning {
		t.Fatalf("extension not running: %s", m.Describe())
	}
	return m, &notices
}

// hookOutcomes calls every hook and describes what it let through: "" when
// the action went ahead, otherwise how it was stopped.
func hookOutcomes(m *ext.Manager, notices *noticeLog) map[string]string {
	ctx := context.Background()
	out := map[string]string{}
	if _, reason := m.UserMessage(ctx, "hi"); reason != "" {
		out[ext.HookUserMessage] = reason
	}
	if _, _, _, err := m.BeforeRequest(ctx, "s", []provider.Message{provider.UserText("q")}, []string{"read"}, false); err != nil {
		out[ext.HookBeforeRequest] = err.Error()
	}
	if _, reason := m.BeforeToolCall(ctx, "bash", json.RawMessage(`{"command":"ls"}`)); reason != "" {
		out[ext.HookBeforeToolCall] = reason
	}
	res := tools.Result{Content: []provider.Block{provider.Text("secret output")}}
	if got := m.AfterToolCall(ctx, "bash", nil, res); got.IsError || got.Content[0].Text != "secret output" {
		out[ext.HookAfterToolCall] = got.Content[0].Text
	}
	if _, err := m.BeforeCompaction(ctx, []provider.Message{provider.UserText("q")}); err != nil {
		out[ext.HookBeforeCompaction] = err.Error()
	}
	before := notices.all()
	m.TurnEnd(ctx)
	if n := strings.TrimPrefix(notices.all(), before); strings.Contains(n, "turn_end") {
		out[ext.HookTurnEnd] = n
	}
	return out
}

// TestFailingHooksFailClosed checks that a hook that errors, times out or
// sends a reply that does not parse stops what it guards, and that a hook
// with nothing to stop is reported.
func TestFailingHooksFailClosed(t *testing.T) {
	tests := []struct {
		name   string
		answer func() (any, error)
		want   string
		// skip are hooks the failure does not reach: turn_end's reply
		// is not read.
		skip []string
	}{
		{"error", func() (any, error) { return nil, errors.New("boom") }, "boom", nil},
		{"timeout", func() (any, error) { time.Sleep(time.Second); return struct{}{}, nil }, "no reply within", nil},
		{"bad reply", func() (any, error) {
			return json.RawMessage(`{"block":5,"text":5,"system":5,"result":5,"summary":5}`), nil
		}, "cannot unmarshal", []string{ext.HookTurnEnd}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m, notices := startScripted(t, func(_ int, method string, _ func()) (any, error) {
				if method == "initialize" {
					return initialized()
				}
				return tt.answer()
			})
			got := hookOutcomes(m, notices)
			for _, hook := range failHooks {
				if slices.Contains(tt.skip, hook) {
					continue
				}
				switch msg := got[hook]; {
				case msg == "":
					t.Errorf("%s let the action through", hook)
				case !strings.Contains(msg, tt.want) || !strings.Contains(msg, "extension fx"):
					t.Errorf("%s: %q does not name the extension and %q", hook, msg, tt.want)
				}
			}
		})
	}
}

// TestHooksAnsweringNothingPass checks the other side: hooks that answer
// {} change nothing.
func TestHooksAnsweringNothingPass(t *testing.T) {
	m, notices := startScripted(t, func(_ int, method string, _ func()) (any, error) {
		if method == "initialize" {
			return initialized()
		}
		return struct{}{}, nil
	})
	if got := hookOutcomes(m, notices); len(got) > 0 {
		t.Errorf("hooks answering {} stopped actions: %v", got)
	}
}

// TestCrashedExtensionHooksFailClosed crashes the extension in a hook: the
// call is blocked, a call during the restart reaches the new process, and
// once the extension stays failed its hooks block until it is turned off.
func TestCrashedExtensionHooksFailClosed(t *testing.T) {
	tests := []struct {
		name string
		// second answers before_tool_call in the restarted process; nil
		// makes the restart fail.
		second    func() (any, error)
		wantAfter string // "" for allowed
	}{
		{"restart heals", func() (any, error) { return struct{}{}, nil }, ""},
		{"restart fails", nil, "is not running"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m, notices := startScripted(t, func(n int, method string, crash func()) (any, error) {
				switch {
				case n > 1 && tt.second == nil:
					return nil, errors.New("cannot start")
				case method == "initialize":
					return initialized()
				case n == 1:
					crash()
					return nil, errors.New("crashed")
				}
				return tt.second()
			})
			ctx := context.Background()
			call := func() string {
				_, reason := m.BeforeToolCall(ctx, "bash", json.RawMessage(`{"command":"ls"}`))
				return reason
			}
			if reason := call(); !strings.Contains(reason, "extension fx") {
				t.Fatalf("the call that crashed the extension was not blocked: %q", reason)
			}
			for i := range 2 {
				reason := call()
				if tt.wantAfter == "" && reason != "" || tt.wantAfter != "" && !strings.Contains(reason, tt.wantAfter) {
					t.Errorf("call %d after the crash: reason %q, want %q", i+1, reason, tt.wantAfter)
				}
			}
			notices.waitFor(t, "extension fx")
			if tt.wantAfter == "" {
				return
			}
			if err := m.SetEnabled(ctx, "fx", false); err != nil {
				t.Fatal(err)
			}
			if reason := call(); reason != "" {
				t.Errorf("a turned off extension still blocks: %q", reason)
			}
		})
	}
}

// TestCancelledHookIsNotReportedAsAFailure checks that cancelling a turn
// while a hook waits stops the action with a reason that says so.
func TestCancelledHookIsNotReportedAsAFailure(t *testing.T) {
	m, _ := startScripted(t, func(_ int, method string, _ func()) (any, error) {
		if method == "initialize" {
			return initialized()
		}
		time.Sleep(200 * time.Millisecond)
		return struct{}{}, nil
	})
	ctx, cancel := context.WithCancel(context.Background())
	time.AfterFunc(20*time.Millisecond, cancel)
	_, reason := m.BeforeToolCall(ctx, "bash", json.RawMessage(`{"command":"ls"}`))
	if want := fmt.Sprintf("cancelled by the user before extension fx answered its %s hook", ext.HookBeforeToolCall); reason != want {
		t.Errorf("reason = %q, want %q", reason, want)
	}
}
