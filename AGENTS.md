# AGENTS.md

Guidance for coding agents and contributors working on Rho.

## Project

Rho is a minimal-core coding agent harness written in Go. The design spec in [docs/rho-spec.md](docs/rho-spec.md) is the source of truth; read the relevant section before changing behavior, and update the spec when a decision changes.

## Architecture rules

- `core/` is a library with **no UI code**. Frontends (`tui/`, `headless/`) consume the event stream from `core/events` and call the command API.
- Keep package seams clean (see the package layout in the spec, section 3). Do not import `tui/` or `headless/` from `core/`.
- JSON-RPC is only for extensions (and a later external RPC mode), never between core and built-in frontends.
- Session files are append-only JSONL. Never rewrite existing entries.

## Code conventions

- Standard Go style: `gofmt`, `go vet`, idiomatic error handling (wrap with `%w`, no panics in library code).
- Prefer libraries over writing our own code. Widely used, actively maintained, modern libraries are preferred: code used by many has fewer bugs than code written fresh. Adding dependencies is fine when they give a better all-in-one experience or let us write less code.
- Platform-specific code goes in `_unix.go` / `_windows.go` files, not runtime `GOOS` checks scattered through logic.
- No `CGO`. The release binary must stay static, so dependencies must be pure Go.

## Testing

- `go build ./...`, `go vet ./...` and `go test ./...` must pass before a change is considered done.
- Prefer table-driven tests. Tests must not require network access or API keys; use recorded fixtures or a fake provider.
- End-to-end checks run through headless mode (`rho -p --json`).

## Writing style

- Plain hyphens only; no em-dashes or en-dashes in code, comments or docs.
- Examples in docs must use generic values (e.g. `http://localhost:8080/v1`), never real hosts, usernames or personal paths.
- Commit messages: short imperative subject line, optional body explaining why.
