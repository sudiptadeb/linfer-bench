package bench

import (
	"fmt"
	"sort"
	"strings"
	"time"
)

// Measurement is the paragraph under the comparison that says what the
// numbers are, so a report stands on its own.
const Measurement = "Each speed cell is two passes over new prompts sent all at once. The cold pass (one token each) gives time to first token and prefill with every prompt arriving together; after a short settle, a warm pass on the now-cached prompts gives decode per stream, combined tok/s (all streams' tokens over the wall time of the pass) and, for one stream, the warm time to first token. The warm pass is the shape of an agent's turn: a cached history and a long reply. Decode and prefill are measured on the client from the stream, the same way for every server; a server's own figures, where it reports them, sit beside, with the stream's granularity in tokens per chunk, since a server that streams in bursts looks different to a client than one that streams a token at a time. Medians, with min–max in brackets when there was more than one run."

// Markdown is report.md.
func Markdown(r Results) string {
	var b strings.Builder
	fmt.Fprintf(&b, "# linfer-bench: %s\n\n", r.Title)
	fmt.Fprintf(&b, "%s · %s · %s · %s profile", r.Started.Format("2006-01-02 15:04"), r.Machine, time.Duration(r.Duration*float64(time.Second)).Round(time.Second), r.Options.Profile)
	if r.ID != "" {
		fmt.Fprintf(&b, " · run `%s`", r.ID)
	}
	if tags := AllTags(r); len(tags) > 0 {
		fmt.Fprintf(&b, " · tags %s", strings.Join(tags, ", "))
	}
	fmt.Fprintln(&b)
	fmt.Fprintln(&b)
	if c := r.Comparison; c != nil {
		fmt.Fprintf(&b, "A comparison of tags %s over runs %s.\n\n", strings.Join(c.Tags, ", "), strings.Join(c.Sources, ", "))
		for _, w := range c.Warnings {
			fmt.Fprintf(&b, "> **Not like for like:** %s\n\n", w)
		}
	}
	fmt.Fprintln(&b, "## Verdict")
	fmt.Fprintln(&b)
	for _, v := range Verdict(r) {
		fmt.Fprintf(&b, "- %s\n", v)
	}
	fmt.Fprintln(&b)
	fmt.Fprintln(&b, "## Targets")
	fmt.Fprintln(&b)
	for _, row := range r.Rows {
		if row.Error != "" {
			fmt.Fprintf(&b, "- **%s**: `%s`, model `%s`: did not run: %s\n", row.Label, row.URL, row.Model, row.Error)
			continue
		}
		fmt.Fprintf(&b, "- **%s**: `%s`, model `%s`", row.Label, row.URL, row.Model)
		if row.Server != "" {
			fmt.Fprintf(&b, ", %s", strings.TrimSpace(row.Server+" "+row.ServerVersion))
		}
		if len(row.Runs) > 0 {
			fmt.Fprintf(&b, "; runs %s", strings.Join(row.Runs, ", "))
		}
		if len(row.Tags) > 0 {
			fmt.Fprintf(&b, "; tags %s", strings.Join(row.Tags, ", "))
		}
		for _, kv := range row.Info {
			fmt.Fprintf(&b, "; %s %s", kv.Key, kv.Value)
		}
		if row.Timing != "" {
			fmt.Fprintf(&b, ". Timings %s.", row.Timing)
		}
		fmt.Fprintln(&b)
	}
	fmt.Fprintln(&b)
	fmt.Fprintln(&b, "## Comparison")
	fmt.Fprintln(&b)
	fmt.Fprintln(&b, "```")
	Table(&b, r, false)
	fmt.Fprintln(&b, "```")
	fmt.Fprintln(&b)
	fmt.Fprintln(&b, Measurement)
	for _, row := range r.Rows {
		if row.Tools != nil && len(row.Tools.Failures) > 0 {
			fmt.Fprintf(&b, "\n### %s: tool failures\n\n", row.Label)
			for _, f := range row.Tools.Failures {
				fmt.Fprintf(&b, "- %s\n", f)
			}
		}
		if f := row.AllFailures(); len(f) > 0 {
			fmt.Fprintf(&b, "\n### %s: errors\n\n", row.Label)
			for _, x := range f {
				fmt.Fprintf(&b, "- %s\n", x)
			}
		}
		if n := serverTimings(row); n != "" {
			fmt.Fprintf(&b, "\n### %s: server-reported timings\n\n%s", row.Label, n)
		}
	}
	if len(r.Notes) > 0 {
		fmt.Fprintln(&b, "\n## Notes")
		fmt.Fprintln(&b)
		for _, n := range r.Notes {
			fmt.Fprintf(&b, "- %s\n", n)
		}
	}
	fmt.Fprintf(&b, "\n---\n%s · results.json schema %d\n", r.Tool, r.Schema)
	return b.String()
}

func serverTimings(row Row) string {
	var out strings.Builder
	for _, s := range row.Speed {
		if len(s.Server) == 0 {
			continue
		}
		var ks []string
		for k := range s.Server {
			ks = append(ks, k)
		}
		sort.Strings(ks)
		fmt.Fprintf(&out, "- %s×%d:", s.Context, s.Concurrency)
		for _, k := range ks {
			fmt.Fprintf(&out, " %s=%.2f", k, s.Server[k])
		}
		fmt.Fprintln(&out)
	}
	return out.String()
}
