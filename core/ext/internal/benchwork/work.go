// Package benchwork holds workloads typical of extensions, written so the
// same source runs compiled and under the Yaegi extension host. The
// benchmarks in core/ext/yaegihost and core/ext compare the two. This file
// is loaded into Yaegi as is (with its package renamed), so it uses only the
// standard library and avoids language features Yaegi lacks (min and max).
package benchwork

import (
	"encoding/json"
	"regexp"
	"strings"
	"unicode"
)

// ScanStream feeds input to a streaming scanner in chunks of n bytes, as a
// middleware that parses the reply would, and returns how many tool-call
// blocks and text pieces it found. It keeps a buffer and never reports a
// half-written tag, like Rho's XML tool call parser.
func ScanStream(input string, n int) int {
	const open, closeTag = "<tool_call", "</tool_call>"
	found := 0
	buf := ""
	inCall := false
	for i := 0; i < len(input); i += n {
		end := i + n
		if end > len(input) {
			end = len(input)
		}
		buf += input[i:end]
		for {
			if inCall {
				j := strings.Index(buf, closeTag)
				if j < 0 {
					break
				}
				buf = buf[j+len(closeTag):]
				inCall = false
				found++
				continue
			}
			j := strings.Index(buf, open)
			if j < 0 {
				// Keep a possible start of the tag; the rest is text.
				keep := partialSuffix(buf, open)
				if len(buf)-keep > 0 {
					found++
				}
				buf = buf[len(buf)-keep:]
				break
			}
			if j > 0 {
				found++
			}
			buf = buf[j:]
			inCall = true
		}
	}
	return found
}

// partialSuffix is the length of the longest suffix of s that starts marker.
func partialSuffix(s, marker string) int {
	for k := len(marker) - 1; k > 0; k-- {
		if len(s) >= k && s[len(s)-k:] == marker[:k] {
			return k
		}
	}
	return 0
}

// texts calls f with every text in messages in the session file's format.
func texts(msgs []map[string]any, f func(string)) {
	for _, m := range msgs {
		blocks, _ := m["content"].([]any)
		for _, b := range blocks {
			bm, _ := b.(map[string]any)
			if t, ok := bm["text"].(string); ok {
				f(t)
			}
			if inner, ok := bm["content"].([]any); ok {
				for _, ib := range inner {
					if im, ok := ib.(map[string]any); ok {
						if t, ok := im["text"].(string); ok {
							f(t)
						}
					}
				}
			}
		}
	}
}

// EstimateTokens counts words and punctuation in every text, rune by rune,
// as a rate limiter or context meter might.
func EstimateTokens(msgs []map[string]any) int {
	tokens := 0
	texts(msgs, func(t string) {
		inWord := false
		for _, r := range t {
			switch {
			case unicode.IsLetter(r) || unicode.IsDigit(r):
				if !inWord {
					tokens++
					inWord = true
				}
			case unicode.IsSpace(r):
				inWord = false
			default:
				tokens++
				inWord = false
			}
		}
	})
	return tokens
}

// EstimateTokensRunes is EstimateTokens ranging over []rune(t) instead of
// the string. The result is the same; under Yaegi v0.16.3 ranging over a
// string is quadratic in its length, and this form is not.
func EstimateTokensRunes(msgs []map[string]any) int {
	tokens := 0
	texts(msgs, func(t string) {
		inWord := false
		for _, r := range []rune(t) { //nolint:staticcheck // on purpose: ranging over the string is quadratic under Yaegi
			switch {
			case unicode.IsLetter(r) || unicode.IsDigit(r):
				if !inWord {
					tokens++
					inWord = true
				}
			case unicode.IsSpace(r):
				inWord = false
			default:
				tokens++
				inWord = false
			}
		}
	})
	return tokens
}

var secret = regexp.MustCompile(`(?i)(api[_-]?key|token|password)\s*[:=]\s*\S+`)

// Redact replaces anything that looks like a secret in every text and
// returns how many texts changed. The work is mostly in regexp.
func Redact(msgs []map[string]any) int {
	changed := 0
	texts(msgs, func(t string) {
		if r := secret.ReplaceAllString(t, "$1=[redacted]"); r != t {
			changed++
		}
	})
	return changed
}

// Rewrite copies the messages and adds a note to every user text, as a
// before_request hook that injects context would. It returns the copy.
func Rewrite(msgs []map[string]any) []map[string]any {
	out := make([]map[string]any, 0, len(msgs))
	for _, m := range msgs {
		c := make(map[string]any, len(m))
		for k, v := range m {
			c[k] = v
		}
		if m["role"] == "user" {
			blocks, _ := m["content"].([]any)
			nb := make([]any, 0, len(blocks)+1)
			nb = append(nb, blocks...)
			nb = append(nb, map[string]any{"type": "text", "text": "(note from an extension)"})
			c["content"] = nb
		}
		out = append(out, c)
	}
	return out
}

// RoundTrip decodes messages from JSON and encodes them again, the work an
// extension's hook does on every call, and returns the encoded size.
func RoundTrip(data []byte) int {
	var msgs []map[string]any
	if err := json.Unmarshal(data, &msgs); err != nil {
		return -1
	}
	out, err := json.Marshal(msgs)
	if err != nil {
		return -1
	}
	return len(out)
}
