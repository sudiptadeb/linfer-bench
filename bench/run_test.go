package bench

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// fakeServer is an OpenAI-compatible endpoint: /v1/models lists what it
// was given, streamed completions take a little longer for a longer prompt
// and carry llama-style timings, and the tool suite is answered correctly
// but for one deliberate miss (the calendar event's location as a string),
// so a failure shows in the report.
func fakeServer(t *testing.T, models ...string) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/v1/models", func(w http.ResponseWriter, r *http.Request) {
		var data []map[string]string
		for _, m := range models {
			data = append(data, map[string]string{"id": m, "owned_by": "llamacpp"})
		}
		json.NewEncoder(w).Encode(map[string]any{"data": data})
	})
	mux.HandleFunc("/v1/chat/completions", fakeChat)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

func fakeChat(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Messages  []Message `json:"messages"`
		Stream    bool      `json:"stream"`
		MaxTokens int       `json:"max_tokens"`
		Tools     []Tool    `json:"tools"`
	}
	body, _ := io.ReadAll(r.Body)
	json.Unmarshal(body, &req)
	last := req.Messages[len(req.Messages)-1]
	promptChars := 0
	for _, m := range req.Messages {
		promptChars += len(m.Content)
	}
	if req.Stream {
		w.Header().Set("Content-Type", "text/event-stream")
		fl, _ := w.(http.Flusher)
		words := []string{"The", " log", " continues", " with", " more", " entries", "."}
		if strings.Contains(last.Content, "vault passphrase") {
			words = []string{"amber", "-falcon", "-7291"}
		}
		n := min(req.MaxTokens, len(words))
		time.Sleep(time.Duration(promptChars/200) * time.Millisecond) // a prefill that grows with the prompt
		for i := 0; i < n; i++ {
			chunk, _ := json.Marshal(map[string]any{"choices": []any{map[string]any{"delta": map[string]any{"content": words[i]}}}})
			fmt.Fprintf(w, "data: %s\n\n", chunk)
			fl.Flush()
			time.Sleep(5 * time.Millisecond)
		}
		final, _ := json.Marshal(map[string]any{
			"choices": []any{},
			"usage":   map[string]any{"prompt_tokens": promptChars/4 + 20, "completion_tokens": n},
			"timings": map[string]any{"predicted_per_second": 123.0, "prompt_per_second": 4567.0},
		})
		fmt.Fprintf(w, "data: %s\n\ndata: [DONE]\n\n", final)
		return
	}
	msg := map[string]any{"role": "assistant", "content": nil}
	calls := func(cs ...ToolCall) { msg["tool_calls"] = cs }
	text := strings.ToLower(last.Content)
	switch {
	case last.Role == "tool":
		msg["content"] = "It is 7°C in Oslo with light rain."
	case strings.Contains(text, "tokyo") && strings.Contains(text, "berlin"):
		calls(call("c1", "get_weather", `{"location":"Tokyo"}`), call("c2", "get_weather", `{"location":"Berlin"}`))
	case strings.Contains(text, "weather"):
		calls(call("c1", "get_weather", `{"location":"Paris","unit":"celsius"}`))
	case strings.Contains(text, "calculator"):
		calls(call("c1", "calculate", `{"expression":"1234 * 5678"}`))
	case strings.Contains(text, "poem"):
		msg["content"] = "Waves fold the light\nand carry it home."
	case strings.Contains(text, "look up that account"):
		calls(call("c1", "lookup_account", `{"account_id":"4471029385"}`))
	case strings.Contains(text, "calendar event"):
		calls(call("c1", "create_calendar_event", `{"title":"Design review","start":"2026-10-12T10:00","end":"2026-10-12T11:00","attendees":["alice@example.com","bob@example.com"],"location":"the Hub"}`))
	case strings.Contains(text, "search the web"):
		calls(call("c1", "web_search", `{"query":"latest Go release notes","max_results":3}`))
	case strings.Contains(text, "save the text"):
		calls(call("c1", "write_file", `{"path":"notes.txt","content":"hello world"}`))
	default:
		msg["content"] = "hello"
	}
	out, _ := json.Marshal(map[string]any{"choices": []any{map[string]any{"message": msg}}})
	w.Header().Set("Content-Type", "application/json")
	w.Write(out)
}

func quickOptions(out string) Options {
	return Options{Profile: ProfileQuick, Contexts: Sizes{128}, Concurrency: []int{1, 2}, ReplyTokens: 7, Settle: Duration(10 * time.Millisecond), Out: out}
}

// A quick run against the fake endpoint: the server is recognised, every
// suite produces rows with the server's timings kept, the deliberate tool
// miss is listed, and the three report files are written.
func TestRunAgainstFakeServer(t *testing.T) {
	srv := fakeServer(t, "fake")
	ctx := context.Background()
	pairs, err := Expand(ctx, []Target{{Name: "fake", URL: srv.URL + "/v1"}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	var events []Event
	var mu sync.Mutex
	res, err := Run(ctx, pairs, quickOptions(""), func(e Event) { mu.Lock(); events = append(events, e); mu.Unlock() })
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Rows) != 1 || res.Title != "fake" || res.Machine.OS == "" || res.Schema != SchemaVersion {
		t.Fatalf("results %+v", res)
	}
	row := res.Rows[0]
	if row.Error != "" || row.Label != "fake" || row.Model != "fake" || row.Server != "llama.cpp" || !strings.Contains(row.Timing, "server's own") {
		t.Fatalf("row %+v", row)
	}
	if len(row.Speed) != 2 || row.Speed[0].DecodeTPS.N == 0 || row.Speed[0].WarmTTFT.N == 0 || row.Speed[1].Concurrency != 2 || row.Speed[1].CombinedTPS.N == 0 {
		t.Errorf("speed rows %+v", row.Speed)
	}
	if row.Speed[0].Server["timings.predicted_per_second"] != 123 || row.Speed[0].ServerPrefill.Median != 4567 {
		t.Errorf("server timings not kept: %v", row.Speed[0].Server)
	}
	if row.Tools == nil || row.Tools.Cases != 9 || row.Tools.Passed != 8 || len(row.Tools.Failures) != 1 || !strings.Contains(row.Tools.Failures[0], "location: is a string") {
		t.Errorf("tools %+v", row.Tools)
	}
	if len(row.Context) != 1 || !row.Context[0].Found {
		t.Errorf("context %+v", row.Context)
	}
	suites := map[string]int{}
	for _, e := range events {
		suites[e.Suite]++
		if e.Row != "fake" {
			t.Errorf("event for row %q", e.Row)
		}
	}
	if suites[SuiteSpeed] < 3 || suites[SuiteTools] < 10 || suites[SuiteContext] < 2 || suites[""] < 1 {
		t.Errorf("events per suite %v", suites)
	}
	dir := t.TempDir()
	files, err := WriteFiles(dir, res)
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range files {
		if _, err := os.Stat(f); err != nil {
			t.Error(err)
		}
	}
	back, err := Load(filepath.Join(dir, "results.json"))
	if err != nil || back.Rows[0].Tools.Passed != 8 {
		t.Errorf("round trip: %v %+v", err, back.Rows)
	}
}

// Two targets run one after another, each a column, and the verdict reads
// them; an endpoint that cannot answer is a row with an error, not a crash.
func TestRunTwoTargetsAndADeadOne(t *testing.T) {
	a, b := fakeServer(t, "m"), fakeServer(t, "m")
	ctx := context.Background()
	pairs, err := Expand(ctx, []Target{{Name: "a", URL: a.URL + "/v1"}, {Name: "b", URL: b.URL + "/v1"}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	pairs = append(pairs, Pair{Target: Target{Name: "dead", URL: "http://127.0.0.1:1/v1"}, Model: "m", Label: "dead"})
	o := quickOptions("")
	o.Suites = []string{SuiteSpeed}
	res, err := Run(ctx, pairs, o, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Rows) != 3 || res.Rows[0].Error != "" || res.Rows[1].Error != "" || !strings.HasPrefix(res.Rows[2].Error, "warm-up:") {
		t.Fatalf("rows %+v", res.Rows)
	}
	if res.Title != "m" {
		t.Errorf("title %q", res.Title)
	}
	v := strings.Join(Verdict(res), "\n")
	if !strings.Contains(v, "fastest single stream (128 context)") || !strings.Contains(v, "dead did not run: warm-up:") {
		t.Errorf("verdict:\n%s", v)
	}
	var tb strings.Builder
	Table(&tb, res, false)
	if !strings.Contains(tb.String(), "metric") || !strings.Contains(tb.String(), "dead did not run") {
		t.Errorf("table:\n%s", tb.String())
	}
}

// A cancelled context ends the run between requests, with the rows that
// did not start marked and the context's error returned.
func TestRunCancelled(t *testing.T) {
	srv := fakeServer(t, "m")
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	res, err := Run(ctx, []Pair{{Target: Target{Name: "a", URL: srv.URL + "/v1"}, Model: "m", Label: "a"}}, quickOptions(""), nil)
	if err != context.Canceled || len(res.Rows) != 1 || res.Rows[0].Error != "context canceled" {
		t.Errorf("err %v rows %+v", err, res.Rows)
	}
}

// Models are chosen per target: named ones as given, "all" from the
// endpoint, nothing when the endpoint serves exactly one, and an error
// naming them when it serves several.
func TestResolveModelsAndExpand(t *testing.T) {
	ctx := context.Background()
	one := fakeServer(t, "solo")
	two := fakeServer(t, "b-model", "a-model")
	got, err := ResolveModels(ctx, Target{Name: "x", URL: one.URL + "/v1"}, nil)
	if err != nil || len(got) != 1 || got[0] != "solo" {
		t.Errorf("one model: %v %v", got, err)
	}
	_, err = ResolveModels(ctx, Target{Name: "two", URL: two.URL + "/v1"}, nil)
	if err == nil || !strings.Contains(err.Error(), "two serves 2 models (a-model, b-model)") {
		t.Errorf("several models: %v", err)
	}
	got, err = ResolveModels(ctx, Target{Name: "two", URL: two.URL + "/v1", Models: []string{"all"}}, nil)
	if err != nil || len(got) != 2 || got[0] != "a-model" {
		t.Errorf("all: %v %v", got, err)
	}
	got, err = ResolveModels(ctx, Target{Name: "two", URL: two.URL + "/v1"}, []string{"all"})
	if err != nil || len(got) != 2 {
		t.Errorf("all from the fallback: %v %v", got, err)
	}
	got, err = ResolveModels(ctx, Target{Name: "dead", URL: "http://127.0.0.1:1/v1"}, []string{"named"})
	if err != nil || len(got) != 1 || got[0] != "named" {
		t.Errorf("named needs no listing: %v %v", got, err)
	}
	if _, err := ResolveModels(ctx, Target{Name: "dead", URL: "http://127.0.0.1:1/v1"}, nil); err == nil {
		t.Error("an unreachable endpoint with no model named must fail")
	}
	pairs, err := Expand(ctx, []Target{{Name: "one", URL: one.URL + "/v1"}, {Name: "two", URL: two.URL + "/v1", Models: []string{"all"}}}, nil)
	if err != nil || len(pairs) != 3 || pairs[0].Label != "one" || pairs[1].Label != "two/a-model" || pairs[2].Label != "two/b-model" {
		t.Errorf("pairs %+v %v", pairs, err)
	}
}

// ListModels reads the ids and owners and sorts them.
func TestListModels(t *testing.T) {
	srv := fakeServer(t, "zeta", "alpha")
	ms, err := ListModels(context.Background(), srv.URL+"/v1", "")
	if err != nil || len(ms) != 2 || ms[0].ID != "alpha" || ms[0].OwnedBy != "llamacpp" {
		t.Errorf("%+v %v", ms, err)
	}
	if _, err := ListModels(context.Background(), "http://127.0.0.1:1/v1", ""); err == nil {
		t.Error("dead endpoint listed")
	}
}
