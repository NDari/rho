# AGENTS.md

Guidance for coding agents and contributors working on Rho.

## Project

Rho is a minimal-core coding agent harness written in Go. The design spec in [docs/rho-spec.md](docs/rho-spec.md) is the source of truth; read the relevant section before changing behavior, and update the spec when a decision changes.

## Architecture rules

- `core/` is a library with **no UI code**. Frontends (`tui/`, `headless/`, `rpcmode/`, `gui/`) consume the event stream from `core/events` and call the command API.
- Keep package seams clean (see the package layout in the spec, section 3). Do not import `tui/`, `headless/` or `gui/` from `core/`.
- `gui/` is a separate Go module that builds the `rho-gui` binary (spec section 18). Never import Wails or `gui/` from the root module.
- JSON-RPC is only for extensions and RPC mode (`rpcmode/`, for other programs), never between core and built-in frontends.
- Session files are append-only JSONL. Never rewrite existing entries.

## Code conventions

- Standard Go style: `gofmt`, `go vet`, idiomatic error handling (wrap with `%w`, no panics in library code).
- Prefer libraries over writing our own code. Widely used, actively maintained, modern libraries are preferred: code used by many has fewer bugs than code written fresh. Adding dependencies is fine when they give a better all-in-one experience or let us write less code.
- Platform-specific code goes in `_unix.go` / `_windows.go` files, not runtime `GOOS` checks scattered through logic.
- No `CGO`. The `rho` binary must stay static, so dependencies must be pure Go. `rho-gui` is also built with `CGO_ENABLED=0` (Wails' pure-Go backend) but loads the system webview at runtime.

## Testing

- `go build ./...`, `go vet ./...` and `go test ./...` must pass before a change is considered done. `task test` runs the tests of both modules (`gui/` needs `-tags purego`).
- Prefer table-driven tests. Tests must not require network access or API keys; use recorded fixtures or the fake provider (`core/provider/fake`).
- Optional integration tests run against a real server when `RHO_TEST_BASE_URL` (Chat Completions) or `RHO_TEST_RESPONSES_URL` (Responses API) is set, with `RHO_TEST_MODEL` and `RHO_TEST_API_KEY` as needed.
- End-to-end checks run through headless mode (`rho -p --json`).
- `integration/` holds cross-cutting tests: the real config, provider, agent, tools and session files, with `rho -p --json` output checked, against `core/provider/replay`, a server that replays responses recorded from real servers (`core/provider/replay/recordings`) and injects faults (dropped connections, streams cut short, stalls, error replies). Some build and run the `rho` binary to interrupt or kill it mid-turn. Add a scenario there when fixing a failure seen in real use, and prefer recorded responses to hand-written ones.

## Writing style

- Plain hyphens only; no em-dashes or en-dashes in code, comments or docs.
- Examples in docs must use generic values (e.g. `http://localhost:9931/v1`), never real hosts, usernames or personal paths.
- Commit messages: short imperative subject line, optional body explaining why.
