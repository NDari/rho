package xmltools

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
)

func sch(t *testing.T, js string) *object {
	t.Helper()
	v, err := decodeOrdered([]byte(js))
	if err != nil {
		t.Fatal(err)
	}
	return asObject(v)
}

// same compares arguments to expected JSON, ignoring key order.
func same(t *testing.T, got any, want string) {
	t.Helper()
	js, err := marshal(got)
	if err != nil {
		t.Fatal(err)
	}
	var g, w any
	if err := json.Unmarshal(js, &g); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal([]byte(want), &w); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(g, w) {
		t.Fatalf("got %s\nwant %s", js, want)
	}
}

const (
	bashSchema = `{"type":"object","properties":{"command":{"type":"string"},"timeout":{"type":"number"}},"required":["command"]}`
	readSchema = `{"type":"object","properties":{"path":{"type":"string"},"offset":{"type":"integer"},"limit":{"type":"integer"}},"required":["path"]}`
	editSchema = `{"type":"object","properties":{"path":{"type":"string"},"edits":{"type":"array","items":{"type":"object","properties":{"old_text":{"type":"string"},"new_text":{"type":"string"}},"required":["old_text","new_text"]}}},"required":["path","edits"]}`
)

func TestParseArguments(t *testing.T) {
	tests := []struct {
		name, schema, body, decode, want string
	}{
		{"raw strings and numbers", bashSchema, "\n<parameter name=\"command\">\ngrep -n \"a < b && c > d\" *.go | head\necho 'done'\n</parameter>\n<parameter name=\"timeout\">30</parameter>\n", "",
			`{"command":"grep -n \"a < b && c > d\" *.go | head\necho 'done'","timeout":30}`},
		{"numbers trimmed", readSchema, `<parameter name="path">a.go</parameter><parameter name="offset"> 10 </parameter>`, "", `{"path":"a.go","offset":10}`},
		{"item lists keep code raw", editSchema, "\n<parameter name=\"path\">app.go</parameter>\n<parameter name=\"edits\">\n<item>\n<old_text>\nx := \"hello\"\nif a < b {}\n</old_text>\n<new_text>\nx := \"hi \\n there\"\n</new_text>\n</item>\n<item>\n<old_text>foo()</old_text>\n<new_text>bar()</new_text>\n</item>\n</parameter>", "",
			`{"path":"app.go","edits":[{"old_text":"x := \"hello\"\nif a < b {}","new_text":"x := \"hi \\n there\""},{"old_text":"foo()","new_text":"bar()"}]}`},
		{"JSON list", editSchema, `<parameter name="path">a</parameter><parameter name="edits">[{"old_text":"a","new_text":"b"}]</parameter>`, "", `{"path":"a","edits":[{"old_text":"a","new_text":"b"}]}`},
		{"value containing </parameter>", bashSchema, "<parameter name=\"command\">echo \"</parameter>\" > x</parameter>\n<parameter name=\"timeout\">5</parameter>", "", `{"command":"echo \"</parameter>\" > x","timeout":5}`},
		{"CDATA", bashSchema, `<parameter name="command"><![CDATA[echo <hi>]]></parameter>`, "", `{"command":"echo <hi>"}`},
		{"child tags", readSchema, "\n<path>a.go</path>\n<limit>5</limit>\n", "", `{"path":"a.go","limit":5}`},
		{"JSON body", bashSchema, `{"command":"ls"}`, "", `{"command":"ls"}`},
		{"JSON name/arguments body", bashSchema, `{"name":"bash","arguments":{"command":"ls"}}`, "", `{"command":"ls"}`},
		{"inner tool wrapper", editSchema, "\n<tool name=\"edit\">\n<parameter name=\"path\">app.go</parameter>\n<parameter name=\"edits\">\n<item>\n<old_text>hello</old_text>\n<new_text>hi <friend></new_text>\n</item>\n</parameter>\n</tool>\n", "",
			`{"path":"app.go","edits":[{"old_text":"hello","new_text":"hi <friend>"}]}`},
		{"parameter named tool", `{"type":"object","properties":{"tool":{"type":"string"}}}`, "<tool>hammer</tool>", "", `{"tool":"hammer"}`},
		{"entities kept by default", editSchema, `<parameter name="path">a</parameter><parameter name="edits"><item><old_text>&amp;amp;</old_text><new_text>&lt;b&gt;</new_text></item></parameter>`, "", `{"path":"a","edits":[{"old_text":"&amp;amp;","new_text":"&lt;b&gt;"}]}`},
		{"entities always decoded in one pass", editSchema, `<parameter name="path">a</parameter><parameter name="edits"><item><old_text>&amp;amp;</old_text><new_text>hi &lt;friend&gt; &amp; &quot;co&quot; &apos;x&apos;</new_text></item></parameter>`, DecodeAlways,
			`{"path":"a","edits":[{"old_text":"&amp;","new_text":"hi <friend> & \"co\" 'x'"}]}`},
		{"CDATA never decoded", bashSchema, `<parameter name="command"><![CDATA[echo &amp;]]></parameter>`, DecodeAlways, `{"command":"echo &amp;"}`},
		{"auto leaves raw markup alone", editSchema, `<parameter name="path">a</parameter><parameter name="edits"><item><old_text><p>Tom &amp; Jerry</p></old_text><new_text>hi &lt;friend&gt; &amp; "co"</new_text></item></parameter>`, DecodeAuto,
			`{"path":"a","edits":[{"old_text":"<p>Tom &amp; Jerry</p>","new_text":"hi <friend> & \"co\""}]}`},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			args, perr := parseArguments(tc.body, sch(t, tc.schema), false, tc.decode)
			if perr != "" {
				t.Fatalf("error: %s", perr)
			}
			same(t, args, tc.want)
		})
	}
}

func TestParseArgumentsErrorsAndPartial(t *testing.T) {
	if _, perr := parseArguments("please run ls", sch(t, bashSchema), false, ""); perr == "" {
		t.Error("garbage body: no error")
	}
	args, perr := parseArguments(`<parameter name="path">a.go</parameter><parameter name="content">half`, nil, true, "")
	if perr != "" {
		t.Fatal(perr)
	}
	same(t, args, `{"path":"a.go"}`)
}

func TestFindNameInBody(t *testing.T) {
	for body, want := range map[string]string{
		`{"name":"bash","arguments":{}}`:                      "bash",
		`<name>read</name>`:                                   "read",
		"\n<tool name=\"edit\">\n<parameter name=\"path\">a":  "edit",
		"\n<tool_call name=\"read\">\n<parameter name=\"x\">": "read",
	} {
		if got := findNameInBody(body); got != want {
			t.Errorf("findNameInBody(%q) = %q, want %q", body, got, want)
		}
	}
}

func TestShouldDecode(t *testing.T) {
	for _, tc := range []struct {
		v, mode string
		want    bool
	}{
		{`hi &lt;friend&gt; &amp; "co"`, DecodeAuto, true},
		{"hi &amp; &quot;co&quot;", DecodeAuto, true},
		{`<a href="?a=1&amp;b=2">Tom &amp; Jerry</a>`, DecodeAuto, false},
		{"a && b &amp; c", DecodeAuto, false},
		{"no entities here", DecodeAuto, false},
		{"&amp;", DecodeOff, false},
		{"<b>&amp;</b>", DecodeAlways, true},
	} {
		if got := shouldDecode(tc.v, tc.mode); got != tc.want {
			t.Errorf("shouldDecode(%q, %s) = %v", tc.v, tc.mode, got)
		}
	}
}

func TestSerializeRoundTrip(t *testing.T) {
	for _, tc := range []struct{ name, schema, args string }{
		{"bash", bashSchema, `{"command":"cd x && ls\necho \"<done>\"","timeout":3}`},
		{"read", readSchema, `{"path":"a.go","offset":1,"limit":20}`},
		{"edit", editSchema, `{"path":"a.go","edits":[{"old_text":"line1\nline2","new_text":"x"},{"old_text":"y","new_text":"z\n  indented"}]}`},
	} {
		xml := serializeToolCall(tc.name, json.RawMessage(tc.args))
		body := xml[strings.Index(xml, ">")+1 : strings.LastIndex(xml, "</tool_call>")]
		args, perr := parseArguments(body, sch(t, tc.schema), false, "")
		if perr != "" {
			t.Fatalf("%s: %s\n%s", tc.name, perr, xml)
		}
		same(t, args, tc.args)
		// Key order is kept, so replays are stable.
		js, _ := marshal(args)
		if string(js) != tc.args {
			t.Errorf("%s: order changed: %s", tc.name, js)
		}
	}
}
