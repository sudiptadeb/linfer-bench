package bench

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

// A bench.yaml carries targets with their own models, shared models,
// k-suffixed contexts, durations and sampling; a target without a name is
// named after its address.
func TestParseFile(t *testing.T) {
	f, err := ParseFile([]byte(`
targets:
  - name: llama
    url: http://127.0.0.1:8090/v1
    max_context: 32768
  - url: http://127.0.0.1:18080
    models: [all]
    api_key_env: MLX_KEY
models: [qwen3-coder:30b]
profile: quick
suites: [speed, tools]
contexts: [2k, 8192]
concurrency: [1, 4]
runs: 2
reply_tokens: 300
sampling: {temperature: 0.7, top_p: 0.9}
request_timeout: 20m
settle: 500ms
parallel: true
out: ./here
`))
	if err != nil {
		t.Fatal(err)
	}
	if len(f.Targets) != 2 || f.Targets[0].MaxContext != 32768 || f.Targets[1].Name != "127.0.0.1:18080" || f.Targets[1].URL != "http://127.0.0.1:18080/v1" || f.Targets[1].APIKeyEnv != "MLX_KEY" || f.Targets[1].Models[0] != "all" {
		t.Errorf("targets %+v", f.Targets)
	}
	o := f.Options
	if f.Models[0] != "qwen3-coder:30b" || o.Profile != ProfileQuick || len(o.Suites) != 2 || o.Contexts[0] != 2048 || o.Contexts[1] != 8192 || o.Concurrency[1] != 4 ||
		o.Runs != 2 || o.ReplyTokens != 300 || *o.Sampling.Temperature != 0.7 || *o.Sampling.TopP != 0.9 ||
		time.Duration(o.RequestTimeout) != 20*time.Minute || time.Duration(o.Settle) != 500*time.Millisecond || !o.Parallel || o.Out != "./here" {
		t.Errorf("options %+v", o)
	}
	// Defaults fill only what the file left out.
	o.Defaults()
	if o.Runs != 2 || o.ReplyTokens != 300 || o.ToolPasses != 1 || len(o.Depths) != 1 || o.Depths[0] != 50 {
		t.Errorf("defaults %+v", o)
	}
}

// Unknown keys, an unknown profile or suite, and a target without a url are
// refused with a reason.
func TestParseFileRefuses(t *testing.T) {
	for _, c := range []struct{ yaml, want string }{
		{"targets: [{url: http://h/v1}]\ncontext: [2k]\n", "field context not found"},
		{"targets: [{url: http://h/v1}]\nprofile: fast\n", `unknown profile "fast"`},
		{"targets: [{url: http://h/v1}]\nsuites: [speed, latency]\n", `unknown suite "latency"`},
		{"targets: [{name: a}]\n", "target 1 has no url"},
		{"targets: [{url: http://h/v1}]\ncontexts: [2x]\n", "want positive numbers"},
	} {
		_, err := ParseFile([]byte(c.yaml))
		if err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%q: %v, want %q", c.yaml, err, c.want)
		}
	}
}

// Each profile has its shape; the quick one is small.
func TestProfiles(t *testing.T) {
	var q Options
	q.Profile = ProfileQuick
	q.Defaults()
	if q.Runs != 1 || q.ReplyTokens != 256 || len(q.Contexts) != 1 || len(q.Concurrency) != 2 || q.ToolPasses != 1 {
		t.Errorf("quick %+v", q)
	}
	var d Options
	d.Defaults()
	if d.Profile != ProfileDefault || d.Runs != 2 || len(d.Contexts) != 2 || len(d.Concurrency) != 3 || len(d.Depths) != 3 || len(d.Suites) != 3 {
		t.Errorf("default %+v", d)
	}
	var f Options
	f.Profile = ProfileFull
	f.Defaults()
	if f.Runs != 3 || f.ReplyTokens != 512 || f.Contexts[1] != 32768 || f.Concurrency[3] != 8 || f.ToolPasses != 3 {
		t.Errorf("full %+v", f)
	}
	if time.Duration(f.Settle) != 2*time.Second || time.Duration(f.RequestTimeout) != 30*time.Minute {
		t.Errorf("timings %+v", f)
	}
	rq, dq := Estimate(q, 9)
	rf, df := Estimate(f, 9)
	if rq <= 0 || dq <= 0 || rf <= rq || df <= dq {
		t.Errorf("estimates quick %d/%s full %d/%s", rq, dq, rf, df)
	}
}

// Durations round-trip through JSON as strings, and the old nanosecond
// form still reads.
func TestDurationJSON(t *testing.T) {
	raw, _ := json.Marshal(Options{Settle: Duration(2 * time.Second)})
	if !strings.Contains(string(raw), `"settle":"2s"`) {
		t.Errorf("json %s", raw)
	}
	var o Options
	if err := json.Unmarshal([]byte(`{"settle":"3s","request_timeout":60000000000}`), &o); err != nil || time.Duration(o.Settle) != 3*time.Second || time.Duration(o.RequestTimeout) != time.Minute {
		t.Errorf("%v %+v", err, o)
	}
}
