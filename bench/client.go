// Package bench measures OpenAI-compatible chat endpoints and compares them:
// decode and prefill speed from the client's side of a stream, with the
// server's own figures beside them where it reports any; a tool-calling
// suite scored case by case; and needle-in-haystack retrieval. It is the
// library behind the linfer-bench command and behind `linfer bench`, which
// starts each backend itself and hands the library its URL.
//
// The measurements carry a few lessons from local backends:
//
//   - Throughput is measured on a warm pass. A backend that prefills new
//     prompts one at a time makes a cold "combined" figure a measure of its
//     prefill queue, not of its decode.
//   - A stream's chunks are not tokens. Some servers send bursts of tens of
//     tokens per SSE chunk, so decode is counted from the usage the server
//     reports, with the first chunk's share estimated from its length.
//   - A prompt lands in the server's cache a moment after its request ends,
//     so the warm pass waits a settle before it starts.
package bench

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"sort"
	"strings"
	"time"
)

// Client talks OpenAI chat completions to one endpoint.
type Client struct {
	Base  string // http://host:port/v1
	Model string
	// APIKey, when set, goes in the Authorization header.
	APIKey string
	// Sampling is sent with every request when set; nil leaves the
	// server's defaults.
	Sampling *Sampling
	HTTP     *http.Client
}

// Sampling is the request-side sampling a bench can pin, so two servers are
// compared on the same settings rather than on their defaults.
type Sampling struct {
	Temperature *float64 `yaml:"temperature" json:"temperature,omitempty"`
	TopP        *float64 `yaml:"top_p" json:"top_p,omitempty"`
}

// NewClient is a client for one model on one endpoint; timeout bounds each
// request.
func NewClient(base, model, apiKey string, timeout time.Duration) *Client {
	if timeout <= 0 {
		timeout = 30 * time.Minute
	}
	return &Client{Base: strings.TrimRight(base, "/"), Model: model, APIKey: apiKey, HTTP: &http.Client{Timeout: timeout}}
}

// Message is one chat message; ToolCalls and ToolCallID carry the tool
// protocol.
type Message struct {
	Role       string     `json:"role" yaml:"role"`
	Content    string     `json:"content,omitempty" yaml:"content,omitempty"`
	ToolCalls  []ToolCall `json:"tool_calls,omitempty" yaml:"tool_calls,omitempty"`
	ToolCallID string     `json:"tool_call_id,omitempty" yaml:"tool_call_id,omitempty"`
}

// ToolCall is a call the model made, or one replayed to it in the history.
type ToolCall struct {
	ID       string       `json:"id,omitempty"`
	Type     string       `json:"type,omitempty"`
	Function FunctionCall `json:"function"`
}

// FunctionCall is the name and the JSON-encoded arguments of a call.
type FunctionCall struct {
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
}

// Tool is a tool definition as the API takes it.
type Tool struct {
	Type     string       `json:"type"`
	Function ToolFunction `json:"function"`
}

// ToolFunction is the function behind a tool: its name, what it does and
// the JSON Schema of its arguments.
type ToolFunction struct {
	Name        string         `json:"name"`
	Description string         `json:"description,omitempty"`
	Parameters  map[string]any `json:"parameters"`
}

// Stream is what one streamed completion measured. Times are from the
// moment the request was written, so they are comparable across servers;
// Server holds whatever timing the server itself reported (llama-server's
// `timings`, oMLX's extra `usage` fields), keyed as the server named them.
type Stream struct {
	TTFT             time.Duration // first content or reasoning token
	Total            time.Duration
	PromptTokens     int
	CompletionTokens int
	// Chunks is how many deltas carried text, and FirstChunkChars how long
	// the first was. llama-server sends one token per delta; oMLX sends
	// reasoning in bursts of many tokens, so its first chunk is already a
	// good part of the output, and a decode rate that assumed one token
	// there would come out several times too high.
	Chunks          int
	FirstChunkChars int
	// UsageReported says the server sent token counts; without them
	// CompletionTokens is the chunk count, which undercounts a bursty
	// stream.
	UsageReported bool
	Content       string
	Server        map[string]float64
	Status        int
	Err           string
}

// DecodeTPS is the stream's decode rate after the first chunk: the tokens
// that came after it, over the time after it. The first chunk's tokens are
// estimated from its length at charsPerToken.
func (s Stream) DecodeTPS(charsPerToken float64) float64 {
	if s.CompletionTokens < 2 || s.Total <= s.TTFT {
		return 0
	}
	return float64(s.CompletionTokens-s.FirstChunkTokens(charsPerToken)) / (s.Total - s.TTFT).Seconds()
}

// FirstChunkTokens estimates how many tokens the first chunk carried: at
// least one, at most all but one.
func (s Stream) FirstChunkTokens(charsPerToken float64) int {
	first := 1
	if charsPerToken > 0 {
		first = max(1, int(float64(s.FirstChunkChars)/charsPerToken+0.5))
	}
	return max(1, min(first, s.CompletionTokens-1))
}

// TokensPerChunk is the stream's granularity: 1 is token-by-token.
func (s Stream) TokensPerChunk() float64 {
	if s.Chunks == 0 {
		return 0
	}
	return float64(s.CompletionTokens) / float64(s.Chunks)
}

// The server-reported rates each kind of server names. llama-server puts a
// `timings` object on the last chunk; oMLX adds per-second rates to `usage`.
// Ollama's and OpenAI's `usage` carry token counts only, so for those the
// client-side figure is the only one.
var (
	serverDecodeKeys  = []string{"timings.predicted_per_second", "usage.generation_tokens_per_second"}
	serverPrefillKeys = []string{"timings.prompt_per_second", "usage.prompt_tokens_per_second"}
)

// ServerDecodeTPS is the server's own decode rate, zero when it gave none.
func (s Stream) ServerDecodeTPS() float64 { return firstOf(s.Server, serverDecodeKeys...) }

// ServerPrefillTPS is the server's own prompt rate, zero when it gave none.
func (s Stream) ServerPrefillTPS() float64 { return firstOf(s.Server, serverPrefillKeys...) }

func firstOf(m map[string]float64, keys ...string) float64 {
	for _, k := range keys {
		if v, ok := m[k]; ok && v > 0 {
			return v
		}
	}
	return 0
}

// PrefillTPS is the prompt rate implied by the time to first token: the
// first decode step is in there too, which costs the prefill a few percent
// on a long prompt and nothing on a short one.
func (s Stream) PrefillTPS() float64 {
	if s.PromptTokens == 0 || s.TTFT <= 0 {
		return 0
	}
	return float64(s.PromptTokens) / s.TTFT.Seconds()
}

func (c *Client) body(msgs []Message, maxTokens int, stream bool, tools []Tool) []byte {
	body := map[string]any{"model": c.Model, "messages": msgs, "max_tokens": maxTokens}
	if stream {
		body["stream"] = true
		body["stream_options"] = map[string]any{"include_usage": true}
	}
	if len(tools) > 0 {
		body["tools"] = tools
	}
	if c.Sampling != nil {
		if c.Sampling.Temperature != nil {
			body["temperature"] = *c.Sampling.Temperature
		}
		if c.Sampling.TopP != nil {
			body["top_p"] = *c.Sampling.TopP
		}
	}
	raw, _ := json.Marshal(body)
	return raw
}

func (c *Client) post(ctx context.Context, path string, raw []byte) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, "POST", c.Base+path, bytes.NewReader(raw))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	if c.APIKey != "" {
		req.Header.Set("Authorization", "Bearer "+c.APIKey)
	}
	return c.HTTP.Do(req)
}

// Complete runs one streamed completion and measures it. A non-2xx is
// returned in the Stream, not as an error: a 400 is a result to score.
func (c *Client) Complete(ctx context.Context, msgs []Message, maxTokens int) Stream {
	start := time.Now()
	resp, err := c.post(ctx, "/chat/completions", c.body(msgs, maxTokens, true, nil))
	if err != nil {
		return Stream{Err: err.Error(), Total: time.Since(start)}
	}
	defer resp.Body.Close()
	st := Stream{Status: resp.StatusCode, Server: map[string]float64{}}
	if resp.StatusCode/100 != 2 {
		msg, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		st.Err = fmt.Sprintf("HTTP %d: %s", resp.StatusCode, firstLine(string(msg)))
		st.Total = time.Since(start)
		return st
	}
	var content strings.Builder
	deltas := 0
	// finished is when the first choice reported a finish reason: the end
	// of the reply. When no text ever arrived (a one-token reply whose one
	// token was a reasoning tag the server's parser swallowed), it stands
	// in for the first token, which it follows by one decode step.
	var finished time.Duration
	sc := bufio.NewScanner(resp.Body)
	sc.Buffer(make([]byte, 1<<20), 1<<24)
	for sc.Scan() {
		line := sc.Text()
		if !strings.HasPrefix(line, "data:") {
			continue
		}
		data := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
		if data == "[DONE]" {
			break
		}
		var chunk struct {
			Choices []struct {
				Delta struct {
					Content          *string `json:"content"`
					ReasoningContent *string `json:"reasoning_content"`
					Reasoning        *string `json:"reasoning"`
				} `json:"delta"`
				FinishReason *string `json:"finish_reason"`
			} `json:"choices"`
			Usage   map[string]any `json:"usage"`
			Timings map[string]any `json:"timings"`
		}
		if err := json.Unmarshal([]byte(data), &chunk); err != nil {
			continue
		}
		for _, ch := range chunk.Choices {
			d := ch.Delta
			text := ""
			if d.Content != nil {
				text = *d.Content
				content.WriteString(text)
			} else if d.ReasoningContent != nil {
				text = *d.ReasoningContent
			} else if d.Reasoning != nil {
				text = *d.Reasoning
			}
			if text != "" {
				if st.TTFT == 0 {
					st.TTFT = time.Since(start)
					st.FirstChunkChars = len(text)
				}
				deltas++
			}
			if ch.FinishReason != nil && *ch.FinishReason != "" && finished == 0 {
				finished = time.Since(start)
			}
		}
		if chunk.Usage != nil {
			st.UsageReported = true
			st.PromptTokens = intOf(chunk.Usage["prompt_tokens"])
			st.CompletionTokens = intOf(chunk.Usage["completion_tokens"])
			for k, v := range chunk.Usage {
				if f, ok := v.(float64); ok && !strings.HasSuffix(k, "_tokens") {
					st.Server["usage."+k] = f
				}
			}
		}
		for k, v := range chunk.Timings {
			if f, ok := v.(float64); ok {
				st.Server["timings."+k] = f
			}
		}
	}
	st.Total = time.Since(start)
	if err := sc.Err(); err != nil {
		st.Err = err.Error()
	}
	if st.TTFT == 0 {
		st.TTFT = finished
		if st.TTFT == 0 {
			st.TTFT = st.Total
		}
	}
	st.Chunks = deltas
	if st.CompletionTokens == 0 {
		// No usage from this server: the deltas are the best count there is.
		st.CompletionTokens = deltas
	}
	st.Content = content.String()
	return st
}

// Reply is a non-streamed completion, for the tool suite, where the content
// and the calls matter and the time does not.
type Reply struct {
	Content   string
	Reasoning string
	ToolCalls []ToolCall
	Status    int
	Err       string
	Raw       string
}

// Chat runs one completion with tools and returns what came back.
func (c *Client) Chat(ctx context.Context, msgs []Message, tools []Tool, maxTokens int) Reply {
	resp, err := c.post(ctx, "/chat/completions", c.body(msgs, maxTokens, false, tools))
	if err != nil {
		return Reply{Err: err.Error()}
	}
	defer resp.Body.Close()
	out, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<22))
	r := Reply{Status: resp.StatusCode, Raw: string(out)}
	if resp.StatusCode/100 != 2 {
		r.Err = fmt.Sprintf("HTTP %d: %s", resp.StatusCode, firstLine(string(out)))
		return r
	}
	return parseReply(out, r)
}

// parseReply reads a chat completion body into a Reply.
func parseReply(out []byte, r Reply) Reply {
	var body struct {
		Choices []struct {
			Message struct {
				Content          *string    `json:"content"`
				ReasoningContent string     `json:"reasoning_content"`
				ToolCalls        []ToolCall `json:"tool_calls"`
			} `json:"message"`
		} `json:"choices"`
	}
	if err := json.Unmarshal(out, &body); err != nil {
		r.Err = "unparseable response: " + err.Error()
		return r
	}
	if len(body.Choices) == 0 {
		r.Err = "no choices in response"
		return r
	}
	m := body.Choices[0].Message
	if m.Content != nil {
		r.Content = *m.Content
	}
	r.Reasoning = m.ReasoningContent
	r.ToolCalls = m.ToolCalls
	return r
}

// --- the endpoint's models and kind ---------------------------------------------------

// ModelInfo is one entry of /v1/models.
type ModelInfo struct {
	ID      string `json:"id"`
	OwnedBy string `json:"owned_by,omitempty"`
}

// ListModels asks the endpoint what it serves. apiKeyEnv names the
// environment variable holding a key, or is empty.
func ListModels(ctx context.Context, base, apiKeyEnv string) ([]ModelInfo, error) {
	req, err := http.NewRequestWithContext(ctx, "GET", strings.TrimRight(base, "/")+"/models", nil)
	if err != nil {
		return nil, err
	}
	if key := keyFromEnv(apiKeyEnv); key != "" {
		req.Header.Set("Authorization", "Bearer "+key)
	}
	resp, err := (&http.Client{Timeout: 15 * time.Second}).Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<22))
	if resp.StatusCode/100 != 2 {
		return nil, fmt.Errorf("GET %s/models: HTTP %d: %s", base, resp.StatusCode, firstLine(string(raw)))
	}
	return parseModels(raw)
}

func parseModels(raw []byte) ([]ModelInfo, error) {
	var body struct {
		Data []ModelInfo `json:"data"`
	}
	if err := json.Unmarshal(raw, &body); err != nil {
		return nil, fmt.Errorf("unparseable /models response: %w", err)
	}
	sort.Slice(body.Data, func(i, j int) bool { return body.Data[i].ID < body.Data[j].ID })
	return body.Data, nil
}

func keyFromEnv(name string) string {
	if name == "" {
		return ""
	}
	return os.Getenv(name)
}

// ServerKind names the software behind an endpoint, from what /v1/models
// says about its owner and what a stream carried. "OpenAI-compatible" is the
// answer when nothing gives it away.
func ServerKind(models []ModelInfo, first Stream) string {
	for _, m := range models {
		switch strings.ToLower(m.OwnedBy) {
		case "llamacpp", "llama.cpp":
			return "llama.cpp"
		case "library", "ollama":
			return "Ollama"
		case "vllm":
			return "vLLM"
		case "omlx":
			return "oMLX"
		}
	}
	if _, ok := first.Server["timings.predicted_per_second"]; ok {
		return "llama.cpp"
	}
	if _, ok := first.Server["usage.generation_tokens_per_second"]; ok {
		return "oMLX"
	}
	return "OpenAI-compatible"
}

// TimingSource says where a row's speed figures can come from, so the
// report states it rather than letting the reader guess: the server's own
// timings beside the client's, or the client's alone, and whether the token
// counts behind them were the server's or counted from the chunks.
func TimingSource(first Stream) string {
	switch {
	case first.ServerDecodeTPS() > 0:
		return "client-side from the stream, with the server's own timings beside it"
	case first.UsageReported:
		return "client-side from the stream; token counts from the server's usage"
	default:
		return "client-side from the stream; tokens counted per chunk, calibrated on the prose"
	}
}

func intOf(v any) int {
	f, _ := v.(float64)
	return int(f)
}

func firstLine(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = s[:i]
	}
	if len(s) > 200 {
		s = s[:200] + "…"
	}
	return s
}

func ifs(cond bool, a, b string) string {
	if cond {
		return a
	}
	return b
}
