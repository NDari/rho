# RPC mode

`rho --mode rpc` lets another program drive Rho: a chat bot, a web backend, an editor plugin or a test harness, in any language. The program starts `rho --mode rpc` as a child process and talks to it with JSON-RPC 2.0, one JSON object per line, on Rho's stdin and stdout. It sends commands as requests and receives every event as a notification. Stderr is free text (warnings, errors) and is not part of the protocol.

The usual flags apply: `-m`, `-c`, `--session`, `--tools`, `--no-extensions`, `--no-project-context`, `--system-file`, `--append-system-file` and `--max-steps`. Rho runs in the directory it was started in.

```bash
rho --mode rpc --tools read --no-project-context
```

## A session

```text
-> {"jsonrpc":"2.0","id":1,"method":"initialize"}
<- {"jsonrpc":"2.0","id":1,"result":{"protocol_version":1,"rho_version":"0.6.1","session":{...}}}
-> {"jsonrpc":"2.0","id":2,"method":"prompt","params":{"text":"What does main.go do?"}}
<- {"jsonrpc":"2.0","id":2,"result":{}}
<- {"jsonrpc":"2.0","method":"event","params":{"type":"turn_start"}}
<- {"jsonrpc":"2.0","method":"event","params":{"type":"text_delta","text":"It starts"}}
   ...
<- {"jsonrpc":"2.0","method":"event","params":{"type":"turn_end","reason":"done"}}
-> {"jsonrpc":"2.0","id":3,"method":"wait"}
<- {"jsonrpc":"2.0","id":3,"result":{"status":"done","answer":"It starts ...","steps":2,...}}
-> {"jsonrpc":"2.0","id":4,"method":"shutdown"}
<- {"jsonrpc":"2.0","id":4,"result":{}}
-> {"jsonrpc":"2.0","method":"exit"}
```

`prompt` returns as soon as the turn starts; the answer arrives as events, and `wait` returns when the turn is over. Send `wait` after the reply to `prompt`, since requests are handled concurrently.

## Client to Rho

| Method | Params | Result |
|---|---|---|
| `initialize` | | `{"protocol_version": 1, "rho_version", "session": {...}, "methods": [...]}`; optional. `methods` lists the requests this Rho serves |
| `prompt` | `text` | `{}` once the turn starts; an error while a turn runs. `@file` references are attached as in the TUI |
| `steer` | `text` | `{}`; interrupts the running turn after the current tool |
| `follow_up` | `text` | `{}`; sent when the running turn finishes |
| `cancel` | | `{}`; stops the running turn, if any |
| `wait` | | when no turn runs: `{"status", "error", "answer", "model", "steps", "usage", "cost", "session"}` for the last turn, as in the last line of `rho -p --json` |
| `session/info` | | `{"id", "file", "cwd", "model", "busy", "aside", "context_tokens", "context_window", "cost", "tools"}` |
| `session/messages` | | `{"messages": [{"id": "<entry id>", "message": {...}}]}`: the current branch as the model sees it |
| `session/new` | | the new session's info; an error while a turn runs |
| `set_model` | `model` (an id or `provider/name`) | `{}`; an error while a turn runs |
| `set_tools` | `tools` (names), or none for the configured set | `{}`; the tools offered from the next request on (never one in `tools.disabled`) |
| `compact` | | `{}` when done; an error while a turn runs |
| `shutdown` | | `{}` after stopping the turn and the extensions |
| `exit` (notification) | | Rho exits. Closing stdin does the same |

`status` is `done`, `error`, `cancelled`, `blocked` (an extension blocked the prompt) or `max_steps`. Messages use the session file's format: `{"role": "user", "content": [{"type": "text", "text": "..."}]}`, with roles `user`, `assistant` and `tool_result`.

## Rho to client

| Method | Params | Answer |
|---|---|---|
| `event` (notification) | one event, as a line of `rho -p --json` (`{"type": "text_delta", ...}`) | |
| `ui/confirm` | `title`, `message` | `{"confirmed": true}` |
| `ui/select` | `title`, `options` | `{"value": "...", "cancelled": false}` |
| `ui/input` | `title`, `placeholder` | `{"value": "...", "cancelled": false}` |
| `ui/notify` (notification) | `level`, `text` | |
| `ui/status` (notification) | `key`, `text` (empty clears) | |

The `ui/` requests come from extensions. A client without a user interface can answer them with an error (for example "method not found"): Rho then treats a confirm as no and a select or input as cancelled, as in headless mode.

Event types are those of the core event stream: `turn_start`, `turn_end` (`reason`: `done`, `cancelled`, `blocked` or `error`; `unanswered` holds the prompt text when the turn ended before anything was saved after it, in which case the conversation was moved back to before the prompt and the next `prompt` replaces it), `message_start`, `text_delta`, `thinking_delta`, `tool_call_delta`, `message_end` (the saved message), `tool_start`, `tool_output`, `tool_end`, `usage`, `notice`, `error`, `retry`, `compaction_start`, `compaction_end`, `model_changed`, `session_changed`, `status` and `queue`. New types may be added; ignore the ones you do not know.

## Embedding in Go

A Go program can also embed Rho without a child process: `core/app` creates the session, agent and extensions, `Agent` runs turns, and an `events.Sink` receives the events. The GUI is built this way. That API is not yet promised to stay stable; RPC mode is.
