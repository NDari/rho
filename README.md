# Rho

A minimal-core, richly presented coding agent harness, written in Go.

Rho keeps the core small (an agent loop, four tools, a session tree, compaction and context files) and puts the effort into a polished terminal experience: streaming markdown, syntax highlighting, word-level diffs and themes. It ships as a single static binary with no runtime dependencies.

> **Status:** early development. Expect rough edges and breaking changes. The [design spec](docs/rho-spec.md) describes where it is going.

## Install

With Go 1.27 or newer:

```sh
go install github.com/NDari/rho/cmd/rho@latest
```

Release binaries will be published on the GitHub releases page. macOS and Linux are supported; Windows is in beta and needs [Git for Windows](https://git-scm.com/download/win) (Git Bash).

## Quick start

Point Rho at a model in `~/.rho/config.toml`. For a local OpenAI-compatible server such as llama.cpp:

```toml
default_model = "local"

[providers.local]
base_url = "http://localhost:8080/v1"

[[models]]
id = "local"
provider = "local"
context_window = 65536
```

Or use a hosted provider directly by setting its API key, for example `OPENAI_API_KEY`, and running `rho --model openai/<model-name>`.

Then, in your project:

```sh
rho                       # interactive TUI
rho "fix the failing test"  # TUI, starting with a prompt
rho -c                    # continue the last session in this directory
rho -r                    # pick a session to resume
rho -p "summarize README.md"          # print mode: one turn, print the answer
git diff | rho -p "review this diff"  # piped input is added to the prompt
rho -p --json "list the TODOs" > events.jsonl  # stream events as JSON lines
```

See [docs/configuration.md](docs/configuration.md) for all settings.

## Features

- **Interactive TUI** built with Bubble Tea: streaming markdown, collapsible tool cards with live bash output, syntax-highlighted diffs (side by side on wide terminals), transcript search, themes.
- **Headless mode** for scripting and tests.
- **Providers:** any OpenAI-compatible Chat Completions endpoint (OpenAI, llama.cpp, Ollama, LM Studio, vLLM, OpenRouter, Gemini), plus the OpenAI Responses API. Anthropic is planned.
- **XML tool calling** for models and gateways without native tool calls, plus per-provider rate limits. See [docs/configuration.md](docs/configuration.md#models-without-native-tool-calling).
- **Four tools:** `read`, `write`, `edit` (exact replacement, line endings preserved) and `bash` (process tree killed on cancel).
- **Session tree:** sessions are append-only JSONL. Continue, resume, rewind, fork, browse every branch with `/tree`, or start a read-only side conversation with `/aside`.
- **Steering:** while the agent works, Enter interrupts after the current tool and Alt+Enter queues a follow-up.
- **Automatic compaction** when the context window fills up.
- **Transparent context:** `AGENTS.md`, `SYSTEM.md`, skills and prompt templates, with `/context` to see exactly what the model receives.
- **Extensions:** out-of-process, over JSON-RPC. Go source extensions run in a built-in interpreter, so no Go toolchain is needed; any other language works through a small manifest. See [docs/extensions.md](docs/extensions.md).

## Keys

| Key | Action |
|---|---|
| Enter | send (while working: steer) |
| Shift+Enter, Alt+Enter, Ctrl+J | new line (Alt+Enter while working: queue a follow-up) |
| Esc | cancel the current turn |
| Ctrl+C | cancel, clear the input, or press twice to quit |
| Ctrl+O | expand tool output and diffs |
| Ctrl+T | show thinking |
| Ctrl+F | search the transcript |
| PgUp/PgDn, mouse wheel | scroll |

Type `/help` for all commands.

## Security

Rho does not sandbox anything. The model's tools and any extensions run with your user permissions. Use a container or VM for untrusted work. Project-level extensions need your approval (and re-approval when they change), and project config cannot change API endpoints or keys.

## License

MIT
