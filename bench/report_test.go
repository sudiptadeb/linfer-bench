package bench

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func sampleResults() Results {
	tools := ToolsScore{Suite: "builtin", Cases: 9, Passed: 8, CallCases: 7, ValidCalls: 7, RightTool: 7, SchemaValid: 6, NoCallCases: 2, NoCallCorrect: 2,
		Failures: []string{"nested object and array args (run 1): create_calendar_event: location: is a string, want an object"}}
	mlxTools := ToolsScore{Suite: "builtin", Cases: 9, Passed: 6, CallCases: 7, ValidCalls: 6, RightTool: 6, SchemaValid: 5, NoCallCases: 2, NoCallCorrect: 1, HTTPErrors: 1, Leaks: 1}
	return Results{
		Schema: SchemaVersion, Tool: "linfer-bench " + Version,
		Started: time.Date(2026, 10, 9, 14, 0, 0, 0, time.UTC), Duration: 600, Title: "m:q8",
		Machine: Machine{OS: "darwin", Arch: "arm64", Chip: "Apple M3 Ultra", CPUs: 32, RAM: 256 << 30, GPU: "metal", GPUMem: 222 << 30, GPUMemSource: "measured"},
		Options: Options{Profile: ProfileFull, Runs: 3, Suites: AllSuites},
		Rows: []Row{
			{Label: "llama", Target: "llama", URL: "http://127.0.0.1:8090/v1", Model: "m:q8", Server: "llama.cpp", Timing: "client-side from the stream, with the server's own timings beside it",
				Info: []KV{{"weights", "176.0 GiB"}, {"launch", "8×131072, cache 16384 MB"}},
				Speed: []SpeedRow{
					{Context: "2k", Concurrency: 1, Runs: 3, DecodeTPS: Stat{Median: 32, Min: 31, Max: 33, N: 3}, CombinedTPS: Stat{Median: 32, Min: 31, Max: 33, N: 3}, TTFT: Stat{Median: 2.1, Min: 2, Max: 2.3, N: 3}, Prefill: Stat{Median: 950, Min: 900, Max: 1000, N: 3}, WarmTTFT: Stat{Median: 0.4, Min: 0.3, Max: 0.5, N: 3},
						ServerDecode: Stat{Median: 31, Min: 30, Max: 32, N: 3}, TokensPerChunk: Stat{Median: 1, Min: 1, Max: 1, N: 3}, Server: map[string]float64{"timings.predicted_per_second": 31}},
					{Context: "2k", Concurrency: 8, Runs: 3, DecodeTPS: Stat{Median: 13, Min: 12, Max: 14, N: 3}, CombinedTPS: Stat{Median: 104, Min: 100, Max: 108, N: 3}, TTFT: Stat{Median: 4, Min: 3.8, Max: 4.2, N: 3}, Prefill: Stat{Median: 500, Min: 480, Max: 520, N: 3}},
				},
				Tools:   &tools,
				Context: []ContextRow{{Context: "2k", Depth: 10, Found: true}, {Context: "2k", Depth: 50, Found: true}, {Context: "2k", Depth: 90, Found: false, Answer: "amber-falcon-7219"}},
			},
			{Label: "mlx", Target: "mlx", URL: "http://127.0.0.1:18080/v1", Model: "m:q8", Server: "oMLX", Timing: "client-side from the stream, with the server's own timings beside it",
				Info: []KV{{"weights", "182.0 GiB"}, {"launch", "6 concurrent, window 262144"}},
				Speed: []SpeedRow{
					{Context: "2k", Concurrency: 1, Runs: 3, DecodeTPS: Stat{Median: 66, Min: 64, Max: 68, N: 3}, CombinedTPS: Stat{Median: 66, Min: 64, Max: 68, N: 3}, TTFT: Stat{Median: 1.5, Min: 1.4, Max: 1.6, N: 3}, Prefill: Stat{Median: 1300, Min: 1250, Max: 1350, N: 3}, WarmTTFT: Stat{Median: 0.3, Min: 0.3, Max: 0.4, N: 3},
						ServerDecode: Stat{Median: 48, Min: 47, Max: 49, N: 3}, ServerPrefill: Stat{Median: 1400, Min: 1380, Max: 1420, N: 3}, TokensPerChunk: Stat{Median: 34.6, Min: 30, Max: 40, N: 3}},
					{Context: "2k", Concurrency: 8, Runs: 3, DecodeTPS: Stat{Median: 13.5, Min: 12, Max: 15, N: 3}, CombinedTPS: Stat{Median: 101, Min: 95, Max: 110, N: 3}, TTFT: Stat{Median: 5, Min: 4.5, Max: 5.5, N: 3}, Prefill: Stat{Median: 450, Min: 400, Max: 500, N: 3},
						Failures: []string{"run 2 stream 7: HTTP 400: prefill exceeds memory"}},
				},
				Tools:   &mlxTools,
				Context: []ContextRow{{Context: "2k", Depth: 10, Found: true}, {Context: "2k", Depth: 50, Found: true}, {Context: "2k", Depth: 90, Found: true}},
			},
			{Label: "spec", Target: "spec", URL: "http://127.0.0.1:8091/v1", Model: "m:q8", Error: "no draft model configured"},
		},
	}
}

// The table has a column per row, a line per cell metric, the tool and
// needle tallies, the callers' details, and the row that did not run says
// why. Columns stay aligned with colour on, which marks the clear best
// green and failures red.
func TestTable(t *testing.T) {
	var b bytes.Buffer
	Table(&b, sampleResults(), false)
	out := b.String()
	for _, want := range []string{
		"metric", "llama", "mlx", "spec",
		"server", "llama.cpp", "oMLX", "weights", "176.0 GiB", "launch", "6 concurrent, window 262144",
		"decode tok/s/stream 2k×1", "32.0 (31.0–33.0)", "66.0 (64.0–68.0)",
		"combined tok/s 2k×8", "104.0 (100.0–108.0)",
		"warm ttft s 2k", "0.40 (0.30–0.50)",
		"  server-reported", "48.0 (47.0–49.0)", "1400 (1380–1420)",
		"  tokens per stream chunk", "34.6 (30.0–40.0)",
		"tools accuracy", "89% (8/9)", "67% (6/9)",
		"leaks / http errors", "1 / 1",
		"needle found", "2 of 3", "3 of 3",
		"failures", "spec did not run: no draft model configured",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("table lacks %q:\n%s", want, out)
		}
	}
	// Every line of the plain table has the same column starts.
	lines := strings.Split(strings.TrimSpace(strings.Split(out, "\n\n")[0]), "\n")
	col := strings.Index(lines[0], "llama")
	for _, l := range lines[1:] {
		if len(l) > col && l[col-1] != ' ' {
			t.Errorf("column misaligned: %q", l)
		}
	}

	b.Reset()
	Table(&b, sampleResults(), true)
	c := b.String()
	if !strings.Contains(c, "\x1b[32;1m66.0 (64.0–68.0)\x1b[0m") {
		t.Error("the clearly faster single stream is not green")
	}
	if strings.Contains(c, "\x1b[32;1m104.0") || strings.Contains(c, "\x1b[32;1m101.0") {
		t.Error("a combined figure within noise was marked best")
	}
	if !strings.Contains(c, "\x1b[31m2\x1b[0m") {
		t.Error("mlx's two failures are not red")
	}
	// Colour codes must not shift the columns: strip them and compare.
	strip := strings.NewReplacer("\x1b[1m", "", "\x1b[2m", "", "\x1b[32;1m", "", "\x1b[31m", "", "\x1b[0m", "")
	if strip.Replace(c) != out {
		t.Errorf("coloured table differs from plain once stripped:\n%s", strip.Replace(c))
	}
}

// The verdict names the fastest single stream with the ratio, calls the
// top-concurrency result within noise when the ranges overlap, gives tool
// accuracy per row, flags bursty streaming, and lists failures.
func TestVerdict(t *testing.T) {
	v := strings.Join(Verdict(sampleResults()), "\n")
	for _, want := range []string{
		"fastest single stream (2k context): mlx at 66.0 tok/s, 2.06× llama (32.0)",
		"most combined output (2k context, 8 streams): llama at 104.0 tok/s; mlx at 101.0 is within noise",
		"tool accuracy: llama 89% (8/9), mlx 67% (6/9)",
		"needle retrieved: llama 2/3, mlx 3/3",
		"mlx: client-side decode 66.0 vs server-reported 48.0 tok/s at 2k×1 (34.6 tokens per stream chunk)",
		"mlx: 2 failure(s), first: speed 2k×8: run 2 stream 7: HTTP 400",
		"spec did not run",
	} {
		if !strings.Contains(v, want) {
			t.Errorf("verdict lacks %q:\n%s", want, v)
		}
	}
	if strings.Contains(v, "single run per cell") {
		t.Error("three runs flagged as a single run")
	}
	one := sampleResults()
	one.Options.Runs = 1
	if !strings.Contains(strings.Join(Verdict(one), "\n"), "single run per cell") {
		t.Error("a single run must carry the noise caveat")
	}
	// A row with client-side figures only is said so, beside one with the server's.
	clientOnly := sampleResults()
	clientOnly.Rows[1].Speed[0].ServerDecode = Stat{}
	if !strings.Contains(strings.Join(Verdict(clientOnly), "\n"), "mlx report no timings of their own") {
		t.Error("client-only timings not noted")
	}
}

// report.md carries the machine, each target's details, the table, the
// measurement note, the tool failures and the errors.
func TestMarkdown(t *testing.T) {
	md := Markdown(sampleResults())
	for _, want := range []string{
		"# linfer-bench: m:q8", "Apple M3 Ultra", "GPU metal 222.0 GiB (measured)", "full profile",
		"**llama**: `http://127.0.0.1:8090/v1`, model `m:q8`, llama.cpp; weights 176.0 GiB; launch 8×131072",
		"**mlx**: `http://127.0.0.1:18080/v1`", "6 concurrent, window 262144",
		"## Verdict", "## Comparison", "decode tok/s/stream 2k×1", "Each speed cell is two passes",
		"### llama: tool failures", "location: is a string, want an object",
		"### mlx: errors", "HTTP 400: prefill exceeds memory",
		"### llama: server-reported timings", "timings.predicted_per_second=31.00",
		"**spec**: `http://127.0.0.1:8091/v1`, model `m:q8`: did not run",
		"results.json schema 1",
	} {
		if !strings.Contains(md, want) {
			t.Errorf("markdown lacks %q", want)
		}
	}
}

// report.html is one self-contained page: the two kinds of chart as inline
// SVG with a bar per row, the overview table with the best marked, the
// sortable detail tables, the target details, light and dark styles and
// no external asset.
func TestHTML(t *testing.T) {
	h, err := HTML(sampleResults())
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"<!doctype html>", "<title>linfer-bench: m:q8</title>",
		"prefers-color-scheme: dark", `data-theme="dark"`, "button class=\"theme\"",
		"Single stream decode, 2k context", "Combined output by concurrency, 2k context", "<svg", `fill="var(--s1)"`, `fill="var(--s2)"`, "<title>mlx: 66.0 tok/s</title>", `class="range"`,
		`<td class="num best">66.0 (64.0–68.0)</td>`, `<tr class="sub"><td>server-reported</td>`,
		`<table class="sortable">`, `data-sort="66.0000"`, "tokens/chunk",
		"Tool calling", "89%", "Needle in a haystack", "amber-falcon-7219",
		"<h2>Targets</h2>", "<code>http://127.0.0.1:8090/v1</code>", "weights 176.0 GiB;", "did not run: no draft model configured",
		"1 tool failure(s)", "server-reported timings", "localStorage",
		"fastest single stream (2k context): mlx at 66.0 tok/s, 2.06× llama (32.0)",
	} {
		if !strings.Contains(h, want) {
			t.Errorf("html lacks %q", want)
		}
	}
	for _, bad := range []string{"<script src=", "<link ", "http://fonts", "cdn."} {
		if strings.Contains(h, bad) {
			t.Errorf("html has an external reference: %q", bad)
		}
	}
	if strings.Count(h, `<path d="`) < 6 {
		t.Errorf("want bars for 2 rows × (1 single-stream + 2 concurrency) charts, got %d paths", strings.Count(h, `<path d="`))
	}
}

// results.json round-trips, and a file from another schema is refused
// with its number.
func TestSaveLoad(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "results.json")
	if err := Save(path, sampleResults()); err != nil {
		t.Fatal(err)
	}
	r, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(r.Rows) != 3 || r.Rows[1].Speed[0].TokensPerChunk.Median != 34.6 || r.Rows[0].Info[0].Value != "176.0 GiB" || r.Options.Profile != ProfileFull {
		t.Errorf("round trip lost data: %+v", r.Rows[1].Speed[0])
	}
	os.WriteFile(path, []byte(`{"schema": 7}`), 0o644)
	if _, err := Load(path); err == nil || !strings.Contains(err.Error(), "schema 7") {
		t.Errorf("schema 7 accepted: %v", err)
	}
	files, err := WriteFiles(filepath.Join(dir, "out"), sampleResults())
	if err != nil || len(files) != 3 {
		t.Fatal(err)
	}
	for _, f := range files {
		if st, err := os.Stat(f); err != nil || st.Size() == 0 {
			t.Errorf("%s: %v", f, err)
		}
	}
}

// compare merges runs: every row becomes a column, a label present in both
// runs gets its run's time behind it, the notes say where each came from
// and when the runs differ in machine or depth, and the fewest runs sets
// the noise rule.
func TestMerge(t *testing.T) {
	before := sampleResults()
	before.Rows = before.Rows[:1]
	after := sampleResults()
	after.Rows = after.Rows[:1]
	after.Started = after.Started.Add(24 * time.Hour)
	after.Rows[0].Speed[0].DecodeTPS = Stat{Median: 40, Min: 39, Max: 41, N: 1}
	after.Options.Runs = 1
	after.Machine.Chip = "Apple M4 Max"
	m := Merge([]Results{before, after}, []string{"a/results.json", "b/results.json"})
	if len(m.Rows) != 2 || m.Rows[0].Label != "llama @10-09 14:00" || m.Rows[1].Label != "llama @10-10 14:00" {
		t.Fatalf("labels %q %q", m.Rows[0].Label, m.Rows[1].Label)
	}
	notes := strings.Join(m.Notes, "\n")
	for _, want := range []string{"llama @10-09 14:00: m:q8 from a/results.json", "from b/results.json (2026-10-10 14:00, full profile)", "b/results.json ran on another machine: darwin/arm64, Apple M4 Max", "b/results.json measured with 1 run(s)"} {
		if !strings.Contains(notes, want) {
			t.Errorf("notes lack %q:\n%s", want, notes)
		}
	}
	if m.Options.Runs != 1 || m.Title != "m:q8 (compared)" {
		t.Errorf("runs %d title %q", m.Options.Runs, m.Title)
	}
	v := strings.Join(Verdict(m), "\n")
	if !strings.Contains(v, "llama @10-10 14:00 at 40.0 tok/s, 1.25× llama @10-09 14:00 (32.0)") || !strings.Contains(v, "single run per cell") {
		t.Errorf("verdict:\n%s", v)
	}
	// Distinct labels keep their names.
	other := sampleResults()
	other.Rows = other.Rows[1:2]
	m = Merge([]Results{before, other}, nil)
	if m.Rows[0].Label != "llama" || m.Rows[1].Label != "mlx" {
		t.Errorf("labels %q %q", m.Rows[0].Label, m.Rows[1].Label)
	}
}
