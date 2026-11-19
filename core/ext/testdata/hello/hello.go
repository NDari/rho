package main

import (
	"fmt"
	"strings"

	"rho"
)

func main() {
	rho.Info("hello", "0.1.0")
	rho.RegisterTool(rho.Tool{
		Name:        "shout",
		Description: "Upper-case some text",
		Parameters: map[string]any{
			"type":       "object",
			"properties": map[string]any{"text": map[string]any{"type": "string"}},
			"required":   []string{"text"},
		},
		ReadOnly: true,
		Execute: func(ctx *rho.ToolContext, args map[string]any) (string, error) {
			ctx.Progress("shouting")
			return strings.ToUpper(fmt.Sprint(args["text"])), nil
		},
	})
	rho.RegisterCommand("greet", "Say hello", func(args string) error {
		rho.UI.Notify("hello " + args)
		return nil
	})
	rho.On("before_tool_call", func(e rho.Event) rho.Response {
		if e.Tool == "bash" {
			if cmd, _ := e.Args["command"].(string); strings.Contains(cmd, "rm -rf") {
				return rho.Response{Block: "dangerous command"}
			}
		}
		return rho.Response{}
	})
	rho.On("after_tool_call", func(e rho.Event) rho.Response {
		if e.Tool == "shout" {
			return rho.Response{ReplaceResult: true, Result: e.Result + "!"}
		}
		return rho.Response{}
	})
}
