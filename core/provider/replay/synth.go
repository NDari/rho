package replay

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"
)

// Chat builds a Chat Completions stream in the exact shape llama.cpp's
// llama-server sends (see recordings/llamacpp_chat_*.sse): a role chunk,
// reasoning and content split into small deltas, tool call arguments in
// pieces, a finish chunk, a usage chunk and [DONE]. It is for replies a
// test needs that were not recorded, such as a summary or a call to a
// particular tool.
type Chat struct {
	Thinking string
	Text     string
	Calls    []ChatCall
	// Finish defaults to "tool_calls" with calls, else "stop".
	Finish string
	// InputTokens and OutputTokens are reported in the usage chunk.
	InputTokens, OutputTokens int
}

// ChatCall is a tool call in a Chat stream.
type ChatCall struct {
	ID, Name, Args string
}

const chatEnvelope = `"created":1795402860,"id":"chatcmpl-synthetic","model":"unsloth/Qwen3.6-35B-A3B-MTP-GGUF:UD-Q4_K_XL","system_fingerprint":"b12359-b1b770d90","object":"chat.completion.chunk"`

// SSE returns the stream body.
func (c Chat) SSE() []byte {
	var b bytes.Buffer
	chunk := func(delta any, finish string) {
		d, _ := json.Marshal(delta)
		f := "null"
		if finish != "" {
			f = `"` + finish + `"`
		}
		fmt.Fprintf(&b, "data: {\"choices\":[{\"finish_reason\":%s,\"index\":0,\"delta\":%s}],%s}\n\n", f, d, chatEnvelope)
	}
	chunk(map[string]any{"role": "assistant", "content": nil}, "")
	for _, piece := range pieces(c.Thinking) {
		chunk(map[string]string{"reasoning_content": piece}, "")
	}
	for _, piece := range pieces(c.Text) {
		chunk(map[string]string{"content": piece}, "")
	}
	for i, call := range c.Calls {
		for j, piece := range pieces(call.Args) {
			tc := map[string]any{"index": i, "function": map[string]string{"arguments": piece}}
			if j == 0 {
				tc["id"], tc["type"] = call.ID, "function"
				tc["function"] = map[string]string{"name": call.Name, "arguments": piece}
			}
			chunk(map[string]any{"tool_calls": []any{tc}}, "")
		}
	}
	finish := c.Finish
	if finish == "" {
		finish = "stop"
		if len(c.Calls) > 0 {
			finish = "tool_calls"
		}
	}
	chunk(map[string]any{}, finish)
	fmt.Fprintf(&b, "data: {\"choices\":[],%s,\"usage\":{\"completion_tokens\":%d,\"prompt_tokens\":%d,\"total_tokens\":%d,\"prompt_tokens_details\":{\"cached_tokens\":0}}}\n\n",
		chatEnvelope, c.OutputTokens, c.InputTokens, c.InputTokens+c.OutputTokens)
	b.WriteString("data: [DONE]\n\n")
	return b.Bytes()
}

// pieces splits s into the short deltas servers stream (a few characters,
// broken at spaces where possible).
func pieces(s string) []string {
	var out []string
	for s != "" {
		n := min(len(s), 6)
		if i := strings.IndexByte(s[1:n], ' '); i >= 0 {
			n = i + 1
		}
		// Do not split a UTF-8 sequence.
		for n < len(s) && s[n]&0xC0 == 0x80 {
			n++
		}
		out = append(out, s[:n])
		s = s[n:]
	}
	return out
}
