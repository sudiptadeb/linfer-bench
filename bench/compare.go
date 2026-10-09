package bench

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"
)

// Merge folds earlier runs into one comparison, as `linfer-bench compare
// a.json b.json` does: every row of every run becomes a column, a label
// that appears in more than one run gets the run's date behind it, and the
// notes say which file each column came from. The first run's machine and
// options head the report; a run from another machine is noted.
func Merge(runs []Results, names []string) Results {
	if len(runs) == 0 {
		return Results{Schema: SchemaVersion, Tool: "linfer-bench " + Version}
	}
	out := Results{Schema: SchemaVersion, Tool: "linfer-bench " + Version, Started: time.Now(), Machine: runs[0].Machine, Options: runs[0].Options}
	count := map[string]int{}
	for _, r := range runs {
		for _, row := range r.Rows {
			count[row.Label]++
		}
	}
	for i, r := range runs {
		name := fmt.Sprintf("run %d", i+1)
		if i < len(names) && names[i] != "" {
			name = names[i]
		}
		for _, row := range r.Rows {
			label := row.Label
			if count[label] > 1 {
				label = fmt.Sprintf("%s @%s", row.Label, r.Started.Format("01-02 15:04"))
			}
			out.Notes = append(out.Notes, fmt.Sprintf("%s: %s from %s (%s, %s profile)", label, row.Model, name, r.Started.Format("2006-01-02 15:04"), r.Options.Profile))
			row.Label = label
			out.Rows = append(out.Rows, row)
		}
		if r.Machine != runs[0].Machine {
			out.Notes = append(out.Notes, fmt.Sprintf("%s ran on another machine: %s", name, r.Machine))
		}
		if r.Options.Runs != runs[0].Options.Runs || r.Options.ReplyTokens != runs[0].Options.ReplyTokens {
			out.Notes = append(out.Notes, fmt.Sprintf("%s measured with %d run(s) per cell and %d-token replies, where %s had %d and %d", name, r.Options.Runs, r.Options.ReplyTokens, firstName(names), runs[0].Options.Runs, runs[0].Options.ReplyTokens))
		}
		out.Duration += r.Duration
	}
	var ts []string
	for _, r := range runs {
		if !contains(ts, r.Title) {
			ts = append(ts, r.Title)
		}
	}
	out.Title = strings.Join(ts, " vs ")
	if len(runs) > 1 {
		out.Title += " (compared)"
	}
	// The noise rule reads Options.Runs; the fewest runs of any input is the
	// honest figure.
	for _, r := range runs {
		if r.Options.Runs < out.Options.Runs {
			out.Options.Runs = r.Options.Runs
		}
	}
	return out
}

func firstName(names []string) string {
	if len(names) > 0 && names[0] != "" {
		return names[0]
	}
	return "run 1"
}

func contains(xs []string, s string) bool {
	for _, x := range xs {
		if x == s {
			return true
		}
	}
	return false
}

// --- comparing tags ------------------------------------------------------------------

// member is one row of one run, as a tag group holds it.
type member struct {
	tag string
	run Results
	row Row
}

// CompareTags builds a comparison across tags: each tag is a column made
// of every row, in every run given, that carries the tag. A tag with one
// row is that row; a tag with several is their aggregate, the median of
// the rows' medians with the spread across them and the run count. Cells
// only some tags measured are marked missing, never guessed, and the
// comparison records what makes it unfair: runs that differ in profile,
// reply length, sampling or machine, or a tag that spans models.
func CompareTags(runs []Results, tags []string) Results {
	out := Results{Schema: SchemaVersion, Tool: "linfer-bench " + Version, Started: time.Now(), Comparison: &Comparison{Tags: tags}}
	var all []member
	for _, tag := range tags {
		var ms []member
		for _, r := range runs {
			for _, row := range r.Rows {
				if row.HasTag(tag, r.Tags) {
					ms = append(ms, member{tag, r, row})
				}
			}
		}
		if len(ms) == 0 {
			out.Rows = append(out.Rows, Row{Label: tag, Error: "no run carries this tag"})
			continue
		}
		all = append(all, ms...)
		out.Rows = append(out.Rows, aggregate(tag, ms))
	}
	if len(all) == 0 {
		return out
	}
	first := all[0].run
	out.Machine, out.Options, out.Title = first.Machine, first.Options, first.Title
	for _, m := range all {
		if !contains(out.Comparison.Sources, m.run.ID) {
			out.Comparison.Sources = append(out.Comparison.Sources, m.run.ID)
		}
		out.Duration += m.row.Duration
		if m.run.Options.Runs < out.Options.Runs {
			out.Options.Runs = m.run.Options.Runs
		}
	}
	out.Title = strings.Join(tags, " vs ")
	out.Comparison.Warnings = unfair(all)
	markMissing(&out)
	return out
}

// aggregate folds a tag's members into one row.
func aggregate(tag string, ms []member) Row {
	if len(ms) == 1 {
		row := ms[0].row
		row.Label = tag
		row.Runs = []string{ms[0].run.ID}
		return row
	}
	row := Row{Label: tag, Started: ms[0].row.Started}
	var targets, models, servers []string
	cells := map[cellKey]*[]SpeedRow{}
	var order []cellKey
	tools := ToolsScore{}
	for _, m := range ms {
		r := m.row
		row.Runs = append(row.Runs, m.run.ID)
		row.Duration += r.Duration
		if !contains(targets, r.Target) {
			targets = append(targets, r.Target)
		}
		if !contains(models, r.Model) {
			models = append(models, r.Model)
		}
		if r.Server != "" && !contains(servers, r.Server+ifs(r.ServerVersion != "", " "+r.ServerVersion, "")) {
			servers = append(servers, r.Server+ifs(r.ServerVersion != "", " "+r.ServerVersion, ""))
		}
		if r.Error != "" {
			row.Failures = append(row.Failures, m.run.ID+": did not run: "+r.Error)
			continue
		}
		for _, f := range r.Failures {
			row.Failures = append(row.Failures, m.run.ID+": "+f)
		}
		for _, s := range r.Speed {
			k := cellKey{s.Context, s.Concurrency}
			if cells[k] == nil {
				cells[k] = &[]SpeedRow{}
				order = append(order, k)
			}
			*cells[k] = append(*cells[k], s)
		}
		if t := r.Tools; t != nil {
			tools.Suite = t.Suite
			tools.Cases += t.Cases
			tools.Passed += t.Passed
			tools.ValidCalls += t.ValidCalls
			tools.RightTool += t.RightTool
			tools.SchemaValid += t.SchemaValid
			tools.NoCallCorrect += t.NoCallCorrect
			tools.NoCallCases += t.NoCallCases
			tools.CallCases += t.CallCases
			tools.Leaks += t.Leaks
			tools.HTTPErrors += t.HTTPErrors
			tools.Outcomes = append(tools.Outcomes, t.Outcomes...)
			for _, f := range t.Failures {
				tools.Failures = append(tools.Failures, m.run.ID+": "+f)
			}
		}
		row.Context = append(row.Context, r.Context...)
	}
	if tools.Cases > 0 {
		row.Tools = &tools
	}
	for _, k := range order {
		row.Speed = append(row.Speed, aggregateCell(k, *cells[k], row.Runs))
	}
	row.Target, row.Model, row.Server = strings.Join(targets, ", "), strings.Join(models, ", "), strings.Join(servers, ", ")
	row.Timing = ms[0].row.Timing
	row.Info = []KV{{"runs", fmt.Sprint(len(ms))}}
	if len(targets) > 1 {
		row.Info = append(row.Info, KV{"targets", row.Target})
	}
	if len(models) > 1 {
		row.Info = append(row.Info, KV{"models", row.Model})
	}
	if len(row.Speed) == 0 && row.Tools == nil && len(row.Context) == 0 {
		row.Error = "none of its runs produced results"
	}
	return row
}

// aggregateCell is the median of the member cells' medians, with the
// spread across members and N the number of members that measured it.
func aggregateCell(k cellKey, cells []SpeedRow, ids []string) SpeedRow {
	out := SpeedRow{Context: k.ctx, Concurrency: k.conc, Server: map[string]float64{}}
	pick := func(f func(SpeedRow) Stat) Stat {
		var xs []float64
		for _, c := range cells {
			if st := f(c); st.N > 0 {
				xs = append(xs, st.Median)
			}
		}
		return Summarise(xs)
	}
	out.DecodeTPS = pick(func(s SpeedRow) Stat { return s.DecodeTPS })
	out.TTFT = pick(func(s SpeedRow) Stat { return s.TTFT })
	out.Prefill = pick(func(s SpeedRow) Stat { return s.Prefill })
	out.CombinedTPS = pick(func(s SpeedRow) Stat { return s.CombinedTPS })
	out.WarmTTFT = pick(func(s SpeedRow) Stat { return s.WarmTTFT })
	out.ServerDecode = pick(func(s SpeedRow) Stat { return s.ServerDecode })
	out.ServerPrefill = pick(func(s SpeedRow) Stat { return s.ServerPrefill })
	out.TokensPerChunk = pick(func(s SpeedRow) Stat { return s.TokensPerChunk })
	server := map[string][]float64{}
	for i, c := range cells {
		out.Runs += c.Runs
		if out.ContextTokens == 0 {
			out.ContextTokens = c.ContextTokens
		}
		for key, v := range c.Server {
			server[key] = append(server[key], v)
		}
		for _, f := range c.Failures {
			if !strings.HasPrefix(f, "skipped") {
				out.Failures = append(out.Failures, ids[min(i, len(ids)-1)]+": "+f)
			}
		}
	}
	for key, v := range server {
		out.Server[key] = Median(v)
	}
	return out
}

// markMissing adds, to each row that lacks a cell another row measured, a
// placeholder the table shows as n/a, and lists them in the comparison.
func markMissing(r *Results) {
	all := cells(*r)
	for i := range r.Rows {
		row := &r.Rows[i]
		if row.Error != "" || len(row.Speed) == 0 {
			continue
		}
		for _, k := range all {
			if s := row.SpeedAt(k.ctx, k.conc); s == nil || (s.DecodeTPS.N == 0 && s.CombinedTPS.N == 0 && s.TTFT.N == 0) {
				if s == nil {
					row.Speed = append(row.Speed, SpeedRow{Context: k.ctx, Concurrency: k.conc, Failures: []string{"missing: not measured in these runs"}})
				}
				r.Comparison.Missing = append(r.Comparison.Missing, fmt.Sprintf("%s×%d was not measured for %s", k.ctx, k.conc, row.Label))
			}
		}
		sort.SliceStable(row.Speed, func(a, b int) bool {
			ia, ib := indexOf(all, cellKey{row.Speed[a].Context, row.Speed[a].Concurrency}), indexOf(all, cellKey{row.Speed[b].Context, row.Speed[b].Concurrency})
			return ia < ib
		})
	}
}

func indexOf(ks []cellKey, k cellKey) int {
	for i, x := range ks {
		if x == k {
			return i
		}
	}
	return len(ks)
}

// unfair lists what differs between the compared runs that makes their
// numbers not like for like.
func unfair(ms []member) []string {
	var out []string
	differ := func(what string, key func(member) string) {
		seen := map[string][]string{}
		var order []string
		for _, m := range ms {
			k := key(m)
			if _, ok := seen[k]; !ok {
				order = append(order, k)
			}
			if !contains(seen[k], m.tag) {
				seen[k] = append(seen[k], m.tag)
			}
		}
		if len(order) > 1 {
			var parts []string
			for _, k := range order {
				parts = append(parts, fmt.Sprintf("%s for %s", k, strings.Join(seen[k], ", ")))
			}
			out = append(out, fmt.Sprintf("%s differs: %s", what, strings.Join(parts, "; ")))
		}
	}
	differ("profile", func(m member) string { return m.run.Options.Profile })
	differ("reply length", func(m member) string { return fmt.Sprintf("%d tokens", m.run.Options.ReplyTokens) })
	differ("sampling", func(m member) string {
		if m.run.Options.Sampling == nil {
			return "the server's defaults"
		}
		raw, _ := json.Marshal(m.run.Options.Sampling)
		return string(raw)
	})
	// The GPU fields are the caller's to fill, so two runs on one machine
	// may differ there; the machine is its OS, chip, CPUs and memory.
	differ("machine", func(m member) string {
		h := m.run.Machine
		return Machine{OS: h.OS, Arch: h.Arch, Chip: h.Chip, CPUs: h.CPUs, RAM: h.RAM}.String()
	})
	differ("tool suite", func(m member) string {
		if m.row.Tools == nil {
			return "none"
		}
		return m.row.Tools.Suite
	})
	return out
}

// CompareVerdict is one line per metric and cell: the best tag against the
// next, as a ratio when the gap is outside the noise, else within noise;
// then tool accuracy and needle retrieval.
func CompareVerdict(r Results) []string {
	var ran []Row
	for _, row := range r.Rows {
		if row.Error == "" {
			ran = append(ran, row)
		}
	}
	var out []string
	if len(ran) < 2 {
		return []string{"nothing to compare: fewer than two tags have results"}
	}
	metric := func(what string, better int, unit string, pick func(Row) Stat) {
		var rs []ranked
		for _, row := range ran {
			if s := pick(row); s.N > 0 {
				rs = append(rs, ranked{row.Label, s})
			}
		}
		if len(rs) < 2 {
			return
		}
		sort.SliceStable(rs, func(i, j int) bool { return float64(better)*(rs[i].stat.Median-rs[j].stat.Median) > 0 })
		var parts []string
		for i, x := range rs {
			s := fmt.Sprintf("%s %s", x.name, fmtStat(x.stat, unit))
			if i > 0 {
				prev := rs[i-1].stat
				switch {
				case WithinNoise(prev, x.stat):
					s = "≈ " + s + " (within noise)"
				case better > 0:
					s = fmt.Sprintf("> %s (%.2f×)", s, prev.Median/x.stat.Median)
				default:
					s = fmt.Sprintf("> %s (%.2f× faster)", s, x.stat.Median/prev.Median)
				}
			}
			parts = append(parts, s)
		}
		out = append(out, what+": "+strings.Join(parts, " "))
	}
	for _, k := range cells(r) {
		at := func(f func(*SpeedRow) Stat) func(Row) Stat {
			return func(row Row) Stat {
				if s := row.SpeedAt(k.ctx, k.conc); s != nil {
					return f(s)
				}
				return Stat{}
			}
		}
		cell := fmt.Sprintf("%s×%d", k.ctx, k.conc)
		if k.conc == 1 {
			metric("decode "+cell, 1, "tok/s", at(func(s *SpeedRow) Stat { return s.DecodeTPS }))
			metric("cold TTFT "+cell, -1, "s", at(func(s *SpeedRow) Stat { return s.TTFT }))
			metric("prefill "+cell, 1, "tok/s", at(func(s *SpeedRow) Stat { return s.Prefill }))
		} else {
			metric("combined "+cell, 1, "tok/s", at(func(s *SpeedRow) Stat { return s.CombinedTPS }))
		}
	}
	if anyTools(r) {
		var parts []string
		for _, row := range ran {
			if row.Tools != nil {
				parts = append(parts, fmt.Sprintf("%s %.0f%% (%d/%d)", row.Label, row.Tools.Accuracy(), row.Tools.Passed, row.Tools.Cases))
			}
		}
		out = append(out, "tool accuracy: "+strings.Join(parts, ", "))
	}
	if anyContext(r) {
		var parts []string
		for _, row := range ran {
			if f, t := row.ContextFound(); t > 0 {
				parts = append(parts, fmt.Sprintf("%s %d/%d", row.Label, f, t))
			}
		}
		if len(parts) > 0 {
			out = append(out, "needle retrieved: "+strings.Join(parts, ", "))
		}
	}
	return out
}

func fmtStat(s Stat, unit string) string {
	format := "%.1f"
	if unit == "s" {
		format = "%.2f"
	}
	out := fmt.Sprintf(format, s.Median)
	if s.N > 1 {
		out += fmt.Sprintf(" ("+format+"–"+format+")", s.Min, s.Max)
	}
	return out + " " + unit
}
