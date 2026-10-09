package bench

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

// The suites.
const (
	SuiteSpeed   = "speed"
	SuiteTools   = "tools"
	SuiteContext = "context"
)

// AllSuites is every suite, in the order they run.
var AllSuites = []string{SuiteSpeed, SuiteTools, SuiteContext}

// The profiles: how much to measure.
const (
	ProfileQuick   = "quick"   // a few minutes: one run per cell, one pass of the tools
	ProfileDefault = "default" // two runs, two contexts, three concurrency levels
	ProfileFull    = "full"    // three runs, 2k and 32k, up to 8 streams
)

// Options is what a run measures. Zero fields are filled by Defaults from
// the profile, so a caller sets only what it wants to pin.
type Options struct {
	Profile     string   `yaml:"profile" json:"profile"`
	Suites      []string `yaml:"suites" json:"suites"`
	Contexts    Sizes    `yaml:"contexts" json:"contexts"`       // prompt tokens per speed and context cell
	Concurrency []int    `yaml:"concurrency" json:"concurrency"` // streams at once
	Runs        int      `yaml:"runs" json:"runs"`               // per speed cell; the median is reported
	ReplyTokens int      `yaml:"reply_tokens" json:"reply_tokens"`
	ToolPasses  int      `yaml:"tool_passes" json:"tool_passes"` // passes over the tool suite
	Depths      []int    `yaml:"depths" json:"depths"`           // needle depths, percent
	// Cases is a cases file (YAML or JSON lines); empty is the built-in suite.
	Cases string `yaml:"cases" json:"cases,omitempty"`
	// Sampling pins temperature and top_p on every request; nil leaves each
	// server's defaults.
	Sampling *Sampling `yaml:"sampling" json:"sampling,omitempty"`
	// RequestTimeout bounds one request; Settle is the pause between the
	// speed suite's cold and warm passes.
	RequestTimeout Duration `yaml:"request_timeout" json:"request_timeout"`
	Settle         Duration `yaml:"settle" json:"settle"`
	// Parallel runs the targets at once. Off by default: local backends on
	// one machine contend for its memory and GPU.
	Parallel bool `yaml:"parallel" json:"parallel,omitempty"`
	// Out is the report directory.
	Out string `yaml:"out" json:"out,omitempty"`
}

// Defaults fills what was not given from the profile.
func (o *Options) Defaults() {
	if o.Profile == "" {
		o.Profile = ProfileDefault
	}
	if len(o.Suites) == 0 {
		o.Suites = append([]string(nil), AllSuites...)
	}
	switch o.Profile {
	case ProfileQuick:
		setInts(&o.Concurrency, []int{1, 4})
		setSizes(&o.Contexts, Sizes{2048})
		setInt(&o.Runs, 1)
		setInt(&o.ReplyTokens, 256)
		setInt(&o.ToolPasses, 1)
		setInts(&o.Depths, []int{50})
	case ProfileFull:
		setInts(&o.Concurrency, []int{1, 2, 4, 8})
		setSizes(&o.Contexts, Sizes{2048, 32768})
		setInt(&o.Runs, 3)
		setInt(&o.ReplyTokens, 512)
		setInt(&o.ToolPasses, 3)
		setInts(&o.Depths, []int{10, 50, 90})
	default:
		setInts(&o.Concurrency, []int{1, 2, 4})
		setSizes(&o.Contexts, Sizes{2048, 8192})
		setInt(&o.Runs, 2)
		setInt(&o.ReplyTokens, 384)
		setInt(&o.ToolPasses, 2)
		setInts(&o.Depths, []int{10, 50, 90})
	}
	if o.RequestTimeout <= 0 {
		o.RequestTimeout = Duration(30 * time.Minute)
	}
	if o.Settle <= 0 {
		o.Settle = Duration(2 * time.Second)
	}
}

// Validate refuses a profile or suite it does not know.
func (o Options) Validate() error {
	switch o.Profile {
	case "", ProfileQuick, ProfileDefault, ProfileFull:
	default:
		return fmt.Errorf("unknown profile %q; want quick, default or full", o.Profile)
	}
	for _, s := range o.Suites {
		switch s {
		case SuiteSpeed, SuiteTools, SuiteContext:
		default:
			return fmt.Errorf("unknown suite %q; want %s", s, strings.Join(AllSuites, ", "))
		}
	}
	return nil
}

// Has says whether the suite is on.
func (o Options) Has(suite string) bool {
	for _, s := range o.Suites {
		if s == suite {
			return true
		}
	}
	return false
}

// Suite is the tool suite the options name: the cases file, or the
// built-in one.
func (o Options) Suite() (Suite, error) {
	if o.Cases == "" {
		return Builtin(), nil
	}
	return LoadSuite(o.Cases)
}

func setInts(p *[]int, v []int) {
	if len(*p) == 0 {
		*p = v
	}
}

func setSizes(p *Sizes, v Sizes) {
	if len(*p) == 0 {
		*p = v
	}
}

func setInt(p *int, v int) {
	if *p == 0 {
		*p = v
	}
}

// Estimate is the request count and a rough duration for one row of the
// options: a cost model of prefill at 500 tok/s, decode at 30 tok/s per
// stream with concurrent streams sharing it, 8 s per tool case. It is for
// the line printed before the run, not for the report.
func Estimate(o Options, cases int) (requests int, d time.Duration) {
	o.Defaults()
	seconds := 0.0
	if o.Has(SuiteSpeed) {
		for _, c := range o.Contexts {
			for _, conc := range o.Concurrency {
				requests += 2 * o.Runs * conc // a cold and a warm pass
				seconds += float64(o.Runs) * (float64(c)/500*float64(conc) + time.Duration(o.Settle).Seconds() + float64(o.ReplyTokens)/30*1.5)
			}
		}
	}
	if o.Has(SuiteTools) {
		requests += cases * o.ToolPasses
		seconds += float64(cases*o.ToolPasses) * 8
	}
	if o.Has(SuiteContext) {
		for _, c := range o.Contexts {
			requests += len(o.Depths)
			seconds += float64(len(o.Depths)) * (float64(c)/500 + 5)
		}
	}
	requests += 2 // warm-up and calibration
	return requests, time.Duration(seconds * float64(time.Second))
}

// --- the YAML forms -----------------------------------------------------------------

// Sizes is a list of token counts that YAML may write as `2k`.
type Sizes []int

// UnmarshalYAML accepts numbers and k-suffixed strings.
func (s *Sizes) UnmarshalYAML(n *yaml.Node) error {
	var raw []string
	if err := n.Decode(&raw); err != nil {
		return err
	}
	out, err := ParseList(strings.Join(raw, ","))
	if err != nil {
		return err
	}
	*s = out
	return nil
}

// Duration is a time.Duration that YAML and JSON write as "30m".
type Duration time.Duration

// UnmarshalYAML reads a duration string.
func (d *Duration) UnmarshalYAML(n *yaml.Node) error {
	var v time.Duration
	if err := n.Decode(&v); err != nil {
		return err
	}
	*d = Duration(v)
	return nil
}

// MarshalJSON writes the duration as a string.
func (d Duration) MarshalJSON() ([]byte, error) { return json.Marshal(time.Duration(d).String()) }

// UnmarshalJSON reads a duration string, or a count of nanoseconds.
func (d *Duration) UnmarshalJSON(raw []byte) error {
	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		v, err := time.ParseDuration(s)
		if err != nil {
			return err
		}
		*d = Duration(v)
		return nil
	}
	var n int64
	if err := json.Unmarshal(raw, &n); err != nil {
		return err
	}
	*d = Duration(n)
	return nil
}

// ParseList reads "1,2,4,8" or "2k,32k" (k is 1024).
func ParseList(s string) ([]int, error) {
	if strings.TrimSpace(s) == "" {
		return nil, nil
	}
	var out []int
	for _, f := range strings.Split(s, ",") {
		f = strings.TrimSpace(strings.ToLower(f))
		mult := 1
		if strings.HasSuffix(f, "k") {
			mult, f = 1024, strings.TrimSuffix(f, "k")
		}
		n, err := strconv.Atoi(f)
		if err != nil || n <= 0 {
			return nil, fmt.Errorf("%q: want positive numbers such as 1,2,4 or 2k,32k", s)
		}
		out = append(out, n*mult)
	}
	return out, nil
}

// --- targets ------------------------------------------------------------------------

// Target is one endpoint to bench: a name for the report, the base URL of
// its OpenAI-compatible API, the models to bench on it, and where its key
// is.
type Target struct {
	Name string `yaml:"name" json:"name"`
	URL  string `yaml:"url" json:"url"` // ends in /v1
	// Models are ids from /v1/models, or ["all"]. Empty takes the file's
	// `models`, and failing that the one model the endpoint serves.
	Models []string `yaml:"models" json:"models,omitempty"`
	// APIKeyEnv names the environment variable holding the bearer token.
	APIKeyEnv string `yaml:"api_key_env" json:"api_key_env,omitempty"`
	// MaxContext is the longest prompt the server takes, in tokens, when
	// known: cells past it are skipped rather than tried. 0 tries them all.
	MaxContext int `yaml:"max_context" json:"max_context,omitempty"`
}

// ParseTarget reads a `--target` flag: `name=url`, or a bare URL named
// after its host and port.
func ParseTarget(s string) (Target, error) {
	name, url := "", strings.TrimSpace(s)
	if i := strings.Index(s, "="); i > 0 && !strings.Contains(s[:i], "/") {
		name, url = strings.TrimSpace(s[:i]), strings.TrimSpace(s[i+1:])
	}
	if !strings.HasPrefix(url, "http://") && !strings.HasPrefix(url, "https://") {
		return Target{}, fmt.Errorf("target %q: want name=http://host:port/v1", s)
	}
	url = strings.TrimRight(url, "/")
	if !strings.HasSuffix(url, "/v1") {
		url += "/v1"
	}
	if name == "" {
		name = strings.TrimSuffix(strings.TrimPrefix(strings.TrimPrefix(url, "https://"), "http://"), "/v1")
	}
	return Target{Name: name, URL: url}, nil
}

// File is a bench.yaml: targets, the models they share, and the options.
//
//	targets:
//	  - name: llama
//	    url: http://127.0.0.1:8090/v1
//	  - name: mlx
//	    url: http://127.0.0.1:18080/v1
//	    models: [all]
//	models: [qwen3-coder:30b]
//	profile: quick
//	suites: [speed, tools, context]
//	contexts: [2k, 8k]
//	concurrency: [1, 4]
//	runs: 2
//	reply_tokens: 384
//	sampling: {temperature: 0.7, top_p: 0.9}
//	request_timeout: 20m
//	tags: [after-upgrade]
//	out: ./bench-out
type File struct {
	Targets []Target `yaml:"targets"`
	Models  []string `yaml:"models"`
	Tags    []string `yaml:"tags"`
	Options `yaml:",inline"`
}

// LoadFile reads a bench.yaml. Unknown keys are an error.
func LoadFile(path string) (File, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return File{}, err
	}
	return ParseFile(raw)
}

// ParseFile reads bench.yaml content.
func ParseFile(raw []byte) (File, error) {
	var f File
	dec := yaml.NewDecoder(bytes.NewReader(raw))
	dec.KnownFields(true)
	if err := dec.Decode(&f); err != nil {
		return f, err
	}
	for i, t := range f.Targets {
		if t.URL == "" {
			return f, fmt.Errorf("target %d has no url", i+1)
		}
		pt, err := ParseTarget(t.URL)
		if err != nil {
			return f, err
		}
		f.Targets[i].URL = pt.URL
		if t.Name == "" {
			f.Targets[i].Name = pt.Name
		}
	}
	return f, f.Options.Validate()
}
