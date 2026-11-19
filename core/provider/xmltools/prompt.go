package xmltools

import (
	"fmt"
	"strings"

	"github.com/NDari/rho/core/provider"
)

// Instructions renders the tool-use instructions and tool list. This is the
// wording to tune if a model keeps making the same mistake; keep the tag
// names in sync with the splitter and parser.
func Instructions(tools []provider.ToolSpec) string {
	var list []string
	for _, t := range tools {
		list = append(list, renderTool(t))
	}
	example, exampleParam := "tool_name", ""
	for _, t := range tools {
		if t.Name == "read" {
			example, exampleParam = "read", `<parameter name="path">src/main.go</parameter>`
			break
		}
	}
	if exampleParam == "" && len(tools) > 0 {
		example = tools[0].Name
		if props := properties(schemaOf(tools[0])); len(props) > 0 {
			exampleParam = fmt.Sprintf(`<parameter name=%q>...</parameter>`, props[0].name)
		}
	}
	return `# Tool use

You can call tools to act on the user's machine. To call a tool, write a tool call block in exactly this format:

<tool_call name="TOOL_NAME">
<parameter name="PARAMETER_NAME">VALUE</parameter>
</tool_call>

How to write values:
- Strings are written raw, exactly as they should be received. Do not quote them, do not JSON-escape them, and do not XML-escape them: write < > & " as they are, never as &lt; &gt; &amp; &quot;. Multi-line text and code go directly between the tags.
- Numbers and booleans are written plainly, e.g. 42 or true.
- A list of objects is written with one <item> per element and one tag per field inside it, for example:
<parameter name="edits">
<item>
<old_text>exact text to replace</old_text>
<new_text>replacement text</new_text>
</item>
</parameter>
- Any other list or object may be written as JSON.
- Leave out optional parameters you don't need.

Rules:
- You may make several tool calls in one reply, one block after another.
- After your last tool call, stop writing and wait. The results arrive in the next message inside <tool_result> blocks. Never write a <tool_result> block yourself and never guess what a tool returned.
- Write a tool_call block as plain text that starts with <tool_call. Never wrap it in a code fence (` + "```" + `).
- Only write a tool_call block when you actually want to call that tool. Do not put tool_call blocks inside examples or explanations.
- If no tool is needed, just answer normally.

## Available tools

` + strings.Join(list, "\n\n") + `

## Example

<tool_call name="` + example + `">
` + exampleParam + `
</tool_call>`
}

func schemaOf(t provider.ToolSpec) *object {
	v, _ := decodeOrdered(t.Parameters)
	return asObject(v)
}

func renderTool(t provider.ToolSpec) string {
	schema := schemaOf(t)
	req := required(schema)
	var params []string
	for _, p := range properties(schema) {
		params = append(params, renderParam(p.name, p.schema, req[p.name], "- "))
	}
	ps := "(no parameters)"
	if len(params) > 0 {
		ps = strings.Join(params, "\n")
	}
	// A markdown heading, not a <tool name="..."> tag: models copy tags.
	return fmt.Sprintf("### %s\n%s\nParameters:\n%s", t.Name, strings.TrimSpace(t.Description), ps)
}

func renderParam(name string, schema *object, isRequired bool, indent string) string {
	need := "optional"
	if isRequired {
		need = "required"
	}
	line := fmt.Sprintf("%s%s (%s, %s)", indent, name, typeLabel(schema), need)
	if d := strings.TrimSpace(str(schema, "description")); d != "" {
		line += ": " + d
	}
	nested := func(s *object, heading string) {
		r := required(s)
		var fields []string
		for _, p := range properties(s) {
			fields = append(fields, renderParam(p.name, p.schema, r[p.name], "    - "))
		}
		line += "\n  " + heading + ":\n" + strings.Join(fields, "\n")
	}
	if items := asObject(mustGet(schema, "items")); len(properties(items)) > 0 {
		nested(items, "Each item has")
	} else if len(properties(schema)) > 0 {
		nested(schema, "Fields")
	}
	return line
}

func typeLabel(schema *object) string {
	if enum, ok := mustGet(schema, "enum").([]any); ok {
		var vs []string
		for _, v := range enum {
			js, _ := marshal(v)
			vs = append(vs, string(js))
		}
		return "one of: " + strings.Join(vs, ", ")
	}
	if c, ok := schema.get("const"); ok {
		js, _ := marshal(c)
		return "exactly " + string(js)
	}
	if vs := variants(schema); vs != nil {
		var labels []string
		for _, v := range vs {
			labels = append(labels, typeLabel(asObject(v)))
		}
		return strings.Join(labels, " or ")
	}
	switch t := mustGet(schema, "type").(type) {
	case string:
		if t == "array" {
			items := asObject(mustGet(schema, "items"))
			switch {
			case len(properties(items)) > 0:
				return "list of objects"
			case items != nil:
				return "list of " + typeLabel(items)
			}
			return "list of values"
		}
		return t
	case []any:
		var parts []string
		for _, v := range t {
			if s, ok := v.(string); ok {
				parts = append(parts, s)
			}
		}
		return strings.Join(parts, " or ")
	}
	if len(properties(schema)) > 0 {
		return "object"
	}
	return "any"
}

// rewriteMessages converts the conversation to plain text turns: tool calls
// become <tool_call> text in assistant messages and tool results become
// <tool_result> blocks in user messages. Consecutive user messages are
// merged, since some gateways reject back-to-back user turns. Thinking
// blocks are kept for the underlying adapter to replay if it can.
func rewriteMessages(msgs []provider.Message, supportsImages bool) []provider.Message {
	names := map[string]string{}
	var out []provider.Message
	pushUser := func(content []provider.Block) {
		if len(content) == 0 {
			return
		}
		if n := len(out); n > 0 && out[n-1].Role == provider.RoleUser {
			out[n-1].Content = mergeText(out[n-1].Content, content)
			return
		}
		out = append(out, provider.Message{Role: provider.RoleUser, Content: content})
	}
	for _, m := range msgs {
		switch m.Role {
		case provider.RoleUser:
			pushUser(filterImages(m.Content, supportsImages))
		case provider.RoleAssistant:
			var thinking []provider.Block
			var parts []string
			for _, b := range m.Content {
				switch b.Type {
				case provider.BlockThinking:
					thinking = append(thinking, b)
				case provider.BlockText:
					if t := strings.TrimSpace(b.Text); t != "" {
						parts = append(parts, t)
					}
				case provider.BlockToolCall:
					names[b.ID] = b.Name
					parts = append(parts, serializeToolCall(b.Name, b.Args))
				}
			}
			if len(parts) == 0 {
				continue
			}
			am := m
			am.Content = append(thinking, provider.Text(strings.Join(parts, "\n\n")))
			out = append(out, am)
		case provider.RoleToolResult:
			for _, b := range m.Content {
				if b.Type == provider.BlockToolResult {
					pushUser(renderToolResult(b, names[b.CallID], supportsImages))
				}
			}
		}
	}
	return out
}

func renderToolResult(b provider.Block, name string, supportsImages bool) []provider.Block {
	status := "ok"
	if b.IsError {
		status = "error"
	}
	var texts []string
	var images []provider.Block
	for _, c := range b.Content {
		switch c.Type {
		case provider.BlockText:
			texts = append(texts, c.Text)
		case provider.BlockImage:
			images = append(images, c)
		}
	}
	note := ""
	if len(images) > 0 && !supportsImages {
		note = fmt.Sprintf("\n[%d image(s) omitted: this model does not accept images]", len(images))
	}
	out := []provider.Block{provider.Text(fmt.Sprintf("<tool_result name=%q status=%q>\n%s%s\n</tool_result>", name, status, strings.Join(texts, "\n"), note))}
	if supportsImages {
		out = append(out, images...)
	}
	return out
}

func filterImages(content []provider.Block, supportsImages bool) []provider.Block {
	if supportsImages {
		return content
	}
	out := make([]provider.Block, 0, len(content))
	for _, c := range content {
		if c.Type == provider.BlockImage {
			out = append(out, provider.Text("[image omitted]"))
			continue
		}
		out = append(out, c)
	}
	return out
}

// mergeText appends b to a, joining adjacent text blocks with a blank line.
func mergeText(a, b []provider.Block) []provider.Block {
	out := append([]provider.Block(nil), a...)
	for _, part := range b {
		if n := len(out); n > 0 && part.Type == provider.BlockText && out[n-1].Type == provider.BlockText {
			out[n-1] = provider.Text(out[n-1].Text + "\n\n" + part.Text)
			continue
		}
		out = append(out, part)
	}
	return out
}

// prependToFirstUser puts the instructions at the start of the first user
// message, for gateways that drop or override system messages.
func prependToFirstUser(msgs []provider.Message, instructions string) []provider.Message {
	if strings.TrimSpace(instructions) == "" {
		return msgs
	}
	block := provider.Text("<instructions>\n" + instructions + "\n</instructions>")
	out := append([]provider.Message(nil), msgs...)
	for i, m := range out {
		if m.Role == provider.RoleUser {
			out[i].Content = mergeText([]provider.Block{block}, m.Content)
			return out
		}
	}
	return append([]provider.Message{{Role: provider.RoleUser, Content: []provider.Block{block}}}, out...)
}
