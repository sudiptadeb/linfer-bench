package bench

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// sse is a fake endpoint that streams the given SSE body for every
// completion and answers /models with the given body.
func sse(t *testing.T, models, stream string) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/v1/models", func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, models) })
	mux.HandleFunc("/v1/chat/completions", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, stream)
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

const llamaStream = `data: {"choices":[{"delta":{"content":"Hi"}}]}

data: {"choices":[{"delta":{"content":" there"}}]}

data: {"choices":[],"usage":{"prompt_tokens":12,"completion_tokens":2},"timings":{"prompt_per_second":950.5,"predicted_per_second":41.2,"predicted_n":2}}

data: [DONE]

`

const omlxStream = `data: {"choices":[{"delta":{"reasoning_content":"Let me think about this for a bit before I answer the question."}}]}

data: {"choices":[{"delta":{"content":"Hi"}}]}

data: {"choices":[],"usage":{"prompt_tokens":12,"completion_tokens":20,"prompt_tokens_per_second":1300.0,"generation_tokens_per_second":98.5}}

data: [DONE]

`

const usageOnlyStream = `data: {"choices":[{"delta":{"content":"Hi"}}]}

data: {"choices":[{"delta":{"content":" there"}}]}

data: {"choices":[],"usage":{"prompt_tokens":12,"completion_tokens":2}}

data: [DONE]

`

const bareStream = `data: {"choices":[{"delta":{"content":"Hi"}}]}

data: {"choices":[{"delta":{"content":" there"}}]}

data: [DONE]

`

// Each kind of server is recognised from what it sends: llama.cpp by its
// timings, oMLX by the rates in its usage, Ollama by the owner it lists,
// and anything else as OpenAI-compatible; and the timing source says what
// the client could measure from.
func TestServerKindAndTimingSource(t *testing.T) {
	cases := []struct {
		name, models, stream, kind, timing string
		serverDecode                       float64
	}{
		{"llama.cpp", `{"data":[{"id":"m","owned_by":"llamacpp"}]}`, llamaStream, "llama.cpp", "server's own timings", 41.2},
		{"llama.cpp by timings alone", `{"data":[{"id":"m"}]}`, llamaStream, "llama.cpp", "server's own timings", 41.2},
		{"oMLX", `{"data":[{"id":"m"}]}`, omlxStream, "oMLX", "server's own timings", 98.5},
		{"Ollama", `{"data":[{"id":"m","owned_by":"library"}]}`, usageOnlyStream, "Ollama", "token counts from the server's usage", 0},
		{"plain", `{"data":[{"id":"m","owned_by":"someone"}]}`, usageOnlyStream, "OpenAI-compatible", "token counts from the server's usage", 0},
		{"no usage", `{"data":[{"id":"m"}]}`, bareStream, "OpenAI-compatible", "counted per chunk", 0},
	}
	for _, c := range cases {
		srv := sse(t, c.models, c.stream)
		models, err := ListModels(context.Background(), srv.URL+"/v1", "")
		if err != nil {
			t.Fatal(err)
		}
		st := NewClient(srv.URL+"/v1", "m", "", time.Minute).Complete(context.Background(), []Message{{Role: "user", Content: "hi"}}, 8)
		if st.Err != "" {
			t.Fatalf("%s: %s", c.name, st.Err)
		}
		if got := ServerKind(models, st); got != c.kind {
			t.Errorf("%s: kind %q, want %q", c.name, got, c.kind)
		}
		if got := TimingSource(st); !strings.Contains(got, c.timing) {
			t.Errorf("%s: timing %q, want it to mention %q", c.name, got, c.timing)
		}
		if st.ServerDecodeTPS() != c.serverDecode {
			t.Errorf("%s: server decode %v, want %v", c.name, st.ServerDecodeTPS(), c.serverDecode)
		}
	}
}

// A one-token reply whose token the server's reasoning parser swallowed
// carries no text: the time to first token is then the finish chunk's,
// which follows the token, not zero; a keepalive sent before prefill does
// not count.
func TestTTFTWithoutText(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		fl, _ := w.(http.Flusher)
		fmt.Fprint(w, "data: {\"model\":\"keepalive\",\"choices\":[{\"delta\":{\"role\":\"assistant\",\"content\":\"\"},\"finish_reason\":null}]}\n\n")
		fl.Flush()
		time.Sleep(120 * time.Millisecond) // the prefill
		fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"role\":\"assistant\",\"content\":null},\"finish_reason\":null}]}\n\n")
		fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{},\"finish_reason\":\"length\"}]}\n\n")
		fmt.Fprint(w, "data: {\"choices\":[],\"usage\":{\"prompt_tokens\":2000,\"completion_tokens\":1}}\n\ndata: [DONE]\n\n")
	}))
	defer srv.Close()
	st := NewClient(srv.URL+"/v1", "m", "", time.Minute).Complete(context.Background(), []Message{{Role: "user", Content: "hi"}}, 1)
	if st.Err != "" || st.TTFT < 120*time.Millisecond || st.TTFT > st.Total || st.CompletionTokens != 1 || st.Chunks != 0 {
		t.Errorf("stream %+v", st)
	}
	if st.PrefillTPS() <= 0 || st.PrefillTPS() > 2000/0.12 {
		t.Errorf("prefill %.0f", st.PrefillTPS())
	}
}

// Without usage the completion count falls back to the chunks; with usage
// it is the server's, and the first chunk is counted from its length.
func TestStreamCounts(t *testing.T) {
	srv := sse(t, `{"data":[]}`, omlxStream)
	st := NewClient(srv.URL+"/v1", "m", "", time.Minute).Complete(context.Background(), []Message{{Role: "user", Content: "hi"}}, 8)
	if st.CompletionTokens != 20 || st.Chunks != 2 || st.TokensPerChunk() != 10 || !st.UsageReported || st.FirstChunkChars == 0 {
		t.Errorf("stream %+v", st)
	}
	srv = sse(t, `{"data":[]}`, bareStream)
	st = NewClient(srv.URL+"/v1", "m", "", time.Minute).Complete(context.Background(), []Message{{Role: "user", Content: "hi"}}, 8)
	if st.CompletionTokens != 2 || st.UsageReported {
		t.Errorf("bare stream %+v", st)
	}
}

// A first chunk of 30 tokens' worth of text does not count as one token:
// the decode rate is the tokens after it over the time after it.
func TestDecodeTPSDiscountsTheFirstChunk(t *testing.T) {
	st := Stream{CompletionTokens: 100, FirstChunkChars: 114, TTFT: time.Second, Total: 2 * time.Second, Chunks: 4}
	if got := st.FirstChunkTokens(3.8); got != 30 {
		t.Errorf("first chunk %d tokens, want 30", got)
	}
	if got := st.DecodeTPS(3.8); got != 70 {
		t.Errorf("decode %.1f, want 70 (a naive count would say 99)", got)
	}
	if got := st.TokensPerChunk(); got != 25 {
		t.Errorf("tokens per chunk %.1f", got)
	}
	if got := (Stream{PromptTokens: 2000, TTFT: 2 * time.Second}).PrefillTPS(); got != 1000 {
		t.Errorf("prefill %.0f", got)
	}
}

// The API key and the pinned sampling go on every request; a non-2xx is a
// scored result, not an error.
func TestRequestShapeAndHTTPErrors(t *testing.T) {
	var auth, body string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		auth = r.Header.Get("Authorization")
		raw := make([]byte, 4096)
		n, _ := r.Body.Read(raw)
		body = string(raw[:n])
		http.Error(w, `{"error":"prompt exceeds memory"}`, 400)
	}))
	defer srv.Close()
	temp := 0.7
	cl := NewClient(srv.URL+"/v1", "m", "secret", time.Minute)
	cl.Sampling = &Sampling{Temperature: &temp}
	st := cl.Complete(context.Background(), []Message{{Role: "user", Content: "hi"}}, 8)
	if auth != "Bearer secret" || !strings.Contains(body, `"temperature":0.7`) || !strings.Contains(body, `"stream":true`) {
		t.Errorf("auth %q body %s", auth, body)
	}
	if st.Status != 400 || !strings.HasPrefix(st.Err, "HTTP 400") {
		t.Errorf("stream %+v", st)
	}
	r := cl.Chat(context.Background(), []Message{{Role: "user", Content: "hi"}}, Builtin().Tools, 8)
	if r.Status != 400 || !strings.Contains(r.Err, "prompt exceeds memory") || !strings.Contains(body, `"tools"`) {
		t.Errorf("reply %+v body %s", r, body)
	}
}

// ParseTarget takes name=url, a bare url named after its host, and adds
// /v1 when it is missing.
func TestParseTarget(t *testing.T) {
	got, err := ParseTarget("llama=http://127.0.0.1:8090/v1")
	if err != nil || got.Name != "llama" || got.URL != "http://127.0.0.1:8090/v1" {
		t.Errorf("%+v %v", got, err)
	}
	got, err = ParseTarget("http://127.0.0.1:8090/")
	if err != nil || got.Name != "127.0.0.1:8090" || got.URL != "http://127.0.0.1:8090/v1" {
		t.Errorf("%+v %v", got, err)
	}
	if _, err := ParseTarget("llama=127.0.0.1:8090"); err == nil {
		t.Error("no scheme accepted")
	}
}
