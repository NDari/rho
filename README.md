# Rho

A minimal-core, richly presented coding agent harness, written in Go.

Rho keeps the core small (an agent loop, four tools, a session tree, compaction and context files) and puts the effort into a polished terminal experience: streaming markdown, syntax highlighting, word-level diffs and themes. It ships as a single static binary with no runtime dependencies.

> **Status:** early development. Nothing is usable yet; see the [design spec](docs/rho-spec.md) for what is planned.

## Highlights (planned)

- **Interactive TUI** built with Bubble Tea, plus a headless mode for scripting.
- **Multiple providers:** OpenAI and any OpenAI-compatible server (llama.cpp, Ollama, LM Studio, vLLM, OpenRouter, Gemini), with Anthropic to follow.
- **Session tree:** continue, resume, rewind, fork and side conversations, stored as append-only JSONL.
- **Automatic compaction** when the context window fills up.
- **Transparent context:** `AGENTS.md`, `SYSTEM.md`, skills and prompt templates, with `/context` to see exactly what the model receives.
- **Out-of-process extensions** over JSON-RPC, including Go source extensions run by a built-in interpreter (no Go toolchain needed).
- **Platforms:** macOS and Linux, with Windows in beta (requires Git Bash).

## Security

Rho does not sandbox anything. The model's tools and any extensions run with your user permissions. Use a container or VM for untrusted work.

## License

MIT
