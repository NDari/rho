package benchwork

import (
	"encoding/json"
	"fmt"
	"strings"
)

// This file is not loaded into Yaegi: it builds the inputs.

// Reply is a model reply as an XML-tools model writes it: prose, a write
// call with about 3 KB of code, and more prose.
func Reply() string {
	var code strings.Builder
	for code.Len() < 3000 {
		code.WriteString("func handler(w http.ResponseWriter, r *http.Request) {\n\tif r.Method != \"GET\" { http.Error(w, \"no\", 405); return }\n}\n")
	}
	return strings.Repeat("Here is the change you asked for, with a short explanation. ", 8) +
		"\n<tool_call name=\"write\">\n<parameter name=\"path\">main.go</parameter>\n<parameter name=\"content\">" +
		code.String() + "</parameter>\n</tool_call>\n" + strings.Repeat("Done; run the tests next. ", 4)
}

// Transcript returns a conversation of about tokens tokens (4 bytes each)
// in the session file's JSON format: user questions, assistant answers
// with read calls, and tool results holding file contents, some with a
// secret to redact.
func Transcript(tokens int) []byte {
	file := strings.Repeat("// The handler checks the request and writes the response.\nfunc serve(w http.ResponseWriter, r *http.Request) { w.Write([]byte(\"ok\")) }\n", 12)
	var msgs []map[string]any
	size := 0
	for i := 0; size < tokens*4; i++ {
		id := fmt.Sprintf("call_%d", i)
		user := fmt.Sprintf("Question %d: why does the server return 500 on /health? token: abc%d", i, i)
		answer := "Let me look at the handler first; it is probably the missing nil check on the config."
		msgs = append(msgs,
			map[string]any{"role": "user", "content": []any{map[string]any{"type": "text", "text": user}}},
			map[string]any{"role": "assistant", "content": []any{
				map[string]any{"type": "text", "text": answer},
				map[string]any{"type": "tool_call", "id": id, "name": "read", "args": map[string]any{"path": "server.go"}},
			}},
			map[string]any{"role": "tool_result", "content": []any{map[string]any{
				"type": "tool_result", "call_id": id, "content": []any{map[string]any{"type": "text", "text": file}},
			}}},
		)
		size += len(user) + len(answer) + len(file) + 120
	}
	data, err := json.Marshal(msgs)
	if err != nil {
		panic(err)
	}
	return data
}

// Maps decodes a transcript for the workloads that take messages.
func Maps(data []byte) []map[string]any {
	var msgs []map[string]any
	if err := json.Unmarshal(data, &msgs); err != nil {
		panic(err)
	}
	return msgs
}

// Source is work.go as Yaegi loads it: package main, for an extension or
// for evaluating directly.
func Source(workGo []byte) string {
	return strings.Replace(string(workGo), "package benchwork", "package main", 1)
}
