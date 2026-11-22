# To do

Gaps in Rho and changes that would make it easier to extend and to use outside the coding TUI and GUI. Nothing here is decided until it moves into the spec ([rho-spec.md](rho-spec.md)). Smaller TUI and GUI ideas live in [feature-requests.md](feature-requests.md).

The short version: much of this already works through headless mode, `RHO_HOME`, `SYSTEM.md`, extensions and the Go library. What is missing is a few control points: which tools are on, where context comes from, and hooks that change what the model sees. All of them are additive. Extensions opt into hooks by name, hooks nobody asked for are never called, and unknown methods return "method not found", so none of this needs a protocol version bump.

## 1. Feature gaps

In order of how much they matter for adoption:

1. **Approval before commands or edits.** Rho never asks before running `bash` or writing files. Many users ask about this first. Keeping core minimal, it can ship as a bundled, opt-in extension with a simple policy (for example, ask before `bash`, or before writes outside the project) that works in both the TUI and GUI. Today it exists only as the `guard.go` example.
2. **Read-only search tools.** **Done:** built-in `grep`, `find` and `ls` (spec §5), offered in side conversations and whenever `bash` is not.
3. **Native Anthropic provider.** Deferred in the spec. Going through an OpenAI-compatible endpoint loses prompt-caching control and thinking.
4. **File checkpoints and a changes view.** Item 1 in [feature-requests.md](feature-requests.md).
5. **MCP.** A non-goal for core, but it is how most internal systems (docs, trackers, GitHub) get connected. A bridge written as an executable extension needs no core change.

Smaller: an optional `powershell` tool so Windows works without Git Bash (as in pi), a note to the model after the third identical tool error in a row, parallel tool calls, a web fetch tool, HTML export, images in the TUI, and running the GUI on Windows (it is cross-compiled but has never been run there).

## 2. Extensibility

### What exists

Extension tools and commands; the `before_tool_call` and `after_tool_call` hooks (block, rewrite arguments, replace results); `before_compaction`; session and turn hooks; UI requests; `session/append_message`. That covers guards, custom tools and workflows, and an extension can run sub-agents by calling `rho -p`.

### What is missing

- **No hook on what the model sees.** Extensions cannot change the system prompt or the messages of a request. This is the biggest lever: retrieval, memory, redaction, personas, policy.
- **No hook on user input** to transform, block or route it.
- **No view of the conversation.** Extensions do not get assistant messages or tool results as they happen and cannot read the session. `AgentControl` only offers `Busy`, `Prompt` and `FollowUp`.
- **Built-in tools are fixed.** `tools.Builtins()` is always added (`core/app/app.go`), and extensions cannot replace or turn off a built-in tool.
- **Nothing to learn from.** Rho ships no docs, so a model working inside Rho cannot find out how to extend it.

### Changes, highest value and lowest impact first

| # | Change | Effort | Unlocks |
|---|---|---|---|
| 1 | **Done.** Tool selection: `tools.enabled`, `tools.disabled` and `--tools`, plus the read-only search tools from gap 2 | Small | Read-only reviewers and document assistants, safer CI runs |
| 2 | Context control: `--no-project-context` (skip the project's `SYSTEM.md`, `APPEND_SYSTEM.md`, `AGENTS.md`, skills and prompts) and `--system-file` / `--append-system-file` | Small | Running on untrusted checkouts. With `RHO_HOME`, this works as a profile. |
| 3 | Behavior hooks: `before_request` (change the system prompt and messages for that one request, never the session file) and `user_message` (transform or block input) | Small to medium, in `core/agent/agent.go` | Retrieval, memory, personas, policies |
| 4 | Following and driving the agent: notifications when a message or tool call ends, plus `session/info`, `session/messages`, `agent/set_model`, `agent/cancel` and `agent/compact` | Small | Logging, auto-continue, reviewers that post their results |
| 5 | Self-extension kit: embed `docs/` and `examples/` in the binary, add `rho docs [topic]`, and a built-in skill that tells the model how to write extensions | Small | "Write me an extension that..." working inside Rho |
| 6 | Headless results: a final JSON object (answer, usage, cost, stop reason), a non-zero exit code on failure, `--max-turns` | Small | CI and bots |

On looks: extension tools could return display data (markdown, diffs) so they get rich cards in the TUI and GUI. Worthwhile, but lower value than the six above.

## 3. Rho outside the coding workflow

### GitHub reviewer

Mostly possible today: a GitHub Action installs `rho`, sets `RHO_HOME` to a reviewer config, pipes `gh pr diff` in, and posts the review with `gh` or an extension tool such as `post_review_comment` (global extensions load in headless mode). Two weak spots, both fixed by changes 1, 2 and 6:

- **Prompt injection from the pull request.** The checkout's `.rho/SYSTEM.md` replaces the system prompt, and its `AGENTS.md`, skills and prompts load too, without approval. Only project extensions are protected.
- **Unrestricted tools.** The model can edit files and run commands on the runner with whatever token is there.

### Expert on internal documents

Possible today: a `SYSTEM.md` persona, the documents folder as the project, and the GUI for non-technical users. But search needs `bash`, and the assistant cannot be made read-only; change 1 fixes both. For large collections, an extension tool such as `search_docs` backed by an index works today, and change 3 would let it add context automatically.

### Slack, web chat, IDE plugins

These need a way to drive Rho from outside:

- **Go programs can embed `core/app` today**, as the GUI does, but that API is not documented as stable.
- **Other languages need the external RPC mode** the spec already plans (`rho --mode rpc`): JSON-RPC over stdio carrying the command API and the event stream. Medium effort, and the main thing between Rho and "use it anywhere".

### Design change: profiles

A profile is a named bundle of system prompt, allowed tools, context rules, skills, extensions and model. `RHO_HOME` already does this crudely by swapping the whole home. Changes 1 and 2 make it practical with no new concepts; `~/.rho/profiles/<name>/` and a `--profile` flag could come later. With profiles, Rho is a general AI runtime whose default profile is the coding agent, which keeps the core small.

## Suggested order

1. Changes 1 (done) and 2: small, and they make reviewers and document assistants safe.
2. Changes 3 and 4: the behavior hooks.
3. Change 5: the self-extension kit.
4. RPC mode.
5. The Anthropic provider and the approval extension, in whichever order users need.
