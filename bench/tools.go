package bench

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"regexp"
	"strings"
)

// Outcome is one case run, scored.
type Outcome struct {
	Case      string `json:"case"`
	Run       int    `json:"run"`
	Pass      bool   `json:"pass"`
	HTTPError bool   `json:"http_error,omitempty"`
	// What went wrong, one line; empty on a pass.
	Why     string `json:"why,omitempty"`
	Content string `json:"content,omitempty"`
}

// ToolsScore is the suite's tally for one row.
type ToolsScore struct {
	Suite         string    `json:"suite"`
	Cases         int       `json:"cases"`
	Passed        int       `json:"passed"`
	ValidCalls    int       `json:"valid_calls"`  // responses whose calls all parsed (of those expecting a call)
	RightTool     int       `json:"right_tool"`   // and named the expected tools
	SchemaValid   int       `json:"schema_valid"` // and whose arguments fit the schema
	NoCallCorrect int       `json:"no_call_correct"`
	NoCallCases   int       `json:"no_call_cases"`
	CallCases     int       `json:"call_cases"`
	Leaks         int       `json:"leaks"` // markup or reasoning in content
	HTTPErrors    int       `json:"http_errors"`
	Outcomes      []Outcome `json:"outcomes"`
	Failures      []string  `json:"failures,omitempty"`
}

// Accuracy is passed over cases, as a percentage.
func (s ToolsScore) Accuracy() float64 {
	if s.Cases == 0 {
		return 0
	}
	return 100 * float64(s.Passed) / float64(s.Cases)
}

var leakRE = regexp.MustCompile(`<think>|</think>|<tool_call>|<\|im_|<\|channel\|>|\{"name"\s*:\s*"[a-z_]+"\s*,\s*"arguments"`)

// Score judges one reply against its case. Every failure has a sentence.
func (s Suite) Score(c Case, r Reply, run int) Outcome {
	o := Outcome{Case: c.Name, Run: run, Content: firstLine(r.Content)}
	if r.Err != "" {
		o.HTTPError = r.Status != 0 && r.Status/100 != 2 || r.Status == 0
		o.Why = r.Err
		return o
	}
	if leakRE.MatchString(r.Content) {
		o.Why = "reasoning or tool markup leaked into content: " + firstLine(r.Content)
		return o
	}
	if len(c.Expect.Tools) == 0 {
		if len(r.ToolCalls) > 0 {
			o.Why = fmt.Sprintf("called %s when no tool was needed", r.ToolCalls[0].Function.Name)
			return o
		}
		if strings.TrimSpace(r.Content) == "" {
			o.Why = "empty content with no tool call"
			return o
		}
		o.Pass = true
		return o
	}
	if len(r.ToolCalls) == 0 {
		o.Why = "no tool call; content: " + firstLine(r.Content)
		return o
	}
	parsed, bad := parseCalls(r.ToolCalls)
	if bad != "" {
		o.Why = bad
		return o
	}
	if !rightTools(parsed, c.Expect.Tools) {
		o.Why = fmt.Sprintf("called %v, want %v", names(parsed), c.Expect.Tools)
		return o
	}
	for _, p := range parsed {
		if why := checkSchema(s.schemaOf(c, p.Name), p.Args, ""); why != "" {
			o.Why = p.Name + ": " + why
			return o
		}
	}
	if why := c.Expect.check(parsed); why != "" {
		o.Why = why
		return o
	}
	o.Pass = true
	return o
}

// parseCalls decodes every call's arguments; the second result says which
// one was not JSON.
func parseCalls(calls []ToolCall) ([]ParsedCall, string) {
	var parsed []ParsedCall
	for _, tc := range calls {
		var args map[string]any
		if err := json.Unmarshal([]byte(tc.Function.Arguments), &args); err != nil {
			return nil, fmt.Sprintf("%s: arguments are not valid JSON: %s", tc.Function.Name, firstLine(tc.Function.Arguments))
		}
		parsed = append(parsed, ParsedCall{Name: tc.Function.Name, Args: args})
	}
	return parsed, ""
}

// rightTools says the calls name exactly the expected tools, each as many
// times as expected, in any order.
func rightTools(parsed []ParsedCall, want []string) bool {
	if len(parsed) != len(want) {
		return false
	}
	got := map[string]int{}
	for _, p := range parsed {
		got[p.Name]++
	}
	for _, w := range want {
		got[w]--
	}
	for _, n := range got {
		if n != 0 {
			return false
		}
	}
	return true
}

func names(ps []ParsedCall) []string {
	var out []string
	for _, p := range ps {
		out = append(out, p.Name)
	}
	return out
}

// checkSchema is the slice of JSON Schema the suites' tools use: type,
// required, properties, enum, items. It returns why a value does not fit.
func checkSchema(schema map[string]any, v any, path string) string {
	if schema == nil {
		return "unknown tool"
	}
	at := func(s string) string {
		if path == "" {
			return s
		}
		return path + ": " + s
	}
	typ, _ := schema["type"].(string)
	switch typ {
	case "object":
		m, ok := v.(map[string]any)
		if !ok {
			return at(fmt.Sprintf("is %s, want an object", jsonType(v)))
		}
		for _, r := range stringList(schema["required"]) {
			if _, ok := m[r]; !ok {
				return at("missing required argument " + r)
			}
		}
		props, _ := schema["properties"].(map[string]any)
		for k, val := range m {
			ps, ok := props[k].(map[string]any)
			if !ok {
				return at("unknown argument " + k)
			}
			if why := checkSchema(ps, val, joinPath(path, k)); why != "" {
				return why
			}
		}
	case "array":
		arr, ok := v.([]any)
		if !ok {
			return at(fmt.Sprintf("is %s, want an array", jsonType(v)))
		}
		if items, ok := schema["items"].(map[string]any); ok {
			for i, x := range arr {
				if why := checkSchema(items, x, fmt.Sprintf("%s[%d]", path, i)); why != "" {
					return why
				}
			}
		}
	case "string":
		s, ok := v.(string)
		if !ok {
			return at(fmt.Sprintf("is %s, want a string", jsonType(v)))
		}
		if enum := stringList(schema["enum"]); len(enum) > 0 {
			for _, e := range enum {
				if e == s {
					return ""
				}
			}
			return at(fmt.Sprintf("%q is not one of %v", s, enum))
		}
	case "integer":
		f, ok := v.(float64)
		if !ok || f != math.Trunc(f) {
			return at(fmt.Sprintf("is %s, want an integer", jsonType(v)))
		}
	case "number":
		if _, ok := v.(float64); !ok {
			return at(fmt.Sprintf("is %s, want a number", jsonType(v)))
		}
	case "boolean":
		if _, ok := v.(bool); !ok {
			return at(fmt.Sprintf("is %s, want a boolean", jsonType(v)))
		}
	}
	return ""
}

// stringList reads a schema list that the YAML or JSON decoder produced.
func stringList(v any) []string {
	switch x := v.(type) {
	case []string:
		return x
	case []any:
		var out []string
		for _, e := range x {
			out = append(out, fmt.Sprint(e))
		}
		return out
	}
	return nil
}

func joinPath(p, k string) string {
	if p == "" {
		return k
	}
	return p + "." + k
}

func jsonType(v any) string {
	switch v.(type) {
	case nil:
		return "null"
	case string:
		return "a string"
	case float64:
		return "a number"
	case bool:
		return "a boolean"
	case []any:
		return "an array"
	case map[string]any:
		return "an object"
	}
	return fmt.Sprintf("%T", v)
}

// Tally folds outcomes into a score. Each outcome counts once; the
// component counters say where the failures were.
func (s Suite) Tally(outcomes []Outcome, replies []Reply) ToolsScore {
	score := ToolsScore{Suite: s.Name, Outcomes: outcomes}
	byName := map[string]Case{}
	for _, c := range s.Cases {
		byName[c.Name] = c
	}
	for i, o := range outcomes {
		c := byName[o.Case]
		score.Cases++
		if o.Pass {
			score.Passed++
		} else {
			score.Failures = append(score.Failures, fmt.Sprintf("%s (run %d): %s", o.Case, o.Run, o.Why))
		}
		if o.HTTPError {
			score.HTTPErrors++
		}
		if strings.HasPrefix(o.Why, "reasoning or tool markup leaked") {
			score.Leaks++
		}
		if len(c.Expect.Tools) == 0 {
			score.NoCallCases++
			if o.Pass {
				score.NoCallCorrect++
			}
			continue
		}
		score.CallCases++
		if i >= len(replies) {
			continue
		}
		r := replies[i]
		if r.Err != "" || len(r.ToolCalls) == 0 {
			continue
		}
		parsed, bad := parseCalls(r.ToolCalls)
		if bad != "" {
			continue
		}
		score.ValidCalls++
		if !rightTools(parsed, c.Expect.Tools) {
			continue
		}
		score.RightTool++
		schema := true
		for _, p := range parsed {
			if checkSchema(s.schemaOf(c, p.Name), p.Args, "") != "" {
				schema = false
			}
		}
		if schema {
			score.SchemaValid++
		}
	}
	return score
}

// runTools runs the suite `passes` times against the endpoint.
func runTools(ctx context.Context, cl *Client, s Suite, passes int, progress func(string)) ToolsScore {
	var outcomes []Outcome
	var replies []Reply
	for run := 1; run <= passes; run++ {
		for _, c := range s.Cases {
			if ctx.Err() != nil {
				break
			}
			r := cl.Chat(ctx, c.Messages, s.toolsFor(c), 1024)
			o := s.Score(c, r, run)
			outcomes = append(outcomes, o)
			replies = append(replies, r)
			mark := "ok  "
			if !o.Pass {
				mark = "FAIL"
			}
			progress(fmt.Sprintf("%s %s%s", mark, c.Name, ifs(!o.Pass, ": "+o.Why, "")))
		}
	}
	return s.Tally(outcomes, replies)
}
