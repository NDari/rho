# To do

Gaps in Rho and changes that would make it easier to extend and to use outside the coding TUI and GUI. Nothing here is decided until it moves into the spec ([rho-spec.md](rho-spec.md)). Smaller TUI and GUI ideas live in [feature-requests.md](feature-requests.md).

The short version: much of this already works through headless mode, `RHO_HOME`, `SYSTEM.md`, extensions and the Go library. What was missing was a few control points: which tools are on, where context comes from, and hooks that change what the model sees. Most of them are now built (section 2). All of them are additive. Extensions opt into hooks by name, hooks nobody asked for are never called, and unknown methods return "method not found", so none of this needs a protocol version bump.

## 1. Feature gaps

In order of how much they matter for adoption:

1. **Approval before commands or edits.** Rho never asks before running `bash` or writing files. Many users ask about this first. Not decided: whether Rho should have it, and if so whether in core or as a bundled, opt-in extension with a simple policy (for example, ask before `bash`, or before writes outside the project) that works in both the TUI and GUI. Today it exists only as the `guard.go` example. As an extension it would also need [prompts that show what is asked](#prompts-that-show-what-is-asked-to-discuss); [failing hooks block](#hooks-fail-closed-done) either way.
2. **Read-only search tools.** **Done:** built-in `grep`, `find` and `ls` (spec §5), offered in side conversations and whenever `bash` is not.
3. **Native Anthropic provider.** Deferred in the spec. Going through an OpenAI-compatible endpoint loses prompt-caching control and thinking.
4. **File checkpoints and a changes view.** Item 1 in [feature-requests.md](feature-requests.md).
5. **MCP.** A non-goal for core, but it is how most internal systems (docs, trackers, GitHub) get connected. A bridge written as an executable extension needs no core change.

Smaller: an optional `powershell` tool so Windows works without Git Bash (as in pi), a note to the model after the third identical tool error in a row, a web fetch tool, and images in the TUI.

## 2. Extensibility

### What exists

- **Tools and commands** from extensions, and tool selection: `tools.enabled`, `tools.disabled` and `--tools`. An extension tool can replace a search tool that was only added because `bash` is off.
- **Hooks** on every stage:
  - `user_message` rewrites or blocks input before it is saved.
  - `before_request` changes the system prompt and messages of one request without touching the session.
  - `before_tool_call` and `after_tool_call` block calls, rewrite arguments and replace results.
  - `before_compaction` supplies a summary.
  - Session and turn hooks.
- **Context control:** `--no-project-context`, `--system-file` and `--append-system-file`, which with `RHO_HOME` work as a profile.
- **UI requests and `session/append_message`.** An extension can run sub-agents by calling `rho -p`.
- **Self-extension:** the docs, examples and a `rho-extensions` skill are built in (`rho docs`, `~/.rho/docs/`).
- **Following and driving the agent:** `message_end` and `tool_end` notifications, `session/info`, `session/messages`, `agent/set_model`, `agent/set_tools`, `agent/cancel` and `agent/compact`; `before_request` can also narrow the tools of one request.
- **Capabilities:** `initialize` lists the hooks and methods this Rho supports.

That covers guards, retrieval, personas, input policies and custom tools.

### What is missing

Compared with what pi offers extensions:

- **No model calls.** An extension that needs the model (to classify, summarize or run a small sub-agent) has to start `rho -p`.
- **Plain results.** Extension tools return text, while built-in tools get diff and code cards.
- **Smaller:** no hooks for model changes, session switches (new, resume, fork), turn start or compaction end; no per-extension settings in `config.toml`; no provider registration or provider-level hooks (see [provider requests](#provider-requests-to-discuss)); no key bindings or CLI flags.

Replacing a built-in tool already works: disable it in `tools.disabled` and an extension can register a tool with its name (a sandboxed `bash`, for example).

### Hooks fail closed (done)

Hooks used to be advisory: a hook that failed, timed out or sent a bad reply was skipped, and the action went ahead as if it had answered `{}`. An extension failing open is a bug, whatever the extension is for: it registered the hook to be consulted, and skipping it is silent and surprising (a sandboxing `before_tool_call` that fails would run the command on the host; a redacting `before_request` that fails would send what it should have removed). Now in spec §12.4 and §12.5:

- `user_message` blocks the message, `before_request` fails the turn, `before_tool_call` blocks the call, `after_tool_call` replaces the result with an error, `before_compaction` fails the compaction. `session_start`, `turn_end` and `session_end` only report.
- A failed extension's hooks fail until `/reload` or until it is turned off; during the automatic restart they wait for the new process.
- No opt-out (such as `"advisory": true`) for now; it is easy to add later and hard to take back.

Separate, not decided: noticing that an extension you rely on is not loaded at all (turned off, project not approved, `--no-project-context`). It never fails, so failing closed does not cover it; something like `extensions.required` would.

### Prompts that show what is asked (to discuss)

`ui/confirm` has a title and a message but only answers yes or no; `ui/select` has more answers but only a title, no message. An approval prompt needs both: the command, or the path and the diff, to show, and more than two answers (yes, yes for the rest of the session, no). A write outside the project should also look different from one inside it. Options:

- add an optional `message` to `ui/select`, shown above the options in the TUI and GUI;
- add a `level` (for example `warning`) to `ui/confirm` and `ui/select`, so a risky prompt stands out;
- or add a dedicated `ui/approve` request with structured fields (kind, command, path, inside or outside the project, diff) that the frontends render as an approval card.

### Provider requests (to discuss)

The built-in rate limiter (`core/provider/ratelimit`) was used as a test of what extensions can see. As an extension it could hold requests in `before_request` (its timeout is 10 times `extensions.timeout`, so waits of a minute fit), learn real usage from `message_end`, and show the wait with `ui/status`. It could not match the built-in one:

- **Requests that skip the hooks:** the agent's retries (`streamWithRetry`) run after `before_request`, and compaction requests do not run it at all. The limiter wraps the provider, so it sees every request.
- **No provider results:** an extension never sees a 429, a status code or an error kind, so it can neither wait for the window nor tell a rate limit from an exhausted quota. `message_end` has usage only for saved assistant messages, not for compaction or failed attempts.
- **No headers:** `x-ratelimit-remaining-*` and `retry-after` are not exposed, and an extension cannot add request headers (proxies, tracing, per-request auth).
- **No settings:** an extension has no section in `config.toml`, so the limits could not stay next to the provider.

pi exposes most of this (`before_provider_request`, `before_provider_headers`, `after_provider_response` with status and headers, `provider_stream_event`, `registerProvider`). Ideas, in order of value for the work:

1. **A notification when each attempt ends** (like `message_end` and `tool_end`): provider, model, attempt number, purpose (turn, side conversation, compaction), status, error kind (rate limited, quota, context overflow, network, other), `retry_after`, usage, duration and the `x-ratelimit-*` headers. Enough on its own for cost and usage tracking, 429 alerts and adaptive limits.
2. **A `before_send` hook on each attempt,** with the same fields and an estimated token count, at the provider seam where `ratelimit.Wrap` is, so retries and compaction pass through it. Replying later delays the attempt; `{"block": "reason"}` stops it. Items 1 and 2 are one design (an attempt and its outcome) and close the gap the limiter showed.
3. **Per-extension settings,** for example `[extensions.settings.<name>]`, passed in `initialize` and perhaps declared there so `/config` can list them. Any extension with options needs this.
4. **Request headers** as an `{"headers": {...}}` result of `before_send`, rather than another hook.
5. **Providers written as extensions** (`provider/stream`): the most power and the most work (streaming over JSON-RPC, cancellation, usage). It would also let the deferred native Anthropic provider ship as an extension.

The same test against other built-ins: approval needs [hooks that fail closed](#hooks-fail-closed-done) and [prompts that show what is asked](#prompts-that-show-what-is-asked-to-discuss); plan mode (`agent/set_tools`) and custom compaction (`before_compaction`) work today; file checkpoints mostly work with `before_tool_call` and `tool_end` but miss what `bash` changes (a limit for core too); "a turn needs you" notifications have `turn_end` but nothing for a prompt waiting on the user (pi has `ui_prompt_start` and `ui_prompt_end`).

### Provider middleware (to review with the extension API)

The same test with XML tool calling (`core/provider/xmltools`, which also wraps the provider) goes further than the rate limiter. On the request it adds instructions built from the tool schemas, sends no native tools, replays earlier calls and results as XML, can move the system prompt into the first user message and adds a stop sequence. On the streamed reply it splits text from `<tool_call>` blocks, emits tool-call events as they arrive (so tool cards show live), types the arguments by schema, decodes HTML entities and stops the request when the model starts writing a tool result itself.

`before_request` covers appending the instructions and rewriting the history. Already in the provider-requests list: per-model settings (item 3) and a hook on every attempt (item 2, which would also need to change the request, not only delay or block it). New:

- **Tool schemas:** `before_request` gets tool names only, so instructions cannot be written and arguments cannot be typed, and other extensions' tools are unknown.
- **Native tools off but callable:** an empty `tools` result means no change, and a tool that was not offered is refused.
- **An empty system prompt:** an empty `system` result means no change. Small.
- **Request parameters** such as stop sequences. Small.
- **Changing the reply before it is saved and before tools run:** `message_end` is a notification after the save. The heart of it.
- **Changing the reply as it streams,** emitting tool-call events and stopping early while keeping what came. Providers written as extensions (item 5) would cover this only by reimplementing the OpenAI adapter.

The rate limiter needs to watch an attempt; XML tools need to sit in the middle of it. Both point at one mechanism: **provider middleware**. An extension registers to wrap the provider for some models; Rho gives it the full request (tool schemas and parameters included); the extension calls back (for example `provider/stream`) to run it on the real provider, gets the stream events and sends its own back, including tool calls and "stop". Rate limits, headers, redaction, caching and routing between models fit the same shape, and it would cover items 1, 2 and 4 above.

Costs to weigh: a JSON-RPC round trip per streamed token, and parsing in a Go extension runs under yaegi, so XML tools themselves should stay in core. Middleware pays off for transforms that touch a request or reply less often than per token.

### What extensions cost (measured)

Benchmarks: `go test -run '^$' -bench . ./core/ext/yaegihost/ ./core/ext/` (the same workloads compiled and under Yaegi, and hooks end to end through each kind of extension process; see the comments in the two `bench_test.go` files). Findings on one Linux machine, 2026-11-22:

- **Calls into the standard library run compiled:** JSON and regexp work is about as fast under Yaegi. The extension's own loops are 5 to 90 times slower (about 90 when each step calls a function, such as `unicode.IsLetter` per rune).
- **Ranging over a string is quadratic in Yaegi v0.16.3** (the latest release): a token count over 100k tokens takes 1 s instead of 0.7 ms. Ranging over `[]rune(s)` is linear. Yaegi v0.16.3 also lacks the `min` and `max` builtins.
- **Hooks end to end:** a small round trip costs 38 µs for a Go extension and 32 µs for a compiled one, mostly the process boundary. `before_request` with 100k tokens costs 6.2 ms (Go) against 3.9 ms (compiled); most of that gap is the host converting messages through JSON twice more each way (`yaegihost/host.go`), not interpretation.
- **Against real requests** (local qwen sessions: median 4.9 s per request, about 59 tokens per second), today's hooks cost under 1% of a request, and a middleware call per streamed token would be about 0.3% of the time between tokens.

Next steps from this: drop the host's extra JSON passes (internal, no API change); warn about ranging over strings in `extensions.md` and report it to Yaegi; native helpers in `yaegihost.Symbols` for loops over every rune or byte (a token estimator, a text walker); batch streamed chunks for middleware. Comparing with Sobek (a JavaScript interpreter) would only change the interpreted-loop numbers; estimated at half a day as a scratch test, on hold.

### Changes, highest value and lowest impact first

| # | Change | Effort | Unlocks |
|---|---|---|---|
| 1 | **Done.** Tool selection: `tools.enabled`, `tools.disabled` and `--tools`, plus the read-only search tools from gap 2 | Small | Read-only reviewers and document assistants, safer CI runs |
| 2 | **Done.** Context control: `--no-project-context` (ignore the project's `.rho/` directory and `AGENTS.md` files) and `--system-file` / `--append-system-file` | Small | Running on untrusted checkouts. With `RHO_HOME`, this works as a profile. |
| 3 | **Done.** Behavior hooks: `before_request` (change the system prompt and messages for that one request, never the session file) and `user_message` (transform or block input) | Small to medium, in `core/agent/agent.go` | Retrieval, memory, personas, policies |
| 4 | **Done.** Following and driving the agent: notifications when a message or tool call ends, plus `session/info`, `session/messages`, `agent/set_model`, `agent/cancel` and `agent/compact` | Small | Logging, auto-continue, reviewers that post their results |
| 5 | **Done.** Self-extension kit: embed `docs/` and `examples/` in the binary, add `rho docs [topic]`, and a built-in skill that tells the model how to write extensions | Small | "Write me an extension that..." working inside Rho |
| 6 | **Done.** Headless results: a final JSON object (answer, usage, cost, stop reason), a non-zero exit code on failure, `--max-steps` | Small | CI and bots |
| 7 | **Done.** Tool control at runtime: `tools` in `before_request` (narrow one request) and `agent/set_tools` (change the set for the session) | Small | Plan mode and other modes as extensions |
| 8 | **Done.** Capabilities: the hooks and methods this Rho supports, in `initialize` | Tiny | Extensions that work across Rho versions |
| 9 | Model calls from extensions: `model/complete` | Small to medium | Classifiers, summarizers and small sub-agents without `rho -p` |
| 10 | Rich results: display data (diff, code, markdown) for extension tools | Medium | Cards for extension tools like the built-ins get |
| 11 | Smaller: hooks for model changes, session switches, turn start and compaction end; per-extension settings in `config.toml`; provider registration; key bindings and CLI flags | Small each | Finer control, company gateways |

Change 9 is next.

## 3. Rho outside the coding workflow

### GitHub reviewer

Possible today, with nothing missing in Rho. A GitHub Action installs `rho` and runs it on the pull request:

```bash
gh pr diff "$PR" | rho -p --json --no-project-context --tools read --system-file reviewer.md --max-steps 30 "Review this pull request." | tail -n 1 > result.json
```

- `--no-project-context` keeps the checkout's `SYSTEM.md`, `AGENTS.md`, skills, prompts and extensions from steering the model.
- `--tools read` makes the run read-only, with `grep`, `find` and `ls` for searching.
- `--max-steps` bounds the cost.
- The result line gives the answer, status and cost, and the exit status fails the job when the turn does not finish.
- The review is posted with `gh`, or by a global extension tool such as `post_review_comment`.

What is left is packaging: an example workflow and a reviewer prompt, perhaps as a published Action.

### Expert on internal documents

Possible today: the documents folder as the project, a persona in `--system-file` or `SYSTEM.md`, `--tools read` for a read-only assistant that can search, and the GUI for non-technical users. For large collections, an extension can add an index:
- as a `search_docs` tool the model calls;
- or in `before_request`, retrieving context for each question automatically.

### Slack, web chat, IDE plugins

These drive Rho from outside:

- **Go programs can embed `core/app` today**, as the GUI does, but that API is not documented as stable.
- **Other languages use RPC mode** (done): `rho --mode rpc` speaks JSON-RPC on stdio, with the command API as requests and the event stream as notifications ([rpc.md](rpc.md)). A bot or plugin starts `rho` as a child process.

### Design change: profiles

A profile is a named bundle of system prompt, allowed tools, context rules, skills, extensions and model. Today that is `RHO_HOME` (which swaps the whole home) plus `--tools`, `--no-project-context` and `--system-file`, with no new concepts. `~/.rho/profiles/<name>/` and a `--profile` flag could come later. With profiles, Rho is a general AI runtime whose default profile is the coding agent, which keeps the core small.

## Suggested order

1. Changes 1 to 8 and RPC mode are done.
2. Parallel tool calls (done: consecutive `read`, `grep`, `find` and `ls` calls run together) and HTML export (done: `/export` and `rho export`).
3. The Anthropic provider and the approval extension, in whichever order users need.
