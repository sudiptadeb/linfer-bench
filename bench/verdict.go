package bench

import (
	"fmt"
	"math"
	"sort"
	"strings"
)

type ranked struct {
	name string
	stat Stat
}

// rank orders rows by a stat's median, returning the best and second.
func rank(rows []Row, pick func(Row) Stat) (*ranked, *ranked) {
	var rs []ranked
	for _, r := range rows {
		if s := pick(r); s.N > 0 {
			rs = append(rs, ranked{r.Label, s})
		}
	}
	sort.Slice(rs, func(i, j int) bool { return rs[i].stat.Median > rs[j].stat.Median })
	switch len(rs) {
	case 0:
		return nil, nil
	case 1:
		return &rs[0], nil
	}
	return &rs[0], &rs[1]
}

// Verdict is the short reading of the numbers, careful not to call a
// difference that is inside the runs' own spread. For a comparison of tags
// it leads with what makes the comparison unfair, then a line per metric.
func Verdict(r Results) []string {
	if r.Comparison != nil {
		var out []string
		for _, w := range r.Comparison.Warnings {
			out = append(out, "not like for like: "+w)
		}
		out = append(out, CompareVerdict(r)...)
		for _, m := range r.Comparison.Missing {
			out = append(out, "missing: "+m)
		}
		for _, row := range r.Rows {
			if row.Error != "" {
				out = append(out, fmt.Sprintf("%s: %s", row.Label, row.Error))
			} else if f := row.AllFailures(); len(f) > 0 {
				out = append(out, fmt.Sprintf("%s: %d failure(s), first: %s", row.Label, len(f), f[0]))
			}
		}
		if r.Options.Runs < 2 {
			out = append(out, "single run per cell in at least one run: treat differences under ~10% as unproven unless several runs per tag give a spread")
		}
		return out
	}
	var ran []Row
	for _, row := range r.Rows {
		if row.Error == "" {
			ran = append(ran, row)
		}
	}
	var out []string
	if len(ran) == 0 {
		return []string{"no target ran"}
	}
	cs := cells(r)
	if len(cs) > 0 {
		// Fastest single agent: concurrency 1 at the smallest context.
		small := cs[0].ctx
		best, second := rank(ran, func(row Row) Stat {
			if s := row.SpeedAt(small, 1); s != nil {
				return s.DecodeTPS
			}
			return Stat{}
		})
		if best != nil {
			out = append(out, compared(fmt.Sprintf("fastest single stream (%s context)", small), best, second))
		}
		// Most combined output at the top concurrency, largest context measured.
		top := cs[len(cs)-1]
		best, second = rank(ran, func(row Row) Stat {
			if s := row.SpeedAt(top.ctx, top.conc); s != nil {
				return s.CombinedTPS
			}
			return Stat{}
		})
		if best != nil && top.conc > 1 {
			out = append(out, compared(fmt.Sprintf("most combined output (%s context, %d streams)", top.ctx, top.conc), best, second))
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
			f, t := row.ContextFound()
			if t > 0 {
				parts = append(parts, fmt.Sprintf("%s %d/%d", row.Label, f, t))
			}
		}
		if len(parts) > 0 {
			out = append(out, "needle retrieved: "+strings.Join(parts, ", "))
		}
	}
	// A server whose own decode figure disagrees with the client's by a
	// fifth or more streams in bursts: say so, since both numbers are in the
	// table and the reader has to pick.
	for _, row := range ran {
		for _, s := range row.Speed {
			if s.Concurrency != 1 || s.DecodeTPS.N == 0 || s.ServerDecode.N == 0 {
				continue
			}
			c, sv := s.DecodeTPS.Median, s.ServerDecode.Median
			if math.Abs(c-sv) > 0.2*math.Max(c, sv) {
				out = append(out, fmt.Sprintf("%s: client-side decode %.1f vs server-reported %.1f tok/s at %s×1 (%.1f tokens per stream chunk); the server's figure is its own, the client's is what a stream consumer sees",
					row.Label, c, sv, s.Context, s.TokensPerChunk.Median))
			}
			break
		}
	}
	// A row with client-side figures only, beside one with the server's.
	var withServer, without []string
	for _, row := range ran {
		if len(row.Speed) == 0 {
			continue
		}
		has := false
		for _, s := range row.Speed {
			if s.ServerDecode.N > 0 {
				has = true
			}
		}
		if has {
			withServer = append(withServer, row.Label)
		} else {
			without = append(without, row.Label)
		}
	}
	if len(withServer) > 0 && len(without) > 0 {
		out = append(out, fmt.Sprintf("%s report no timings of their own: their figures are client-side only", strings.Join(without, ", ")))
	}
	for _, row := range r.Rows {
		if row.Error != "" {
			out = append(out, fmt.Sprintf("%s did not run: %s", row.Label, row.Error))
		} else if f := row.AllFailures(); len(f) > 0 {
			out = append(out, fmt.Sprintf("%s: %d failure(s), first: %s", row.Label, len(f), f[0]))
		}
	}
	if r.Options.Runs < 2 {
		out = append(out, "single run per cell: no spread to judge noise by, so treat differences under ~10% as unproven")
	}
	return out
}

// compared is one verdict line: the best, and the second as a ratio or as
// within noise.
func compared(what string, best, second *ranked) string {
	line := fmt.Sprintf("%s: %s at %.1f tok/s", what, best.name, best.stat.Median)
	if second != nil {
		if WithinNoise(best.stat, second.stat) {
			line += fmt.Sprintf("; %s at %.1f is within noise", second.name, second.stat.Median)
		} else {
			line += fmt.Sprintf(", %.2f× %s (%.1f)", best.stat.Median/second.stat.Median, second.name, second.stat.Median)
		}
	}
	return line
}
