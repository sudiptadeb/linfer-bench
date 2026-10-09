package bench

import (
	"strings"
	"testing"
	"time"
)

// taggedRun is a one-row run of the sample's llama row with the decode and
// combined figures scaled, so several runs under a tag have a spread.
func taggedRun(id string, at time.Time, tags []string, scale float64, rowTags ...string) Results {
	r := sampleResults()
	r.ID, r.Tags, r.Started = id, tags, at
	r.Rows = r.Rows[:1]
	r.Rows[0].Tags = rowTags
	for i := range r.Rows[0].Speed {
		s := &r.Rows[0].Speed[i]
		for _, st := range []*Stat{&s.DecodeTPS, &s.CombinedTPS, &s.ServerDecode} {
			st.Median *= scale
			st.Min *= scale
			st.Max *= scale
		}
	}
	return r
}

// Several runs under one tag aggregate to the median of their medians with
// the spread across runs and n the run count; tool and needle tallies
// pool; a tag with one run is that run; a tag no run carries is a column
// that says so.
func TestCompareTagsAggregates(t *testing.T) {
	at := time.Date(2026, 10, 9, 14, 0, 0, 0, time.UTC)
	runs := []Results{
		taggedRun("r1", at, []string{"baseline"}, 1.0),
		taggedRun("r2", at.Add(time.Hour), []string{"baseline"}, 1.1),
		taggedRun("r3", at.Add(2*time.Hour), []string{"baseline"}, 0.9),
		taggedRun("r4", at.Add(3*time.Hour), []string{"mlx-mtp"}, 2.0),
		taggedRun("r5", at.Add(4*time.Hour), nil, 3.0, "backend:llama"), // a row tag
	}
	c := CompareTags(runs, []string{"baseline", "mlx-mtp", "backend:llama", "nothing"})
	if c.Comparison == nil || len(c.Rows) != 4 || c.Title != "baseline vs mlx-mtp vs backend:llama vs nothing" {
		t.Fatalf("comparison %+v title %q", c.Comparison, c.Title)
	}
	if strings.Join(c.Comparison.Sources, ",") != "r1,r2,r3,r4,r5" {
		t.Errorf("sources %v", c.Comparison.Sources)
	}
	base := c.Rows[0]
	if base.Label != "baseline" || len(base.Runs) != 3 || base.Info[0].Key != "runs" || base.Info[0].Value != "3" {
		t.Fatalf("baseline row %+v", base)
	}
	d := base.SpeedAt("2k", 1).DecodeTPS
	if d.N != 3 || d.Median != 32 || d.Min < 28.79 || d.Min > 28.81 || d.Max < 35.19 || d.Max > 35.21 {
		t.Errorf("aggregated decode %+v, want median 32 over 28.8–35.2 n=3", d)
	}
	if base.Tools == nil || base.Tools.Cases != 27 || base.Tools.Passed != 24 || len(base.Tools.Failures) != 3 || !strings.HasPrefix(base.Tools.Failures[1], "r2: ") {
		t.Errorf("pooled tools %+v", base.Tools)
	}
	if f, n := base.ContextFound(); f != 6 || n != 9 {
		t.Errorf("pooled needle %d/%d", f, n)
	}
	one := c.Rows[1]
	if one.Label != "mlx-mtp" || len(one.Runs) != 1 || one.Runs[0] != "r4" || one.SpeedAt("2k", 1).DecodeTPS.Median != 64 || one.SpeedAt("2k", 1).DecodeTPS.N != 3 {
		t.Errorf("single-run tag %+v", one)
	}
	if c.Rows[2].Label != "backend:llama" || c.Rows[2].Runs[0] != "r5" {
		t.Errorf("row tag not found: %+v", c.Rows[2])
	}
	if c.Rows[3].Error != "no run carries this tag" {
		t.Errorf("empty tag %+v", c.Rows[3])
	}
	if len(c.Comparison.Warnings) != 0 {
		t.Errorf("like-for-like runs warned: %v", c.Comparison.Warnings)
	}
	v := strings.Join(Verdict(c), "\n")
	for _, want := range []string{
		"decode 2k×1: backend:llama 96.0 (93.0–99.0) tok/s > mlx-mtp 64.0 (62.0–66.0) tok/s (1.50×) > baseline 32.0 (28.8–35.2) tok/s (2.00×)",
		"combined 2k×8: backend:llama 312.0 (300.0–324.0) tok/s > mlx-mtp 208.0 (200.0–216.0) tok/s (1.50×) > baseline 104.0 (93.6–114.4) tok/s (2.00×)",
		"cold TTFT 2k×1: ", "(within noise)",
		"tool accuracy: baseline 89% (24/27), mlx-mtp 89% (8/9), backend:llama 89% (8/9)",
		"needle retrieved: baseline 6/9",
		"nothing: no run carries this tag",
	} {
		if !strings.Contains(v, want) {
			t.Errorf("verdict lacks %q:\n%s", want, v)
		}
	}
}

// Two tags whose medians sit inside each other's spread are within noise,
// not a winner; a lower-is-better metric reads "faster".
func TestCompareVerdictNoise(t *testing.T) {
	at := time.Now()
	runs := []Results{
		taggedRun("a1", at, []string{"a"}, 1.0), taggedRun("a2", at, []string{"a"}, 1.2),
		taggedRun("b1", at, []string{"b"}, 1.1), taggedRun("b2", at, []string{"b"}, 1.15),
	}
	c := CompareTags(runs, []string{"a", "b"})
	v := strings.Join(CompareVerdict(c), "\n")
	if !strings.Contains(v, "decode 2k×1: b 36.0 (35.2–36.8) tok/s ≈ a 35.2 (32.0–38.4) tok/s (within noise)") {
		t.Errorf("verdict:\n%s", v)
	}
	// TTFT is the same in every sample run: within noise, and worded as faster when not.
	c.Rows[1].Speed[0].TTFT = Stat{Median: 1.0, Min: 1.0, Max: 1.0, N: 2}
	v = strings.Join(CompareVerdict(c), "\n")
	if !strings.Contains(v, "cold TTFT 2k×1: b 1.00 (1.00–1.00) s > a 2.10 (2.10–2.10) s (2.10× faster)") {
		t.Errorf("ttft verdict:\n%s", v)
	}
}

// A cell one tag measured and another did not is marked n/a and listed,
// never filled in or compared.
func TestCompareTagsMissingCells(t *testing.T) {
	at := time.Now()
	a := taggedRun("a", at, []string{"a"}, 1.0)
	b := taggedRun("b", at, []string{"b"}, 1.0)
	b.Rows[0].Speed = b.Rows[0].Speed[:1] // b has 2k×1 only
	c := CompareTags([]Results{a, b}, []string{"a", "b"})
	if len(c.Comparison.Missing) != 1 || c.Comparison.Missing[0] != "2k×8 was not measured for b" {
		t.Errorf("missing %v", c.Comparison.Missing)
	}
	s := c.Rows[1].SpeedAt("2k", 8)
	if s == nil || !s.Missing() {
		t.Fatalf("no placeholder for b 2k×8: %+v", c.Rows[1].Speed)
	}
	var tb strings.Builder
	Table(&tb, c, false)
	if !strings.Contains(tb.String(), "n/a") {
		t.Errorf("table lacks n/a:\n%s", tb.String())
	}
	v := strings.Join(Verdict(c), "\n")
	if strings.Contains(v, "combined 2k×8") || !strings.Contains(v, "missing: 2k×8 was not measured for b") {
		t.Errorf("verdict compared a missing cell or did not list it:\n%s", v)
	}
	if len(c.Rows[1].AllFailures()) != 0 {
		t.Errorf("a missing cell counted as a failure: %v", c.Rows[1].AllFailures())
	}
}

// Runs that differ in profile, reply length, sampling or machine make the
// comparison unfair, and the warnings say which tags carry which.
func TestCompareTagsWarnsWhenUnfair(t *testing.T) {
	at := time.Now()
	a := taggedRun("a", at, []string{"a"}, 1.0)
	b := taggedRun("b", at, []string{"b"}, 1.0)
	b.Options.Profile = ProfileQuick
	b.Options.ReplyTokens = 256
	temp := 0.7
	b.Options.Sampling = &Sampling{Temperature: &temp}
	b.Machine.Chip = "Apple M4 Max"
	a.Options.ReplyTokens = 512
	c := CompareTags([]Results{a, b}, []string{"a", "b"})
	w := strings.Join(c.Comparison.Warnings, "\n")
	for _, want := range []string{
		"profile differs: full for a; quick for b",
		"reply length differs: 512 tokens for a; 256 tokens for b",
		`sampling differs: the server's defaults for a; {"temperature":0.7} for b`,
		"machine differs: darwin/arm64, Apple M3 Ultra, 32 CPUs, 256.0 GiB RAM for a; darwin/arm64, Apple M4 Max, 32 CPUs, 256.0 GiB RAM for b",
	} {
		if !strings.Contains(w, want) {
			t.Errorf("warnings lack %q:\n%s", want, w)
		}
	}
	v := Verdict(c)
	if len(v) < 4 || !strings.HasPrefix(v[0], "not like for like: profile differs") {
		t.Errorf("verdict does not lead with the warnings: %v", v)
	}
	md := Markdown(c)
	if !strings.Contains(md, "> **Not like for like:** profile differs") || !strings.Contains(md, "A comparison of tags a, b over runs a, b.") {
		t.Errorf("markdown lacks the comparison head:\n%s", md[:600])
	}
	h, err := HTML(c)
	if err != nil || !strings.Contains(h, `<div class="warn">Not like for like: profile differs`) || !strings.Contains(h, "<strong>a</strong>, <strong>b</strong>") || !strings.Contains(h, "Single stream decode, 2k context") {
		t.Errorf("html lacks the comparison head: %v", err)
	}
	// A like-for-like pair has no warnings, even when only one of the runs
	// knew its GPU.
	b1 := taggedRun("b1", at, []string{"b"}, 1.0)
	b1.Machine.GPU, b1.Machine.GPUMem = "", 0
	c = CompareTags([]Results{taggedRun("a1", at, []string{"a"}, 1.0), b1}, []string{"a", "b"})
	if len(c.Comparison.Warnings) != 0 {
		t.Errorf("unexpected warnings %v", c.Comparison.Warnings)
	}
}
