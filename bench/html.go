package bench

import (
	"bytes"
	"fmt"
	"html/template"
	"math"
	"sort"
	"strings"
	"time"
)

// report.html is self-contained: no external assets, its own styles for
// light and dark, sortable tables, and inline SVG bar charts for the two
// numbers people compare first, single-stream decode and combined output
// per concurrency. The transposed comparison table of the terminal is at
// the top; below it every cell is a row of a sortable table.

type htmlData struct {
	Title, Started, Duration, Machine, Profile, Tool string
	ID, Tags                                         string
	Comparison                                       *Comparison
	Schema                                           int
	Verdict                                          []string
	Legend                                           []legendItem
	Charts                                           []chartBlock
	Overview                                         []overviewTR
	OverviewHead                                     []string
	Speed                                            []speedTR
	Tools                                            []toolsTR
	Context                                          []ctxTR
	Targets                                          []targetTR
	Details                                          []rowDetail
	Notes                                            []string
	Measurement                                      string
}

type legendItem struct {
	Label string
	Slot  int
}

type chartBlock struct {
	Title string
	Note  string
	SVG   template.HTML
}

type overviewTR struct {
	Label string
	Sub   bool
	Cells []td
}

// td is a table cell with a sort key and whether it is the best of its line.
type td struct {
	Text string
	Sort string
	Best bool
	Dim  bool
}

type speedTR struct {
	Label                                                                       string
	Slot                                                                        int
	Context                                                                     string
	ContextSort                                                                 int
	Conc                                                                        int
	Decode, ServerDecode, TPC, Combined, TTFT, Prefill, ServerPrefill, WarmTTFT td
	Failures                                                                    int
}

type toolsTR struct {
	Label                                          string
	Slot                                           int
	Accuracy                                       td
	Passed, Cases, Valid, Right, Schema, CallCases int
	NoCallCorrect, NoCallCases, Leaks, HTTPErrors  int
}

type ctxTR struct {
	Label   string
	Slot    int
	Context string
	Tokens  int
	Depth   int
	Found   string
	Answer  string
}

type targetTR struct {
	Label, Target, URL, Model, Server, Timing, Error string
	Runs, Tags                                       string
	Slot                                             int
	Info                                             []KV
}

type rowDetail struct {
	Label                               string
	ToolFailures, Errors, ServerTimings []string
}

// HTML renders report.html.
func HTML(r Results) (string, error) {
	d := htmlData{
		Title: r.Title, Started: r.Started.Format("2006-01-02 15:04"), Machine: r.Machine.String(), Profile: r.Options.Profile,
		Duration: time.Duration(r.Duration * float64(time.Second)).Round(time.Second).String(), Tool: r.Tool, Schema: r.Schema,
		Verdict: Verdict(r), Notes: r.Notes, Measurement: Measurement,
		ID: r.ID, Tags: strings.Join(AllTags(r), ", "), Comparison: r.Comparison,
	}
	slots := map[string]int{}
	for i, row := range r.Rows {
		slots[row.Label] = i
		if len(r.Rows) > 1 {
			d.Legend = append(d.Legend, legendItem{Label: row.Label, Slot: slot(i)})
		}
	}
	d.OverviewHead = []string{"metric"}
	for _, row := range r.Rows {
		d.OverviewHead = append(d.OverviewHead, row.Label)
	}
	for _, l := range lines(r) {
		tr := overviewTR{Label: strings.TrimSpace(l.label), Sub: l.style == dim}
		for _, c := range l.cells {
			tr.Cells = append(tr.Cells, td{Text: c.text, Best: c.style == green, Dim: c.style == dim})
		}
		d.Overview = append(d.Overview, tr)
	}
	d.Charts = charts(r)
	for _, row := range r.Rows {
		s := slot(slots[row.Label])
		for _, sp := range row.Speed {
			if sp.Skipped() {
				continue
			}
			d.Speed = append(d.Speed, speedTR{
				Label: row.Label, Slot: s, Context: sp.Context, ContextSort: tokensOf(sp.Context), Conc: sp.Concurrency,
				Decode: stat(sp.DecodeTPS, "%.1f"), ServerDecode: stat(sp.ServerDecode, "%.1f"), TPC: stat(sp.TokensPerChunk, "%.1f"),
				Combined: stat(sp.CombinedTPS, "%.1f"), TTFT: stat(sp.TTFT, "%.2f"), Prefill: stat(sp.Prefill, "%.0f"),
				ServerPrefill: stat(sp.ServerPrefill, "%.0f"), WarmTTFT: stat(sp.WarmTTFT, "%.2f"), Failures: len(sp.Failures),
			})
		}
		if t := row.Tools; t != nil {
			d.Tools = append(d.Tools, toolsTR{Label: row.Label, Slot: s, Accuracy: td{Text: fmt.Sprintf("%.0f%%", t.Accuracy()), Sort: fmt.Sprintf("%.2f", t.Accuracy())},
				Passed: t.Passed, Cases: t.Cases, Valid: t.ValidCalls, Right: t.RightTool, Schema: t.SchemaValid, CallCases: t.CallCases,
				NoCallCorrect: t.NoCallCorrect, NoCallCases: t.NoCallCases, Leaks: t.Leaks, HTTPErrors: t.HTTPErrors})
		}
		for _, c := range row.Context {
			found := "yes"
			switch {
			case c.Skipped():
				found = "skipped"
			case c.Err != "":
				found = "error"
			case !c.Found:
				found = "no"
			}
			d.Context = append(d.Context, ctxTR{Label: row.Label, Slot: s, Context: c.Context, Tokens: c.Tokens, Depth: c.Depth, Found: found, Answer: ifs(c.Err != "", c.Err, c.Answer)})
		}
		d.Targets = append(d.Targets, targetTR{Label: row.Label, Slot: s, Target: row.Target, URL: row.URL, Model: row.Model, Server: strings.TrimSpace(row.Server + " " + row.ServerVersion), Timing: row.Timing, Error: row.Error, Info: row.Info,
			Runs: strings.Join(row.Runs, ", "), Tags: strings.Join(row.Tags, ", ")})
		det := rowDetail{Label: row.Label, Errors: row.AllFailures()}
		if row.Tools != nil {
			det.ToolFailures = row.Tools.Failures
		}
		if st := serverTimings(row); st != "" {
			for _, l := range strings.Split(strings.TrimSpace(st), "\n") {
				det.ServerTimings = append(det.ServerTimings, strings.TrimPrefix(l, "- "))
			}
		}
		if len(det.Errors)+len(det.ToolFailures)+len(det.ServerTimings) > 0 {
			d.Details = append(d.Details, det)
		}
	}
	var b bytes.Buffer
	if err := page.Execute(&b, d); err != nil {
		return "", err
	}
	return b.String(), nil
}

// slot is the colour slot of the i-th row: eight distinct hues, then grey.
func slot(i int) int {
	if i < 8 {
		return i + 1
	}
	return 0
}

func stat(s Stat, format string) td {
	if s.N == 0 {
		return td{Text: "-", Sort: "-1", Dim: true}
	}
	t := fmt.Sprintf(format, s.Median)
	if s.N > 1 {
		t += fmt.Sprintf(" ("+format+"–"+format+")", s.Min, s.Max)
	}
	return td{Text: t, Sort: fmt.Sprintf("%.4f", s.Median)}
}

func tokensOf(label string) int {
	n, _ := ParseList(label)
	if len(n) == 1 {
		return n[0]
	}
	return 0
}

// --- charts ----------------------------------------------------------------------------

// charts are the bar charts: single-stream decode per context, and combined
// output by concurrency per context.
func charts(r Results) []chartBlock {
	var out []chartBlock
	var contexts []string
	for _, c := range cells(r) {
		if !contains(contexts, c.ctx) {
			contexts = append(contexts, c.ctx)
		}
	}
	for _, ctx := range contexts {
		var bars []hbar
		for i, row := range r.Rows {
			if s := row.SpeedAt(ctx, 1); s != nil && s.DecodeTPS.N > 0 {
				bars = append(bars, hbar{label: row.Label, slot: slot(i), stat: s.DecodeTPS})
			}
		}
		if len(bars) > 0 {
			out = append(out, chartBlock{Title: fmt.Sprintf("Single stream decode, %s context", ctx), Note: "tokens per second per stream on a warm prompt; the line is the min–max over runs", SVG: hbars(bars)})
		}
	}
	for _, ctx := range contexts {
		var concs []int
		for _, c := range cells(r) {
			if c.ctx == ctx {
				concs = append(concs, c.conc)
			}
		}
		sort.Ints(concs)
		var series []vseries
		for i, row := range r.Rows {
			vs := vseries{label: row.Label, slot: slot(i)}
			any := false
			for _, conc := range concs {
				st := Stat{}
				if s := row.SpeedAt(ctx, conc); s != nil {
					st = s.CombinedTPS
				}
				if st.N > 0 {
					any = true
				}
				vs.values = append(vs.values, st)
			}
			if any {
				series = append(series, vs)
			}
		}
		if len(series) > 0 && len(concs) > 1 {
			out = append(out, chartBlock{Title: fmt.Sprintf("Combined output by concurrency, %s context", ctx), Note: "all streams' tokens per second over the warm pass's wall time", SVG: vbars(concs, series)})
		}
	}
	return out
}

type hbar struct {
	label string
	slot  int
	stat  Stat
}

// hbars is a horizontal bar chart: a label, a bar and the value per item.
func hbars(bars []hbar) template.HTML {
	const w, labelW, valueW, rowH, barH = 480.0, 110.0, 56.0, 30.0, 20.0
	plotW := w - labelW - valueW - 10
	maxV := 0.0
	for _, b := range bars {
		maxV = math.Max(maxV, b.stat.Max)
	}
	if maxV == 0 {
		maxV = 1
	}
	h := rowH*float64(len(bars)) + 8
	var s strings.Builder
	fmt.Fprintf(&s, `<svg viewBox="0 0 %.0f %.0f" width="100%%" role="img" aria-label="bar chart">`, w, h)
	for i, b := range bars {
		y := 4 + rowH*float64(i)
		bw := plotW * b.stat.Median / maxV
		fmt.Fprintf(&s, `<text x="%.0f" y="%.1f" class="lbl" text-anchor="end">%s</text>`, labelW-8, y+barH/2+4, template.HTMLEscapeString(trunc(b.label, 16)))
		fmt.Fprintf(&s, `<path d="%s" fill="var(--s%d)"><title>%s: %.1f tok/s</title></path>`, barPath(labelW, y, bw, barH), b.slot, template.HTMLEscapeString(b.label), b.stat.Median)
		if b.stat.N > 1 {
			x0, x1 := labelW+plotW*b.stat.Min/maxV, labelW+plotW*b.stat.Max/maxV
			fmt.Fprintf(&s, `<line x1="%.1f" x2="%.1f" y1="%.1f" y2="%.1f" class="range"/>`, x0, x1, y+barH/2, y+barH/2)
		}
		fmt.Fprintf(&s, `<text x="%.1f" y="%.1f" class="val">%.1f</text>`, labelW+math.Max(bw, 0)+6, y+barH/2+4, b.stat.Median)
	}
	s.WriteString(`</svg>`)
	return template.HTML(s.String())
}

type vseries struct {
	label  string
	slot   int
	values []Stat // one per concurrency
}

// vbars is a grouped vertical bar chart: a group per concurrency, a bar per
// row, the value above each bar.
func vbars(concs []int, series []vseries) template.HTML {
	const w, h, left, right, top, bottom = 480.0, 220.0, 40.0, 8.0, 18.0, 26.0
	plotW, plotH := w-left-right, h-top-bottom
	maxV := 0.0
	for _, s := range series {
		for _, v := range s.values {
			maxV = math.Max(maxV, v.Median)
		}
	}
	if maxV == 0 {
		maxV = 1
	}
	maxV *= 1.15 // room for the value labels
	groupW := plotW / float64(len(concs))
	barW := math.Min(40, groupW*0.8/float64(len(series)))
	var s strings.Builder
	fmt.Fprintf(&s, `<svg viewBox="0 0 %.0f %.0f" width="100%%" role="img" aria-label="grouped bar chart">`, w, h)
	for i := 0; i <= 3; i++ {
		y := top + plotH - plotH*float64(i)/3
		v := maxV * float64(i) / 3
		fmt.Fprintf(&s, `<line x1="%.0f" x2="%.0f" y1="%.1f" y2="%.1f" class="grid"/>`, left, w-right, y, y)
		fmt.Fprintf(&s, `<text x="%.0f" y="%.1f" class="axis" text-anchor="end">%.0f</text>`, left-6, y+4, v)
	}
	for gi, conc := range concs {
		gx := left + groupW*float64(gi)
		x := gx + (groupW-barW*float64(len(series))-2*float64(len(series)-1))/2
		for _, sr := range series {
			v := sr.values[gi]
			if v.N > 0 {
				bh := plotH * v.Median / maxV
				fmt.Fprintf(&s, `<path d="%s" fill="var(--s%d)"><title>%s ×%d: %.1f tok/s</title></path>`, vbarPath(x, top+plotH-bh, barW, bh), sr.slot, template.HTMLEscapeString(sr.label), conc, v.Median)
				fmt.Fprintf(&s, `<text x="%.1f" y="%.1f" class="val" text-anchor="middle">%.0f</text>`, x+barW/2, top+plotH-bh-4, v.Median)
			}
			x += barW + 2
		}
		fmt.Fprintf(&s, `<text x="%.1f" y="%.0f" class="axis" text-anchor="middle">×%d</text>`, gx+groupW/2, h-8, conc)
	}
	s.WriteString(`</svg>`)
	return template.HTML(s.String())
}

// barPath is a horizontal bar from x, with the data end rounded.
func barPath(x, y, w, h float64) string {
	r := math.Min(4, w)
	return fmt.Sprintf("M%.1f %.1f H%.1f a%.1f %.1f 0 0 1 %.1f %.1f V%.1f a%.1f %.1f 0 0 1 %.1f %.1f H%.1f Z",
		x, y, x+w-r, r, r, r, r, y+h-r, r, r, -r, r, x)
}

// vbarPath is a vertical bar standing on y+h, with the top rounded.
func vbarPath(x, y, w, h float64) string {
	r := math.Min(4, h)
	return fmt.Sprintf("M%.1f %.1f V%.1f a%.1f %.1f 0 0 1 %.1f %.1f H%.1f a%.1f %.1f 0 0 1 %.1f %.1f V%.1f Z",
		x, y+h, y+r, r, r, r, -r, x+w-r, r, r, r, r, y+h)
}

func trunc(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n-1] + "…"
}

var page = template.Must(template.New("report").Parse(`<!doctype html>
<html lang="en">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>linfer-bench: {{.Title}}</title>
<style>
:root {
  color-scheme: light;
  --bg: #fcfcfb; --fg: #0b0b0b; --fg2: #52514e; --line: #e4e3df; --card: #f4f4f1; --best: #0a6b2d; --bad: #b3261e;
  --s0: #8a8985; --s1: #2a78d6; --s2: #eb6834; --s3: #1baf7a; --s4: #eda100; --s5: #e87ba4; --s6: #008300; --s7: #4a3aa7; --s8: #e34948;
}
@media (prefers-color-scheme: dark) {
  :root:not([data-theme="light"]) {
    color-scheme: dark;
    --bg: #1a1a19; --fg: #ffffff; --fg2: #c3c2b7; --line: #353532; --card: #232322; --best: #5fd38d; --bad: #ff8a80;
    --s0: #8a8985; --s1: #3987e5; --s2: #d95926; --s3: #199e70; --s4: #c98500; --s5: #d55181; --s6: #008300; --s7: #9085e9; --s8: #e66767;
  }
}
:root[data-theme="dark"] {
  color-scheme: dark;
  --bg: #1a1a19; --fg: #ffffff; --fg2: #c3c2b7; --line: #353532; --card: #232322; --best: #5fd38d; --bad: #ff8a80;
  --s0: #8a8985; --s1: #3987e5; --s2: #d95926; --s3: #199e70; --s4: #c98500; --s5: #d55181; --s6: #008300; --s7: #9085e9; --s8: #e66767;
}
* { box-sizing: border-box; }
body { margin: 0; background: var(--bg); color: var(--fg); font: 15px/1.5 -apple-system, BlinkMacSystemFont, "Segoe UI", Roboto, Helvetica, Arial, sans-serif; }
main { max-width: 1100px; margin: 0 auto; padding: 24px 16px 48px; }
header { display: flex; align-items: baseline; justify-content: space-between; gap: 16px; flex-wrap: wrap; }
h1 { font-size: 22px; margin: 0; } h1 small { color: var(--fg2); font-weight: normal; }
h2 { font-size: 17px; margin: 32px 0 8px; }
.meta { color: var(--fg2); font-size: 13px; margin: 4px 0 0; }
button.theme { background: var(--card); color: var(--fg); border: 1px solid var(--line); border-radius: 6px; padding: 4px 10px; font: inherit; font-size: 13px; cursor: pointer; }
ul.verdict { padding-left: 20px; } ul.verdict li { margin: 4px 0; }
.legend { display: flex; gap: 14px; flex-wrap: wrap; font-size: 13px; color: var(--fg2); margin: 8px 0; }
.legend span::before, td.lab::before { content: ""; display: inline-block; width: 10px; height: 10px; border-radius: 2px; margin-right: 6px; vertical-align: -1px; background: var(--sw); }
.charts { display: grid; grid-template-columns: repeat(auto-fit, minmax(400px, 1fr)); gap: 16px; align-items: start; }
.chart { background: var(--card); border: 1px solid var(--line); border-radius: 8px; padding: 12px 14px; }
.chart h3 { margin: 0 0 2px; font-size: 14px; } .chart p { margin: 0 0 8px; color: var(--fg2); font-size: 12px; }
svg text { fill: var(--fg); font-size: 12px; } svg .lbl { fill: var(--fg2); } svg .val { fill: var(--fg); font-size: 11px; } svg .axis { fill: var(--fg2); font-size: 11px; }
svg .grid { stroke: var(--line); stroke-width: 1; } svg .range { stroke: var(--fg); stroke-width: 2; opacity: .55; }
.scroll { overflow-x: auto; }
table { border-collapse: collapse; width: 100%; font-size: 13px; font-variant-numeric: tabular-nums; }
th, td { text-align: left; padding: 6px 10px; border-bottom: 1px solid var(--line); white-space: nowrap; vertical-align: top; }
th { color: var(--fg2); font-weight: 600; } table.sortable th { cursor: pointer; user-select: none; }
table.sortable th[aria-sort="ascending"]::after { content: " ↑"; } table.sortable th[aria-sort="descending"]::after { content: " ↓"; }
td.num, th.num { text-align: right; } td.best { color: var(--best); font-weight: 600; } td.dim, tr.sub td { color: var(--fg2); } td.bad { color: var(--bad); }
tr.sub td:first-child { padding-left: 24px; }
td.wrap { white-space: normal; }
code { font-size: 12px; background: var(--card); padding: 1px 4px; border-radius: 3px; }
details { margin: 8px 0; } summary { cursor: pointer; color: var(--fg2); }
p.measure { color: var(--fg2); font-size: 13px; }
.warn { background: var(--card); border-left: 4px solid var(--bad); padding: 8px 12px; margin: 8px 0; font-size: 14px; }
footer { margin-top: 40px; color: var(--fg2); font-size: 12px; border-top: 1px solid var(--line); padding-top: 12px; }
</style>
</head>
<body>
<main>
<header>
  <div>
    <h1>linfer-bench <small>{{.Title}}</small></h1>
    <p class="meta">{{.Started}} · {{.Machine}} · {{.Duration}} · {{.Profile}} profile{{if .ID}} · run <code>{{.ID}}</code>{{end}}{{if .Tags}} · tags {{.Tags}}{{end}}</p>
  </div>
  <button class="theme" type="button" aria-label="theme">theme: auto</button>
</header>

{{if .Comparison}}
<p class="meta">A comparison of tags {{range $i, $t := .Comparison.Tags}}{{if $i}}, {{end}}<strong>{{$t}}</strong>{{end}} over runs {{range $i, $s := .Comparison.Sources}}{{if $i}}, {{end}}<code>{{$s}}</code>{{end}}.</p>
{{range .Comparison.Warnings}}<div class="warn">Not like for like: {{.}}</div>{{end}}
{{end}}
<h2>Verdict</h2>
<ul class="verdict">{{range .Verdict}}<li>{{.}}</li>{{end}}</ul>

{{if .Charts}}
<h2>Throughput</h2>
{{if .Legend}}<div class="legend">{{range .Legend}}<span style="--sw: var(--s{{.Slot}})">{{.Label}}</span>{{end}}</div>{{end}}
<div class="charts">
{{range .Charts}}<div class="chart"><h3>{{.Title}}</h3><p>{{.Note}}</p>{{.SVG}}</div>{{end}}
</div>
{{end}}

<h2>Comparison</h2>
<div class="scroll"><table>
<thead><tr>{{range $i, $h := .OverviewHead}}<th{{if $i}} class="num"{{end}}>{{$h}}</th>{{end}}</tr></thead>
<tbody>
{{range .Overview}}<tr{{if .Sub}} class="sub"{{end}}><td>{{.Label}}</td>{{range .Cells}}<td class="num{{if .Best}} best{{end}}{{if .Dim}} dim{{end}}">{{.Text}}</td>{{end}}</tr>
{{end}}</tbody></table></div>
<p class="measure">{{.Measurement}}</p>

{{if .Speed}}
<h2>Speed</h2>
<div class="scroll"><table class="sortable">
<thead><tr><th>target</th><th>context</th><th class="num">streams</th><th class="num">decode tok/s</th><th class="num">server decode</th><th class="num">tokens/chunk</th><th class="num">combined tok/s</th><th class="num">cold TTFT s</th><th class="num">prefill tok/s</th><th class="num">server prefill</th><th class="num">warm TTFT s</th><th class="num">failures</th></tr></thead>
<tbody>
{{range .Speed}}<tr><td class="lab" style="--sw: var(--s{{.Slot}})">{{.Label}}</td><td data-sort="{{.ContextSort}}">{{.Context}}</td><td class="num" data-sort="{{.Conc}}">{{.Conc}}</td>
<td class="num{{if .Decode.Dim}} dim{{end}}" data-sort="{{.Decode.Sort}}">{{.Decode.Text}}</td><td class="num{{if .ServerDecode.Dim}} dim{{end}}" data-sort="{{.ServerDecode.Sort}}">{{.ServerDecode.Text}}</td><td class="num{{if .TPC.Dim}} dim{{end}}" data-sort="{{.TPC.Sort}}">{{.TPC.Text}}</td>
<td class="num{{if .Combined.Dim}} dim{{end}}" data-sort="{{.Combined.Sort}}">{{.Combined.Text}}</td><td class="num{{if .TTFT.Dim}} dim{{end}}" data-sort="{{.TTFT.Sort}}">{{.TTFT.Text}}</td><td class="num{{if .Prefill.Dim}} dim{{end}}" data-sort="{{.Prefill.Sort}}">{{.Prefill.Text}}</td>
<td class="num{{if .ServerPrefill.Dim}} dim{{end}}" data-sort="{{.ServerPrefill.Sort}}">{{.ServerPrefill.Text}}</td><td class="num{{if .WarmTTFT.Dim}} dim{{end}}" data-sort="{{.WarmTTFT.Sort}}">{{.WarmTTFT.Text}}</td><td class="num{{if .Failures}} bad{{end}}" data-sort="{{.Failures}}">{{.Failures}}</td></tr>
{{end}}</tbody></table></div>
{{end}}

{{if .Tools}}
<h2>Tool calling</h2>
<div class="scroll"><table class="sortable">
<thead><tr><th>target</th><th class="num">accuracy</th><th class="num">passed</th><th class="num">valid JSON</th><th class="num">right tool</th><th class="num">schema-valid</th><th class="num">no-call correct</th><th class="num">leaks</th><th class="num">HTTP errors</th></tr></thead>
<tbody>
{{range .Tools}}<tr><td class="lab" style="--sw: var(--s{{.Slot}})">{{.Label}}</td><td class="num" data-sort="{{.Accuracy.Sort}}">{{.Accuracy.Text}}</td><td class="num" data-sort="{{.Passed}}">{{.Passed}} of {{.Cases}}</td>
<td class="num" data-sort="{{.Valid}}">{{.Valid}} of {{.CallCases}}</td><td class="num" data-sort="{{.Right}}">{{.Right}} of {{.CallCases}}</td><td class="num" data-sort="{{.Schema}}">{{.Schema}} of {{.CallCases}}</td>
<td class="num" data-sort="{{.NoCallCorrect}}">{{.NoCallCorrect}} of {{.NoCallCases}}</td><td class="num{{if .Leaks}} bad{{end}}" data-sort="{{.Leaks}}">{{.Leaks}}</td><td class="num{{if .HTTPErrors}} bad{{end}}" data-sort="{{.HTTPErrors}}">{{.HTTPErrors}}</td></tr>
{{end}}</tbody></table></div>
{{end}}

{{if .Context}}
<h2>Needle in a haystack</h2>
<div class="scroll"><table class="sortable">
<thead><tr><th>target</th><th>context</th><th class="num">prompt tokens</th><th class="num">depth</th><th>found</th><th>answer</th></tr></thead>
<tbody>
{{range .Context}}<tr><td class="lab" style="--sw: var(--s{{.Slot}})">{{.Label}}</td><td data-sort="{{.Tokens}}">{{.Context}}</td><td class="num" data-sort="{{.Tokens}}">{{.Tokens}}</td><td class="num" data-sort="{{.Depth}}">{{.Depth}}%</td><td{{if eq .Found "no"}} class="bad"{{end}}{{if eq .Found "error"}} class="bad"{{end}}>{{.Found}}</td><td class="wrap">{{.Answer}}</td></tr>
{{end}}</tbody></table></div>
{{end}}

<h2>Targets</h2>
<div class="scroll"><table>
<thead><tr><th>target</th><th>URL</th><th>model</th><th>server</th><th>details</th></tr></thead>
<tbody>
{{range .Targets}}<tr><td class="lab" style="--sw: var(--s{{.Slot}})">{{.Label}}</td><td><code>{{.URL}}</code></td><td><code>{{.Model}}</code></td><td>{{.Server}}</td>
<td class="wrap">{{if .Error}}<span class="bad">did not run: {{.Error}}</span>{{end}}{{range .Info}}{{.Key}} {{.Value}}; {{end}}{{if .Runs}}runs <code>{{.Runs}}</code>; {{end}}{{if .Tags}}tags {{.Tags}}; {{end}}{{if .Timing}}timings {{.Timing}}{{end}}</td></tr>
{{end}}</tbody></table></div>

{{if .Details}}
<h2>Details</h2>
{{range .Details}}
{{if .ToolFailures}}<details><summary>{{.Label}}: {{len .ToolFailures}} tool failure(s)</summary><ul>{{range .ToolFailures}}<li>{{.}}</li>{{end}}</ul></details>{{end}}
{{if .Errors}}<details><summary>{{.Label}}: {{len .Errors}} error(s)</summary><ul>{{range .Errors}}<li>{{.}}</li>{{end}}</ul></details>{{end}}
{{if .ServerTimings}}<details><summary>{{.Label}}: server-reported timings</summary><ul>{{range .ServerTimings}}<li><code>{{.}}</code></li>{{end}}</ul></details>{{end}}
{{end}}
{{end}}

{{if .Notes}}
<h2>Notes</h2>
<ul>{{range .Notes}}<li>{{.}}</li>{{end}}</ul>
{{end}}

<footer>{{.Tool}} · results.json schema {{.Schema}} · the same figures are in report.md and results.json beside this file</footer>
</main>
<script>
(function () {
  var root = document.documentElement, btn = document.querySelector('button.theme');
  var modes = ['auto', 'light', 'dark'], mode = 'auto';
  try { mode = localStorage.getItem('linfer-bench-theme') || 'auto'; } catch (e) {}
  function apply() {
    if (mode === 'auto') root.removeAttribute('data-theme'); else root.setAttribute('data-theme', mode);
    btn.textContent = 'theme: ' + mode;
  }
  btn.addEventListener('click', function () {
    mode = modes[(modes.indexOf(mode) + 1) % modes.length];
    try { localStorage.setItem('linfer-bench-theme', mode); } catch (e) {}
    apply();
  });
  apply();
  document.querySelectorAll('table.sortable th').forEach(function (th, idx) {
    th.addEventListener('click', function () {
      var table = th.closest('table'), tbody = table.tBodies[0];
      var dir = th.getAttribute('aria-sort') === 'ascending' ? 'descending' : 'ascending';
      table.querySelectorAll('th').forEach(function (h) { h.removeAttribute('aria-sort'); });
      th.setAttribute('aria-sort', dir);
      var rows = Array.prototype.slice.call(tbody.rows);
      rows.sort(function (a, b) {
        var ca = a.cells[idx], cb = b.cells[idx];
        var na = parseFloat(ca.dataset.sort), nb = parseFloat(cb.dataset.sort);
        var r = (!isNaN(na) && !isNaN(nb)) ? na - nb : ca.textContent.localeCompare(cb.textContent);
        return dir === 'ascending' ? r : -r;
      });
      rows.forEach(function (r) { tbody.appendChild(r); });
    });
  });
})();
</script>
</body>
</html>
`))
