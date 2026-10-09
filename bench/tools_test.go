package bench

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func caseNamed(t *testing.T, s Suite, name string) Case {
	t.Helper()
	for _, c := range s.Cases {
		if c.Name == name {
			return c
		}
	}
	t.Fatalf("no case %q", name)
	return Case{}
}

func reply(content string, calls ...ToolCall) Reply {
	return Reply{Status: 200, Content: content, ToolCalls: calls}
}

func call(id, name, args string) ToolCall {
	return ToolCall{ID: id, Type: "function", Function: FunctionCall{Name: name, Arguments: args}}
}

// A clean call with the right tool and arguments passes; each kind of
// malformed reply fails with a sentence that says what was wrong.
func TestScoreToolCases(t *testing.T) {
	s := Builtin()
	weather := caseNamed(t, s, "pick the right tool, enum arg")
	cases := []struct {
		name string
		c    Case
		r    Reply
		pass bool
		why  string
	}{
		{"good call", weather, reply("", call("1", "get_weather", `{"location":"Paris","unit":"celsius"}`)), true, ""},
		{"wrong tool", weather, reply("", call("1", "web_search", `{"query":"Paris weather"}`)), false, "called [web_search], want [get_weather]"},
		{"broken json", weather, reply("", call("1", "get_weather", `{"location":"Paris",`)), false, "not valid JSON"},
		{"bad enum", weather, reply("", call("1", "get_weather", `{"location":"Paris","unit":"kelvin"}`)), false, `"kelvin" is not one of`},
		{"missing required", weather, reply("", call("1", "get_weather", `{"unit":"celsius"}`)), false, "missing required argument location"},
		{"unknown argument", weather, reply("", call("1", "get_weather", `{"location":"Paris","city":"Paris"}`)), false, "unknown argument city"},
		{"wrong city", weather, reply("", call("1", "get_weather", `{"location":"Rome","unit":"celsius"}`)), false, `location "Rome" does not contain "paris"`},
		{"no call", weather, reply("It is sunny in Paris."), false, "no tool call"},
		{"leaked markup", weather, reply(`<tool_call>{"name":"get_weather","arguments":{"location":"Paris"}}</tool_call>`), false, "leaked into content"},
		{"leaked reasoning", weather, reply("<think>I should call get_weather</think>", call("1", "get_weather", `{"location":"Paris","unit":"celsius"}`)), false, "leaked into content"},
		{"http error", weather, Reply{Status: 400, Err: "HTTP 400: prompt exceeds memory"}, false, "HTTP 400"},
		{"no-call case, answered", caseNamed(t, s, "no call when none is needed"), reply("Waves fold the light\nand carry it home."), true, ""},
		{"no-call case, called anyway", caseNamed(t, s, "no call when none is needed"), reply("", call("1", "web_search", `{"query":"sea poem"}`)), false, "called web_search when no tool was needed"},
		{"no-call case, empty", caseNamed(t, s, "no call when none is needed"), reply(""), false, "empty content"},
		{"two calls", caseNamed(t, s, "two calls in one turn"), reply("", call("1", "get_weather", `{"location":"Tokyo"}`), call("2", "get_weather", `{"location":"Berlin"}`)), true, ""},
		{"one of two", caseNamed(t, s, "two calls in one turn"), reply("", call("1", "get_weather", `{"location":"Tokyo"}`)), false, "want [get_weather get_weather]"},
		{"two calls, same city", caseNamed(t, s, "two calls in one turn"), reply("", call("1", "get_weather", `{"location":"Tokyo"}`), call("2", "get_weather", `{"location":"Tokyo"}`)), false, `does not contain "berlin"`},
		{"follow-up answered", caseNamed(t, s, "follow-up after a tool result"), reply("It is 7°C in Oslo with light rain."), true, ""},
		{"follow-up re-called", caseNamed(t, s, "follow-up after a tool result"), reply("", call("2", "get_weather", `{"location":"Oslo"}`)), false, "when no tool was needed"},
		{"exact id", caseNamed(t, s, "argument copied exactly from context"), reply("", call("1", "lookup_account", `{"account_id":"4471029385"}`)), true, ""},
		{"id as number", caseNamed(t, s, "argument copied exactly from context"), reply("", call("1", "lookup_account", `{"account_id":4471029385}`)), false, "want a string"},
		{"id mangled", caseNamed(t, s, "argument copied exactly from context"), reply("", call("1", "lookup_account", `{"account_id":"447102938"}`)), false, "want 4471029385"},
		{"nested ok", caseNamed(t, s, "nested object and array args"), reply("", call("1", "create_calendar_event",
			`{"title":"Design review","start":"2026-10-12T10:00","end":"2026-10-12T11:00","attendees":["alice@example.com","bob@example.com"],"location":{"name":"the Hub","address":"12 Main Street"}}`)), true, ""},
		{"nested as string", caseNamed(t, s, "nested object and array args"), reply("", call("1", "create_calendar_event",
			`{"title":"Design review","start":"2026-10-12T10:00","end":"2026-10-12T11:00","attendees":["alice@example.com","bob@example.com"],"location":"the Hub"}`)), false, "want an object"},
		{"one attendee", caseNamed(t, s, "nested object and array args"), reply("", call("1", "create_calendar_event",
			`{"title":"Design review","start":"2026-10-12T10:00","end":"2026-10-12T11:00","attendees":["alice@example.com"],"location":{"name":"the Hub"}}`)), false, "attendees has 1 item(s), want 2"},
		{"integer as string", caseNamed(t, s, "integer arg"), reply("", call("1", "web_search", `{"query":"Go release notes","max_results":"3"}`)), false, "want an integer"},
		{"integer ok", caseNamed(t, s, "integer arg"), reply("", call("1", "web_search", `{"query":"Go release notes","max_results":3}`)), true, ""},
		{"wrong integer", caseNamed(t, s, "integer arg"), reply("", call("1", "web_search", `{"query":"Go release notes","max_results":5}`)), false, "max_results is 5, want 3"},
		{"two required ok", caseNamed(t, s, "two required args"), reply("", call("1", "write_file", `{"path":"notes.txt","content":"hello world"}`)), true, ""},
	}
	for _, c := range cases {
		o := s.Score(c.c, c.r, 1)
		if o.Pass != c.pass {
			t.Errorf("%s: pass %v, want %v (%s)", c.name, o.Pass, c.pass, o.Why)
			continue
		}
		if !c.pass && !strings.Contains(o.Why, c.why) {
			t.Errorf("%s: why %q, want it to mention %q", c.name, o.Why, c.why)
		}
	}
}

// The tally counts each component where it fails: valid JSON, right tool,
// schema, no-call, leaks, HTTP errors.
func TestTally(t *testing.T) {
	s := Builtin()
	weather := caseNamed(t, s, "pick the right tool, enum arg")
	noCall := caseNamed(t, s, "no call when none is needed")
	replies := []Reply{
		reply("", call("1", "get_weather", `{"location":"Paris","unit":"celsius"}`)), // pass
		reply("", call("1", "get_weather", `{"location":"Paris","unit":"kelvin"}`)),  // right tool, bad schema
		reply("", call("1", "calculate", `{"expression":"1"}`)),                      // valid, wrong tool
		reply("", call("1", "get_weather", `{bad`)),                                  // invalid json
		Reply{Status: 400, Err: "HTTP 400: too long"},                                // http error
		reply("<think>hm</think>ok"),                                                 // leak, on the no-call case
		reply("Here is a poem."),                                                     // no-call pass
	}
	cases := []Case{weather, weather, weather, weather, weather, noCall, noCall}
	var outcomes []Outcome
	for i, r := range replies {
		outcomes = append(outcomes, s.Score(cases[i], r, 1))
	}
	sc := s.Tally(outcomes, replies)
	if sc.Cases != 7 || sc.Passed != 2 || sc.CallCases != 5 || sc.NoCallCases != 2 || sc.NoCallCorrect != 1 {
		t.Errorf("counts %+v", sc)
	}
	if sc.ValidCalls != 3 || sc.RightTool != 2 || sc.SchemaValid != 1 || sc.HTTPErrors != 1 || sc.Leaks != 1 {
		t.Errorf("components valid %d right %d schema %d http %d leaks %d", sc.ValidCalls, sc.RightTool, sc.SchemaValid, sc.HTTPErrors, sc.Leaks)
	}
	if len(sc.Failures) != 5 || sc.Accuracy() < 28 || sc.Accuracy() > 29 || sc.Suite != "builtin" {
		t.Errorf("failures %d, accuracy %.1f, suite %q", len(sc.Failures), sc.Accuracy(), sc.Suite)
	}
}

// The built-in suite has its nine cases, and every expected tool exists in
// its tool set, so a right answer can always be schema-checked.
func TestBuiltinSuite(t *testing.T) {
	s := Builtin()
	if len(s.Cases) != 9 || len(s.Tools) != 7 {
		t.Fatalf("%d cases, %d tools", len(s.Cases), len(s.Tools))
	}
	for _, c := range s.Cases {
		for _, w := range c.Expect.Tools {
			if s.schemaOf(c, w) == nil {
				t.Errorf("%s wants unknown tool %s", c.Name, w)
			}
		}
	}
	// The replayed call in the follow-up case was read from its mapping form.
	fu := caseNamed(t, s, "follow-up after a tool result")
	if tc := fu.Messages[1].ToolCalls; len(tc) != 1 || tc[0].Function.Name != "get_weather" || !strings.Contains(tc[0].Function.Arguments, `"location":"Oslo"`) {
		t.Errorf("replayed call %+v", tc)
	}
}

// A user's cases load from YAML and from JSON lines, with the same checks,
// and a case may bring its own tools.
func TestLoadSuiteYAMLAndJSONL(t *testing.T) {
	dir := t.TempDir()
	yml := filepath.Join(dir, "mine.yaml")
	os.WriteFile(yml, []byte(`
tools:
  - name: ping
    description: Ping a host.
    parameters: {type: object, required: [host], properties: {host: {type: string}}}
cases:
  - name: ping it
    messages: [{role: user, content: "Ping example.com"}]
    expect: {tools: [ping], args: [{path: host, equals: example.com}]}
  - name: own tool
    tools:
      - name: shout
        parameters: {type: object, properties: {text: {type: string}}}
    messages: [{role: user, content: "Shout hi"}]
    expect: {tools: [shout], args: [{path: text, matches: "^[A-Z]+$"}]}
`), 0o644)
	s, err := LoadSuite(yml)
	if err != nil {
		t.Fatal(err)
	}
	if s.Name != "mine" || len(s.Cases) != 2 || s.Cases[1].Tools[0].Function.Name != "shout" {
		t.Fatalf("suite %+v", s)
	}
	if o := s.Score(s.Cases[0], reply("", call("1", "ping", `{"host":"example.com"}`)), 1); !o.Pass {
		t.Errorf("ping: %s", o.Why)
	}
	if o := s.Score(s.Cases[1], reply("", call("1", "shout", `{"text":"hi"}`)), 1); o.Pass || !strings.Contains(o.Why, "does not match") {
		t.Errorf("shout: pass %v %s", o.Pass, o.Why)
	}
	// The tool set of case 2 is its own: ping is unknown there.
	if o := s.Score(s.Cases[1], reply("", call("1", "ping", `{"host":"x"}`)), 1); o.Pass || !strings.Contains(o.Why, "want [shout]") {
		t.Errorf("wrong tool: %s", o.Why)
	}

	jsonl := filepath.Join(dir, "mine.jsonl")
	os.WriteFile(jsonl, []byte(`{"tools":[{"name":"ping","parameters":{"type":"object","required":["host"],"properties":{"host":{"type":"string"}}}}]}
{"name":"ping it","messages":[{"role":"user","content":"Ping example.com"}],"expect":{"tools":["ping"],"args":[{"path":"host","contains":"example"}]}}
{"name":"chat","messages":[{"role":"user","content":"hi"}],"expect":{"no_call":true}}
`), 0o644)
	s, err = LoadSuite(jsonl)
	if err != nil {
		t.Fatal(err)
	}
	if len(s.Cases) != 2 || len(s.Tools) != 1 || !s.Cases[1].Expect.NoCall {
		t.Fatalf("jsonl suite %+v", s)
	}
}

// A cases file with a mistake is refused with the case named: no
// expectation, an unknown tool, two checks in one args entry, an unknown
// key.
func TestLoadSuiteRefusesMistakes(t *testing.T) {
	for _, c := range []struct{ yaml, want string }{
		{"cases:\n  - name: a\n    messages: [{role: user, content: x}]\n", `case "a" expects neither tools nor no_call`},
		{"cases:\n  - name: a\n    messages: [{role: user, content: x}]\n    expect: {tools: [nope]}\n", `unknown tool "nope"`},
		{"tools: [{name: t}]\ncases:\n  - name: a\n    messages: [{role: user, content: x}]\n    expect: {tools: [t], args: [{path: p, equals: 1, contains: x}]}\n", "exactly one of"},
		{"cases:\n  - name: a\n    message: []\n", "field message not found"},
		{"cases: []\n", "no cases"},
	} {
		_, err := parseSuite([]byte(c.yaml), "t")
		if err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%q: error %v, want %q", c.yaml, err, c.want)
		}
	}
}

// parseReply reads what a server sends, including a null content with
// calls, and reports an unparseable body as an error rather than a pass.
func TestParseReply(t *testing.T) {
	r := parseReply([]byte(`{"choices":[{"message":{"role":"assistant","content":null,"reasoning_content":"thinking","tool_calls":[{"id":"c1","type":"function","function":{"name":"calculate","arguments":"{\"expression\":\"1+1\"}"}}]}}]}`), Reply{Status: 200})
	if r.Err != "" || r.Content != "" || r.Reasoning != "thinking" || len(r.ToolCalls) != 1 || r.ToolCalls[0].Function.Name != "calculate" {
		t.Errorf("got %+v", r)
	}
	if r := parseReply([]byte(`not json`), Reply{Status: 200}); r.Err == "" {
		t.Error("garbage parsed")
	}
	if r := parseReply([]byte(`{"choices":[]}`), Reply{Status: 200}); r.Err == "" {
		t.Error("no choices parsed")
	}
}
