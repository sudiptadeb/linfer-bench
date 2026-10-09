package bench

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"
)

// A server that answers every completion with max_tokens tokens and records
// what it was asked, when.
type recorded struct {
	maxTokens int
	at        time.Time
}

func recordingServer(t *testing.T) (*httptest.Server, func() []recorded) {
	var mu sync.Mutex
	var got []recorded
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			MaxTokens int `json:"max_tokens"`
		}
		json.NewDecoder(r.Body).Decode(&body)
		mu.Lock()
		got = append(got, recorded{body.MaxTokens, time.Now()})
		mu.Unlock()
		w.Header().Set("Content-Type", "text/event-stream")
		for i := 0; i < body.MaxTokens; i++ {
			fmt.Fprintf(w, "data: {\"choices\":[{\"delta\":{\"content\":\"tok \"}}]}\n\n")
		}
		fmt.Fprintf(w, "data: {\"choices\":[],\"usage\":{\"prompt_tokens\":2000,\"completion_tokens\":%d}}\n\ndata: [DONE]\n\n", body.MaxTokens)
	}))
	t.Cleanup(srv.Close)
	return srv, func() []recorded { mu.Lock(); defer mu.Unlock(); return append([]recorded(nil), got...) }
}

// A speed cell is a cold pass of one token per stream, the settle, then a
// warm pass of the reply length; combined counts the warm pass only.
func TestSpeedRunsAColdPassThenAWarmPassAfterTheSettle(t *testing.T) {
	srv, got := recordingServer(t)
	cl := NewClient(srv.URL, "m", "", time.Minute)
	rows := runSpeed(context.Background(), cl, speedPlan{contexts: []int{256}, concurrency: []int{3}, runs: 1,
		replyTokens: 40, charsPerToken: 4, maxContext: 8192, settle: 300 * time.Millisecond}, func(string) {})

	reqs := got()
	if len(reqs) != 6 {
		t.Fatalf("%d requests, want 3 cold and 3 warm", len(reqs))
	}
	for i, r := range reqs {
		want := 1
		if i >= 3 {
			want = 40
		}
		if r.maxTokens != want {
			t.Errorf("request %d asked for %d tokens, want %d", i, r.maxTokens, want)
		}
	}
	if gap := reqs[3].at.Sub(reqs[2].at); gap < 300*time.Millisecond {
		t.Errorf("warm pass began %v after the cold one, before the settle", gap)
	}
	if c := rows[0].CombinedTPS; c.N != 1 || c.Median <= 0 {
		t.Fatalf("combined %+v", c)
	}
	if rows[0].TTFT.N != 3 || rows[0].DecodeTPS.N != 3 || rows[0].ContextTokens != 2000 {
		t.Errorf("ttft %d and decode %d samples, want 3 each; context %d", rows[0].TTFT.N, rows[0].DecodeTPS.N, rows[0].ContextTokens)
	}
}

// A context past the server's is skipped, not sent, in both suites.
func TestCellsPastTheContextAreSkipped(t *testing.T) {
	srv, got := recordingServer(t)
	cl := NewClient(srv.URL, "m", "", time.Minute)
	rows := runSpeed(context.Background(), cl, speedPlan{contexts: []int{32768}, concurrency: []int{1}, runs: 1, replyTokens: 40, charsPerToken: 4, maxContext: 8192, settle: 0}, func(string) {})
	if len(rows) != 1 || !rows[0].Skipped() || len(got()) != 0 {
		t.Errorf("rows %+v, %d requests", rows, len(got()))
	}
	ctxRows := runContext(context.Background(), cl, []int{32768}, []int{50}, 4, 8192, func(string) {})
	if len(ctxRows) != 1 || !ctxRows[0].Skipped() || len(got()) != 0 {
		t.Errorf("context rows %+v", ctxRows)
	}
	// An unknown maximum tries everything.
	rows = runSpeed(context.Background(), cl, speedPlan{contexts: []int{32768}, concurrency: []int{1}, runs: 1, replyTokens: 4, charsPerToken: 4, maxContext: 0, settle: 0}, func(string) {})
	if rows[0].Skipped() || len(got()) != 2 {
		t.Errorf("unknown max: %+v, %d requests", rows, len(got()))
	}
}
