package bench

import (
	"bufio"
	"bytes"
	_ "embed"
	"encoding/json"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"

	"gopkg.in/yaml.v3"
)

// A tool-calling suite is a file: the tools every case is offered, and the
// cases, each with its messages and what a right answer looks like. The
// built-in suite is one such file, embedded; a user's cases use the same
// format, as YAML or as JSON lines.
//
//	tools:
//	  - name: get_weather
//	    description: Get the current weather for a location.
//	    parameters: {type: object, required: [location], properties: {...}}
//	cases:
//	  - name: pick the right tool
//	    messages:
//	      - {role: user, content: "What's the weather in Paris, in celsius?"}
//	    expect:
//	      tools: [get_weather]
//	      args:
//	        - {path: location, contains: paris}
//	        - {path: unit, equals: celsius}
//	  - name: no call when none is needed
//	    messages: [{role: user, content: "Write a two-line poem. Use no tools."}]
//	    expect: {no_call: true}
//
// In JSON lines a line is a case, or `{"tools": [...]}` to set the tools the
// following cases are offered.

//go:embed cases/builtin.yaml
var builtinCases []byte

// Suite is a set of cases with the tools they are offered.
type Suite struct {
	Name  string `yaml:"name" json:"name"`
	Tools []Tool `yaml:"tools" json:"tools"`
	Cases []Case `yaml:"cases" json:"cases"`
}

// Case is one tool-calling case.
type Case struct {
	Name     string    `yaml:"name" json:"name"`
	Messages []Message `yaml:"messages" json:"messages"`
	// Tools, when set, are offered instead of the suite's for this case.
	Tools  []Tool `yaml:"tools,omitempty" json:"tools,omitempty"`
	Expect Expect `yaml:"expect" json:"expect"`
}

// Expect is what a right answer looks like: the tools called (by name, in
// any order, each as many times as listed), or no call at all, and checks
// on the arguments.
type Expect struct {
	Tools  []string   `yaml:"tools,omitempty" json:"tools,omitempty"`
	NoCall bool       `yaml:"no_call,omitempty" json:"no_call,omitempty"`
	Args   []ArgCheck `yaml:"args,omitempty" json:"args,omitempty"`
}

// ArgCheck is one condition on the arguments of the calls to Tool (every
// call when Tool is empty). Path is dotted into the arguments
// (`location.name`, `attendees.0`). Exactly one of Equals, Contains,
// Matches, Type and Length applies. Equals compares type as well as value:
// an id the case gives as a string fails when the model sends a number.
// Every selected call must pass unless Any is set, when one is enough.
type ArgCheck struct {
	Tool     string `yaml:"tool,omitempty" json:"tool,omitempty"`
	Path     string `yaml:"path" json:"path"`
	Equals   any    `yaml:"equals,omitempty" json:"equals,omitempty"`
	Contains string `yaml:"contains,omitempty" json:"contains,omitempty"` // case-insensitive substring
	Matches  string `yaml:"matches,omitempty" json:"matches,omitempty"`   // a regular expression
	Type     string `yaml:"type,omitempty" json:"type,omitempty"`         // string, number, integer, object, array, boolean
	Length   *int   `yaml:"length,omitempty" json:"length,omitempty"`     // of an array or a string
	Any      bool   `yaml:"any,omitempty" json:"any,omitempty"`
}

// Builtin is the bundled suite: nine generic cases over public schemas, the
// things an agent harness meets every day. Nothing in it is specific to a
// model.
func Builtin() Suite {
	s, err := parseSuite(builtinCases, "builtin")
	if err != nil {
		panic("built-in cases: " + err.Error())
	}
	return s
}

// LoadSuite reads a cases file, YAML or JSON lines by its extension.
func LoadSuite(path string) (Suite, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return Suite{}, err
	}
	name := strings.TrimSuffix(filepath.Base(path), filepath.Ext(path))
	if strings.EqualFold(filepath.Ext(path), ".jsonl") {
		return parseJSONL(raw, name)
	}
	return parseSuite(raw, name)
}

func parseSuite(raw []byte, name string) (Suite, error) {
	var s Suite
	dec := yaml.NewDecoder(bytes.NewReader(raw))
	dec.KnownFields(true)
	if err := dec.Decode(&s); err != nil {
		return s, err
	}
	if s.Name == "" {
		s.Name = name
	}
	return s, s.validate()
}

func parseJSONL(raw []byte, name string) (Suite, error) {
	s := Suite{Name: name}
	sc := bufio.NewScanner(bytes.NewReader(raw))
	sc.Buffer(make([]byte, 1<<20), 1<<26)
	line := 0
	for sc.Scan() {
		line++
		text := strings.TrimSpace(sc.Text())
		if text == "" || strings.HasPrefix(text, "#") {
			continue
		}
		var probe struct {
			Name  string          `json:"name"`
			Tools json.RawMessage `json:"tools"`
		}
		if err := json.Unmarshal([]byte(text), &probe); err != nil {
			return s, fmt.Errorf("line %d: %w", line, err)
		}
		if probe.Name == "" {
			// A tools line: the set the following cases are offered.
			var tl struct {
				Tools []Tool `json:"tools"`
			}
			if err := yamlFromJSON(text, &tl); err != nil {
				return s, fmt.Errorf("line %d: %w", line, err)
			}
			s.Tools = tl.Tools
			continue
		}
		var c Case
		if err := yamlFromJSON(text, &c); err != nil {
			return s, fmt.Errorf("line %d: %w", line, err)
		}
		s.Cases = append(s.Cases, c)
	}
	if err := sc.Err(); err != nil {
		return s, err
	}
	return s, s.validate()
}

// yamlFromJSON decodes JSON through the YAML decoder, so the flat tool and
// tool-call forms the files allow work in both formats.
func yamlFromJSON(text string, v any) error {
	dec := yaml.NewDecoder(strings.NewReader(text))
	dec.KnownFields(true)
	return dec.Decode(v)
}

func (s Suite) validate() error {
	if len(s.Cases) == 0 {
		return fmt.Errorf("no cases")
	}
	seen := map[string]bool{}
	for i, c := range s.Cases {
		if c.Name == "" {
			return fmt.Errorf("case %d has no name", i+1)
		}
		if seen[c.Name] {
			return fmt.Errorf("case %q appears twice", c.Name)
		}
		seen[c.Name] = true
		if len(c.Messages) == 0 {
			return fmt.Errorf("case %q has no messages", c.Name)
		}
		if len(c.Expect.Tools) == 0 && !c.Expect.NoCall {
			return fmt.Errorf("case %q expects neither tools nor no_call", c.Name)
		}
		if len(c.Expect.Tools) > 0 && c.Expect.NoCall {
			return fmt.Errorf("case %q expects both tools and no_call", c.Name)
		}
		for _, w := range c.Expect.Tools {
			if s.schemaOf(c, w) == nil {
				return fmt.Errorf("case %q expects unknown tool %q", c.Name, w)
			}
		}
		for _, a := range c.Expect.Args {
			if a.Path == "" {
				return fmt.Errorf("case %q: an args check has no path", c.Name)
			}
			if a.Matches != "" {
				if _, err := regexp.Compile(a.Matches); err != nil {
					return fmt.Errorf("case %q: %s: %v", c.Name, a.Path, err)
				}
			}
			if n := countChecks(a); n != 1 {
				return fmt.Errorf("case %q: %s: want exactly one of equals, contains, matches, type, length (got %d)", c.Name, a.Path, n)
			}
		}
	}
	return nil
}

func countChecks(a ArgCheck) int {
	n := 0
	if a.Equals != nil {
		n++
	}
	if a.Contains != "" {
		n++
	}
	if a.Matches != "" {
		n++
	}
	if a.Type != "" {
		n++
	}
	if a.Length != nil {
		n++
	}
	return n
}

// toolsFor is the tool set a case is offered.
func (s Suite) toolsFor(c Case) []Tool {
	if len(c.Tools) > 0 {
		return c.Tools
	}
	return s.Tools
}

// schemaOf is the parameter schema of the named tool for the case, nil
// when the case is not offered it.
func (s Suite) schemaOf(c Case, name string) map[string]any {
	for _, t := range s.toolsFor(c) {
		if t.Function.Name == name {
			return t.Function.Parameters
		}
	}
	return nil
}

// --- the file forms of tools and tool calls ---------------------------------------

// UnmarshalYAML accepts a tool as the files write it, flat
// (`name`, `description`, `parameters`), and as the API nests it
// (`type`, `function: {...}`).
func (t *Tool) UnmarshalYAML(n *yaml.Node) error {
	var flat struct {
		Type        string         `yaml:"type"`
		Name        string         `yaml:"name"`
		Description string         `yaml:"description"`
		Parameters  map[string]any `yaml:"parameters"`
		Function    *ToolFunction  `yaml:"function"`
	}
	if err := n.Decode(&flat); err != nil {
		return err
	}
	t.Type = "function"
	if flat.Function != nil {
		t.Function = *flat.Function
	} else {
		t.Function = ToolFunction{Name: flat.Name, Description: flat.Description, Parameters: flat.Parameters}
	}
	if t.Function.Name == "" {
		return fmt.Errorf("a tool has no name")
	}
	if t.Function.Parameters == nil {
		t.Function.Parameters = map[string]any{"type": "object", "properties": map[string]any{}}
	}
	return nil
}

// UnmarshalYAML accepts a replayed call flat (`id`, `name`, `arguments`)
// or nested under `function`; arguments may be a JSON string or a mapping.
func (c *ToolCall) UnmarshalYAML(n *yaml.Node) error {
	var flat struct {
		ID        string `yaml:"id"`
		Type      string `yaml:"type"`
		Name      string `yaml:"name"`
		Arguments any    `yaml:"arguments"`
		Function  *struct {
			Name      string `yaml:"name"`
			Arguments any    `yaml:"arguments"`
		} `yaml:"function"`
	}
	if err := n.Decode(&flat); err != nil {
		return err
	}
	c.ID, c.Type = flat.ID, "function"
	name, args := flat.Name, flat.Arguments
	if flat.Function != nil {
		name, args = flat.Function.Name, flat.Function.Arguments
	}
	if name == "" {
		return fmt.Errorf("a tool call has no name")
	}
	c.Function.Name = name
	switch a := args.(type) {
	case nil:
		c.Function.Arguments = "{}"
	case string:
		c.Function.Arguments = a
	default:
		raw, err := json.Marshal(a)
		if err != nil {
			return err
		}
		c.Function.Arguments = string(raw)
	}
	return nil
}

// --- argument checks ------------------------------------------------------------------

// ParsedCall is a tool call with its arguments decoded.
type ParsedCall struct {
	Name string
	Args map[string]any
}

// check runs the expectation's argument checks over the parsed calls and
// returns why they fail, or "".
func (e Expect) check(calls []ParsedCall) string {
	for _, a := range e.Args {
		var selected []ParsedCall
		for _, c := range calls {
			if a.Tool == "" || c.Name == a.Tool {
				selected = append(selected, c)
			}
		}
		if len(selected) == 0 {
			return "no call to " + a.Tool
		}
		first, passes := "", 0
		for _, c := range selected {
			if why := a.checkCall(c); why == "" {
				passes++
			} else if first == "" {
				first = why
			}
		}
		if a.Any && passes > 0 {
			continue
		}
		if first != "" {
			return first
		}
	}
	return ""
}

func (a ArgCheck) checkCall(c ParsedCall) string {
	v, ok := lookup(c.Args, a.Path)
	if !ok {
		return a.Path + " is missing"
	}
	switch {
	case a.Equals != nil:
		want := normalise(a.Equals)
		if jsonType(want) != jsonType(v) {
			return fmt.Sprintf("%s is %s, want %s", a.Path, jsonType(v), jsonType(want))
		}
		if !equal(v, want) {
			return fmt.Sprintf("%s is %s, want %v", a.Path, show(v), want)
		}
	case a.Contains != "":
		s, ok := v.(string)
		if !ok {
			return fmt.Sprintf("%s is %s, want a string", a.Path, jsonType(v))
		}
		if !strings.Contains(strings.ToLower(s), strings.ToLower(a.Contains)) {
			return fmt.Sprintf("%s %q does not contain %q", a.Path, s, a.Contains)
		}
	case a.Matches != "":
		s := show(v)
		if str, ok := v.(string); ok {
			s = str
		}
		if !regexp.MustCompile(a.Matches).MatchString(s) {
			return fmt.Sprintf("%s %q does not match %s", a.Path, s, a.Matches)
		}
	case a.Type != "":
		if got := jsonType(v); got != article(a.Type) {
			return fmt.Sprintf("%s is %s, want %s", a.Path, got, article(a.Type))
		}
	case a.Length != nil:
		n := -1
		switch x := v.(type) {
		case []any:
			n = len(x)
		case string:
			n = len(x)
		case map[string]any:
			n = len(x)
		}
		if n != *a.Length {
			return fmt.Sprintf("%s has %d item(s), want %d", a.Path, n, *a.Length)
		}
	}
	return ""
}

// lookup follows a dotted path through decoded JSON; a numeric segment
// indexes an array.
func lookup(v any, path string) (any, bool) {
	for _, seg := range strings.Split(path, ".") {
		switch x := v.(type) {
		case map[string]any:
			next, ok := x[seg]
			if !ok {
				return nil, false
			}
			v = next
		case []any:
			i, err := strconv.Atoi(seg)
			if err != nil || i < 0 || i >= len(x) {
				return nil, false
			}
			v = x[i]
		default:
			return nil, false
		}
	}
	return v, true
}

// normalise turns a value the YAML decoder produced into the shape the
// JSON decoder produces, so the two compare: ints become float64, nested
// maps become map[string]any.
func normalise(v any) any {
	switch x := v.(type) {
	case int:
		return float64(x)
	case int64:
		return float64(x)
	case float32:
		return float64(x)
	case []any:
		out := make([]any, len(x))
		for i := range x {
			out[i] = normalise(x[i])
		}
		return out
	case map[string]any:
		out := map[string]any{}
		for k, val := range x {
			out[k] = normalise(val)
		}
		return out
	case map[any]any:
		out := map[string]any{}
		for k, val := range x {
			out[fmt.Sprint(k)] = normalise(val)
		}
		return out
	}
	return v
}

func equal(a, b any) bool {
	switch x := a.(type) {
	case float64:
		y, ok := b.(float64)
		return ok && math.Abs(x-y) < 1e-9
	case []any, map[string]any:
		ra, _ := json.Marshal(a)
		rb, _ := json.Marshal(b)
		return bytes.Equal(ra, rb)
	}
	return a == b
}

func show(v any) string {
	switch x := v.(type) {
	case string:
		return strconv.Quote(x)
	case float64:
		if x == math.Trunc(x) {
			return strconv.FormatInt(int64(x), 10)
		}
	}
	raw, _ := json.Marshal(v)
	return string(raw)
}

// article is the JSON type with its article, as jsonType writes it, for the
// `type` check's messages.
func article(t string) string {
	switch t {
	case "string":
		return "a string"
	case "number", "integer":
		return "a number"
	case "boolean":
		return "a boolean"
	case "array":
		return "an array"
	case "object":
		return "an object"
	case "null":
		return "null"
	}
	return t
}
