# Recorded responses

Raw HTTP response bodies captured from real servers, replayed by the
`replay` package in tests. They are kept byte for byte as the server sent
them, including its many small reasoning deltas, usage and timing fields.

| File | Server | Request |
|---|---|---|
| `llamacpp_chat_text.sse` | llama.cpp `llama-server` (build b12359), Qwen3.6-35B-A3B with thinking | Chat Completions, streamed: a one-sentence arithmetic answer |
| `llamacpp_chat_tool_call.sse` | same | Chat Completions, streamed, one `read` tool offered: a call to `read` with `{"path":"go.mod"}` |
| `llamacpp_chat_overflow.json` | same, 8192-token context | Chat Completions with a 22900-token prompt: the HTTP 400 error body |
| `llamacpp_responses_text.sse` | same | Responses API, streamed: the arithmetic answer |
| `llamacpp_responses_tool_call.sse` | same | Responses API, streamed, one `read` tool: a `function_call` to `read` |

To record more, send the request with `curl -sN` to a local server and save
the body. Check that nothing personal (hosts, paths, user names) is in it.
