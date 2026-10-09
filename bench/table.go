package bench

import (
	"fmt"
	"io"
	"strings"
	"unicode/utf8"
)

// The terminal table: one column per row of results, one line per metric.
// Widths are computed on the plain text, so colour codes do not throw the
// columns off the way they would under text/tabwriter.

type style int

const (
	plain style = iota
	bold
	dim
	green
	red
)

var ansi = map[style]string{bold: "\x1b[1m", dim: "\x1b[2m", green: "\x1b[32;1m", red: "\x1b[31m"}

type cell struct {
	text  string
	style style
}

type line struct {
	label string
	style style
	cells []cell
}

// cellKey identifies a (context, concurrency) cell.
type cellKey struct {
	ctx  string
	conc int
}

// cells is every (context, concurrency) any row measured, in order.
func cells(r Results) []cellKey {
	seen := map[cellKey]bool{}
	var out []cellKey
	for _, row := range r.Rows {
		for _, s := range row.Speed {
			c := cellKey{s.Context, s.Concurrency}
			if !seen[c] {
				seen[c] = true
				out = append(out, c)
			}
		}
	}
	return out
}

func statStr(s *SpeedRow, pick func(*SpeedRow) Stat, format string) string {
	if s == nil {
		return "-"
	}
	st := pick(s)
	if st.N == 0 {
		switch {
		case s.Missing():
			return "n/a"
		case s.Skipped():
			return "skipped"
		}
		return "-"
	}
	out := fmt.Sprintf(format, st.Median)
	if st.N > 1 {
		out += fmt.Sprintf(" ("+format+"–"+format+")", st.Min, st.Max)
	}
	return out
}

// statLine is a metric line over the cells: the value of pick for each
// row's speed cell from at, with the best marked when higher (or lower) is
// better and the gap is outside the noise. A cell that was skipped says so.
func statLine(r Results, label string, sub bool, better int, at func(Row) *SpeedRow, pick func(*SpeedRow) Stat, format string) line {
	l := line{label: label}
	if sub {
		l.style = dim
	}
	var stats []Stat
	for _, row := range r.Rows {
		if row.Error != "" {
			l.cells = append(l.cells, cell{text: "-", style: dim})
			stats = append(stats, Stat{})
			continue
		}
		s := at(row)
		if s == nil {
			l.cells = append(l.cells, cell{text: "-", style: dim})
			stats = append(stats, Stat{})
			continue
		}
		st := pick(s)
		stats = append(stats, st)
		text := statStr(s, pick, format)
		l.cells = append(l.cells, cell{text: text, style: ifStyle(sub || st.N == 0, dim, plain)})
	}
	if i := best(stats, better); i >= 0 && !sub {
		l.cells[i].style = green
	}
	return l
}

// best is the index of the best stat, -1 when there is no clear one: fewer
// than two measured, or the top two within noise.
func best(stats []Stat, better int) int {
	if better == 0 {
		return -1
	}
	bi, si := -1, -1
	for i, s := range stats {
		if s.N == 0 {
			continue
		}
		switch {
		case bi < 0 || float64(better)*(s.Median-stats[bi].Median) > 0:
			si, bi = bi, i
		case si < 0 || float64(better)*(s.Median-stats[si].Median) > 0:
			si = i
		}
	}
	if bi < 0 || si < 0 || WithinNoise(stats[bi], stats[si]) {
		return -1
	}
	return bi
}

func ifStyle(cond bool, a, b style) style {
	if cond {
		return a
	}
	return b
}

// lines is the table's content, shared by the terminal and the markdown.
func lines(r Results) []line {
	var out []line
	textLine := func(label string, f func(Row) (string, style)) {
		l := line{label: label, style: ifStyle(strings.HasPrefix(label, "  "), dim, plain)}
		for _, row := range r.Rows {
			if row.Error != "" {
				l.cells = append(l.cells, cell{text: "-", style: dim})
				continue
			}
			t, s := f(row)
			l.cells = append(l.cells, cell{text: t, style: s})
		}
		out = append(out, l)
	}
	anyInfo := false
	for _, row := range r.Rows {
		if len(row.Info) > 0 || row.Server != "" {
			anyInfo = true
		}
	}
	if anyInfo {
		textLine("server", func(row Row) (string, style) { return ifs(row.Server != "", row.Server, "-"), plain })
		for _, k := range infoKeys(r) {
			textLine(k, func(row Row) (string, style) {
				for _, kv := range row.Info {
					if kv.Key == k {
						return kv.Value, plain
					}
				}
				return "-", dim
			})
		}
	}
	for _, c := range cells(r) {
		at := func(row Row) *SpeedRow { return row.SpeedAt(c.ctx, c.conc) }
		cell := fmt.Sprintf("%s×%d", c.ctx, c.conc)
		out = append(out,
			statLine(r, "decode tok/s/stream "+cell, false, 1, at, func(s *SpeedRow) Stat { return s.DecodeTPS }, "%.1f"),
			statLine(r, "  server-reported", true, 0, at, func(s *SpeedRow) Stat { return s.ServerDecode }, "%.1f"),
			statLine(r, "  tokens per stream chunk", true, 0, at, func(s *SpeedRow) Stat { return s.TokensPerChunk }, "%.1f"),
			statLine(r, "combined tok/s "+cell, false, 1, at, func(s *SpeedRow) Stat { return s.CombinedTPS }, "%.1f"),
			statLine(r, "cold ttft s "+cell, false, -1, at, func(s *SpeedRow) Stat { return s.TTFT }, "%.2f"),
			statLine(r, "prefill tok/s "+cell, false, 1, at, func(s *SpeedRow) Stat { return s.Prefill }, "%.0f"),
			statLine(r, "  server-reported", true, 0, at, func(s *SpeedRow) Stat { return s.ServerPrefill }, "%.0f"),
		)
		if c.conc == 1 {
			out = append(out, statLine(r, "warm ttft s "+c.ctx, false, -1, at, func(s *SpeedRow) Stat { return s.WarmTTFT }, "%.2f"))
		}
	}
	if anyTools(r) {
		l := line{label: "tools accuracy"}
		var stats []Stat
		for _, row := range r.Rows {
			if row.Error != "" || row.Tools == nil {
				l.cells = append(l.cells, cell{text: "-", style: dim})
				stats = append(stats, Stat{})
				continue
			}
			l.cells = append(l.cells, cell{text: fmt.Sprintf("%.0f%% (%d/%d)", row.Tools.Accuracy(), row.Tools.Passed, row.Tools.Cases)})
			stats = append(stats, Stat{Median: row.Tools.Accuracy(), N: 1})
		}
		if i := best(stats, 1); i >= 0 {
			l.cells[i].style = green
		}
		out = append(out, l)
		textLine("  valid / right tool / schema", func(row Row) (string, style) {
			if row.Tools == nil {
				return "-", dim
			}
			return fmt.Sprintf("%d / %d / %d of %d", row.Tools.ValidCalls, row.Tools.RightTool, row.Tools.SchemaValid, row.Tools.CallCases), dim
		})
		textLine("  no-call correct", func(row Row) (string, style) {
			if row.Tools == nil {
				return "-", dim
			}
			return fmt.Sprintf("%d of %d", row.Tools.NoCallCorrect, row.Tools.NoCallCases), dim
		})
		textLine("  leaks / http errors", func(row Row) (string, style) {
			if row.Tools == nil {
				return "-", dim
			}
			return fmt.Sprintf("%d / %d", row.Tools.Leaks, row.Tools.HTTPErrors), ifStyle(row.Tools.Leaks+row.Tools.HTTPErrors > 0, red, dim)
		})
	}
	if anyContext(r) {
		l := line{label: "needle found"}
		var stats []Stat
		for _, row := range r.Rows {
			f, t := row.ContextFound()
			if row.Error != "" || t == 0 {
				l.cells = append(l.cells, cell{text: "-", style: dim})
				stats = append(stats, Stat{})
				continue
			}
			l.cells = append(l.cells, cell{text: fmt.Sprintf("%d of %d", f, t)})
			stats = append(stats, Stat{Median: float64(f) / float64(t), N: 1})
		}
		if i := best(stats, 1); i >= 0 {
			l.cells[i].style = green
		}
		out = append(out, l)
	}
	textLine("failures", func(row Row) (string, style) {
		n := len(row.AllFailures())
		return fmt.Sprint(n), ifStyle(n > 0, red, plain)
	})
	return out
}

// infoKeys is every Info key any row has, in first-seen order.
func infoKeys(r Results) []string {
	seen := map[string]bool{}
	var out []string
	for _, row := range r.Rows {
		for _, kv := range row.Info {
			if !seen[kv.Key] {
				seen[kv.Key] = true
				out = append(out, kv.Key)
			}
		}
	}
	return out
}

func anyTools(r Results) bool {
	for _, row := range r.Rows {
		if row.Tools != nil {
			return true
		}
	}
	return false
}

func anyContext(r Results) bool {
	for _, row := range r.Rows {
		if len(row.Context) > 0 {
			return true
		}
	}
	return false
}

// Table prints the terminal comparison: one column per row of results.
// With color, the header is bold, the best value of a metric is green
// when it is outside the noise of the next, secondary lines are dim and
// failures are red.
func Table(w io.Writer, r Results, color bool) {
	ls := lines(r)
	widths := make([]int, len(r.Rows)+1)
	widths[0] = len("metric")
	for _, l := range ls {
		widths[0] = max(widths[0], utf8.RuneCountInString(l.label))
		for i, c := range l.cells {
			widths[i+1] = max(widths[i+1], utf8.RuneCountInString(c.text))
		}
	}
	for i, row := range r.Rows {
		widths[i+1] = max(widths[i+1], utf8.RuneCountInString(row.Label))
	}
	paint := func(text string, s style, width int) string {
		pad := strings.Repeat(" ", max(0, width-utf8.RuneCountInString(text)))
		if color && s != plain {
			return ansi[s] + text + "\x1b[0m" + pad
		}
		return text + pad
	}
	head := paint("metric", bold, widths[0])
	for i, row := range r.Rows {
		head += "  " + paint(row.Label, bold, widths[i+1])
	}
	fmt.Fprintln(w, strings.TrimRight(head, " "))
	for _, l := range ls {
		s := paint(l.label, l.style, widths[0])
		for i, c := range l.cells {
			s += "  " + paint(c.text, c.style, widths[i+1])
		}
		fmt.Fprintln(w, strings.TrimRight(s, " "))
	}
	for _, row := range r.Rows {
		if row.Error != "" {
			fmt.Fprintf(w, "\n%s did not run: %s\n", paint(row.Label, red, 0), row.Error)
		}
	}
}
