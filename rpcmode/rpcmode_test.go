package rpcmode

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/NDari/rho/core/app"
	"github.com/NDari/rho/core/ext/rpc"
	"github.com/NDari/rho/headless"
)

// chatServer answers every Chat Completions request with "hello".
func chatServer(t *testing.T) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "text/event-stream")
		for _, c := range []string{
			`{"id":"x","object":"chat.completion.chunk","created":1,"model":"m","choices":[{"index":0,"delta":{"role":"assistant","content":"hello"},"finish_reason":null}]}`,
			`{"id":"x","object":"chat.completion.chunk","created":1,"model":"m","choices":[{"index":0,"delta":{},"finish_reason":"stop"}],"usage":{"prompt_tokens":10,"completion_tokens":2,"total_tokens":12}}`,
		} {
			fmt.Fprintf(w, "data: %s\n\n", c)
		}
		fmt.Fprint(w, "data: [DONE]\n\n")
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestRPCMode(t *testing.T) {
	home := t.TempDir()
	t.Setenv("RHO_HOME", home)
	srv := chatServer(t)
	cfg := fmt.Sprintf(`{
	"default_model": "m",
	"providers": {"test": {
		"base_url": %q,
		"models":   [{"id": "m", "context_window": 32000}],
	}},
}`, srv.URL)
	if err := os.WriteFile(filepath.Join(home, "config.jsonc"), []byte(cfg), 0o644); err != nil {
		t.Fatal(err)
	}

	toServer, fromClient := io.Pipe()
	toClient, fromServer := io.Pipe()
	done := make(chan error, 1)
	go func() {
		done <- Run(context.Background(), app.Options{Cwd: t.TempDir(), NoExtensions: true, Version: "test"}, toServer, fromServer)
		_ = fromServer.Close()
	}()

	var mu sync.Mutex
	var types []string
	client := rpc.NewConn(toClient, fromClient, func(_ context.Context, method string, params json.RawMessage) (any, error) {
		if method == "event" {
			var e struct{ Type string }
			_ = json.Unmarshal(params, &e)
			mu.Lock()
			types = append(types, e.Type)
			mu.Unlock()
		}
		return nil, nil
	})
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	var init InitializeResult
	if err := client.Call(ctx, "initialize", struct{}{}, &init); err != nil {
		t.Fatal(err)
	}
	if init.ProtocolVersion != ProtocolVersion || init.RhoVersion != "test" || init.Session.Model != "m" || len(init.Methods) == 0 {
		t.Errorf("initialize = %+v", init)
	}
	if err := client.Call(ctx, "set_tools", ToolsParams{Tools: []string{"read", "grep"}}, nil); err != nil {
		t.Fatal(err)
	}
	var info struct{ Tools []string }
	if err := client.Call(ctx, "session/info", struct{}{}, &info); err != nil || strings.Join(info.Tools, ",") != "read,grep" {
		t.Errorf("tools after set_tools = %v, %v", info.Tools, err)
	}
	if err := client.Call(ctx, "set_tools", ToolsParams{Tools: []string{"nope"}}, nil); err == nil {
		t.Error("an unknown tool should be refused")
	}
	if err := client.Call(ctx, "set_tools", struct{}{}, nil); err != nil {
		t.Fatal(err)
	}
	if err := client.Call(ctx, "prompt", TextParams{Text: "hi"}, nil); err != nil {
		t.Fatal(err)
	}
	var res headless.Result
	if err := client.Call(ctx, "wait", struct{}{}, &res); err != nil {
		t.Fatal(err)
	}
	if res.Status != "done" || res.Answer != "hello" || res.Steps != 1 || res.Usage.InputTokens != 10 {
		t.Errorf("wait = %+v", res)
	}
	var msgs MessagesResult
	if err := client.Call(ctx, "session/messages", struct{}{}, &msgs); err != nil {
		t.Fatal(err)
	}
	if len(msgs.Messages) != 2 || msgs.Messages[1].Message.TextContent() != "hello" || msgs.Messages[0].ID == "" {
		t.Errorf("messages = %+v", msgs)
	}
	if err := client.Call(ctx, "prompt", TextParams{}, nil); err == nil {
		t.Error("a prompt without text should be an error")
	}
	if err := client.Call(ctx, "no_such_method", struct{}{}, nil); err == nil {
		t.Error("an unknown method should be an error")
	}
	if err := client.Call(ctx, "shutdown", struct{}{}, nil); err != nil {
		t.Fatal(err)
	}
	if err := client.Notify("exit", nil); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-ctx.Done():
		t.Fatal("Run did not return after exit")
	}
	mu.Lock()
	defer mu.Unlock()
	seen := map[string]bool{}
	for _, ty := range types {
		seen[ty] = true
	}
	for _, want := range []string{"turn_start", "text_delta", "message_end", "usage", "turn_end"} {
		if !seen[want] {
			t.Errorf("no %s event among %v", want, types)
		}
	}
}

func TestClientUI(t *testing.T) {
	pipe := func(answer bool) (clientUI, func()) {
		toServer, fromClient := io.Pipe()
		toClient, fromServer := io.Pipe()
		server := rpc.NewConn(toServer, fromServer, nil)
		_ = rpc.NewConn(toClient, fromClient, func(_ context.Context, method string, _ json.RawMessage) (any, error) {
			if !answer {
				return nil, &rpc.Error{Code: rpc.CodeMethodNotFound, Message: "no UI"}
			}
			switch method {
			case "ui/confirm":
				return map[string]bool{"confirmed": true}, nil
			case "ui/select", "ui/input":
				return map[string]any{"value": "picked"}, nil
			}
			return nil, nil
		})
		return clientUI{server}, func() { _ = fromClient.Close(); _ = fromServer.Close() }
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	ui, stop := pipe(true)
	if ok, _ := ui.Confirm(ctx, "t", "m"); !ok {
		t.Error("confirm should be forwarded")
	}
	if v, err := ui.Select(ctx, "t", []string{"a", "picked"}); err != nil || v != "picked" {
		t.Errorf("select = %q, %v", v, err)
	}
	if v, err := ui.Input(ctx, "t", "p"); err != nil || v != "picked" {
		t.Errorf("input = %q, %v", v, err)
	}
	stop()

	ui, stop = pipe(false)
	defer stop()
	if ok, _ := ui.Confirm(ctx, "t", "m"); ok {
		t.Error("a client without a UI should answer no")
	}
	if _, err := ui.Select(ctx, "t", []string{"a"}); err == nil {
		t.Error("a client without a UI should cancel a select")
	}
}
