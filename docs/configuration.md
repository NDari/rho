# Configuration

Rho reads `~/.rho/config.jsonc` (global) and `.rho/config.jsonc` in the project (the nearest directory with `.git`, or the working directory). Project settings override global ones, except for keys that could redirect your API keys or run code: **providers and `bash.shell` are only honored in the global config**. A project config can add models to a provider configured globally (or a built-in one), but not add a provider or change one's other settings.

The files are JSONC: JSON that also allows `//` and `/* */` comments and trailing commas. Keys are case-sensitive, and Rho warns about a key it doesn't know.

Set `RHO_HOME` to use a directory other than `~/.rho`. `rho init` creates `~/.rho/config.jsonc` with a starting configuration for a local server (it never replaces an existing file).

In the TUI, `/config` changes common settings without editing the file: Up/Down select a setting and Enter cycles it through its usual values (`/config bash.timeout 45` sets any value). The change applies at once and is saved to `~/.rho/config.jsonc`, keeping the file's comments (Rho formats the file as it saves it). `reasoning_effort` applies to the current model and is saved in its entry in its provider's `models` (`default` removes it); for a model that isn't in the global file, it changes only for the session. A value set in the project config still wins the next time Rho starts.

Earlier versions of Rho used TOML files. When it finds a `config.toml` with no `config.jsonc` next to it, it writes `config.jsonc` from it once, warns, and from then on reads only `config.jsonc`. Its `[[models]]` entries move under their providers, and when the file had no `default_model`, it is set to the first model, which was the default. It does the same for TOML themes in `~/.rho/themes/` and `rho-extension.toml` manifests. Comments in the old files are not carried over, and the old files are left in place for you to delete.

## Updates

Rho asks GitHub for the latest release at most once a day, when the TUI or the GUI starts (never in print mode), and shows a banner (TUI) or an offer to update and restart (GUI) when there is a newer one. `rho update` installs it. Set `update_check = false`, or the `RHO_NO_UPDATE_CHECK` environment variable, to stop the check; `rho update` still works. Builds of a modified checkout have no version and never check.

## Example

```jsonc
{
	"default_model": "local",
	"theme":         "system", // a theme name, or "system" (see Themes)
	"max_retries":   3,        // retries for 429/5xx/network errors
	"update_check":  true,     // look for a new Rho release once a day

	"providers": {
		// A local OpenAI-compatible server (llama.cpp, Ollama, LM Studio, vLLM...).
		// llama.cpp's llama-server listens on port 9931 by default (older versions: 8080).
		"local": {
			"base_url": "http://localhost:9931/v1",
			"models": [
				{
					"id":                "local",       // the name you use in Rho (--model, /model)
					"name":              "qwen3-coder", // the model name sent to the server (defaults to id)
					"context_window":    65536,         // local servers often don't report this; set it
					"max_output":        8192,
					"supports_thinking": true,
				},
			],
		},
		// A built-in provider (see Providers) needs only its models. Pricing is for
		// cost tracking (US dollars per million tokens; the numbers here are
		// placeholders, use your provider's prices).
		"openai": {
			"models": [
				{
					"id":               "hosted",
					"name":             "provider-model-name",
					"context_window":   200000,
					"max_output":       32000,
					"supports_images":  true,
					"reasoning_effort": "medium",
					"pricing":          {"input": 1.0, "output": 5.0, "cache_read": 0.1},
				},
			],
		},
	},

	"bash": {
		"timeout":     120, // default timeout in seconds
		"max_timeout": 600, // the most the model may request
	},

	"tui": {
		"side_by_side_width": 160,   // show diffs side by side at this terminal width or more
		"show_thinking":      false, // start with thinking expanded (ctrl+t toggles)
		"mouse_select":       true,  // drag to select transcript text and copy it; false: the terminal's own selection
	},

	"compaction": {
		"auto":        true,  // false: compact only on /compact or when the server says the context is full
		"reserve":     16000, // compact when the context comes within this many tokens of the window
		"keep_recent": 20000, // roughly how many recent tokens to keep verbatim
	},

	"extensions": {
		"timeout":  30, // seconds per hook/command call
		"disabled": [], // extension names not to start
	},

	"tools": {
		"enabled":  ["read", "write", "edit", "bash"], // built-in tools to offer (this is the default)
		"disabled": [],                                // tools never to offer, built-in or from extensions
	},
}
```

## Tools

Rho has seven built-in tools: `read`, `write`, `edit` and `bash`, offered by default, and the read-only search tools `grep`, `find` and `ls`. `grep` and `find` skip `.git` and anything ignored by `.gitignore` or `.ignore`; hidden files are included.

- `tools.enabled` chooses the built-in tools. `rho --tools read,grep` (also with `-p`, and `rho-gui -tools`) replaces it for one run.
- `tools.disabled` removes tools by name, including extension tools, and wins over both. The extension itself keeps running, and `/extensions` marks the tool as disabled. A name that matches no tool gets a warning, so a typo like `bassh` does not leave `bash` on unnoticed.
- Whenever `bash` is not offered, `grep`, `find` and `ls` are added unless disabled. A read-only assistant is `--tools read`. An extension tool with one of those names replaces the built-in one, unless you listed the built-in yourself.
- Side conversations (`/aside`) always get the read-only built-ins that are not disabled.
- A project's `.rho/config.jsonc` can add to `tools.disabled` but cannot set `tools.enabled`.

For example, to also offer the search tools in normal sessions:

```jsonc
{
	"tools": {
		"enabled": ["read", "write", "edit", "bash", "grep", "find", "ls"],
	},
}
```

## Providers

| Field | Meaning |
|---|---|
| `type` | `openai-compat` (Chat Completions, default), `openai-responses` (Responses API) or `anthropic` (Messages API) |
| `base_url` | API root, e.g. `http://localhost:9931/v1` (for `anthropic`, without `/v1`) |
| `api_key_env` | environment variable holding the API key |
| `api_key` | a raw key (works, but Rho warns; prefer `api_key_env`) |
| `flavor` | endpoint-specific handling; `gemini` is detected automatically |
| `headers` | extra HTTP headers |
| `requests_per_minute`, `tokens_per_minute` | wait before sending a request that would exceed these limits (0, the default, means no limit) |
| `debug_log` | a file that receives one JSON line per request with the exact payload and the response; it holds whole conversations, so leave it off normally |
| `stream_idle_timeout` | seconds a response may go without data (including the wait for the first data, while the server reads the prompt) before the request counts as stalled and is retried; 0, the default, means 300, and a negative value turns the check off. Raise it for slow local servers that take minutes to read long prompts |
| `tool_format`, `system_prompt_in`, `decode_entities`, `stop_sequences` | defaults for this provider's models, see below |
| `models` | the models used through this provider, see Models |

Built-in providers you can use without configuring them (set the API key variable):

| Name | Endpoint | Key variable |
|---|---|---|
| `openai` | `https://api.openai.com/v1` | `OPENAI_API_KEY` |
| `openrouter` | `https://openrouter.ai/api/v1` | `OPENROUTER_API_KEY` |
| `gemini` | Google's OpenAI-compatible endpoint | `GEMINI_API_KEY` |
| `anthropic` | `https://api.anthropic.com` (Messages API) | `ANTHROPIC_API_KEY` |

Without an entry in `models` you can still use any model as `provider/name`, e.g. `rho --model openrouter/qwen/qwen3-coder`. Ad-hoc models assume a 128k context window.

## Anthropic

The built-in `anthropic` provider uses the Messages API with a key from the [Claude Console](https://platform.claude.com/settings/keys) in `ANTHROPIC_API_KEY`. Rho never uses a claude.ai sign-in (a Pro or Max subscription can't be used by other tools), and it ignores Anthropic credentials found elsewhere in the environment, so the key in `api_key_env` is the only one sent. List the models you use, with `supports_thinking` so they think and you see summaries of it:

```jsonc
{
	"providers": {
		"anthropic": {
			"models": [
				{
					"id":                "opus",
					"name":              "claude-opus-5-5",
					"context_window":    1000000,
					"max_output":        128000,
					"supports_thinking": true,
					"supports_images":   true,
					"reasoning_effort":  "high",
					"pricing":           {"input": 4.0, "output": 20.0, "cache_read": 0.2, "cache_write": 5.0},
				},
			],
		},
	},
}
```

`reasoning_effort` is sent as the API's effort (`low`, `medium`, `high`, `xhigh` or `max`); without it the model's default applies. Rho caches the system prompt and the conversation so far (5-minute cache), which makes long sessions much cheaper. The prices above are examples; check Anthropic's pricing page.

Any server that speaks the Messages API works with `"type": "anthropic"` and its `base_url`. llama.cpp's `llama-server` serves it at the same port as Chat Completions; give the root without `/v1`, e.g. `"base_url": "http://localhost:9931"`.

## Models

Each provider lists its models in `models`:

| Field | Meaning |
|---|---|
| `id` | the name you use in Rho (`--model`, `/model`, `default_model`); it must be unique across all providers |
| `name` | the model name sent to the API (defaults to `id`) |
| `context_window`, `max_output` | token limits (default 128000 and 16384); local servers often don't report the context window, so set it |
| `supports_images`, `supports_thinking` | what the model can do |
| `reasoning_effort` | `low`, `medium` or `high`, for models that support it |
| `pricing` | US dollars per million tokens (`input`, `output`, `cache_read`, `cache_write`), for cost tracking |
| `tool_format`, `system_prompt_in`, `decode_entities`, `stop_sequences` | override the provider's, see below |

`default_model` picks the model for new sessions. Without it, Rho uses the first model of the first provider by name.

## Models without native tool calling

Some models and gateways don't support tool calls. Set `tool_format = "xml"` on the provider (or on individual models) and Rho describes the tools in the prompt, the model calls them by writing XML blocks, and Rho turns those into ordinary tool calls:

```jsonc
{
	"providers": {
		"gateway": {
			"base_url":            "https://gateway.example.com/v1",
			"api_key_env":         "GATEWAY_API_KEY",
			"tool_format":         "xml",
			"system_prompt_in":    "user", // for gateways that drop or override system messages
			"requests_per_minute": 120,
			"tokens_per_minute":   500000,
		},
	},
}
```

Values are written raw, without JSON escaping, which also makes code edits more reliable for some models that do support native tools.

| Option | Default | Meaning |
|---|---|---|
| `tool_format` | `"native"` | `"xml"` for prompt-based tool calling |
| `system_prompt_in` | `"system"` | `"user"` puts the system prompt and tool instructions at the start of the first user message |
| `decode_entities` | `"off"` | `"auto"` decodes `&lt; &gt; &amp; &quot; &apos;` in values that look fully escaped (for models that escape them anyway, such as Gemma 3); `"always"` decodes every value |
| `stop_sequences` | `true` | sends `stop: ["<tool_result"]` so the server stops before the model invents a tool result; set `false` if a gateway rejects `stop` (Rho still cuts invented results off itself) |

The tool descriptions are part of every request. They sit at the start of the prompt, so prompt caching usually absorbs most of the cost.

## Local models

- llama.cpp's `llama-server` needs `--jinja` for tool calling. It listens on port 9931 by default; versions before the switch use 8080 (or set `--port`).
- Tool-calling quality varies a lot between models; small models often struggle with the `edit` tool's exact matching.
- Smaller context windows mean more frequent compaction. Set `context_window` to the server's actual context size (for llama.cpp, the `-c` value).

## Themes

One theme colors everything: the TUI, the GUI, syntax highlighting, Markdown, diffs and the HTML export. The built-in themes use their projects' published palettes, each with the matching [Chroma](https://github.com/alecthomas/chroma) style for code:

| Dark | Light |
|---|---|
| `gruvbox-dark` | `gruvbox-light` |
| `github-dark` | `github-light` |
| `catppuccin-mocha` | `catppuccin-latte` |
| `tokyo-night` | `tokyo-night-day` |
| `rose-pine` | `rose-pine-dawn` |
| `solarized-dark` | `solarized-light` |
| `kanagawa-wave` | `kanagawa-lotus` |
| `dracula`, `nord`, `one-dark` | |

`theme` names one of them (or one of yours), or is `system` (the default): then `theme_dark` (default `gruvbox-dark`) or `theme_light` (default `github-light`) is used, whichever matches the system. The TUI asks the terminal for its background color when it starts; the GUI follows the system's light or dark mode, also while it runs; an export holds both and follows the reader's system.

`/theme` lists the themes and shows each one as the cursor reaches it; Enter saves it, Esc puts back the theme you had. The TUI paints the terminal's background in the theme's color (and puts the terminal's own back on exit), so a light theme works in a dark terminal. In `/config`, Enter on `theme`, `theme_dark` or `theme_light` opens the same list.

### Your own themes

A theme is a JSONC file in `~/.rho/themes/`, named after the file (`~/.rho/themes/mine.jsonc` is `mine`). It starts from `base` (any theme; `gruvbox-dark` when not set) and changes only what it sets:

```jsonc
{
	"base":   "nord",
	"accent": "#ebcb8b",
	"chroma": "nord",
}
```

Colors are `#rrggbb`, or ANSI 256 numbers such as `"39"` (but a color worked out from others needs hex ones). The roles:

| Role | Used for |
|---|---|
| `background`, `text` | the page and its text (in the TUI, the terminal's background and text) |
| `surface` | code blocks and cards |
| `muted`, `subtle` | secondary text, and fainter text and rules |
| `border`, `accent` | borders, and headings, links and the cursor |
| `user_bg` | your messages and the selected row |
| `error`, `warning`, `success` | errors, warnings and inline code, success marks |
| `tool_name`, `thinking`, `line_number` | tool names, thinking text, diff line numbers |
| `diff_add_bg`, `diff_del_bg` | backgrounds of added and removed lines |
| `diff_add_emph`, `diff_del_emph` | the changed words within them |
| `diff_add_sign`, `diff_del_sign` | the `+` and `-` signs |
| `status_bg`, `status_fg` | the status bar |
| `search_bg`, `search_fg` | search matches |
| `chroma` | the [Chroma style](https://github.com/alecthomas/chroma/tree/master/styles) for code (the file name without `.xml`) |

A role that neither the theme nor its base sets is worked out from the others: `surface`, `user_bg`, `border`, `subtle` and `muted` mix `text` into `background`, the diff colors mix `success` and `error` into it, and `search_fg` is black or white, whichever reads better. The built-in themes set most roles themselves, so a theme that changes `background` over one of them should set its surfaces too. Rho warns about an unknown key, base theme or Chroma style.

## Files Rho reads

| File | Purpose |
|---|---|
| `~/.rho/AGENTS.md`, then every `AGENTS.md` from the project root down to the working directory | instructions added to the system prompt |
| `.rho/SYSTEM.md` or `~/.rho/SYSTEM.md` | replaces the built-in system prompt |
| `~/.rho/APPEND_SYSTEM.md`, `.rho/APPEND_SYSTEM.md` | appended to the system prompt |
| `~/.rho/skills/*/SKILL.md`, `.rho/skills/*/SKILL.md` | skills (name and description are listed in the prompt; the model reads the file on demand) |
| `~/.rho/prompts/*.md`, `.rho/prompts/*.md` | prompt templates, available as `/name args`, with `$ARGS` and `$1`, `$2`... |
| `.rho/COMPACT.md` or `~/.rho/COMPACT.md` | replaces the compaction instructions |
| `~/.rho/themes/*.jsonc` | color themes, see [Themes](#themes) |
| `~/.rho/extensions/`, `.rho/extensions/` | extensions, see [extensions.md](extensions.md) |

Use `/context` to see exactly what is sent to the model and what each part costs.

`--no-project-context` ignores every file in this table that comes from the project (the `.rho/` directory and `AGENTS.md` files) and the project's `.rho/config.jsonc`, for running on a checkout you don't trust. `--system-file <file>` replaces the system prompt and `--append-system-file <file>` adds to it, without putting anything in the project. For example, a read-only pull request reviewer in CI:

```bash
gh pr diff 123 | rho -p --no-project-context --tools read --system-file reviewer.md "Review this pull request."
```

## Data Rho writes

| Path | Contents |
|---|---|
| `~/.rho/sessions/<directory>/*.jsonl` | sessions (append-only) |
| `~/.rho/history.jsonl` | prompt history |
| `~/.rho/trust.json` | approved project extensions |
| `~/.rho/update.json` | when Rho last looked for a new release, and the latest version it found |
| `~/.rho/docs/<version>/` | a copy of the built-in docs, examples and skills for the model to read (rewritten when they change; see `rho docs`) |
