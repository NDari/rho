# Extensions

Extensions add tools, slash commands and hooks to Rho. Every extension runs as a **separate process** and talks to Rho with JSON-RPC 2.0 over stdin/stdout. Extensions run with your user permissions, like Rho itself.

There are two kinds:

- **Go source extensions:** a `.go` file, or a directory of `.go` files, run by Rho's built-in interpreter (`rho ext-host`, based on Yaegi). No Go toolchain is needed.
- **Executable extensions:** any program, described by a `rho-extension.toml` manifest. The program's runtime (Python, Node, a compiled binary...) is the extension's own dependency.

**Letting the model write them.** This guide, the configuration guide, the spec and the examples are built into `rho`: `rho docs` lists them and `rho docs extensions` prints this one. Rho also installs them in `~/.rho/docs/<version>/` and offers the model a built-in `rho-extensions` skill that points there, so asking Rho to "write an extension that..." works without anything else. A skill of your own named `rho-extensions` replaces the built-in one.

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

Import the `rho` package and register things in `main`. A `//go:build rho` line at the top is optional: Rho's interpreter sets the `rho` build tag, so the line keeps `go build`, `go vet` and gopls from treating the file as part of a Go module it sits in, without changing how Rho runs it.

```go
//go:build rho

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
| `RegisterTool(Tool)` | add a tool; `Tool.ReadOnly` tools are also offered in side conversations. Calls to extension tools run one at a time, never in parallel. A tool may not use the name of a built-in tool that is offered, except a search tool (`grep`, `find`, `ls`) added only because `bash` is off, which it replaces. `tools.disabled` can turn it off |
| `RegisterCommand(name, description, func(args string) error)` | add a slash command |
| `On(event, func(Event) Response)` | handle a hook (see [the hooks](#rho-to-extension)). `Event` has `Tool`, `Args`, `Result`, `IsError` (tool hooks, `tool_end`), `Transcript`, `Text` (`user_message`), `System`, `Messages`, `Tools` and `Aside` (`before_request`), `EntryID` and `Message` (`message_end`); `Response` has the matching `Block`, `Args`, `Result`, `Summary`, `Text`, `System`, `Messages` and `Tools` |
| `Info(name, version)` | set the extension name and version |
| `UI.Confirm(title, message) bool` | ask yes/no |
| `UI.Select(title, options) (string, bool)` | pick one option |
| `UI.Input(title, placeholder) (string, bool)` | ask for text |
| `UI.Notify(text)` | show a message |
| `UI.Status(text)` | set this extension's status bar text |
| `Log(format, args...)` | write to the extension log |
| `AppendMessage(text, trigger)` | send a user message to the agent |
| `Session.Info() (SessionInfo, error)` | the session id, file, working directory, model, whether a turn runs, context size and cost |
| `Session.Messages() ([]map[string]any, error)` | the current branch, each `{"id": ..., "message": ...}` |
| `Agent.SetModel(model) error`, `Agent.Cancel() error`, `Agent.Compact() error` | switch models, stop the turn, compact now |
| `Agent.SetTools(names) error` | change the tools offered (nil: the configured set) |
| `Supports(name) bool` | whether this Rho has a hook or method, such as `"before_request"` or `"agent/set_tools"` (answers once Rho has connected: in hooks, tools and commands, not in `main`) |

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
{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocol_version":1,"rho_version":"0.1.0","cwd":"/path/to/project",
  "hooks":["session_start","user_message","before_request",...],"methods":["log","ui/confirm","agent/set_tools",...]}}
```

`hooks` and `methods` list the hooks this Rho can call and the methods it serves, so an extension can check for a feature instead of guessing from the version (older versions of Rho send neither). Go extensions use `rho.Supports(name)`.

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
| `hook/user_message` | `text` | `{"block": "reason"}` to stop the message, `{"text": "..."}` to replace its text, or `{}` |
| `hook/before_request` | `system`, `messages`, `tools` (names offered), `aside` | `{"system": "..."}`, `{"messages": [...]}` and/or `{"tools": [...]}` to change them for this request only, or `{}`. `tools` can only narrow the set |
| `hook/before_tool_call` | `tool`, `args` | `{"block": "reason"}` to stop the call, `{"args": {...}}` to replace arguments, or `{}` |
| `hook/after_tool_call` | `tool`, `args`, `result` | `{"result": {...}}` to replace the result, or `{}` |
| `hook/turn_end` | | `{}` |
| `hook/before_compaction` | `transcript` | `{"summary": "..."}` to supply the summary, or `{}` |
| `hook/session_end` | | `{}` |
| `hook/message_end` (notification) | `entry_id`, `message` | none: a message was saved (the user's, the model's or a tool result) |
| `hook/tool_end` (notification) | `tool`, `args`, `result` | none: a tool call finished, with the arguments it ran with and its final result (blocked and failed calls too) |
| `shutdown` | | `{}`; then stdin is closed |

Content blocks may also be images: `{"type": "image", "mime": "image/png", "data": "<base64>"}`.

**Changing what the model sees.** `user_message` runs on every user message before it is saved: typed, queued (steering and follow-ups, joined into one) and appended by extensions. The text it returns is what is saved and shown; attachments are kept. A blocked message is not saved or sent, and Rho shows a notice with the reason.

`before_request` runs before every model request of a turn, side conversations included, but not before compaction. `messages` are in the session file's format (`{"role": "user", "content": [{"type": "text", "text": "..."}]}`, with roles `user`, `assistant` and `tool_result`). What it returns is sent once and never saved, so use it for retrieved context, reminders, redaction or a persona that should not become part of the history. Rho re-pairs tool calls with their results afterwards and counts what you add toward the output limit. Adding to the end of the system prompt or after the last message keeps the provider's prompt cache; changing earlier messages does not.

With several extensions, hooks run in turn, each seeing the previous one's changes.

**Hooks fail closed.** A hook that returns an error, times out, sends a reply that does not parse or belongs to a crashed extension never lets the action go ahead unchecked: `user_message` blocks the message, `before_request` fails the turn, `before_tool_call` blocks the call (the model gets an error result naming your extension), `after_tool_call` replaces the result with an error, and `before_compaction` fails the compaction. `session_start`, `turn_end` and `session_end` have nothing to stop, so their failures are only shown as a notice. To fall back to what Rho would do without your extension, catch your own errors and answer `{}`.

**Changing the tools.** `agent/set_tools` changes the tools for the rest of the session, which is how a "plan mode" extension works: a `/plan` command sets `["read", "grep", "find", "ls"]`, and `/act` sends no `tools` to go back. `tools` in `before_request` narrows one request instead. Either way the system prompt's tool list is rebuilt to match (unless `before_request` also replaced the system prompt), and a call to a tool that was not offered is refused. `session/info` reports the tools currently offered.

**Following the conversation.** `message_end` and `tool_end` are notifications: Rho sends them in order without waiting, so an extension can log, count or react without slowing the agent, and a reply is ignored. Each extension has a queue of 256; if it falls that far behind, notifications are dropped and the log says so. In an executable extension, do not call Rho (for example `session/messages`) on the thread that reads stdin while handling a notification, or the reply can never be read; Go extensions need not care, since Rho runs their notification handlers separately. To act on the agent, an extension can read `session/info` and `session/messages`, and call `agent/set_model`, `agent/cancel` and `agent/compact`.

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
| `session/info` | | `{"id", "file", "cwd", "model", "busy", "aside", "context_tokens", "context_window", "cost"}` |
| `session/messages` | | `{"messages": [{"id": "<entry id>", "message": {...}}]}`: the current branch as the model sees it (after compaction, the summary first) |
| `agent/set_model` | `model` (an id or `provider/name`) | `{}`; an error while a turn runs or for an unknown model |
| `agent/set_tools` | `tools` (names), or none to go back to the configured set | `{}`; the tools offered from the next request on, until changed or Rho exits. Any configured tool or built-in tool may be named, but never one in `tools.disabled`. Rho shows a notice with the new set |
| `agent/cancel` | | `{}`; stops the current turn, if any |
| `agent/compact` | | `{}` when done; an error while a turn runs |
| `log` (notification) | `level`, `message` | |

In headless mode, `ui/confirm` answers `false`, and `ui/select` and `ui/input` are cancelled.

### Lifecycle

- One process per extension, started with the session.
- Hooks and commands have a per-call timeout (`extensions.timeout` in `config.toml`, default 30 seconds; longer for hooks that may wait on the user). Time a `ui/confirm`, `ui/select` or `ui/input` prompt is open does not count, so a prompt can wait for the user as long as it takes. Tools run until they finish or the user cancels.
- A prompt opened while Rho waits for a hook, command or tool is closed (answered as dismissed) when that call ends, so it cannot be answered after the call was decided. The TUI and GUI take it off the screen and say why: the turn was cancelled (by the user, the RPC client or an extension, named), the extension stopped, or it replied without waiting for the answer. A tool call cancelled while its `before_tool_call` hook waits never runs, and its result says who cancelled it.
- If an extension crashes, it is marked failed, you are notified (with the last lines it wrote to stderr), and Rho continues. Rho restarts it after a second, at most once a minute; if it crashes again sooner it stays failed until `/reload`, which restarts everything. Hooks called during the restart wait for the new process; while it stays failed, its hooks fail (see above) until `/reload` or until you turn it off in `/extensions`. A restarted extension is a fresh process, so it gets `session_start` again and keeps no state from before the crash.
- There are no per-token or per-frame hooks, which keeps out-of-process extensions cheap.
