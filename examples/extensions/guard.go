//go:build rho

// guard is a Rho extension that asks for confirmation before risky bash
// commands. Copy it to ~/.rho/extensions/ to use it.
package main

import (
	"strings"

	"rho"
)

var risky = []string{"rm -rf", "git push --force", "git reset --hard", "mkfs", "dd if="}

func main() {
	rho.Info("guard", "0.1.0")
	rho.On("before_tool_call", func(e rho.Event) rho.Response {
		if e.Tool != "bash" {
			return rho.Response{}
		}
		cmd, _ := e.Args["command"].(string)
		for _, r := range risky {
			if strings.Contains(cmd, r) {
				if rho.UI.Confirm("Allow risky command?", cmd) {
					return rho.Response{}
				}
				return rho.Response{Block: "the user declined this command"}
			}
		}
		return rho.Response{}
	})
}
