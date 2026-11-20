# Configuration

Rho reads `~/.rho/config.toml` (global) and `.rho/config.toml` in the project (the nearest directory with `.git`, or the working directory). Project settings override global ones, except for keys that could redirect your API keys or run code: **`providers` and `bash.shell` are only honored in the global config**.

Set `RHO_HOME` to use a directory other than `~/.rho`.

In the TUI, `/config` changes common settings without editing the file: Up/Down select a setting and Enter cycles it through its usual values (`/config bash.timeout 45` sets any value). The change applies at once and is saved to `~/.rho/config.toml`, keeping the rest of the file and its comments. A value set in the project config still wins the next time Rho starts.

## Example

```toml
default_model = "local"
theme = "dark"          # "dark" (Gruvbox), "light", or a theme in ~/.rho/themes/
max_retries = 3         # retries for 429/5xx/network errors

# A local OpenAI-compatible server (llama.cpp, Ollama, LM Studio, vLLM...)
[providers.local]
base_url = "http://localhost:8080/v1"

[[models]]
id = "local"             # the name you use in Rho (--model, /model)
name = "qwen3-coder"     # the model name sent to the server (defaults to id)
provider = "local"
context_window = 65536   # local servers often don't report this; set it
max_output = 8192
supports_thinking = true

# A hosted model, with pricing for cost tracking (US dollars per million
# tokens; the numbers here are placeholders, use your provider's prices)
[[models]]
id = "hosted"
name = "provider-model-name"
provider = "openai"
context_window = 200000
max_output = 32000
supports_images = true
reasoning_effort = "medium"
pricing = { input = 1.0, output = 5.0, cache_read = 0.1 }

[bash]
timeout = 120       # default timeout in seconds
max_timeout = 600   # the most the model may request

[tui]
side_by_side_width = 160   # show diffs side by side at this terminal width or more

[compaction]
reserve = 16000      # compact when the context comes within this many tokens of the window
keep_recent = 20000  # roughly how many recent tokens to keep verbatim

[extensions]
timeout = 30         # seconds per hook/command call
disabled = []        # extension names not to start
```

## Providers

| Field | Meaning |
|---|---|
| `type` | `openai-compat` (Chat Completions, default) or `openai-responses` (Responses API) |
| `base_url` | API root, e.g. `http://localhost:8080/v1` |
| `api_key_env` | environment variable holding the API key |
| `api_key` | a raw key (works, but Rho warns; prefer `api_key_env`) |
| `flavor` | endpoint-specific handling; `gemini` is detected automatically |
| `headers` | extra HTTP headers |
| `requests_per_minute`, `tokens_per_minute` | wait before sending a request that would exceed these limits (0, the default, means no limit) |
| `debug_log` | a file that receives one JSON line per request with the exact payload and the response; it holds whole conversations, so leave it off normally |
| `tool_format`, `system_prompt_in`, `decode_entities`, `stop_sequences` | defaults for this provider's models, see below |

Built-in providers you can use without configuring them (set the API key variable):

| Name | Endpoint | Key variable |
|---|---|---|
| `openai` | `https://api.openai.com/v1` | `OPENAI_API_KEY` |
| `openrouter` | `https://openrouter.ai/api/v1` | `OPENROUTER_API_KEY` |
| `gemini` | Google's OpenAI-compatible endpoint | `GEMINI_API_KEY` |

Without a `[[models]]` entry you can still use any model as `provider/name`, e.g. `rho --model openrouter/qwen/qwen3-coder`. Ad-hoc models assume a 128k context window.

## Models without native tool calling

Some models and gateways don't support tool calls. Set `tool_format = "xml"` on the provider (or on individual models) and Rho describes the tools in the prompt, the model calls them by writing XML blocks, and Rho turns those into ordinary tool calls:

```toml
[providers.gateway]
base_url = "https://gateway.example.com/v1"
api_key_env = "GATEWAY_API_KEY"
tool_format = "xml"
system_prompt_in = "user"   # for gateways that drop or override system messages
requests_per_minute = 120
tokens_per_minute = 500000
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

- llama.cpp's `llama-server` needs `--jinja` for tool calling.
- Tool-calling quality varies a lot between models; small models often struggle with the `edit` tool's exact matching.
- Smaller context windows mean more frequent compaction. Set `context_window` to the server's actual context size (for llama.cpp, the `-c` value).

## Files Rho reads

| File | Purpose |
|---|---|
| `~/.rho/AGENTS.md`, then every `AGENTS.md` from the project root down to the working directory | instructions added to the system prompt |
| `.rho/SYSTEM.md` or `~/.rho/SYSTEM.md` | replaces the built-in system prompt |
| `~/.rho/APPEND_SYSTEM.md`, `.rho/APPEND_SYSTEM.md` | appended to the system prompt |
| `~/.rho/skills/*/SKILL.md`, `.rho/skills/*/SKILL.md` | skills (name and description are listed in the prompt; the model reads the file on demand) |
| `~/.rho/prompts/*.md`, `.rho/prompts/*.md` | prompt templates, available as `/name args`, with `$ARGS` and `$1`, `$2`... |
| `.rho/COMPACT.md` or `~/.rho/COMPACT.md` | replaces the compaction instructions |
| `~/.rho/themes/*.toml` | color themes (any field of the built-in theme; `base = "light"` to start from the light theme) |
| `~/.rho/extensions/`, `.rho/extensions/` | extensions, see [extensions.md](extensions.md) |

Use `/context` to see exactly what is sent to the model and what each part costs.

## Data Rho writes

| Path | Contents |
|---|---|
| `~/.rho/sessions/<directory>/*.jsonl` | sessions (append-only) |
| `~/.rho/history.jsonl` | prompt history |
| `~/.rho/trust.json` | approved project extensions |
