---
name: rho-extensions
description: How to extend or change Rho itself (the harness you are running in) with extensions, hooks, tools, commands, configuration and system prompt files. Read this before writing a Rho extension or changing how Rho behaves.
---

# Extending Rho

You are running inside Rho, a coding agent harness. Its documentation is installed in `{{DOCS}}`:

- `{{DOCS}}/extensions.md`: the extension guide. Go extensions (run by Rho's built-in interpreter, no Go toolchain needed), executable extensions in any language, the JSON-RPC protocol, every hook and what it can return.
- `{{DOCS}}/configuration.md`: `~/.rho/config.toml`, tool selection, and the files Rho reads (`AGENTS.md`, `SYSTEM.md`, `APPEND_SYSTEM.md`, skills, prompt templates, `COMPACT.md`).
- `{{DOCS}}/rho-spec.md`: the design spec, for how things work inside.
- `{{DOCS}}/examples/`: complete example extensions.

`rho docs` prints the same files from the command line.

## Choosing the lightest change

1. **Instructions only:** `AGENTS.md` (project or `~/.rho/`), `APPEND_SYSTEM.md`, or a skill in `~/.rho/skills/<name>/SKILL.md`.
2. **A reusable prompt:** a template in `~/.rho/prompts/<name>.md`, used as `/<name>`.
3. **Settings:** `~/.rho/config.toml` (models, tools, compaction).
4. **Behavior:** an extension. It can add tools and slash commands, block or rewrite tool calls (`before_tool_call`, `after_tool_call`), rewrite or block user messages (`user_message`), change what the model sees on each request (`before_request`), and supply compaction summaries (`before_compaction`).

## Writing an extension

- Read `extensions.md` first; the API is small but exact. Prefer a Go extension: one `.go` file importing `"rho"`, with `//go:build rho` at the top so Go tooling ignores it. Start from an example in `examples/`.
- Put global extensions in `~/.rho/extensions/` (a file or a directory). Project extensions in `.rho/extensions/` need the user's approval.
- After writing it, ask the user to run `/reload`, then `/extensions` to check that it is running and to see its errors and log. Fix what it reports.
