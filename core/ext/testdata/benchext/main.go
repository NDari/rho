// Command benchext is a compiled extension for the benchmarks in
// core/ext/bench_test.go: the same hooks as the Go (Yaegi) extension there,
// so the two differ only in being compiled. "noop" reads before_request's
// messages and answers with nothing; "rewrite" returns them rewritten.
package main

import (
	"context"
	"encoding/json"
	"os"

	"github.com/NDari/rho/core/ext"
	"github.com/NDari/rho/core/ext/internal/benchwork"
	"github.com/NDari/rho/core/ext/rpc"
)

func main() {
	rewrite := len(os.Args) > 1 && os.Args[1] == "rewrite"
	conn := rpc.NewConn(os.Stdin, os.Stdout, func(_ context.Context, method string, params json.RawMessage) (any, error) {
		switch method {
		case "initialize":
			return ext.InitializeResult{ProtocolVersion: 1, Name: "bench", Hooks: []string{"user_message", "before_request"}}, nil
		case "hook/before_request":
			// Decode the messages either way, as an extension must to read
			// them (the Yaegi host always does).
			var p struct {
				Messages []map[string]any `json:"messages"`
			}
			if err := json.Unmarshal(params, &p); err != nil {
				return nil, err
			}
			if !rewrite {
				return struct{}{}, nil
			}
			return map[string]any{"messages": benchwork.Rewrite(p.Messages)}, nil
		case "hook/user_message", "shutdown":
			return struct{}{}, nil
		}
		return nil, &rpc.Error{Code: rpc.CodeMethodNotFound, Message: method}
	})
	<-conn.Done()
}
