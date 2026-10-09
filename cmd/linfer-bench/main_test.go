package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/sudiptadeb/linfer-bench/bench"
)

// The test binary doubles as a fake OpenAI-compatible backend when
// LINFER_BENCH_FAKE is set, as an installed backend would be: a separate
// process the command reaches over HTTP. It streams a few tokens with
// llama-style timings and answers the tool suite well enough to score.
func TestMain(m *testing.M) {
	if os.Getenv("LINFER_BENCH_FAKE") == "1" {
		fakeBackend()
		return
	}
	os.Exit(m.Run())
}

func fakeBackend() {
	port := ""
	for i, a := range os.Args {
		if a == "--port" && i+1 < len(os.Args) {
			port = os.Args[i+1]
		}
	}
	ln, err := net.Listen("tcp", "127.0.0.1:"+port)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/v1/models", func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, `{"data":[{"id":"fake","owned_by":"llamacpp"}]}`)
	})
	mux.HandleFunc("/v1/chat/completions", func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Messages  []bench.Message `json:"messages"`
			Stream    bool            `json:"stream"`
			MaxTokens int             `json:"max_tokens"`
		}
		body, _ := io.ReadAll(r.Body)
		json.Unmarshal(body, &req)
		last := req.Messages[len(req.Messages)-1]
		chars := 0
		for _, m := range req.Messages {
			chars += len(m.Content)
		}
		if req.Stream {
			w.Header().Set("Content-Type", "text/event-stream")
			fl, _ := w.(http.Flusher)
			words := []string{"The", " log", " goes", " on", "."}
			if strings.Contains(last.Content, "vault passphrase") {
				words = []string{"amber", "-falcon", "-7291"}
			}
			n := min(req.MaxTokens, len(words))
			for i := 0; i < n; i++ {
				fmt.Fprintf(w, "data: {\"choices\":[{\"delta\":{\"content\":%q}}]}\n\n", words[i])
				fl.Flush()
				time.Sleep(3 * time.Millisecond)
			}
			fmt.Fprintf(w, "data: {\"choices\":[],\"usage\":{\"prompt_tokens\":%d,\"completion_tokens\":%d},\"timings\":{\"predicted_per_second\":250.0,\"prompt_per_second\":9000.0}}\n\ndata: [DONE]\n\n", chars/4+20, n)
			return
		}
		text := strings.ToLower(last.Content)
		call := func(name, args string) string {
			return fmt.Sprintf(`{"choices":[{"message":{"role":"assistant","content":null,"tool_calls":[{"id":"c1","type":"function","function":{"name":%q,"arguments":%q}}]}}]}`, name, args)
		}
		say := func(s string) string {
			return fmt.Sprintf(`{"choices":[{"message":{"role":"assistant","content":%q}}]}`, s)
		}
		out := say("hello")
		switch {
		case last.Role == "tool":
			out = say("It is 7°C in Oslo with light rain.")
		case strings.Contains(text, "tokyo"):
			out = `{"choices":[{"message":{"role":"assistant","content":null,"tool_calls":[{"id":"c1","type":"function","function":{"name":"get_weather","arguments":"{\"location\":\"Tokyo\"}"}},{"id":"c2","type":"function","function":{"name":"get_weather","arguments":"{\"location\":\"Berlin\"}"}}]}}]}`
		case strings.Contains(text, "weather"):
			out = call("get_weather", `{"location":"Paris","unit":"celsius"}`)
		case strings.Contains(text, "calculator"):
			out = call("calculate", `{"expression":"1234 * 5678"}`)
		case strings.Contains(text, "poem"):
			out = say("Waves fold the light\nand carry it home.")
		case strings.Contains(text, "look up that account"):
			out = call("lookup_account", `{"account_id":"4471029385"}`)
		case strings.Contains(text, "calendar event"):
			out = call("create_calendar_event", `{"title":"Design review","start":"2026-10-12T10:00","end":"2026-10-12T11:00","attendees":["alice@example.com","bob@example.com"],"location":{"name":"the Hub","address":"12 Main Street"}}`)
		case strings.Contains(text, "search the web"):
			out = call("web_search", `{"query":"latest Go release notes","max_results":3}`)
		case strings.Contains(text, "save the text"):
			out = call("write_file", `{"path":"notes.txt","content":"hello world"}`)
		}
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, out)
	})
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM)
	defer stop()
	srv := &http.Server{Handler: mux}
	go srv.Serve(ln)
	<-ctx.Done()
	srv.Close()
}

// startFake runs the test binary as the backend on a free port and returns
// its base URL.
func startFake(t *testing.T) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	_, port, _ := net.SplitHostPort(ln.Addr().String())
	ln.Close()
	cmd := exec.Command(os.Args[0], "--port", port)
	cmd.Env = append(os.Environ(), "LINFER_BENCH_FAKE=1")
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { cmd.Process.Signal(syscall.SIGTERM); cmd.Wait() })
	url := "http://127.0.0.1:" + port + "/v1"
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if resp, err := http.Get(url + "/models"); err == nil {
			resp.Body.Close()
			return url
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatal("the fake backend did not come up")
	return ""
}

// capture runs f with stdout redirected to a file and returns what it wrote.
func capture(t *testing.T, f func() error) (string, error) {
	t.Helper()
	tmp, err := os.CreateTemp(t.TempDir(), "out")
	if err != nil {
		t.Fatal(err)
	}
	old := os.Stdout
	os.Stdout = tmp
	ferr := f()
	os.Stdout = old
	tmp.Seek(0, 0)
	raw, _ := io.ReadAll(tmp)
	tmp.Close()
	return string(raw), ferr
}

// `run -f bench.yaml` against the fake backend: the estimate is printed
// first, the model is found without being named, every suite runs, the
// table and verdict are printed, and the three files land in --out.
// Then `compare` of the run with itself tells the two apart by time, and
// `models` lists the fake.
func TestRunCompareAndModels(t *testing.T) {
	url := startFake(t)
	dir := t.TempDir()
	t.Setenv("XDG_DATA_HOME", dir) // the default store, kept out of the real one
	store := filepath.Join(dir, "store")
	cfg := filepath.Join(dir, "bench.yaml")
	os.WriteFile(cfg, []byte(fmt.Sprintf("targets:\n  - name: fake\n    url: %s\nprofile: quick\ncontexts: [128]\nconcurrency: [1, 2]\nreply_tokens: 5\nsettle: 10ms\ntags: [baseline]\n", url)), 0o644)
	out := filepath.Join(dir, "out")
	stdout, err := capture(t, func() error {
		return run(context.Background(), []string{"-f", cfg, "--out", out, "--color", "never", "--store", store, "--tag", "first"})
	})
	if err != nil {
		t.Fatalf("%v\n%s", err, stdout)
	}
	for _, want := range []string{"linfer-bench: fake", "quick profile", "9 tool cases (builtin)", "rough estimate", "== fake", "llama.cpp; timings", "128 × 1:", "ok   calculator", "metric", "decode tok/s/stream 128×1", "tools accuracy", "100% (9/9)", "needle found", "1 of 1", "report: " + filepath.Join(out, "report.html")} {
		if !strings.Contains(stdout, want) {
			t.Errorf("output lacks %q:\n%s", want, stdout)
		}
	}
	for _, f := range []string{"results.json", "report.md", "report.html"} {
		if st, err := os.Stat(filepath.Join(out, f)); err != nil || st.Size() == 0 {
			t.Errorf("%s: %v", f, err)
		}
	}
	res, err := bench.Load(filepath.Join(out, "results.json"))
	if err != nil || len(res.Rows) != 1 || res.Rows[0].Model != "fake" || res.Rows[0].Tools.Passed != 9 || res.Options.Profile != "quick" {
		t.Fatalf("results: %v %+v", err, res.Rows)
	}
	if res.ID == "" || len(res.Tags) != 2 || res.Tags[0] != "baseline" || res.Tags[1] != "first" || !strings.Contains(stdout, "stored as run "+res.ID) {
		t.Errorf("id %q tags %v", res.ID, res.Tags)
	}
	if _, err := os.Stat(filepath.Join(store, res.ID+".json")); err != nil {
		t.Error("the run is not in the store:", err)
	}

	cmp := filepath.Join(dir, "cmp")
	stdout, err = capture(t, func() error {
		// Flags after the files, as one types them.
		return compare([]string{filepath.Join(out, "results.json"), filepath.Join(out, "results.json"), "--out", cmp, "--color", "never"})
	})
	if err != nil {
		t.Fatalf("%v\n%s", err, stdout)
	}
	if !strings.Contains(stdout, "fake @") || !strings.Contains(stdout, "within noise") {
		t.Errorf("compare output:\n%s", stdout)
	}
	if _, err := os.Stat(filepath.Join(cmp, "report.html")); err != nil {
		t.Error(err)
	}

	stdout, err = capture(t, func() error { return models(context.Background(), []string{url}) })
	if err != nil || !strings.Contains(stdout, "fake\tllamacpp") {
		t.Errorf("models: %v %q", err, stdout)
	}

	// A second run with another tag, then the store commands: runs lists
	// both, tag and untag edit one, and compare --tags puts the two tags
	// side by side with the stored runs found by tag.
	time.Sleep(1100 * time.Millisecond) // ids are to the second
	_, err = capture(t, func() error {
		return run(context.Background(), []string{"-f", cfg, "--out", filepath.Join(dir, "out2"), "--color", "never", "--store", store, "--tag", "second"})
	})
	if err != nil {
		t.Fatal(err)
	}
	stdout, err = capture(t, func() error { return runs([]string{"--store", store}) })
	if err != nil || strings.Count(stdout, res.ID[:9]) != 2 || !strings.Contains(stdout, "baseline,first") || !strings.Contains(stdout, "baseline,second") || !strings.Contains(stdout, "quick") || !strings.Contains(stdout, "fake:fake") {
		t.Errorf("runs: %v\n%s", err, stdout)
	}
	stdout, err = capture(t, func() error { return runs([]string{"--store", store, "--tag", "second"}) })
	if err != nil || strings.Count(stdout, "\n") != 1 || !strings.Contains(stdout, "second") {
		t.Errorf("runs --tag: %v\n%s", err, stdout)
	}
	stdout, err = capture(t, func() error { return tag([]string{res.ID, "retagged", "x", "--store", store}, true) })
	if err != nil || !strings.Contains(stdout, "baseline, first, retagged, x") {
		t.Errorf("tag: %v %q", err, stdout)
	}
	stdout, err = capture(t, func() error { return tag([]string{"--store", store, res.ID[:len(res.ID)-1], "x", "first"}, false) })
	if err != nil || !strings.Contains(stdout, "tags baseline, retagged\n") {
		t.Errorf("untag by prefix: %v %q", err, stdout)
	}
	cmp = filepath.Join(dir, "cmp-tags")
	stdout, err = capture(t, func() error {
		return compare([]string{"--store", store, "--tags", "retagged,second,nothing", "--out", cmp, "--color", "never"})
	})
	if err != nil {
		t.Fatalf("%v\n%s", err, stdout)
	}
	for _, want := range []string{"metric", "retagged", "second", "nothing", "decode 128×1: ", "nothing: no run carries this tag", "report: "} {
		if !strings.Contains(stdout, want) {
			t.Errorf("compare --tags lacks %q:\n%s", want, stdout)
		}
	}
	c, err := bench.Load(filepath.Join(cmp, "comparison.json"))
	if err != nil || c.Comparison == nil || len(c.Comparison.Sources) != 2 || len(c.Rows) != 3 || c.Rows[0].Label != "retagged" || c.Rows[2].Error == "" {
		t.Errorf("comparison.json: %v %+v", err, c.Comparison)
	}
}

// Flags are checked before anything runs.
func TestRunRefusesBadFlags(t *testing.T) {
	for _, c := range []struct {
		args []string
		want string
	}{
		{[]string{}, "no targets"},
		{[]string{"--target", "a=http://127.0.0.1:1/v1", "--quick", "--full"}, "exclusive"},
		{[]string{"--target", "a=http://127.0.0.1:1/v1", "--suites", "speed,nope"}, `unknown suite "nope"`},
		{[]string{"--target", "a=http://127.0.0.1:1/v1", "--contexts", "2x"}, "--contexts"},
		{[]string{"--target", "a=http://127.0.0.1:1/v1", "--cases", "/nonexistent.yaml"}, "cases:"},
		{[]string{"--target", "a=http://127.0.0.1:1/v1"}, "list models"},
	} {
		_, err := capture(t, func() error { return run(context.Background(), c.args) })
		if err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%v: %v, want %q", c.args, err, c.want)
		}
	}
}
