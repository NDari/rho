package config

// Field describes a config key for a settings form (the GUI's settings
// view): what it holds and how to show it.
type Field struct {
	// Key is the key's path, dotted ("bash.timeout"). For provider and
	// model fields it is the key inside the provider or model.
	Key         string `json:"key"`
	Label       string `json:"label"`
	Description string `json:"description,omitempty"`
	// Kind is "string", "int", "number", "bool", "choice" (one of
	// Choices), "list" (strings), "headers" (string to string) or
	// "pricing".
	Kind    string   `json:"kind"`
	Choices []string `json:"choices,omitempty"`
	// Section groups top-level fields.
	Section string `json:"section,omitempty"`
	// Advanced fields are shown on request.
	Advanced bool `json:"advanced,omitempty"`
	// GlobalOnly fields are ignored in a project config.
	GlobalOnly bool `json:"global_only,omitempty"`
	// Default is the value that applies when the key is not set, when
	// the merged config does not show it (a nil pointer).
	Default any `json:"default,omitempty"`
}

// Sections of FileFields, in display order.
var Sections = []string{"General", "Appearance", "Tools", "Compaction", "Extensions", "Terminal"}

// FileFields are the top-level settings, by section. Providers and their
// models have their own fields (ProviderFields, ModelFields), and
// tui.keybindings is edited in the file.
var FileFields = []Field{
	{Key: "default_model", Label: "Default model", Kind: "choice", Section: "General",
		Description: "The model for new sessions. Without one, Rho uses the first model of the first provider by name."},
	{Key: "max_retries", Label: "Retries", Kind: "int", Section: "General",
		Description: "How many times to retry a request after a rate limit, server error or dropped connection."},
	{Key: "update_check", Label: "Check for updates", Kind: "bool", Section: "General", Default: true,
		Description: "Look for a new Rho release once a day."},

	{Key: "theme", Label: "Theme", Kind: "choice", Section: "Appearance",
		Description: "The color theme, or system to follow the system's light or dark mode."},
	{Key: "theme_dark", Label: "Dark theme", Kind: "choice", Section: "Appearance",
		Description: "The theme used with system when the system is dark."},
	{Key: "theme_light", Label: "Light theme", Kind: "choice", Section: "Appearance",
		Description: "The theme used with system when the system is light."},
	{Key: "tui.show_thinking", Label: "Show thinking", Kind: "bool", Section: "Appearance",
		Description: "Start with the model's thinking expanded."},

	{Key: "tools.enabled", Label: "Built-in tools", Kind: "list", Section: "Tools", GlobalOnly: true,
		Choices:     []string{"read", "write", "edit", "bash", "grep", "find", "ls"},
		Description: "The built-in tools offered to the model. Unset means read, write, edit and bash; grep, find and ls are added whenever bash is not offered."},
	{Key: "tools.disabled", Label: "Disabled tools", Kind: "list", Section: "Tools",
		Description: "Tools never to offer, built-in or from extensions. This wins over the built-in tools list."},
	{Key: "bash.timeout", Label: "Bash timeout", Kind: "int", Section: "Tools",
		Description: "The default timeout of a bash command, in seconds."},
	{Key: "bash.max_timeout", Label: "Longest bash timeout", Kind: "int", Section: "Tools",
		Description: "The longest timeout the model may ask for, in seconds."},
	{Key: "bash.shell", Label: "Shell", Kind: "string", Section: "Tools", GlobalOnly: true, Advanced: true,
		Description: "The shell the bash tool runs, instead of the one found automatically (useful on Windows)."},

	{Key: "compaction.auto", Label: "Compact automatically", Kind: "bool", Section: "Compaction", Default: true,
		Description: "Summarize older turns when the context nears the model's limit. Off: only on /compact, or when the server says the context is full."},
	{Key: "compaction.reserve", Label: "Reserve", Kind: "int", Section: "Compaction",
		Description: "Compact when the context comes within this many tokens of the window."},
	{Key: "compaction.keep_recent", Label: "Keep recent", Kind: "int", Section: "Compaction",
		Description: "Roughly how many recent tokens to keep word for word when compacting."},

	{Key: "extensions.timeout", Label: "Call timeout", Kind: "int", Section: "Extensions",
		Description: "Seconds an extension's hook or command may take."},
	{Key: "extensions.disabled", Label: "Disabled extensions", Kind: "list", Section: "Extensions",
		Description: "Extensions not to start. /extensions turns them on and off too."},

	{Key: "tui.mouse_select", Label: "Mouse selection", Kind: "bool", Section: "Terminal", Default: true,
		Description: "In the terminal, drag to select text and copy it. Off: the terminal's own selection."},
	{Key: "tui.side_by_side_width", Label: "Side-by-side diffs", Kind: "int", Section: "Terminal",
		Description: "Show diffs side by side when the terminal is at least this wide (0: never)."},
}

// ProviderTypes are the values of a provider's type.
var ProviderTypes = []string{ProviderOpenAICompat, ProviderOpenAIResponses, ProviderAnthropic}

// toolOptionFields are the XML tool calling options, which a provider sets
// for its models and a model can override.
var toolOptionFields = []Field{
	{Key: "tool_format", Label: "Tool format", Kind: "choice", Choices: []string{ToolFormatNative, ToolFormatXML}, Advanced: true,
		Description: "native uses the API's tool calling. xml describes the tools in the prompt and reads XML calls from the reply, for models or gateways without tool calls."},
	{Key: "system_prompt_in", Label: "System prompt in", Kind: "choice", Choices: []string{"system", "user"}, Advanced: true,
		Description: "user puts the system prompt at the start of the first user message, for gateways that drop system messages. Only with the xml tool format."},
	{Key: "decode_entities", Label: "Decode entities", Kind: "choice", Choices: []string{"off", "auto", "always"}, Advanced: true,
		Description: "Decode &lt; &gt; &amp; &quot; &apos; in XML tool arguments, for models that escape them. auto decodes only values with no raw markup."},
	{Key: "stop_sequences", Label: "Stop sequences", Kind: "bool", Advanced: true, Default: true,
		Description: "Send a stop sequence so the server ends the reply before the model writes a tool result itself. Turn it off if a gateway rejects it."},
}

// ProviderFields are a provider's settings, without its models.
var ProviderFields = append([]Field{
	{Key: "type", Label: "API", Kind: "choice", Choices: ProviderTypes, Default: ProviderOpenAICompat,
		Description: "openai-compat is Chat Completions (most servers, including llama.cpp, Ollama and vLLM), openai-responses the Responses API, anthropic the Messages API."},
	{Key: "base_url", Label: "Base URL", Kind: "string",
		Description: "The API root, for example http://localhost:9931/v1. For anthropic, leave out /v1."},
	{Key: "api_key_env", Label: "API key variable", Kind: "string",
		Description: "The environment variable that holds the API key. Local servers usually need none."},
	{Key: "api_key", Label: "API key", Kind: "string", Advanced: true,
		Description: "The key itself, stored in the file in plain text. It works, but Rho warns: prefer the key variable."},
	{Key: "headers", Label: "Headers", Kind: "headers", Advanced: true,
		Description: "Extra HTTP headers sent with each request."},
	{Key: "requests_per_minute", Label: "Requests per minute", Kind: "int", Advanced: true,
		Description: "Wait before sending a request that would exceed this (0: no limit)."},
	{Key: "tokens_per_minute", Label: "Tokens per minute", Kind: "int", Advanced: true,
		Description: "Wait before sending a request that would exceed this (0: no limit)."},
	{Key: "stream_idle_timeout", Label: "Idle timeout", Kind: "int", Advanced: true,
		Description: "Seconds a reply may go without data before it counts as stalled and is retried. 0 means 300, negative turns the check off. Raise it for slow local servers."},
	{Key: "flavor", Label: "Flavor", Kind: "string", Advanced: true,
		Description: "Endpoint-specific handling. gemini is detected from the base URL."},
	{Key: "debug_log", Label: "Debug log", Kind: "string", Advanced: true,
		Description: "A file that gets one JSON line per request with the exact payload and response. It holds whole conversations, so leave it off normally."},
}, toolOptionFields...)

// ModelFields are a model's settings.
var ModelFields = append([]Field{
	{Key: "id", Label: "ID", Kind: "string",
		Description: "The name you use in Rho (--model, /model, the default model). It must be unique across all providers."},
	{Key: "name", Label: "API model name", Kind: "string",
		Description: "The model name sent to the API. Defaults to the ID."},
	{Key: "context_window", Label: "Context window", Kind: "int", Default: DefaultContextWindow,
		Description: "Tokens the model can take in. Local servers often don't report it, so set it to the server's context size."},
	{Key: "max_output", Label: "Max output", Kind: "int", Default: DefaultMaxOutput,
		Description: "The most tokens one reply may have. Rho also keeps each reply within the room left in the context."},
	{Key: "supports_thinking", Label: "Thinking", Kind: "bool",
		Description: "The model thinks before it answers, and Rho shows its thinking."},
	{Key: "supports_images", Label: "Images", Kind: "bool",
		Description: "The model can read images."},
	{Key: "reasoning_effort", Label: "Reasoning effort", Kind: "choice", Choices: []string{"low", "medium", "high", "xhigh", "max"},
		Description: "How hard the model thinks, for models that support it (xhigh and max: Anthropic only). Unset: the model's default."},
	{Key: "pricing", Label: "Pricing", Kind: "pricing",
		Description: "US dollars per million tokens, for cost tracking."},
}, toolOptionFields...)
