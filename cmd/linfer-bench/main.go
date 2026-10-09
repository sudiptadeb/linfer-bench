// linfer-bench measures OpenAI-compatible endpoints and compares them.
//
//	linfer-bench run --target llama=http://127.0.0.1:8090/v1 --target mlx=http://127.0.0.1:18080/v1 --model qwen3-coder:30b
//	linfer-bench run -f bench.yaml --tag after-upgrade
//	linfer-bench models http://127.0.0.1:8090/v1
//	linfer-bench runs [--tag T]
//	linfer-bench tag RUN-ID TAG...     (and untag)
//	linfer-bench compare --tags baseline,after-upgrade
//	linfer-bench compare a/results.json b/results.json
//
// run writes results.json, report.md and report.html to --out (default
// ./linfer-bench-<timestamp>), prints the comparison and a verdict, and
// keeps a copy of results.json in the store (~/.local/share/linfer-bench/runs)
// under the run's id, where runs and tag find it.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/sudiptadeb/linfer-bench/bench"
)

const usage = `usage:
  linfer-bench run [--target name=URL]... [--model ID|all]... [-f bench.yaml] [--tag T]... [flags]
  linfer-bench models URL [--api-key-env NAME]
  linfer-bench runs [--tag T] [--store DIR]
  linfer-bench tag RUN-ID TAG...
  linfer-bench untag RUN-ID TAG...
  linfer-bench compare [--tags T1,T2,...] [RUN-ID|results.json]... [--out DIR]
  linfer-bench version

run flags:`

func main() {
	if len(os.Args) < 2 {
		fmt.Fprintln(os.Stderr, usage)
		os.Exit(2)
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	var err error
	switch os.Args[1] {
	case "run":
		err = run(ctx, os.Args[2:])
	case "models":
		err = models(ctx, os.Args[2:])
	case "runs":
		err = runs(os.Args[2:])
	case "tag":
		err = tag(os.Args[2:], true)
	case "untag":
		err = tag(os.Args[2:], false)
	case "compare":
		err = compare(os.Args[2:])
	case "version":
		fmt.Println("linfer-bench", bench.Version)
	case "-h", "--help", "help":
		run(ctx, []string{"-h"})
	default:
		err = fmt.Errorf("unknown command %q\n%s", os.Args[1], usage)
	}
	if err != nil {
		if errors.Is(err, flag.ErrHelp) {
			os.Exit(2)
		}
		fmt.Fprintln(os.Stderr, "linfer-bench:", err)
		os.Exit(1)
	}
}

// list is a repeatable string flag.
type list []string

func (l *list) String() string     { return strings.Join(*l, ",") }
func (l *list) Set(s string) error { *l = append(*l, s); return nil }

func storeFlag(fs *flag.FlagSet) *string {
	return fs.String("store", bench.DefaultStoreDir(), "the results store")
}

// parseMixed parses flags that may come before, between or after the
// positional arguments, which the flag package alone stops at, and returns
// the positionals.
func parseMixed(fs *flag.FlagSet, args []string) ([]string, error) {
	var pos []string
	for {
		if err := fs.Parse(args); err != nil {
			return nil, err
		}
		rest := fs.Args()
		if len(rest) == 0 {
			return pos, nil
		}
		pos = append(pos, rest[0])
		args = rest[1:]
	}
}

func run(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("run", flag.ContinueOnError)
	fs.Usage = func() {
		fmt.Fprintln(os.Stderr, usage)
		fs.PrintDefaults()
	}
	var targets, modelFlags, keyEnvs, tags list
	fs.Var(&targets, "target", "an endpoint, name=http://host:port/v1 (repeatable)")
	fs.Var(&modelFlags, "model", "a model id to bench on every target, or all (repeatable; default: the one model the endpoint serves)")
	fs.Var(&keyEnvs, "api-key-env", "environment variable holding the bearer token, NAME or target=NAME (repeatable)")
	fs.Var(&tags, "tag", "a tag for this run, for `compare --tags` (repeatable)")
	file := fs.String("f", "", "a bench.yaml with targets, models, tags and options")
	suites := fs.String("suites", "", "comma list of speed, tools, context (default all)")
	conc := fs.String("concurrency", "", "comma list of stream counts (profile default)")
	contexts := fs.String("contexts", "", "comma list of prompt sizes, k = 1024 tokens (profile default)")
	runs := fs.Int("runs", 0, "runs per speed cell, median taken (profile default)")
	reply := fs.Int("reply-tokens", 0, "tokens per reply in the speed suite (profile default)")
	passes := fs.Int("tool-passes", 0, "passes over the tool suite (profile default)")
	cases := fs.String("cases", "", "a cases file, YAML or JSON lines (default: the built-in suite)")
	quick := fs.Bool("quick", false, "the quick profile: a few minutes")
	full := fs.Bool("full", false, "the full profile: 2k and 32k contexts, up to 8 streams, 3 runs")
	maxCtx := fs.Int("max-context", 0, "longest prompt the servers take, in tokens; larger cells are skipped")
	parallel := fs.Bool("parallel", false, "run the targets at once (for remote endpoints; local ones contend)")
	out := fs.String("out", "", "report directory (default ./linfer-bench-<timestamp>)")
	store := storeFlag(fs)
	noStore := fs.Bool("no-store", false, "do not keep a copy of results.json in the store")
	color := fs.String("color", "auto", "auto, always or never")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() > 0 {
		return fmt.Errorf("unexpected argument %q", fs.Arg(0))
	}

	var f bench.File
	if *file != "" {
		var err error
		if f, err = bench.LoadFile(*file); err != nil {
			return err
		}
	}
	for _, t := range targets {
		pt, err := bench.ParseTarget(t)
		if err != nil {
			return err
		}
		f.Targets = append(f.Targets, pt)
	}
	if len(f.Targets) == 0 {
		return errors.New("no targets: pass --target name=URL or -f bench.yaml")
	}
	if len(modelFlags) > 0 {
		f.Models = modelFlags
	}
	f.Tags = append(f.Tags, tags...)
	for _, k := range keyEnvs {
		name, env, ok := strings.Cut(k, "=")
		for i := range f.Targets {
			if !ok {
				f.Targets[i].APIKeyEnv = name
			} else if f.Targets[i].Name == name {
				f.Targets[i].APIKeyEnv = env
			}
		}
	}
	if *maxCtx > 0 {
		for i := range f.Targets {
			f.Targets[i].MaxContext = *maxCtx
		}
	}
	o := &f.Options
	switch {
	case *quick && *full:
		return errors.New("--quick and --full are exclusive")
	case *quick:
		o.Profile = bench.ProfileQuick
	case *full:
		o.Profile = bench.ProfileFull
	}
	if *suites != "" {
		o.Suites = strings.Split(*suites, ",")
	}
	var err error
	if *conc != "" {
		if o.Concurrency, err = bench.ParseList(*conc); err != nil {
			return fmt.Errorf("--concurrency %w", err)
		}
	}
	if *contexts != "" {
		var cs []int
		if cs, err = bench.ParseList(*contexts); err != nil {
			return fmt.Errorf("--contexts %w", err)
		}
		o.Contexts = cs
	}
	setIf := func(p *int, v int) {
		if v > 0 {
			*p = v
		}
	}
	setIf(&o.Runs, *runs)
	setIf(&o.ReplyTokens, *reply)
	setIf(&o.ToolPasses, *passes)
	if *cases != "" {
		o.Cases = *cases
	}
	if *parallel {
		o.Parallel = true
	}
	if *out != "" {
		o.Out = *out
	}
	if o.Out == "" {
		o.Out = bench.DefaultOut(time.Now())
	}
	if err := o.Validate(); err != nil {
		return err
	}
	o.Defaults()
	suite, err := o.Suite()
	if err != nil {
		return fmt.Errorf("cases: %w", err)
	}

	pairs, err := bench.Expand(ctx, f.Targets, f.Models)
	if err != nil {
		return err
	}
	requests, est := bench.Estimate(*o, len(suite.Cases))
	var labels []string
	for _, p := range pairs {
		labels = append(labels, p.Label)
	}
	fmt.Printf("linfer-bench: %s\n", strings.Join(labels, ", "))
	fmt.Printf("  %s profile; suites %s; contexts %v tokens; concurrency %v; %d run(s) per cell; %d-token replies; %d pass(es) of %d tool cases (%s)\n",
		o.Profile, strings.Join(o.Suites, ","), []int(o.Contexts), o.Concurrency, o.Runs, o.ReplyTokens, o.ToolPasses, len(suite.Cases), suite.Name)
	total := est * time.Duration(len(pairs))
	if total > 2*time.Minute {
		total = total.Round(time.Minute)
	} else {
		total = total.Round(time.Second)
	}
	fmt.Printf("  about %d requests per row, %d row(s); rough estimate %s%s\n", requests, len(pairs), total, ifs(o.Parallel, " (targets in parallel)", ""))
	if len(f.Tags) > 0 {
		fmt.Printf("  tags %s\n", strings.Join(f.Tags, ", "))
	}

	res, runErr := bench.Run(ctx, pairs, *o, printProgress)
	res.Tags = f.Tags
	files, err := bench.WriteFiles(o.Out, res)
	if err != nil {
		return err
	}
	fmt.Println()
	bench.Table(os.Stdout, res, useColor(*color))
	fmt.Println()
	for _, line := range bench.Verdict(res) {
		fmt.Printf("- %s\n", line)
	}
	fmt.Printf("\nreport: %s\n", files[2])
	if !*noStore {
		if _, err := (bench.Store{Dir: *store}).Put(res); err != nil {
			fmt.Fprintf(os.Stderr, "linfer-bench: could not store the run: %v\n", err)
		} else {
			fmt.Printf("stored as run %s (linfer-bench runs; compare --tags ...)\n", res.ID)
		}
	}
	return runErr
}

// printProgress is the run's progress on stdout: a heading per row, the
// suite names indented once, their lines twice.
func printProgress(e bench.Event) {
	switch {
	case e.Suite == "" && e.Text != "":
		fmt.Printf("\n== %s\n  %s\n", e.Row, e.Text)
	case e.Text == e.Suite || strings.HasPrefix(e.Text, e.Suite+" "):
		fmt.Printf("  %s\n", e.Text)
	default:
		fmt.Printf("    %s\n", e.Text)
	}
}

func useColor(mode string) bool {
	switch mode {
	case "always":
		return true
	case "never":
		return false
	}
	if os.Getenv("NO_COLOR") != "" {
		return false
	}
	st, err := os.Stdout.Stat()
	return err == nil && st.Mode()&os.ModeCharDevice != 0
}

func models(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("models", flag.ContinueOnError)
	keyEnv := fs.String("api-key-env", "", "environment variable holding the bearer token")
	pos, err := parseMixed(fs, args)
	if err != nil {
		return err
	}
	if len(pos) != 1 {
		return errors.New("usage: linfer-bench models URL [--api-key-env NAME]")
	}
	t, err := bench.ParseTarget(pos[0])
	if err != nil {
		return err
	}
	ms, err := bench.ListModels(ctx, t.URL, *keyEnv)
	if err != nil {
		return err
	}
	if len(ms) == 0 {
		fmt.Println("no models listed")
		return nil
	}
	for _, m := range ms {
		if m.OwnedBy != "" {
			fmt.Printf("%s\t%s\n", m.ID, m.OwnedBy)
		} else {
			fmt.Println(m.ID)
		}
	}
	return nil
}

// runs lists the store: id, date, tags, rows and profile.
func runs(args []string) error {
	fs := flag.NewFlagSet("runs", flag.ContinueOnError)
	store := storeFlag(fs)
	only := fs.String("tag", "", "list only runs carrying this tag")
	if err := fs.Parse(args); err != nil {
		return err
	}
	rs, err := (bench.Store{Dir: *store}).List()
	if err != nil {
		fmt.Fprintf(os.Stderr, "linfer-bench: %v\n", err)
	}
	n := 0
	for _, r := range rs {
		if *only != "" && !bench.HasTag(r, *only) {
			continue
		}
		n++
		var labels []string
		for _, row := range r.Rows {
			labels = append(labels, row.Label+":"+row.Model)
		}
		tags := strings.Join(bench.AllTags(r), ",")
		if tags == "" {
			tags = "-"
		}
		fmt.Printf("%s  %s  %-8s %-32s  %s\n", r.ID, r.Started.Format("2006-01-02 15:04"), r.Options.Profile, tags, strings.Join(labels, " "))
	}
	if n == 0 {
		fmt.Printf("no runs in %s\n", *store)
	}
	return nil
}

// tag adds (or removes) tags on a stored run.
func tag(args []string, add bool) error {
	verb := ifs(add, "tag", "untag")
	fs := flag.NewFlagSet(verb, flag.ContinueOnError)
	store := storeFlag(fs)
	pos, err := parseMixed(fs, args)
	if err != nil {
		return err
	}
	if len(pos) < 2 {
		return fmt.Errorf("usage: linfer-bench %s RUN-ID TAG...", verb)
	}
	var addT, rmT []string
	if add {
		addT = pos[1:]
	} else {
		rmT = pos[1:]
	}
	r, err := (bench.Store{Dir: *store}).SetTags(pos[0], addT, rmT)
	if err != nil {
		return err
	}
	fmt.Printf("%s: tags %s\n", r.ID, ifs(len(r.Tags) > 0, strings.Join(r.Tags, ", "), "(none)"))
	return nil
}

// compare builds a comparison: across tags, with the runs that carry them
// from the store and from the files or ids given; or, without --tags, of
// the given runs side by side.
func compare(args []string) error {
	fs := flag.NewFlagSet("compare", flag.ContinueOnError)
	tagList := fs.String("tags", "", "comma list of tags to compare, each a column")
	store := storeFlag(fs)
	out := fs.String("out", "", "report directory (default ./linfer-bench-compare-<timestamp>)")
	color := fs.String("color", "auto", "auto, always or never")
	pos, err := parseMixed(fs, args)
	if err != nil {
		return err
	}
	if len(pos) < 1 && *tagList == "" {
		return errors.New("usage: linfer-bench compare [--tags T1,T2] [RUN-ID|results.json]... [--out DIR]")
	}
	st := bench.Store{Dir: *store}
	var given []bench.Results
	var names []string
	for _, p := range pos {
		var r bench.Results
		var err error
		if strings.HasSuffix(p, ".json") {
			r, err = bench.Load(p)
			names = append(names, filepath.Base(filepath.Dir(p))+"/"+filepath.Base(p))
		} else {
			r, err = st.Find(p)
			names = append(names, r.ID)
		}
		if err != nil {
			return err
		}
		given = append(given, r)
	}
	var res bench.Results
	if *tagList != "" {
		tags := strings.Split(*tagList, ",")
		pool, err := st.List()
		if err != nil {
			fmt.Fprintf(os.Stderr, "linfer-bench: %v\n", err)
		}
		for _, g := range given {
			dup := false
			for _, p := range pool {
				if p.ID != "" && p.ID == g.ID {
					dup = true
				}
			}
			if !dup {
				pool = append(pool, g)
			}
		}
		res = bench.CompareTags(pool, tags)
	} else {
		res = bench.Merge(given, names)
	}
	if *out == "" {
		*out = "linfer-bench-compare-" + time.Now().Format("2006-01-02-150405")
	}
	files, err := bench.WriteFiles(*out, res)
	if err != nil {
		return err
	}
	bench.Table(os.Stdout, res, useColor(*color))
	fmt.Println()
	for _, line := range bench.Verdict(res) {
		fmt.Printf("- %s\n", line)
	}
	fmt.Printf("\nreport: %s\n", files[2])
	return nil
}

func ifs(cond bool, a, b string) string {
	if cond {
		return a
	}
	return b
}
