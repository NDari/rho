// Command crashext is a compiled extension that crashes on request, for
// the crash and restart tests. Its "work" tool answers with the process id
// (so tests can tell a restarted process apart), and "crash" prints a Go
// panic to stderr and exits mid-call, as a real extension bug would.
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strconv"

	"github.com/NDari/rho/core/ext"
	"github.com/NDari/rho/core/ext/rpc"
)

func main() {
	var conn *rpc.Conn
	conn = rpc.NewConn(os.Stdin, os.Stdout, func(_ context.Context, method string, params json.RawMessage) (any, error) {
		switch method {
		case "initialize":
			obj := json.RawMessage(`{"type":"object","properties":{}}`)
			return ext.InitializeResult{ProtocolVersion: ext.ProtocolVersion, Name: "crashy", Tools: []ext.ToolInfo{
				{Name: "work", Description: "Returns the process id.", Parameters: obj},
				{Name: "crash", Description: "Crashes the extension.", Parameters: obj},
			}}, nil
		case "tool/execute":
			var p ext.ToolExecuteParams
			_ = json.Unmarshal(params, &p)
			if p.Name == "crash" {
				fmt.Fprintln(os.Stderr, "panic: runtime error: index out of range [3] with length 3")
				fmt.Fprintln(os.Stderr, "goroutine 7 [running]:")
				os.Exit(2)
			}
			return ext.ToolResult{Content: []ext.ContentBlock{{Type: "text", Text: strconv.Itoa(os.Getpid())}}}, nil
		case "shutdown":
			return struct{}{}, nil
		}
		return nil, &rpc.Error{Code: rpc.CodeMethodNotFound, Message: method}
	})
	<-conn.Done()
}
