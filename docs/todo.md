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

### Rho against pi on local models (measured 2026-11-23 to 25)

Rho and pi ran the same prompts against the same llama-server, with matched settings (output limit, thinking, compaction) and the server restarted before each run. A logging proxy recorded every request. The tasks were scoped changes to a small Go project, 8 runs per tool, graded by hidden tests the model never saw. The model was mostly Qwen 3.6 35B-A3B with thinking off, plus Gemma 3 27B (XML tools).

**Fixed along the way:**
- Whole-file reads have no line numbers; they made each read about 50% more expensive on Gemma.
- `AGENTS.md` goes inside `<project_context>` tags.
- The context count leaves out thinking that is not sent back.
- A final reply cut off at the output limit is reported as cut off.
- The system prompt says to look at files with `read`, not `cat`, `sed` or `head`. Without it, rho's model took about 25% more steps than pi's, mostly `grep -n` and `sed -n` calls to view lines. With it, rho took fewer steps than pi (12.2 against 14.2 requests a run).
- `edit` ignores trailing whitespace when nothing matches exactly. When nothing matches at all, it names the first differing line and shows the file's text to copy. Recovering from a failed edit went from about 3 tool calls to 1.5; pi takes about 3.

After these, both tools got 7 or 8 of 8 runs fully right on each task, with about the same steps, tokens and time.

**Still open:**
1. **No thinking budget.** pi's llama.cpp provider sends `thinking_budget_tokens`; rho can only send `reasoning_effort`. Without a cap, Qwen and Ornith often thought for 20 to 37k tokens in one reply and ran out of room before answering. Today the workaround is the server's `--reasoning-budget`. Options: a per-model `thinking_budget` setting, or per-model extra request fields.
2. **Compaction settings are global.** `compaction.reserve` and `keep_recent` cannot suit a 32K and a 128K model at once.
3. **Edits still fail more often than pi's** (8% against 4% of edits on the last task). Almost all failures are `old_text` the model wrote from memory, which no matcher should accept. Not done: a note when the model resends an `old_text` that just failed (no repeats were seen after the new hint), and pi's normalization of smart quotes, Unicode dashes and spaces.
4. **How to test this again.** The proxy, runner and hidden tests were throwaway scripts. If this is repeated, keep them as a small suite (under `scripts/`, since it needs a live model) and try each change on at least two quite different models before keeping it, so the harness is not tuned to one model. Open-ended prompts ("find the most important bug") were too slow on 128K local models (25 to 45 minutes a run) and varied too much between runs to compare anything. Scoped tasks with hidden tests took 1 to 3 minutes.

Seen in both tools, so model behavior rather than harness: running `go build` in the repository (leaving a binary) despite "do not change any files", changes outside what was asked, and test files left unformatted.

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

### Approval: whether, and in core or as an extension (to discuss)

Not at all: without a sandbox, prompts suggest a safety they do not give (an allowed `bash` call can do almost anything), and the real answer is a container or VM (spec §13); prompts that come often get approved without reading; pi has none; the `guard.go` example already shows how to add one, and headless and RPC mode stay simpler. Against: it is what users ask about first, and its absence may stop some from trying Rho.

For an extension: core stays minimal (as in pi) and costs nothing for users who do not want prompts; policies vary (every `bash`, allowlists, writes outside the project, none in CI) and can be swapped; prompts are not a security boundary without a sandbox anyway.

For core: defaults decide, and an opt-in extension leaves most users without it while they expect what other harnesses do; an extension sees the arguments at its place among the hooks, so a later extension's `before_tool_call` can rewrite them after the user said yes, while core can ask on the final arguments; a good prompt needs core UI work anyway (below); what `-p` and RPC mode do when nobody can answer is core behavior either way.

One shape: the mechanism in core (asking after all hooks, the prompt, "allow for this session", headless defaults) with the policy as a short setting, and extensions still able to block through `before_tool_call`. Whether it is on by default is a separate question.

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

Next steps from this: drop the host's extra JSON passes (internal, no API change); warn about ranging over strings in `extensions.md` and report it to Yaegi; native helpers in `yaegihost.Symbols` for loops over every rune or byte (a token estimator, a text walker); batch streamed chunks for middleware.

### Extension runtimes: Yaegi, TypeScript on goja, executables (to explore)

Every extension feature works over JSON-RPC, so executable extensions can already do everything Go source extensions can. Yaegi is a convenience: no toolchain, a typed `rho` package, notification handlers that cannot deadlock, and source that a trust approval can be read against. `--mode rpc` does not use it at all. The question is whether a third runtime, TypeScript bundled with esbuild and run by goja (both pure Go, so the binary stays static), would add anything. The numbers for goja and esbuild come from the spike below; the other goja points are from its documentation.

| | Yaegi (today) | TypeScript with esbuild and goja | Executable (today) |
|---|---|---|---|
| Libraries | Go standard library only; the host stages just the extension's files, so no third-party modules | Pure-JS npm packages esbuild can bundle from a local `node_modules`; nothing needing Node built-ins or native addons | Anything, in any language, native code included |
| Files, network, processes | Yes, through the standard library | Only what Rho exposes as bindings | Yes |
| Concurrency | Goroutines | One thread per runtime; async needs an event loop from Rho | The language's own |
| Isolation | Separate process, full user permissions | Can be a real sandbox, and a runaway script can be stopped (`Runtime.Interrupt`) | Separate process, full user permissions; can run in a container or as another user |
| What the user installs | Nothing | Nothing (esbuild is built in), unless bundling npm packages | The extension's runtime |
| Author API | The `rho` package | `.d.ts` typings to write; esbuild strips types without checking them | Raw JSON-RPC; no SDKs yet |
| Hook round trip | 38 µs | 1.2 µs in process; out of process, presumably about the same as the others | 32 µs compiled |
| Extension's own loops | 5 to 90 times slower than compiled Go | 130 to 240 times slower than compiled Go (interpreted, no JIT) | Native, or V8's JIT for Node |

Only possible with each:

- **Executables:** native libraries and other ecosystems (Python ML, native tree-sitter, database drivers), full native speed, and running with other privileges than Rho.
- **Yaegi:** the full Go standard library with no toolchain, at compiled speed for library calls, in Rho's own language.
- **TypeScript on goja:** a capability-based sandbox (an extension can do only what Rho exposes), which could let restricted project extensions load without full trust; in-process hooks that cost far less than a process round trip; stopping a stuck hook without stopping Rho (in-process Yaegi has no equivalent); and familiarity for authors coming from pi, whose extensions are TypeScript (they would still need porting, since they use pi's and Node's APIs).

Costs: two dependencies adding 9.8 MB to the stripped binary (about what Yaegi costs; see the spike); a binding layer, typings and an event loop to write and keep up; the sandbox is only as good as the API exposed to it; and run in process, a bug in goja or the bindings can still crash Rho (a host process avoids that but gives up most of the speed). Per-token cost alone is not a reason: the measurements above put a middleware call per streamed token over JSON-RPC at about 0.3% of the time between tokens.

Spike, 2026-11-25 (scratch code, not kept): the `benchwork` workloads ported to TypeScript, bundled by esbuild v0.28.2 (target ES2017) and run in goja (a pseudo-version from 2026-11-25), on one Linux machine. Times per call, multiples against compiled Go:

| Workload | Compiled Go | Yaegi | goja |
|---|---|---|---|
| Stream scan, 4-byte chunks | 0.19 ms | 0.83 ms (4.4x) | 1.23 ms (6.6x) |
| Token estimate, 100k tokens, ranging over the string | 0.72 ms | 971 ms (the quadratic range) | 176 ms (243x) |
| Token estimate, 100k tokens, the fast loop (`[]rune` in Go, `charCodeAt` in JS) | 1.47 ms | 132 ms (90x) | 198 ms (135x) |
| Redact, 100k tokens (regexp) | 14.7 ms | 15.6 ms | 4.0 ms (3.7 times faster than Go's `regexp`) |
| Rewrite messages, 100k tokens | 0.11 ms | 0.67 ms (6x) | 2.7 ms (25x) |
| JSON decode and encode, 100k tokens | 3.75 ms | 3.33 ms | 6.26 ms (1.7x) |

- **Hooks:** a small hook called in process costs 1.2 µs (converting the event in, calling, exporting the response), against 32 to 38 µs for a process round trip. `before_request` with 100k tokens, JSON in and out inside goja, takes 10.1 ms.
- **Go values work as they are:** Go maps passed to goja unconverted (`ToValue`) cost only 10 to 40% more than plain JS objects, so an in-process host would not need JSON.
- **Load:** esbuild bundles the file in 0.7 ms and Yaegi loads the Go version in 2.0 ms. goja's load took 23 ms, all of it from one regexp: `\S` with the `i` flag takes 23 ms to compile, while other patterns take 13 to 34 µs. Worth a note to extension authors, or a fix upstream.
- **Correctness and control:** the ports give the same results as the Go workloads, `\p{L}` with the `u` flag included. `Runtime.Interrupt` stopped an endless loop after 50 ms, and the runtime worked normally afterwards.
- **Binary size** (stripped, `CGO_ENABLED=0`): 66.3 MB today, 76.1 MB with goja and esbuild linked in (+9.8 MB, +15%), and 55.9 MB without Yaegi (Yaegi is 10.4 MB). The build stays statically linked, and esbuild to goja ran inside it.

So speed decides little between the two interpreters, except that goja makes in-process hooks about 30 times cheaper and is much better at regexps. Swapping Yaegi for goja and esbuild would leave the binary about the same size.

Deciding question: is a sandbox for extensions wanted, or is in-process speed needed for something specific? If neither, Yaegi plus executables already covers every capability.

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

### Frontends on the core: a shared command layer (planned), a stable Go API (later)

Anyone can build a frontend on the core today: nothing in `core/` imports a frontend, the GUI is a separate module built only on `core/app`, `core/agent`, `core/events` and helpers, and a new frontend supplies an `events.Sink` and an `ext.UI`. Two things make it harder than it should be.

**Shared command layer (planned).** Slash commands are written once per frontend: about 22 in `tui/commands.go`, and the GUI's equivalents as methods in `gui/bridge/bridge.go`. They can drift apart, and a new frontend rewires each one. A command registry in core (name, description, arguments, handler) would let the TUI, the GUI, RPC mode and extensions run the same commands, and RPC clients would get commands like `/tree`, `/fork` and `/export` without a protocol method for each. Interactive commands (the model picker, tree navigation) return data for the frontend to render, and ask the user through an interface like `ext.UI` when they need a choice. A contained refactor with no change for users; the two existing frontends show whether it works.

**A stable Go API (later, not now).** Rho is pre-1.0 and `core/` changes often, so `core/app` makes no stability promise, and RPC mode stays the stable way to embed Rho. When an outside Go frontend needs one, promise a small facade package instead of freezing `core/`: starting the core, the commands, the event types and the UI interface, under semver. The command layer keeps that facade small.

### Design change: profiles

A profile is a named bundle of system prompt, allowed tools, context rules, skills, extensions and model. Today that is `RHO_HOME` (which swaps the whole home) plus `--tools`, `--no-project-context` and `--system-file`, with no new concepts. `~/.rho/profiles/<name>/` and a `--profile` flag could come later. With profiles, Rho is a general AI runtime whose default profile is the coding agent, which keeps the core small.

## Suggested order

1. Changes 1 to 8 and RPC mode are done.
2. Parallel tool calls (done: consecutive `read`, `grep`, `find` and `ls` calls run together) and HTML export (done: `/export` and `rho export`).
3. The Anthropic provider and the approval extension, in whichever order users need.
