# Rho: Design Spec

*A minimal-core, richly presented coding agent harness, written in Go.*

Status: draft v0.1 · License: MIT

---

## 1. Principles

1. **Small core, small concepts.** The harness has few moving parts: an agent loop, a few built-in tools, a session tree, compaction, context files. Everything else is a file, an extension, or a patch to your own copy.
2. **Rich, beautiful UX.** Minimal does not mean bare. Diffs, syntax highlighting, markdown, themes and smooth streaming are first-class. Presentation lives in the frontend, never in the core.
3. **No runtime dependencies.** One static Go binary. No Node, Bun, Deno or Python required, neither for the harness nor for Go extensions.
4. **Transparent context.** The user can always see what goes into the model: system prompt, context files, skills, compaction summaries.
5. **Cheap features are fine; expensive ones must earn their place.**
6. **Public tool, personal first.** Built for daily personal use, but documented and stable enough for others (versioned extension protocol, clear config, MIT license).

## 2. Goals and non-goals

### Goals (v1)
- Interactive TUI (Bubble Tea) with a polished, rich experience.
- Headless mode for scripting and tests.
- Multiple providers, including local models via OpenAI-compatible servers.
- Session tree: continue, resume, rewind (conversation only), fork, side conversations.
- Automatic compaction.
- AGENTS.md / SYSTEM.md context, skills, prompt templates.
- Out-of-process extensions with a built-in Go interpreter host (Yaegi).
- macOS and Linux fully supported; Windows as beta (Git Bash required).

### Non-goals (v1)
- MCP, sub-agents, plan mode, permission prompts, built-in to-dos, background bash. (All possible later as extensions.)
- Subscription / OAuth logins. API keys only.
- Sandboxing. Security parity with pi: Rho runs with the user's permissions (see §13).
- Filesystem rewind. Rewind only ever affects the conversation.
- In-process extensions. Deep customization = editing your copy of Rho.
- A GUI inside the `rho` binary. The GUI is a separate `rho-gui` binary with its own downloads (see §18).

## 3. Architecture

```
            ┌──────────────┐   ┌──────────────┐   ┌──────────────┐
 frontends  │  TUI (Bubble │   │  Headless    │   │  GUI (Wails) │
            │  Tea)        │   │  (print/json)│   │              │
            └──────┬───────┘   └──────┬───────┘   └──────┬───────┘
                   │      Go event interface (in-process) │
            ┌──────┴──────────────────┴──────────────────┴───────┐
 core       │  agent loop · tools · session tree · compaction ·  │
 (library)  │  context builder · provider registry · ext manager │
            └──────┬───────────────────────────────┬─────────────┘
                   │                               │ JSON-RPC over stdio
            ┌──────┴───────┐               ┌───────┴──────────────┐
            │  providers   │               │  extension processes │
            │  (HTTP/SSE)  │               │  (Yaegi host or any  │
            └──────────────┘               │   executable)        │
                                           └──────────────────────┘
```

- The **core is a Go library** with no UI code. Frontends consume a typed **event stream** and call a small **command API** (send message, steer, follow-up, cancel, switch model, checkout entry, etc.).
- The TUI and the GUI both use this Go interface directly (Wails is Go, so no RPC needed).
- **JSON-RPC is used only** for extensions and, later, an optional external RPC mode.
- Transport inside the extension manager is an interface, so an in-memory transport could be added later without protocol changes.

### Package layout (proposal)

```
cmd/rho/            main, flag parsing, subcommands (`rho update`, `rho init`, `rho ext-host`)
core/app/           wires config, sessions, providers, tools, extensions and the agent
core/agent/         agent loop, turn state, steering/follow-up queue, branching
core/tools/         read, write, edit, bash, grep, find, ls (+ platform files)
core/session/       JSONL tree store, context assembly, checkout
core/compact/       compaction policy + summarizer
core/prompt/        system prompt, AGENTS.md, SYSTEM.md, skills, prompt templates
core/provider/      neutral message types; fake/ (tests), registry/
core/provider/openaicompat/  Chat Completions and Responses API adapters
core/provider/xmltools/      XML tool calling for models without native tool calls
core/provider/ratelimit/     requests/tokens per minute limits
core/ext/           extension manager, protocol, trust store; rpc/ (JSON-RPC)
core/ext/yaegihost/ Yaegi-based extension host + `rho` symbol package
core/config/        config loading, merging, validation
core/events/        event types shared by frontends
core/update/        release checks and self-update (§19)
tui/                Bubble Tea app, components, themes, diff renderer
headless/           print and JSON modes
gui/                separate Go module: Wails GUI (`rho-gui`), see §18
gui/frontend/       Svelte + TypeScript web frontend
```

Clean package seams keep personal patches small and easy to rebase.

## 4. Agent loop

- A **turn** = user message → model streams → zero or more tool calls → tool results → model continues → until the model stops calling tools.
- Tool calls within one assistant message execute **sequentially** by default (simple, predictable diffs). Parallel execution can be considered later.
- **Steering message:** sent while the agent is working. Interrupts after the current tool finishes; remaining queued tool calls are skipped with a "skipped by user" result; the steering message is injected and the loop continues.
- **Follow-up message:** queued; delivered when the agent finishes the turn.
- **Cancel (Esc / Ctrl+C):** aborts the stream or kills the running tool (process tree), records a cancelled result, returns control to the user.
- Errors from providers are surfaced as events; transient errors (429, 5xx, network) retry with backoff (configurable, default 3 attempts).
- Context overflow error from provider → compact and retry once (see §8).

## 5. Tools

Seven built-in tools. Four are offered by default, matching pi: `read`, `write`, `edit` and `bash`. The read-only search tools `grep`, `find` and `ls` are offered when `bash` is not (see Tool selection). Each tool returns a model-facing result plus structured **display data** for the frontend (e.g., a diff).

### read
- Params: `path`, optional `offset` (line), `limit` (lines).
- Text files: returns numbered lines; default limit 2000 lines; truncation noted in result.
- Images (png/jpg/gif/webp): returned as image content if the model supports images, otherwise an error message.
- Binary files: refuse with a short description (size, type).

### write
- Params: `path`, `content`. Creates parent directories.
- Display data: diff against previous content (or "new file").
- Writes atomically (temp file + rename) where possible.

### edit
- Params: `path`, `edits: [{old_text, new_text}]`.
- Each `old_text` must match **exactly once**; otherwise fail with a helpful message (0 matches / N matches, with nearby context).
- **Line endings:** match against LF-normalized text; write back preserving the file's original line ending style (LF or CRLF) and trailing newline state.
- Display data: unified diff with intraline (word-level) changes.

### bash
- Params: `command`, optional `timeout` (seconds, default 120, configurable max).
- Runs in the session working directory with the user's environment.
- Output: combined stdout/stderr, streamed to the frontend live.
- Truncation: model receives the **last** 2000 lines / 50 KB (whichever is smaller); full output saved to a temp file whose path is included in the result.
- Cancellation/timeout kills the **whole process tree** (Unix: process group; Windows: Job Object).
- Exit code included in result.

### grep, find, ls
Read-only, written in Go so they behave the same on every platform and need no external programs.
- **What they search:** `grep` and `find` walk the tree in path order and skip `.git` (and `.hg`, `.svn`), paths matched by `.gitignore` and `.ignore` files from the repository root down, and `.git/info/exclude`. Hidden files are included. Symbolic links are never followed. Ignore rules are matched with go-git's gitignore package, globs with doublestar.
- `grep`: params `pattern` (RE2 regular expression), optional `path` (a file or directory), `glob`, `ignore_case`, `literal`, `context` (lines, at most 10), `limit` (matching lines, default 100). Prints `path:line:text`, with `path-line-text` for context lines and `--` between groups. Skips binary files (a NUL byte or invalid UTF-8 in the first 8000 bytes). Lines are cut to 500 characters around the match. Output stops at the limit or 50 KB with a note.
- `find`: params `pattern` (glob), optional `path`, `limit` (default 1000). The glob is matched against paths relative to `path`; a glob without a slash matches names at any depth, as in rg and fd. Directories are listed too, ending in `/`.
- `ls`: params optional `path`, `limit` (default 500). One entry per line, sorted, directories ending in `/` and symbolic links in `@`; nothing is skipped.
- Paths in results are relative to the working directory when inside it.

### Tool selection
- `tools.enabled` lists the built-in tools to offer (default `read`, `write`, `edit`, `bash`); `--tools a,b` replaces it for one run (`rho`, `rho -p` and `rho-gui`). An unknown name is an error at start.
- `tools.disabled` lists tools never to offer, built-in or from extensions, by name. It wins over `tools.enabled` and `--tools`. The extension keeps running (its hooks and commands still work), and `/extensions` marks the tool as disabled. A name that matches no built-in tool and no loaded extension tool gets a warning at start and on `/reload`, since it is usually a typo.
- When `bash` is not offered, `grep`, `find` and `ls` are added unless disabled, so a read-only setup can still search.
- An extension tool may not take the name of a built-in tool that is offered (it is ignored with a warning), with one exception: it replaces a search tool that was only added because `bash` is off, with a notice. A search tool listed in `tools.enabled` or `--tools` stays built-in.
- The built-in system prompt's tool guidelines follow the tools offered.

### Read-only tool set
Used in side conversations (§7.5): the read-only tools offered, plus the read-only built-ins (`read`, `grep`, `find`, `ls`) that are not disabled, even when the main conversation does not offer them. **No bash**, since no shell command can be guaranteed read-only.

### Path handling
- All tools accept absolute or cwd-relative paths.
- On Windows, accept both `C:\x\y` and Git Bash style `/c/x/y`; normalize internally.

## 6. Providers

### Neutral message format
Stored in sessions and used internally:

```
Message  { role: user | assistant | tool_result, content: []Block, meta }
Block    = Text{text}
         | Thinking{text, provider, opaque}               // opaque = signatures etc.
         | Image{mime, data|path}
         | ToolCall{id, name, args, provider, opaque}     // opaque = e.g. Gemini thought signature
         | ToolResult{call_id, content []Block, is_error}
```

`opaque` holds provider-specific data that must be sent back verbatim to the provider that produced it. For example, Gemini 3 and 4 attach a thought signature to function calls, and requests fail if it is not returned within the current turn. It is stored in the session log but never interpreted by the core.

### v1 providers
- **OpenAI-compatible** (Chat Completions) with configurable `base_url`: covers OpenAI itself, llama.cpp `llama-server`, Ollama, LM Studio, vLLM, OpenRouter, Google Gemini's OpenAI-compatible endpoint and similar.
  - Gemini needs tool call thought signatures preserved (`ToolCall.opaque`) and thought summaries requested explicitly (`include_thoughts`).
- **OpenAI Responses API** (provider `type = "openai-responses"`): stateless (`store=false`), with encrypted reasoning items kept in the session as `Thinking.opaque`.
- **Anthropic** (Messages API): deferred until after the OpenAI-compatible path is solid.
- Native Google Gemini API: later, only if the OpenAI-compatible endpoint proves lossy.

### XML tool calling
For models or gateways without native tool calls, `tool_format = "xml"` (on a provider or a model) wraps the adapter:
- Tool definitions and calling instructions are added to the system prompt; no `tools` field is sent. `system_prompt_in = "user"` moves the system prompt into the first user message for gateways that drop system messages.
- The model writes `<tool_call name="...">` blocks with raw (unescaped) values. They are parsed from the stream into ordinary tool calls, so sessions, hooks and the TUI are unaffected, and switching between native and XML models mid-session works.
- Earlier calls and results are replayed as XML text; consecutive user messages are merged.
- A stop sequence (`<tool_result`) ends the response before the model invents a result; if it starts one anyway, the stream is cut there.
- The parser also accepts common fallbacks: child tags, JSON bodies, CDATA, and calls written as ` ```tool_call ` fences. `decode_entities` handles models that XML-escape values.
- Calls that cannot be parsed are answered with an error result showing what was received, so the model can retry.

### Rate limits and debugging
- `requests_per_minute` and `tokens_per_minute` on a provider make requests wait (with a status-bar countdown) until they fit a rolling 60-second window. A 429 is retried up to 5 times with waits sized to the window before the agent's own retry sees it; quota errors are not retried.
- `debug_log` on a provider appends one JSON line per request with the exact payload and the response.

Implementation preference: use the **official Go SDKs** (or a widely used, well-maintained client library) per provider. Hand-written HTTP + SSE clients only where no good library exists.

### Models config
Per model: `id`, `provider`, `context_window`, `max_output`, `supports_images`, `supports_thinking`, optional pricing (for cost display), and the XML tool options (`tool_format`, `system_prompt_in`, `decode_entities`, `stop_sequences`), which default to the provider's. Local servers often don't report context size, so it's always configurable.

### Mid-session switching
- `/model` switches the model for subsequent requests; recorded as a `model_change` entry.
- When switching across providers, opaque data from another provider (on `Thinking` and `ToolCall` blocks) is dropped from the outgoing request (kept in the log). If the target provider requires it (e.g., Gemini signatures on tool calls in the current turn), the adapter substitutes that provider's documented placeholder.

### Local models notes (document for users)
- llama.cpp needs `--jinja` for tool calling.
- Tool-calling quality varies a lot by model.
- Smaller context windows mean more frequent compaction.

### Auth
API keys only: from environment variables, or config entries that reference environment variables (e.g., `api_key_env = "ANTHROPIC_API_KEY"`). Avoid storing raw keys in config; allow it but warn.

## 7. Sessions

### 7.1 Storage
- Location: `~/.rho/sessions/<encoded-cwd>/<timestamp>_<short-id>.jsonl`.
- **Append-only JSONL.** One entry per line. Never rewritten (except explicit delete).

### 7.2 Entry schema

Common fields: `id` (short random), `parent` (id or null), `type`, `ts`.

| type | purpose |
|---|---|
| `header` | first line: version, cwd, created, rho version |
| `message` | a neutral `Message` |
| `model_change` | provider/model switch |
| `compaction` | `summary`, `first_kept` (entry id), token stats |
| `checkout` | moves the current position to `target` (used by rewind / back) |
| `aside_start` | begins a side conversation branching from `parent` |
| `aside_summary` | optional note carried back into the main branch |
| `label` | user bookmark/name for an entry |

### 7.3 Current position
The current leaf is the most recently appended entry, unless a later `checkout` entry moves it. New entries attach to the current leaf, so rewinding then sending a message naturally creates a branch.

### 7.4 Context assembly
Walk from the current leaf to the root. If a `compaction` entry lies on the path, send its summary followed by entries from `first_kept` onward. Side-conversation branches are never on the main path, so they are excluded automatically.

### 7.5 Operations
- **Continue** (`rho -c`): reopen the most recent session for this cwd at its current leaf.
- **Resume** (`rho -r` or `/resume`): pick from a list (title = first user message or label; shows date, model, size).
- **Rewind** (`/rewind`): pick an earlier entry; append `checkout`. Conversation only: show a clear banner that files on disk are **not** reverted.
- **Fork** (`/fork`): create a **new session file** starting from a chosen entry (copies the path), so both sessions continue independently.
- **Tree** (`/tree`): navigate all branches, check out any entry, add labels.
- **Side conversation** (`/aside`, return with `/back`):
  - Appends `aside_start` from the current main leaf. The side thread sees the full main context but uses the **read-only tool set**.
  - Messages in the side thread never join the main branch.
  - `/back` appends a `checkout` to the main leaf. Optionally the user can carry back a short note (`aside_summary`), written by the user or generated on request.
  - Shares the main conversation's prefix, so it should benefit from prompt caching.
- **Export** (later): HTML export of a branch.

## 8. Compaction

- **Automatic.** Triggered when estimated context tokens exceed `context_window − reserve` (default reserve: 16k tokens, constant for now).
- Token estimate: last provider-reported usage + rough estimate (chars/4) for newer entries.
- Process: summarize everything before a cut point, keeping roughly the most recent 20k tokens verbatim (cut at a turn boundary, never inside a tool call/result pair). Append a `compaction` entry.
- The summary is requested at the end of the conversation itself, with the same system prompt and tools, so the provider's prompt cache covers everything except the instruction. If the model calls a tool, returns nothing, or the request overflows, Rho retries once with a plain transcript (long tool results shortened) and shows a notice.
- The summarization prompt can be overridden with `~/.rho/COMPACT.md` (or project `.rho/COMPACT.md`).
- Provider context-overflow error → compact immediately and retry once.
- Output limit: each request sends the model's `max_output`, lowered when the context plus the output would not fit the window: `min(max_output, window - tokens - margin)`, where the margin is `max(4096, half the part counted from characters)`, since characters / 4 undercounts code and tool output. It never goes below 4096 (or `max_output`, if lower); below that the server decides. This only changes requests near the window, so large windows keep their full limit.
- A response cut off at a lowered limit is discarded; Rho compacts and sends the request again, once per turn. This only happens when compaction would leave room for a higher limit (the context is well above what compaction keeps); otherwise, or if compaction has nothing to do, the response is kept with the usual cut-off warning.
- `/compact` manual command: allowed (cheap), even though automatic is the default. `compaction.auto = false` turns automatic compaction off; a request the server rejects as too long is still compacted and retried.
- The original entries stay in the log; compaction only changes what is sent.

## 9. Context: AGENTS.md, SYSTEM.md, skills, templates

- **AGENTS.md:** load `~/.rho/AGENTS.md`, then every `AGENTS.md` from the repo root (or filesystem root) down to cwd. Concatenate in that order with path headers.
- **SYSTEM.md:** `.rho/SYSTEM.md` or `~/.rho/SYSTEM.md` replaces the built-in system prompt; `APPEND_SYSTEM.md` appends to it.
- **Built-in system prompt:** short (target under 1,000 tokens): role, tools, cwd, date, platform/shell notes.
- **Skills:** Agent Skills format (`SKILL.md` with `name` and `description` frontmatter). Discovered from `~/.rho/skills/` and `.rho/skills/`. Only names, descriptions and paths go into the system prompt; the model reads a skill on demand with `read`.
- **Prompt templates:** markdown files in `~/.rho/prompts/` and `.rho/prompts/` become slash commands (`/name args…`), with `$ARGS` / `$1…` substitution.
- `/context` command shows exactly what was loaded and its token cost.

## 10. TUI

Built with Bubble Tea, Bubbles, Lip Gloss, Glamour (markdown) and Chroma (syntax highlighting).

### Layout
- Full-screen: scrollable transcript, input editor at the bottom, status bar.
- Status bar: model, context usage (% and tokens), session cost, cwd/branch, current position (main vs aside).
- Built-in transcript search and copy mode (to make up for leaving terminal scrollback).

### Rendering
- **Markdown:** Glamour, themed. While streaming, render completed blocks once and only re-render the in-progress tail, to avoid re-rendering the whole message per token.
- **Streaming:** batch updates to the frame rate (no per-token redraws).
- **Code:** Chroma highlighting in code blocks, `read` results and diffs.
- **Diffs (edit/write):** unified diff with line numbers, syntax highlighting and word-level intraline changes. Side-by-side when the terminal is wide enough (threshold configurable). Collapsible.
- **Tool calls:** compact cards (tool, args summary, status, duration). Collapsed by default with a one-line preview; expand to see full output. Bash output streams live.
- **Thinking:** shown dimmed and collapsible; toggle to hide.
- **Errors and banners:** clearly styled (e.g., rewind "files not reverted" notice).
- **Themes:** built-in dark (Gruvbox palette, `gruvbox` syntax style) and light (`catppuccin-latte` syntax style); markdown headings, links and inline code take the theme's colors; user themes as files; respect terminal color capabilities.
- Images: later, via Kitty/iTerm2 graphics protocols where supported.

### Input
- Multi-line editor, history, paste handling.
- Completion for `/commands` and `@file` paths (inserts file reference/contents).
- Mouse: the wheel scrolls; dragging selects transcript text and copies it on release (OSC 52, to both the clipboard and the primary selection). On by default; `tui.mouse_select = false` leaves selection to the terminal. Shift+drag always uses the terminal's selection.
- Keys: Enter send · Shift+Enter / Alt+Enter / Ctrl+J newline · Esc cancel · Ctrl+C cancel/clear/quit (twice) · Ctrl+G edit the input in `$VISUAL` or `$EDITOR` (else `vi`, or `notepad` on Windows; editors with arguments such as `code --wait` work; the text comes back unsent, and an empty file or a failed editor leaves the input as it was) · while busy: Enter steers, Alt+Enter queues a follow-up.

### Commands
`/model` · `/new` · `/resume` · `/rewind` · `/fork` · `/tree` · `/aside` · `/back` · `/compact` · `/context` · `/extensions` · `/reload` · `/theme` · `/config` · `/keybindings` · `/help` · `/quit`, plus prompt templates and extension commands.

`/config` lists the settings that can change while Rho runs (`default_model`, `reasoning_effort` for the current model, `theme`, `tui.show_thinking`, `tui.mouse_select`, `tui.side_by_side_width`, `compaction.auto`, `compaction.reserve`, `compaction.keep_recent`, `max_retries`, `bash.timeout`, `bash.max_timeout`). Up/Down select, Enter moves the selected setting to its next value; `/config KEY VALUE` sets any value. Changes apply at once and are saved to the global config by editing just that line (per-model settings in the model's `[[models]]` entry), so comments and layout are kept.

## 11. Headless mode

- `rho -p "prompt"`: run one turn (with tools), print the final assistant text.
- `rho -p --json "prompt"`: stream core events as JSONL to stdout.
- Works with `-c` / `-r`, `--model` and `--tools`.
- Used heavily for automated tests.
- External JSON-RPC mode (`rho --mode rpc`) is a later addition.

## 12. Extensions

### 12.1 Layers of customization
1. **Files and config:** skills, prompts, AGENTS.md, SYSTEM.md, themes, models.
2. **Extensions:** always a **separate process**, speaking JSON-RPC over stdio.
3. **Editing Rho itself:** for anything deeper.

### 12.2 Kinds
- **Go source extensions** (`*.go` or a directory): run by the built-in host: Rho re-executes itself as `rho ext-host <path>`, which embeds Yaegi. No Go toolchain or other runtime needed.
- **Executable extensions:** any program (compiled Go, Python, TS, Rust…) declared in a small manifest (`rho-extension.toml`: name, command, args). The runtime is the extension's own dependency, never Rho's.

### 12.3 Discovery and trust
- Global: `~/.rho/extensions/`, loaded automatically.
- Project: `.rho/extensions/`, requires **one-time approval**, keyed to a **content hash** of the extension's files. Changes (e.g., after `git pull`) require re-approval.
- Trust store: `~/.rho/trust.json`.

### 12.4 Protocol (JSON-RPC 2.0, newline-delimited, over stdin/stdout)
- **Handshake:** `initialize` with `protocol_version`; extension replies with name, version and registrations. Version mismatch → refuse with a clear message.
- **Registrations:**
  - **Tools:** name, description, JSON Schema params. Rho calls `tool/execute`; extension may stream progress.
  - **Commands:** slash commands with description; Rho calls `command/run`.
  - **Hooks** (events): `session_start`, `before_tool_call` (may block or modify args), `after_tool_call` (may modify result), `turn_end`, `before_compaction` (may supply its own summary), `session_end`.
- **Extension → Rho requests:** `ui/confirm`, `ui/select`, `ui/input`, `ui/notify`, `ui/status` (status-bar text), `session/append_message` (inject a message), `log`.
- No per-token or per-frame hooks (keeps out-of-process cheap).
- UI requests are deliberately simple so any frontend (TUI, headless, GUI) can render them; headless mode answers them with defaults or fails them.

### 12.5 Lifecycle
- Started at session start; one process per extension, running in the session working directory (`RHO_EXTENSION_DIR` points to the extension's own directory; manifests may use `${dir}`).
- Per-call timeouts (configurable).
- Crash → extension marked failed, user notified, Rho continues. `/reload` restarts all extensions (this is "hot reload").
- `/extensions` lists extensions; Enter turns one on or off without restarting the others and saves the choice in `extensions.disabled` in the global config. Tab shows status, registrations and errors.

### 12.6 Yaegi host API
A tiny `rho` package exposed to interpreted code: `RegisterTool`, `RegisterCommand`, `On(event, fn)`, `UI.Confirm/Select/Input/Notify/Status`, `Log`. Interpreted code also gets the Go standard library. No broader SDK for now.

## 13. Security model

- Parity with pi: **no sandbox**. Tools and extensions run with the user's permissions. Documented prominently; recommend containers/VMs for untrusted work.
- No permission prompts in core; an extension can add them via `before_tool_call`.
- Project extensions require hash-based approval (§12.3).
- **Project config is restricted:** `.rho/config.toml` may not set provider endpoints (`base_url`), API key sources, extension commands or `tools.enabled`, so a cloned repo can't redirect your API keys, run code without approval or turn tools back on. It may add to `tools.disabled`. Those keys are only honored from global config (or after the project is trusted).

## 14. Configuration

- Global: `~/.rho/config.toml`; project: `.rho/config.toml` (restricted keys, see §13). Project overrides global.
- Contents: default model, providers, models, theme, keybindings (not applied yet, see Later), bash timeout defaults, tool selection (§5), compaction reserve (internal for now), side-by-side diff width, `update_check` (§19).
- `rho init` writes a starting `~/.rho/config.toml` for a local llama.cpp server (`http://localhost:9931/v1`, llama-server's default port; older versions use 8080) and never replaces an existing file. When no model is configured, `rho` prints the same example and points at `rho init`; the GUI's start-up screen offers to create the file.
- Data directories via Go's standard per-OS config/cache locations where appropriate; `~/.rho/` as the user-facing home.

## 15. Platforms

- **macOS, Linux:** fully supported.
- **GUI (`rho-gui`):** loads the system webview at runtime: WebView2 on Windows, WKWebView on macOS, GTK 4.8+ and WebKitGTK 6.0 (2.40+) on Linux. `rho` itself needs none of these.
- **Release builds:** Linux amd64 and arm64, Windows amd64, macOS arm64 (§19).
- **Windows (beta):** requires Git Bash.
  - Locate Git Bash from standard install locations, then PATH; config override. Never use `C:\Windows\System32\bash.exe` (that's WSL).
  - Process trees killed with Job Objects (`golang.org/x/sys/windows`).
  - Path normalization (§5), CRLF handling (§5 edit).
  - Clear errors for locked files.
  - Child processes (bash commands, extensions) start with `CREATE_NO_WINDOW`, so `rho-gui`, which has no console, does not open a console window for each.
  - Windows CI runner from day one.

## 16. Milestones

1. **M0 - Skeleton:** repo, packages, config loading, event types, CI (macOS/Linux/Windows).
2. **M1 - Core loop:** neutral messages, OpenAI-compatible provider (Chat Completions), four tools, headless print/JSON mode, fake provider for tests, cancel.
3. **M2 - Sessions & base context:** JSONL tree store, context assembly, continue/resume, built-in system prompt, AGENTS.md, SYSTEM.md.
4. **M3 - Compaction:** automatic compaction, compact-and-retry on context overflow, `COMPACT.md` override.
5. **M4 - TUI:** transcript, streaming markdown, tool cards, diffs, input editor, status bar, themes, steering/follow-up, `/compact`.
6. **M5 - Skills & templates:** skills, prompt templates, `/context`.
7. **M6 - Branching:** rewind, fork, tree view, side conversations.
8. **M7 - Extensions:** protocol, manager, trust store, Yaegi host, executable extensions.
9. **M8 - Polish & Windows beta:** OpenAI Responses API, model switching UX, cost tracking, docs, packaging/releases.
10. **M9 - GUI:** separate `rho-gui` binary with Wails (§18): transcript, tool cards and diffs, sessions and branching, settings, extensions, release builds.
11. **M10 - Release cleanup (next):**
    - Done: the release set (§19), with every published file in `checksums.txt`; update checks, `rho update` and the GUI's update-and-restart; `rho init` and the GUI's create-config button; no console windows from `rho-gui` on Windows; separate `rho` and GUI downloads in each release (§19). (The GUI spent v0.4.0 in its own repository and came back for v0.5.0.)
    - Speed up the `gui` CI job (about 7 minutes): cache the `wails3` tool build used for bindings, and check what else the job rebuilds each run.
    - Test `rho-gui` on Windows (only cross-compiled so far), including the update and the absence of console windows.
    - Releases stay unsigned for now. macOS signing and notarization start working once the secrets in `docs/releasing.md` are added; Windows signing is not set up.
12. **Later:** external RPC mode, HTML export, images in TUI, Anthropic, native Gemini API, extension SDK, in-memory extension transport, optional compaction settings, custom key bindings (`tui.keybindings` is parsed but not applied yet; `/keybindings` lists the fixed bindings).

## 17. Decisions on former open questions

- Default theme palette: Gruvbox for dark; the light theme keeps `catppuccin-latte` highlighting.
- `/fork` stays in the current working directory.
- Side-conversation summaries stay opt-in (`/back --summarize`).

## 18. GUI

A desktop frontend built with [Wails](https://wails.io) v3. It is a frontend like `tui/`: it drives `core/app` and the agent directly and renders the `core/events` stream. No JSON-RPC is involved.

### Binary and module
- The GUI is a **separate binary, `rho-gui`**, in its own Go module under `gui/` (with a `replace` pointing at the root module). The root module, `go build ./...` and the `rho` binary never depend on Wails or GTK.
- Keeping it out of `rho` matters: linking Wails loads GTK and WebKitGTK when the program starts, even if no window is opened, and needs those libraries on the machine. `rho` must stay a static binary that runs on servers without a desktop.
- `rho-gui` is built with Wails' experimental pure-Go backend (`CGO_ENABLED=0`, `-tags purego,production`), so every platform builds and cross-compiles without a C toolchain. It is not fully static: it loads the system webview at runtime (§15). If the pure-Go backend misbehaves, dropping the tag selects Wails' cgo backend with the same code.

### Structure
- **Go bridge** (`gui/`): creates the App, forwards each event to the frontend as JSON (`events.Marshal`) over Wails events, exposes the command API (send, steer, follow-up, cancel, model, sessions, branching, compaction, settings, extensions) as bound service methods, and implements the extension UI (`Confirm`, `Select`, `Input`, `Notify`, `SetStatus`) as modal dialogs answered by the frontend.
- **Frontend** (`gui/frontend/`): Svelte + TypeScript, built with Vite; the built assets are embedded in the binary.
- **Shared view helpers** in `core/app` (transcript rebuilding, message lists for rewind and fork, the session tree, `@file` attachments and completion) keep session logic out of both frontends, so the TUI and GUI only differ in presentation.

### Behavior
- Same capabilities as the TUI (§10): streaming markdown and thinking, tool cards with live output, diffs from the structured display data, status bar (model, context, cost, position), steering and follow-ups, the slash commands, prompt templates and extension commands.
- Keys follow the TUI where it makes sense: Enter sends, Shift+Enter adds a line, while working Enter steers and Alt+Enter queues a follow-up, Esc cancels.
- Light and dark themes; the GUI follows the system setting unless the user picks one.
- Ctrl+F searches the rendered transcript (what is on screen; collapsed tool output is not searched).
- The window can switch to another project directory (`/open`, a native folder dialog) with a new session, without restarting.
- When the project cannot be opened (no model configured, a broken config file), the window still opens and shows the error, an example configuration and a way to try again, since a GUI started from a desktop launcher has no terminal for the message. Without a config file it offers to create one from the example.
- Release builds offer a newer version in a notice (§19), from the GUI's own downloads: **Update and restart** installs it and starts the new `rho-gui` with the same folder and session (`-session`, once the session has a file); it is disabled while the agent works. **Later** hides it until the next start.
- Go extensions run as `<host> ext-host <file>`. `rho-gui` prefers a `rho` binary next to itself (or on `PATH` with the same version) as the host, because starting `rho-gui` again would load the GUI libraries in every extension process; it falls back to itself. The GUI archives and `Rho.app` include `rho` for this reason.
- Packaging: the icon comes from `gui/build/appicon.svg`. The application ID `io.github.ndari.rho` names the Linux `.desktop` file (GTK 4 has no per-window icons, so Wayland desktops take the icon from it) and the macOS bundle. Releases add Windows resources (icon, high-DPI manifest, version information) and an Apple silicon `Rho.app` for macOS, signed and notarized when Apple credentials are configured.

## 19. Releases and updates

### Release set
- Every release has `rho` on its own for each platform, for general use: `rho_<version>_linux_amd64.tar.gz`, `rho_<version>_linux_arm64.tar.gz`, `rho_<version>_darwin_arm64.tar.gz` and `rho_<version>_windows_amd64.zip`.
- Next to them, the GUI downloads, for those who want it: `rho-gui_<version>_linux_amd64.tar.gz`, `rho-gui_<version>_linux_arm64.tar.gz` (both with the desktop entry and icon) and `rho-gui_<version>_windows_amd64.zip` hold `rho-gui` and `rho`; on macOS (Apple silicon only) `Rho_<version>_macos.zip` holds `Rho.app` with both programs. The macOS archive `Rho.app` is built from goes only to the job that builds it and is not published.
- Both programs always have the same version.
- `checksums.txt` covers every published file. goreleaser creates the release as a draft; the `macos-app` job adds `Rho.app` and its checksum, then publishes it, so a published release is always complete.

### Update checks
- `core/update` asks the GitHub API for the latest release (drafts and pre-releases excluded) at most once a day and keeps the answer in `~/.rho/update.json`. A `Product` names the archives and programs to update: the default is `rho` alone (`rho_*` archives); `rho-gui` passes its own (`rho-gui_*` archives and `Rho.app`, with `~/.rho/update-gui.json`). Frontends show a newer version from that file at once and check again in the background.
- The TUI shows a banner at start-up: "Rho update to version X is available! Quit and run 'rho update' to update to the latest version." The GUI shows the notice in §18. Headless mode never checks.
- Checks are off with `update_check = false` (also in `/config`) or `RHO_NO_UPDATE_CHECK`. Builds without a version (`dev`, or a modified checkout) never check. A `go install` copy, recognized by the module version in its build information, is told to run `go install github.com/NDari/rho/cmd/rho@latest` instead; the GUI only updates release builds.

### Installing an update
- `rho update` (and the GUI) downloads its product's archive for this platform, or the product's macOS app when the program runs from an app bundle, and checks it against `checksums.txt` before changing anything. The `rho` inside `Rho.app` is updated with the app, not on its own.
- It replaces the running program and the product's other programs that are next to it (it does not add missing ones), using `go-selfupdate`'s `update` package to swap files, which also works for a running `.exe` on Windows. An app bundle is replaced as a whole.
- When the folder is not writable, the update goes to `~/.local/bin` (`~/Applications` for an app, `%LOCALAPPDATA%\Programs\Rho` on Windows) without `sudo`, and the result says to add that folder to `PATH`, or that the old copy comes first on `PATH`. The old copy is left alone.
