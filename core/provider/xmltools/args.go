package xmltools

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

// Decode modes for XML entities in string values.
const (
	DecodeOff    = "off"
	DecodeAuto   = "auto"
	DecodeAlways = "always"
)

var (
	paramOpen  = regexp.MustCompile(`<parameter\s+name\s*=\s*["']([^"']+)["']\s*>`)
	cdata      = regexp.MustCompile(`^\s*<!\[CDATA\[((?s).*)\]\]>\s*$`)
	innerOpen  = regexp.MustCompile(`^\s*<tool(?:_call)?\s[^>]*>`)
	innerClose = regexp.MustCompile(`</tool(?:_call)?>\s*$`)
	itemTag    = regexp.MustCompile(`<(/?)item\s*>`)

	wrapperName = regexp.MustCompile(`^\s*<tool(?:_call)?\s[^>]*?\bname\s*=\s*["']?([\w.:-]+)`)
	tagName     = regexp.MustCompile(`<(?:name|tool_name|tool|function)>\s*([\w.:-]+)\s*</`)
	fnName      = regexp.MustCompile(`<function\s*=\s*["']?([\w.:-]+)`)
	jsonLike    = regexp.MustCompile(`^(?:[\[{]|-?\d|true$|false$|null$)`)
)

// parseArguments converts a tool call body to arguments, guided by the
// tool's parameter schema. Accepted bodies, most to least preferred:
// <parameter name="x">v</parameter> tags, <x>v</x> child tags named after
// the parameters, and a JSON object (optionally {"name":..,"arguments":..}).
// With partial set (the body is still streaming), problems are not errors.
func parseArguments(body string, schema *object, partial bool, decode string) (any, string) {
	if innerOpen.MatchString(body) {
		body = innerClose.ReplaceAllString(innerOpen.ReplaceAllString(body, ""), "")
	}
	if params := extractParameterTags(body, partial); len(params) > 0 {
		return convertProperties(params, schema, decode)
	}
	if props := properties(schema); len(props) > 0 {
		names := make([]string, len(props))
		for i, p := range props {
			names[i] = p.name
		}
		if children := extractChildTags(body, names, partial); len(children) > 0 {
			return convertProperties(children, schema, decode)
		}
	}
	trimmed := strings.TrimSpace(body)
	if trimmed == "" {
		return newObject(), ""
	}
	if strings.HasPrefix(trimmed, "{") {
		v, err := decodeOrdered([]byte(trimmed))
		if err != nil {
			if partial {
				return newObject(), ""
			}
			return newObject(), "The tool call body looked like JSON but did not parse: " + err.Error()
		}
		if o := asObject(v); o != nil {
			if _, named := mustGet(o, "name").(string); named {
				for _, k := range []string{"arguments", "parameters", "args"} {
					inner, ok := o.get(k)
					if !ok {
						continue
					}
					if io := asObject(inner); io != nil {
						return io, ""
					}
					if s, ok := inner.(string); ok {
						if iv, err := decodeOrdered([]byte(s)); err == nil && asObject(iv) != nil {
							return iv, ""
						}
					}
					break
				}
			}
			return o, ""
		}
	}
	if partial {
		return newObject(), ""
	}
	return newObject(), `Could not find any <parameter name="..."> tags in the tool call.`
}

// findNameInBody finds a tool name in the body when the opening tag had none.
func findNameInBody(body string) string {
	for _, re := range []*regexp.Regexp{wrapperName, tagName, fnName} {
		if m := re.FindStringSubmatch(body); m != nil {
			return m[1]
		}
	}
	if t := strings.TrimSpace(body); strings.HasPrefix(t, "{") {
		var v struct {
			Name string `json:"name"`
		}
		if json.Unmarshal([]byte(t), &v) == nil {
			return v.Name
		}
	}
	return ""
}

type rawProperty struct {
	name, raw string
}

// extractParameterTags extracts <parameter name="x">...</parameter> pairs.
// A value ends at the last </parameter> before the next opening tag, so a
// value containing "</parameter>" survives.
func extractParameterTags(body string, partial bool) []rawProperty {
	locs := paramOpen.FindAllStringSubmatchIndex(body, -1)
	var out []rawProperty
	for i, loc := range locs {
		limit := len(body)
		if i+1 < len(locs) {
			limit = locs[i+1][0]
		}
		name := body[loc[2]:loc[3]]
		segment := body[loc[1]:limit]
		end := strings.LastIndex(segment, "</parameter>")
		if end == -1 {
			if partial {
				continue
			}
			out = append(out, rawProperty{name, segment}) // unterminated: take the rest
			continue
		}
		out = append(out, rawProperty{name, segment[:end]})
	}
	return out
}

// extractChildTags extracts <name>...</name> children for known names,
// skipping openings nested inside an open element.
func extractChildTags(body string, names []string, partial bool) []rawProperty {
	if len(names) == 0 {
		return nil
	}
	quoted := make([]string, len(names))
	for i, n := range names {
		quoted[i] = regexp.QuoteMeta(n)
	}
	pattern := regexp.MustCompile(`<(` + strings.Join(quoted, "|") + `)\s*>`)
	type open struct {
		name           string
		start, cnStart int
	}
	var opens []open
	from := 0
	for from <= len(body) {
		loc := pattern.FindStringSubmatchIndex(body[from:])
		if loc == nil {
			break
		}
		name := body[from+loc[2] : from+loc[3]]
		o := open{name, from + loc[0], from + loc[1]}
		opens = append(opens, o)
		end := matchingClose(body, name, o.cnStart)
		if end == -1 {
			break
		}
		from = end
	}
	var out []rawProperty
	for i, o := range opens {
		limit := len(body)
		if i+1 < len(opens) {
			limit = opens[i+1].start
		}
		segment := body[o.cnStart:limit]
		end := strings.LastIndex(segment, "</"+o.name+">")
		if end == -1 {
			if partial {
				continue
			}
			out = append(out, rawProperty{o.name, segment})
			continue
		}
		out = append(out, rawProperty{o.name, segment[:end]})
	}
	return out
}

// matchingClose returns the index just past the close tag matching an
// element opened before from, honoring nesting of the same tag.
func matchingClose(body, name string, from int) int {
	re := regexp.MustCompile(`<(/?)` + regexp.QuoteMeta(name) + `\s*>`)
	depth := 1
	for _, m := range re.FindAllStringSubmatchIndex(body[from:], -1) {
		if m[3] > m[2] {
			depth--
		} else {
			depth++
		}
		if depth == 0 {
			return from + m[1]
		}
	}
	return -1
}

// extractItems splits top-level <item>...</item> elements.
func extractItems(raw string) []string {
	var items []string
	depth, start := 0, -1
	for _, m := range itemTag.FindAllStringSubmatchIndex(raw, -1) {
		closing := m[3] > m[2]
		switch {
		case !closing:
			if depth == 0 {
				start = m[1]
			}
			depth++
		case depth > 0:
			depth--
			if depth == 0 {
				items = append(items, raw[start:m[0]])
			}
		}
	}
	return items
}

func convertProperties(props []rawProperty, schema *object, decode string) (*object, string) {
	args := newObject()
	var errs []string
	for _, p := range props {
		v, err := convertValue(p.raw, property(schema, p.name), decode)
		if err != "" {
			errs = append(errs, p.name+": "+err)
		}
		if prev, ok := args.get(p.name); ok {
			// A repeated parameter collects into a list.
			if arr, isArr := prev.([]any); isArr {
				args.set(p.name, append(arr, v))
			} else {
				args.set(p.name, []any{prev, v})
			}
			continue
		}
		args.set(p.name, v)
	}
	return args, strings.Join(errs, "; ")
}

// trimBlock strips one leading and one trailing newline.
func trimBlock(s string) string {
	switch {
	case strings.HasPrefix(s, "\r\n"):
		s = s[2:]
	case strings.HasPrefix(s, "\n"):
		s = s[1:]
	}
	switch {
	case strings.HasSuffix(s, "\r\n"):
		s = s[:len(s)-2]
	case strings.HasSuffix(s, "\n"):
		s = s[:len(s)-1]
	}
	return s
}

func has(types []string, t string) bool {
	for _, x := range types {
		if x == t {
			return true
		}
	}
	return false
}

// convertValue converts a raw value by the schema's type. Strings are
// taken raw; numbers and booleans are parsed; lists use <item> children
// or JSON; objects use child tags or JSON.
func convertValue(rawValue string, schema *object, decode string) (any, string) {
	raw, isCDATA := rawValue, false
	if m := cdata.FindStringSubmatch(rawValue); m != nil {
		raw, isCDATA = m[1], true
	}
	text := func() string {
		v := trimBlock(raw)
		if !isCDATA && shouldDecode(v, decode) {
			return decodeEntities(v)
		}
		return v
	}
	types := schemaTypes(schema)
	trimmed := strings.TrimSpace(raw)

	if has(types, "array") {
		if items := extractItems(raw); items != nil {
			itemSchema := asObject(mustGet(pickVariant(schema, "array"), "items"))
			values := make([]any, 0, len(items))
			var errs []string
			for i, it := range items {
				v, err := convertValue(it, itemSchema, decode)
				values = append(values, v)
				if err != "" {
					errs = append(errs, fmt.Sprintf("item %d: %s", i+1, err))
				}
			}
			return values, strings.Join(errs, "; ")
		}
		if strings.HasPrefix(trimmed, "[") {
			v, err := decodeOrdered([]byte(trimmed))
			if err == nil {
				return v, ""
			}
			if !has(types, "string") {
				return trimmed, "invalid JSON list (" + err.Error() + ")"
			}
		}
	}
	if has(types, "object") {
		if strings.HasPrefix(trimmed, "{") {
			v, err := decodeOrdered([]byte(trimmed))
			if err == nil {
				return v, ""
			}
			if !has(types, "string") {
				return trimmed, "invalid JSON object (" + err.Error() + ")"
			}
		}
		objSchema := pickVariant(schema, "object")
		props := properties(objSchema)
		names := make([]string, len(props))
		for i, p := range props {
			names[i] = p.name
		}
		if children := extractChildTags(raw, names, false); len(children) > 0 {
			return convertProperties(children, objSchema, decode)
		}
		if params := extractParameterTags(raw, false); len(params) > 0 {
			return convertProperties(params, objSchema, decode)
		}
	}
	if has(types, "string") {
		return text(), ""
	}
	if has(types, "integer") || has(types, "number") {
		if n, ok := number(trimmed); ok {
			return n, ""
		}
	}
	if has(types, "boolean") {
		switch strings.ToLower(trimmed) {
		case "true":
			return true, ""
		case "false":
			return false, ""
		}
	}
	if has(types, "null") && (trimmed == "" || trimmed == "null") {
		return nil, ""
	}
	if len(types) == 0 && !jsonLike.MatchString(trimmed) {
		return text(), ""
	}
	// No schema, or a type mismatch: JSON if it parses, else the text. The
	// tool's own validation reports a mismatch to the model.
	if v, err := decodeOrdered([]byte(trimmed)); err == nil && trimmed != "" {
		return v, ""
	}
	return text(), ""
}

// number parses a JSON-style number.
func number(s string) (any, bool) {
	if s == "" {
		return nil, false
	}
	if json.Valid([]byte(s)) {
		if _, err := strconv.ParseFloat(s, 64); err == nil {
			return json.Number(s), true
		}
	}
	if f, err := strconv.ParseFloat(s, 64); err == nil {
		return f, true
	}
	return nil, false
}

var entities = map[string]string{"lt": "<", "gt": ">", "amp": "&", "quot": `"`, "apos": "'"}

var entityRe = regexp.MustCompile(`&(lt|gt|amp|quot|apos);`)

// shouldDecode reports whether a string value should be entity-decoded.
// "auto" decodes only values with an entity and no raw markup: no < or >,
// and no & that doesn't start one of the five entities.
func shouldDecode(v, mode string) bool {
	switch mode {
	case DecodeAlways:
		return true
	case DecodeAuto:
		if !entityRe.MatchString(v) || strings.ContainsAny(v, "<>") {
			return false
		}
		for i := 0; i < len(v); i++ {
			if v[i] != '&' {
				continue
			}
			if loc := entityRe.FindStringIndex(v[i:]); loc == nil || loc[0] != 0 {
				return false
			}
		}
		return true
	}
	return false
}

// decodeEntities decodes in a single pass, so "&amp;lt;" becomes "&lt;".
func decodeEntities(s string) string {
	return entityRe.ReplaceAllStringFunc(s, func(m string) string {
		return entities[m[1:len(m)-1]]
	})
}

// serializeToolCall renders a call in the format the model is asked to
// write, for replaying earlier calls.
func serializeToolCall(name string, args json.RawMessage) string {
	var b strings.Builder
	fmt.Fprintf(&b, "<tool_call name=%q>\n", name)
	v, err := decodeOrdered(args)
	if o := asObject(v); err == nil && o != nil {
		for _, k := range o.keys {
			fmt.Fprintf(&b, "<parameter name=%q>%s</parameter>\n", k, serializeValue(o.vals[k]))
		}
	}
	b.WriteString("</tool_call>")
	return b.String()
}

func serializeValue(v any) string {
	switch t := v.(type) {
	case string:
		if strings.Contains(t, "\n") {
			return "\n" + t + "\n"
		}
		return t
	case []any:
		allObjects := len(t) > 0
		for _, e := range t {
			if asObject(e) == nil {
				allObjects = false
				break
			}
		}
		if allObjects {
			var b strings.Builder
			b.WriteString("\n")
			for _, e := range t {
				b.WriteString("<item>\n")
				o := asObject(e)
				for _, k := range o.keys {
					if s, ok := o.vals[k].(string); ok {
						if strings.Contains(s, "\n") {
							fmt.Fprintf(&b, "<%s>\n%s\n</%s>\n", k, s, k)
						} else {
							fmt.Fprintf(&b, "<%s>%s</%s>\n", k, s, k)
						}
						continue
					}
					js, _ := marshal(o.vals[k])
					fmt.Fprintf(&b, "<%s>%s</%s>\n", k, js, k)
				}
				b.WriteString("</item>\n")
			}
			return b.String()
		}
	}
	js, _ := marshal(v)
	return string(js)
}
