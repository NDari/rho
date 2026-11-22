# Extensions

Extensions add tools, slash commands and hooks to Rho. Every extension runs as a **separate process** and talks to Rho with JSON-RPC 2.0 over stdin/stdout. Extensions run with your user permissions, like Rho itself.

There are two kinds:

- **Go source extensions:** a `.go` file, or a directory of `.go` files, run by Rho's built-in interpreter (`rho ext-host`, based on Yaegi). No Go toolchain is needed.
- **Executable extensions:** any program, described by a `rho-extension.toml` manifest. The program's runtime (Python, Node, a compiled binary...) is the extension's own dependency.

## Where extensions live

| Location | Loaded |
|---|---|
| `~/.rho/extensions/` | automatically |
| `.rho/extensions/` in a project | after a one-time approval, keyed to a hash of the extension's files; any change requires approval again |

Approvals are stored in `~/.rho/trust.json`. In `-p` (headless) mode, unapproved project extensions are skipped.

Each entry in an extensions directory is one extension:

- `name.go`: a single-file Go extension.
- `name/` containing `*.go` files: a multi-file Go extension (`package main`).
- `name/` containing `rho-extension.toml`: an executable extension.

Names starting with `.` or `_` are ignored. To disable an extension without deleting it, turn it off in `/extensions` or add its name to `extensions.disabled` in `config.toml`.

Use `/extensions` to list extensions: Enter turns the selected one on or off (it starts or stops right away, the others keep running, and the choice is saved to `extensions.disabled` in `~/.rho/config.toml`), and Tab shows status, registrations, errors and recent log lines. `/reload` restarts all extensions.

## Go extensions

Import the `rho` package and register things in `main`:

```go
package main

import (
	"strings"

	"rho"
)

func main() {
	rho.Info("shout", "0.1.0")

	rho.RegisterTool(rho.Tool{
		Name:        "shout",
		Description: "Upper-case some text",
		Parameters: map[string]any{
			"type":       "object",
			"properties": map[string]any{"text": map[string]any{"type": "string"}},
			"required":   []string{"text"},
		},
		Execute: func(ctx *rho.ToolContext, args map[string]any) (string, error) {
			ctx.Progress("working...")
			return strings.ToUpper(args["text"].(string)), nil
		},
	})

	rho.RegisterCommand("hello", "Say hello", func(args string) error {
		rho.UI.Notify("hello " + args)
		return nil
	})

	rho.On("before_tool_call", func(e rho.Event) rho.Response {
		if e.Tool == "bash" && strings.Contains(e.Args["command"].(string), "rm -rf") {
			return rho.Response{Block: "not allowed"}
		}
		return rho.Response{}
	})
}
```

The `rho` package:

| Symbol | Purpose |
|---|---|
| `RegisterTool(Tool)` | add a tool; `Tool.ReadOnly` tools are also offered in side conversations. A tool may not use the name of a built-in tool that is offered, except a search tool (`grep`, `find`, `ls`) added only because `bash` is off, which it replaces. `tools.disabled` can turn it off |
| `RegisterCommand(name, description, func(args string) error)` | add a slash command |
| `On(event, func(Event) Response)` | handle a hook (see below) |
| `Info(name, version)` | set the extension name and version |
| `UI.Confirm(title, message) bool` | ask yes/no |
| `UI.Select(title, options) (string, bool)` | pick one option |
| `UI.Input(title, placeholder) (string, bool)` | ask for text |
| `UI.Notify(text)` | show a message |
| `UI.Status(text)` | set this extension's status bar text |
| `Log(format, args...)` | write to the extension log |
| `AppendMessage(text, trigger)` | send a user message to the agent |

Interpreted code can use the Go standard library. Anything written to stdout is redirected to stderr (the log), so `fmt.Println` is safe.

See [`examples/extensions/guard.go`](../examples/extensions/guard.go).

## Executable extensions

```toml
# rho-extension.toml
name = "wordcount"
command = "python3"
args = ["${dir}/main.py"]
```

`${dir}` expands to the extension's directory. A relative `command` containing a path separator is resolved against that directory too. The process runs in the session's working directory, with `RHO_EXTENSION_DIR` set to the extension's directory.

See [`examples/extensions/wordcount-py`](../examples/extensions/wordcount-py) for a complete extension in about 50 lines of Python.

## Protocol

Messages are JSON-RPC 2.0 objects, one per line (newline-delimited). Both sides may send requests. Write diagnostics to stderr, never stdout.

### Startup

Rho sends `initialize`:

```json
{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocol_version":1,"rho_version":"0.1.0","cwd":"/path/to/project"}}
```

The extension replies with its registrations:

```json
{"jsonrpc":"2.0","id":1,"result":{
  "protocol_version": 1,
  "name": "wordcount",
  "version": "0.1.0",
  "tools": [{"name": "word_count", "description": "...", "parameters": {"type": "object", "properties": {}}, "read_only": true}],
  "commands": [{"name": "stats", "description": "..."}],
  "hooks": ["before_tool_call", "turn_end"]
}}
```

If `protocol_version` differs from Rho's, the extension is refused with a clear error.

### Rho to extension

| Method | Params | Result |
|---|---|---|
| `tool/execute` | `call_id`, `name`, `args` | `{"content": [{"type": "text", "text": "..."}], "is_error": false}` |
| `tool/cancel` (notification) | `call_id` | |
| `command/run` | `name`, `args` (string) | `{}` |
| `hook/session_start` | | `{}` |
| `hook/before_tool_call` | `tool`, `args` | `{"block": "reason"}` to stop the call, `{"args": {...}}` to replace arguments, or `{}` |
| `hook/after_tool_call` | `tool`, `args`, `result` | `{"result": {...}}` to replace the result, or `{}` |
| `hook/turn_end` | | `{}` |
| `hook/before_compaction` | `transcript` | `{"summary": "..."}` to supply the summary, or `{}` |
| `hook/session_end` | | `{}` |
| `shutdown` | | `{}`; then stdin is closed |

Content blocks may also be images: `{"type": "image", "mime": "image/png", "data": "<base64>"}`.

### Extension to Rho

| Method | Params | Result |
|---|---|---|
| `tool/progress` (notification) | `call_id`, `text` | |
| `ui/confirm` | `title`, `message` | `{"confirmed": true}` |
| `ui/select` | `title`, `options` | `{"value": "...", "cancelled": false}` |
| `ui/input` | `title`, `placeholder` | `{"value": "...", "cancelled": false}` |
| `ui/notify` | `level`, `text` | `{}` |
| `ui/status` | `key`, `text` (empty clears) | `{}` |
| `session/append_message` | `text`, `trigger` | `{}`; queued as a follow-up while the agent works, or starts a turn when `trigger` is true and the agent is idle |
| `log` (notification) | `level`, `message` | |

In headless mode, `ui/confirm` answers `false`, and `ui/select` and `ui/input` are cancelled.

### Lifecycle

- One process per extension, started with the session.
- Hooks and commands have a per-call timeout (`extensions.timeout` in `config.toml`, default 30 seconds; longer for hooks that may wait on the user). Tools run until they finish or the user cancels.
- If an extension crashes, it is marked failed, you are notified, and Rho continues. `/reload` restarts everything.
- There are no per-token or per-frame hooks, which keeps out-of-process extensions cheap.
