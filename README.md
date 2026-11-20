# Rho

A minimal-core, richly presented coding agent harness, written in Go.

Rho keeps the core small (an agent loop, four tools, a session tree, compaction and context files) and puts the effort into a polished terminal experience: streaming markdown, syntax highlighting, word-level diffs and themes. It ships as a single static binary with no runtime dependencies.

> **Status:** early development. Expect rough edges and breaking changes. The [design spec](https://github.com/NDari/rho/blob/main/docs/rho-spec.md) describes where it is going.

## Install

### From a release

Download the file for your platform from the [releases page](https://github.com/NDari/rho/releases/latest):

| Platform | File |
|---|---|
| Linux (x86-64 or ARM64) | `rho_<version>_linux_<arch>.tar.gz` |
| macOS (Apple silicon) | `rho_<version>_darwin_arm64.tar.gz` |
| Windows (x86-64) | `rho_<version>_windows_amd64.zip` |

Unpack it and put `rho` in a folder on your `PATH`, for example `~/.local/bin`.

For a desktop app, see [Rho GUI](https://github.com/NDari/rho-gui), which is released on its own.

macOS and Linux are supported; Windows is in beta and needs [Git for Windows](https://git-scm.com/download/win) (Git Bash).

### Updating

Rho looks for a new release once a day. When there is one, the TUI shows a banner when it starts. To update, quit Rho and run:

```sh
rho update
```

This replaces `rho` where it is. If that folder is not writable, the new version goes to `~/.local/bin` instead (`%LOCALAPPDATA%\Programs\Rho` on Windows), and Rho tells you if you need to add that folder to your `PATH`. To turn the daily check off, set `update_check = false` in `~/.rho/config.toml` or set the `RHO_NO_UPDATE_CHECK` environment variable.

### With Go

With Go 1.27 or newer:

```sh
go install github.com/NDari/rho/cmd/rho@latest
```

This puts `rho` in Go's `bin` folder (`go env GOPATH`, then `bin`), which needs to be on your `PATH`. Run the same command again to update; `rho update` reminds you of it.

### Working on Rho

The repository has a Taskfile for [Task](https://taskfile.dev):

| Task | What it does |
|---|---|
| `task tui` | builds `bin/rho` and starts the TUI in the folder you ran `task` from |
| `task build` | builds `bin/rho` |
| `task install` | installs `rho` into Go's `bin` folder |
| `task test` | runs the tests |

Arguments after `--` go to `rho`, for example `task tui -- -c`. To try your build on another project, run `task -d path/to/rho tui` from that project.

## Quick start

Point Rho at a model in `~/.rho/config.toml`. `rho init` creates the file with this example for a local OpenAI-compatible server such as llama.cpp's `llama-server` (port 9931 by default; older versions use 8080):

```toml
default_model = "local"

[providers.local]
base_url = "http://localhost:9931/v1"

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
- **Desktop GUI** ([Rho GUI](https://github.com/NDari/rho-gui), early, released separately): the same sessions and commands in a desktop window.
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
