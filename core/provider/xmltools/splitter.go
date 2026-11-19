package xmltools

import (
	"regexp"
	"strings"
	"unicode"
	"unicode/utf8"
)

// Tags the model is asked to write. Keep in sync with the prompt.
const (
	openTag    = "<tool_call"
	closeTag   = "</tool_call>"
	fenceOpen  = "```tool_call"
	fence      = "```"
	fenceClose = "\n```"
	toolClose  = "</tool>"
)

// haltMarkers mean the model has started inventing a tool result.
var haltMarkers = []string{"<tool_result", "<tool_response"}

type splitKind int

const (
	splitText splitKind = iota
	// splitToolStart: the opening tag is complete. Name may be empty when
	// it is given inside the body instead.
	splitToolStart
	// splitToolDelta: more of the current call's body arrived. Body is
	// everything so far.
	splitToolDelta
	// splitToolEnd: the call is complete, or the stream ended inside it
	// (Complete false).
	splitToolEnd
	splitHalt
)

type splitEvent struct {
	kind     splitKind
	text     string // text, or the delta for splitToolDelta
	name     string
	body     string
	complete bool
}

var (
	nameAttr      = regexp.MustCompile(`^<tool_call(?:\s+[^>]*?\b(?:name|tool|function)\s*=\s*["']?([\w.:-]+)["']?)?[^>]*>`)
	fenceNameAttr = regexp.MustCompile(`\b(?:name|tool|function)\s*=\s*["']?([\w.:-]+)`)
)

// splitter separates ordinary text from <tool_call ...>...</tool_call>
// blocks in streamed output, without ever emitting a half-written tag as
// text. It also stops at the first sign of the model inventing a tool
// result: everything from there on is dropped and splitHalt is reported.
//
// Some models (Gemma 3 among them) write the call as a code block tagged
// tool_call instead:
//
//	```tool_call name="edit">            ```tool_call
//	<parameter ...>...</parameter>       <tool name="edit">
//	</tool_call>                         <parameter ...>...</parameter>
//	```                                  </tool>
//	                                     ```
//
// These forms are accepted too. The body ends at </tool_call> or </tool>
// (or, for a JSON body, at the closing fence), and the closing fence after
// it is dropped.
type splitter struct {
	// drop lists text that is removed wherever it appears outside a call,
	// e.g. a closing tag the model copies from the prompt.
	drop              []string
	buffer            string
	inTool            bool
	headerDone        bool
	name              string
	body              string
	halted            bool
	fenced            bool
	pendingFenceClose bool
}

func (s *splitter) push(chunk string) []splitEvent {
	if s.halted {
		return nil
	}
	s.buffer += chunk
	var events []splitEvent
	s.drain(&events, false)
	return events
}

// finish flushes what is left when the stream ends.
func (s *splitter) finish() []splitEvent {
	if s.halted {
		return nil
	}
	var events []splitEvent
	s.drain(&events, true)
	switch {
	case s.inTool && !s.headerDone:
		events = append(events, splitEvent{kind: splitText, text: s.buffer})
	case s.inTool:
		events = append(events, splitEvent{kind: splitToolEnd, name: s.name, body: s.body + s.buffer})
	case s.buffer != "":
		events = append(events, splitEvent{kind: splitText, text: s.buffer})
	}
	s.buffer = ""
	s.inTool = false
	return events
}

func (s *splitter) drain(events *[]splitEvent, final bool) {
	for {
		if s.halted {
			return
		}
		if !s.inTool {
			if s.pendingFenceClose {
				rest := strings.TrimLeftFunc(s.buffer, unicode.IsSpace)
				if strings.HasPrefix(rest, fence) {
					s.buffer = rest[len(fence):]
					s.pendingFenceClose = false
					continue
				}
				if !final && strings.HasPrefix(fence, rest) {
					return // only whitespace or part of ``` so far
				}
				s.pendingFenceClose = false
			}
			fo := findOpen(s.buffer, fenceOpen)
			to := findOpen(s.buffer, openTag)
			fenced := fo != -1 && (to == -1 || fo < to)
			open := to
			if fenced {
				open = fo
			}
			halt, _ := findFirst(s.buffer, haltMarkers)
			if d, marker := findFirst(s.buffer, s.drop); d != -1 && (open == -1 || d < open) && (halt == -1 || d < halt) {
				if d > 0 {
					*events = append(*events, splitEvent{kind: splitText, text: s.buffer[:d]})
				}
				s.buffer = s.buffer[d+len(marker):]
				continue
			}
			if halt != -1 && (open == -1 || halt < open) {
				if halt > 0 {
					*events = append(*events, splitEvent{kind: splitText, text: s.buffer[:halt]})
				}
				s.buffer = ""
				s.halted = true
				*events = append(*events, splitEvent{kind: splitHalt})
				return
			}
			if open == -1 {
				keep := 0
				if !final {
					keep = partialSuffix(s.buffer, append(append([]string{openTag, fenceOpen}, haltMarkers...), s.drop...))
				}
				if emit := s.buffer[:len(s.buffer)-keep]; emit != "" {
					*events = append(*events, splitEvent{kind: splitText, text: emit})
				}
				s.buffer = s.buffer[len(s.buffer)-keep:]
				return
			}
			if open > 0 {
				*events = append(*events, splitEvent{kind: splitText, text: s.buffer[:open]})
			}
			s.buffer = s.buffer[open:]
			s.inTool, s.fenced, s.headerDone, s.name, s.body = true, fenced, false, "", ""
			continue
		}

		if !s.headerDone {
			var end int
			if s.fenced {
				// A fenced header ends at ">" or at the end of the fence line.
				end = firstOf(s.buffer, []string{">", "\n"}, len(fenceOpen))
			} else {
				end = strings.Index(s.buffer, ">")
			}
			if end == -1 {
				return
			}
			header := s.buffer[:end+1]
			re := nameAttr
			if s.fenced {
				re = fenceNameAttr
			}
			if m := re.FindStringSubmatch(header); m != nil {
				s.name = m[1]
			}
			s.headerDone = true
			s.buffer = s.buffer[end+1:]
			*events = append(*events, splitEvent{kind: splitToolStart, name: s.name})
			continue
		}

		closers := s.closers()
		closeAt, marker := findFirst(s.buffer, closers)
		if closeAt == -1 {
			keep := 0
			if !final {
				ms := []string{closeTag}
				if s.fenced {
					ms = []string{closeTag, toolClose, fenceClose}
				}
				keep = partialSuffix(s.buffer, ms)
			}
			if delta := s.buffer[:len(s.buffer)-keep]; delta != "" {
				s.body += delta
				*events = append(*events, splitEvent{kind: splitToolDelta, text: delta, body: s.body})
			}
			s.buffer = s.buffer[len(s.buffer)-keep:]
			return
		}
		if delta := s.buffer[:closeAt]; delta != "" {
			s.body += delta
			*events = append(*events, splitEvent{kind: splitToolDelta, text: delta, body: s.body})
		}
		*events = append(*events, splitEvent{kind: splitToolEnd, name: s.name, body: s.body, complete: true})
		s.buffer = s.buffer[closeAt+len(marker):]
		s.inTool = false
		s.pendingFenceClose = s.fenced && marker != fenceClose
	}
}

// closers are the markers that end the current call's body.
func (s *splitter) closers() []string {
	if !s.fenced {
		return []string{closeTag}
	}
	head := strings.TrimLeftFunc(s.body+s.buffer, unicode.IsSpace)
	if head == "" {
		return nil
	}
	// XML inside the fence ends at its closing tag; JSON at the fence.
	if strings.HasPrefix(head, "<") {
		return []string{closeTag, toolClose}
	}
	return []string{fenceClose}
}

// findOpen finds marker followed by whitespace or ">" (so "<tool_calls"
// doesn't match). A marker at the very end is undecided and not reported.
func findOpen(s, marker string) int {
	from := 0
	for {
		i := strings.Index(s[from:], marker)
		if i == -1 {
			return -1
		}
		i += from
		next := i + len(marker)
		if next >= len(s) {
			return -1
		}
		r, _ := utf8.DecodeRuneInString(s[next:])
		if r == '>' || unicode.IsSpace(r) {
			return i
		}
		from = i + 1
	}
}

func findFirst(s string, markers []string) (int, string) {
	best, marker := -1, ""
	for _, m := range markers {
		if i := strings.Index(s, m); i != -1 && (best == -1 || i < best) {
			best, marker = i, m
		}
	}
	return best, marker
}

func firstOf(s string, markers []string, from int) int {
	if from > len(s) {
		return -1
	}
	i, _ := findFirst(s[from:], markers)
	if i == -1 {
		return -1
	}
	return from + i
}

// partialSuffix is the length of the longest suffix of s that is a prefix
// of a marker (including a whole marker).
func partialSuffix(s string, markers []string) int {
	best := 0
	for _, m := range markers {
		for n := min(len(m), len(s)); n > best; n-- {
			if strings.HasSuffix(s, m[:n]) {
				best = n
				break
			}
		}
	}
	return best
}
