# To do

Gaps in Rho and changes that would make it easier to extend and to use outside the coding TUI and GUI. Nothing here is decided until it moves into the spec ([rho-spec.md](rho-spec.md)). Smaller TUI and GUI ideas live in [feature-requests.md](feature-requests.md).

The short version: much of this already works through headless mode, `RHO_HOME`, `SYSTEM.md`, extensions and the Go library. What was missing was a few control points: which tools are on, where context comes from, and hooks that change what the model sees. Most of them are now built (section 2). All of them are additive. Extensions opt into hooks by name, hooks nobody asked for are never called, and unknown methods return "method not found", so none of this needs a protocol version bump.

## 1. Feature gaps

In order of how much they matter for adoption:

1. **Approval before commands or edits.** Rho never asks before running `bash` or writing files. Many users ask about this first. Keeping core minimal, it can ship as a bundled, opt-in extension with a simple policy (for example, ask before `bash`, or before writes outside the project) that works in both the TUI and GUI. Today it exists only as the `guard.go` example.
2. **Read-only search tools.** **Done:** built-in `grep`, `find` and `ls` (spec §5), offered in side conversations and whenever `bash` is not.
3. **Native Anthropic provider.** Deferred in the spec. Going through an OpenAI-compatible endpoint loses prompt-caching control and thinking.
4. **File checkpoints and a changes view.** Item 1 in [feature-requests.md](feature-requests.md).
5. **MCP.** A non-goal for core, but it is how most internal systems (docs, trackers, GitHub) get connected. A bridge written as an executable extension needs no core change.

Smaller: an optional `powershell` tool so Windows works without Git Bash (as in pi), a note to the model after the third identical tool error in a row, parallel tool calls, a web fetch tool, HTML export, and images in the TUI.

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
- **Following and driving the agent:** `message_end` and `tool_end` notifications, `session/info`, `session/messages`, `agent/set_model`, `agent/cancel` and `agent/compact`.

That covers guards, retrieval, personas, input policies and custom tools.

### What is missing

- **No way to drive Rho from another program** except embedding `core/app` in Go (see section 3).

### Changes, highest value and lowest impact first

| # | Change | Effort | Unlocks |
|---|---|---|---|
| 1 | **Done.** Tool selection: `tools.enabled`, `tools.disabled` and `--tools`, plus the read-only search tools from gap 2 | Small | Read-only reviewers and document assistants, safer CI runs |
| 2 | **Done.** Context control: `--no-project-context` (ignore the project's `.rho/` directory and `AGENTS.md` files) and `--system-file` / `--append-system-file` | Small | Running on untrusted checkouts. With `RHO_HOME`, this works as a profile. |
| 3 | **Done.** Behavior hooks: `before_request` (change the system prompt and messages for that one request, never the session file) and `user_message` (transform or block input) | Small to medium, in `core/agent/agent.go` | Retrieval, memory, personas, policies |
| 4 | **Done.** Following and driving the agent: notifications when a message or tool call ends, plus `session/info`, `session/messages`, `agent/set_model`, `agent/cancel` and `agent/compact` | Small | Logging, auto-continue, reviewers that post their results |
| 5 | **Done.** Self-extension kit: embed `docs/` and `examples/` in the binary, add `rho docs [topic]`, and a built-in skill that tells the model how to write extensions | Small | "Write me an extension that..." working inside Rho |
| 6 | **Done.** Headless results: a final JSON object (answer, usage, cost, stop reason), a non-zero exit code on failure, `--max-steps` | Small | CI and bots |

On looks: extension tools could return display data (markdown, diffs) so they get rich cards in the TUI and GUI. Worthwhile, but lower value than RPC mode.

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

These need a way to drive Rho from outside:

- **Go programs can embed `core/app` today**, as the GUI does, but that API is not documented as stable.
- **Other languages need the external RPC mode** the spec already plans (`rho --mode rpc`): JSON-RPC over stdio carrying the command API and the event stream. Medium effort, and the main thing between Rho and "use it anywhere".

### Design change: profiles

A profile is a named bundle of system prompt, allowed tools, context rules, skills, extensions and model. Today that is `RHO_HOME` (which swaps the whole home) plus `--tools`, `--no-project-context` and `--system-file`, with no new concepts. `~/.rho/profiles/<name>/` and a `--profile` flag could come later. With profiles, Rho is a general AI runtime whose default profile is the coding agent, which keeps the core small.

## Suggested order

1. Changes 1 to 6 are done.
2. RPC mode.
3. Parallel tool calls, then HTML export.
4. The Anthropic provider and the approval extension, in whichever order users need.
