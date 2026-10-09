package bench

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"
)

// Event is one line of progress: which row and suite it is about, and the
// text. Suite is "" for the row's own setup lines.
type Event struct {
	Row   string
	Suite string
	Text  string
}

// Progress receives events as a run goes; nil is fine.
type Progress func(Event)

func (p Progress) say(row, suite, text string) {
	if p != nil {
		p(Event{Row: row, Suite: suite, Text: text})
	}
}

// Pair is one row to run: a target and one of its models.
type Pair struct {
	Target Target
	Model  string
	Label  string
}

// ResolveModels is the models to bench on a target: its own list, else the
// fallback, with "all" expanded from /v1/models, and an empty list taken
// as the one model the endpoint serves.
func ResolveModels(ctx context.Context, t Target, fallback []string) ([]string, error) {
	wanted := t.Models
	if len(wanted) == 0 {
		wanted = fallback
	}
	all := len(wanted) == 0
	for _, m := range wanted {
		if m == "all" {
			all = true
		}
	}
	if !all {
		return wanted, nil
	}
	listed, err := ListModels(ctx, t.URL, t.APIKeyEnv)
	if err != nil {
		return nil, fmt.Errorf("%s: list models: %w", t.Name, err)
	}
	var ids []string
	for _, m := range listed {
		ids = append(ids, m.ID)
	}
	if len(ids) == 0 {
		return nil, fmt.Errorf("%s: the endpoint lists no models", t.Name)
	}
	if len(wanted) > 0 {
		return ids, nil // "all"
	}
	if len(ids) == 1 {
		return ids, nil
	}
	return nil, fmt.Errorf("%s serves %d models (%s); name the ones to bench, or \"all\"", t.Name, len(ids), strings.Join(ids, ", "))
}

// Expand turns targets into the rows to run, one per target × model, with
// labels: the target's name, or target/model when a target has several.
func Expand(ctx context.Context, targets []Target, models []string) ([]Pair, error) {
	var pairs []Pair
	for _, t := range targets {
		ids, err := ResolveModels(ctx, t, models)
		if err != nil {
			return nil, err
		}
		for _, id := range ids {
			label := t.Name
			if len(ids) > 1 {
				label = t.Name + "/" + id
			}
			pairs = append(pairs, Pair{Target: t, Model: id, Label: label})
		}
	}
	return pairs, nil
}

// Run benches every pair, targets one after another unless o.Parallel, and
// returns the results with the machine filled in. Rows that could not run
// carry their error; the error returned is the context's when it was
// cancelled.
func Run(ctx context.Context, pairs []Pair, o Options, p Progress) (Results, error) {
	o.Defaults()
	res := Results{Schema: SchemaVersion, Tool: "linfer-bench " + Version, Started: time.Now(), Machine: DetectMachine(), Options: o}
	res.Title = title(pairs)
	res.ID = NewID(res.Started, res.Title)
	rows := make([]Row, len(pairs))
	if o.Parallel {
		// Targets at once; a target's models still one at a time.
		var wg sync.WaitGroup
		byTarget := map[string][]int{}
		var order []string
		for i, pr := range pairs {
			if _, ok := byTarget[pr.Target.Name]; !ok {
				order = append(order, pr.Target.Name)
			}
			byTarget[pr.Target.Name] = append(byTarget[pr.Target.Name], i)
		}
		for _, name := range order {
			wg.Add(1)
			go func(idx []int) {
				defer wg.Done()
				for _, i := range idx {
					rows[i] = RunRow(ctx, pairs[i], o, p)
				}
			}(byTarget[name])
		}
		wg.Wait()
	} else {
		for i, pr := range pairs {
			if ctx.Err() != nil {
				rows[i] = Row{Label: pr.Label, Target: pr.Target.Name, URL: pr.Target.URL, Model: pr.Model, Error: ctx.Err().Error()}
				continue
			}
			rows[i] = RunRow(ctx, pr, o, p)
		}
	}
	res.Rows = rows
	res.Duration = time.Since(res.Started).Seconds()
	return res, ctx.Err()
}

func title(pairs []Pair) string {
	models := map[string]bool{}
	var targets []string
	for _, p := range pairs {
		models[p.Model] = true
		targets = append(targets, p.Target.Name)
	}
	if len(models) == 1 && len(pairs) > 0 {
		return pairs[0].Model
	}
	return strings.Join(uniq(targets), ", ")
}

func uniq(xs []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, x := range xs {
		if !seen[x] {
			seen[x] = true
			out = append(out, x)
		}
	}
	return out
}

// RunRow benches one target × model and returns its row. This is the hook
// for an orchestrator that starts backends itself: start one, call RunRow
// on its URL, stop it, and merge the rows into a Results for the report.
// The context cancels the run between requests; progress gets a line per
// cell, case and retrieval.
func RunRow(ctx context.Context, pr Pair, o Options, p Progress) Row {
	o.Defaults()
	t := pr.Target
	label := pr.Label
	if label == "" {
		label = t.Name
	}
	row := Row{Label: label, Target: t.Name, URL: t.URL, Model: pr.Model, Started: time.Now()}
	defer func() { row.Duration = time.Since(row.Started).Seconds() }()
	suite, err := o.Suite()
	if err != nil {
		row.Error = "cases: " + err.Error()
		return row
	}
	say := func(suite, format string, args ...any) { p.say(label, suite, fmt.Sprintf(format, args...)) }

	models, _ := ListModels(ctx, t.URL, t.APIKeyEnv)
	cl := NewClient(t.URL, pr.Model, keyFromEnv(t.APIKeyEnv), time.Duration(o.RequestTimeout))
	cl.Sampling = o.Sampling
	// Warm-up: the first request after a load pays for buffers and compiles,
	// and an endpoint that cannot answer it cannot answer anything.
	if st := cl.Complete(ctx, []Message{{Role: "user", Content: "Say hello."}}, 8); st.Err != "" {
		row.Error = "warm-up: " + st.Err
		say("", "%s", row.Error)
		return row
	}
	cpt, first, err := calibrate(ctx, cl)
	if err != nil {
		row.Failures = append(row.Failures, err.Error())
		cpt = 3.8
	}
	row.Server = ServerKind(models, first)
	row.ServerVersion = serverVersion(ctx, t.URL, keyFromEnv(t.APIKeyEnv))
	row.Timing = TimingSource(first)
	say("", "%s; timings %s", strings.TrimSpace(row.Server+" "+row.ServerVersion), row.Timing)
	progress := func(suite string) func(string) {
		return func(s string) { say(suite, "%s", s) }
	}
	if o.Has(SuiteSpeed) && ctx.Err() == nil {
		say(SuiteSpeed, "speed")
		row.Speed = runSpeed(ctx, cl, speedPlan{
			contexts: o.Contexts, concurrency: o.Concurrency, runs: o.Runs,
			replyTokens: o.ReplyTokens, charsPerToken: cpt, maxContext: t.MaxContext, settle: time.Duration(o.Settle),
		}, progress(SuiteSpeed))
	}
	if o.Has(SuiteTools) && ctx.Err() == nil {
		say(SuiteTools, "tools (%s, %d cases × %d)", suite.Name, len(suite.Cases), o.ToolPasses)
		ts := runTools(ctx, cl, suite, o.ToolPasses, progress(SuiteTools))
		row.Tools = &ts
	}
	if o.Has(SuiteContext) && ctx.Err() == nil {
		say(SuiteContext, "context")
		row.Context = runContext(ctx, cl, o.Contexts, o.Depths, cpt, t.MaxContext, progress(SuiteContext))
	}
	if ctx.Err() != nil {
		row.Failures = append(row.Failures, "the run was cancelled: "+ctx.Err().Error())
	}
	return row
}

// serverVersion asks the endpoint for its version where a server tells one:
// llama-server's /props (build_info), Ollama's /api/version, vLLM's
// /version. Empty when none answers; the bench goes on without it.
func serverVersion(ctx context.Context, base, apiKey string) string {
	root := strings.TrimSuffix(strings.TrimRight(base, "/"), "/v1")
	client := &http.Client{Timeout: 3 * time.Second}
	for _, probe := range []struct{ path, field string }{{"/props", "build_info"}, {"/api/version", "version"}, {"/version", "version"}} {
		req, err := http.NewRequestWithContext(ctx, "GET", root+probe.path, nil)
		if err != nil {
			continue
		}
		if apiKey != "" {
			req.Header.Set("Authorization", "Bearer "+apiKey)
		}
		resp, err := client.Do(req)
		if err != nil {
			continue
		}
		raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
		resp.Body.Close()
		if resp.StatusCode != 200 {
			continue
		}
		var body map[string]any
		if json.Unmarshal(raw, &body) != nil {
			continue
		}
		if v, ok := body[probe.field].(string); ok && v != "" {
			return firstLine(v)
		}
	}
	return ""
}
