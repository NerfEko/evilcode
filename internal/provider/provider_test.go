package provider

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"slices"
	"sort"
	"strings"
	"sync/atomic"
	"testing"
)

// drain collects a stream into text, reasoning, calls, and the first error.
func drain(ch <-chan Chunk) (text, reasoning string, calls []ToolCall, usage *Usage, err error) {
	for c := range ch {
		if c.Err != nil && err == nil {
			err = c.Err
		}
		text += c.Text
		reasoning += c.Reasoning
		calls = append(calls, c.ToolCalls...)
		if c.Usage != nil {
			usage = c.Usage
		}
	}
	return
}

func collectOllama(t *testing.T, body string) (string, string, []ToolCall, *Usage, error) {
	t.Helper()
	ch := make(chan Chunk)
	go func() {
		defer close(ch)
		var seq atomic.Int64
		streamOllamaNDJSON(context.Background(), strings.NewReader(body), ch, &seq)
	}()
	return drain(ch)
}

func collectOpenAI(t *testing.T, body string) (string, string, []ToolCall, *Usage, error) {
	t.Helper()
	ch := make(chan Chunk)
	go func() {
		defer close(ch)
		var seq atomic.Int64
		streamOpenAISSE(context.Background(), strings.NewReader(body), ch, &seq)
	}()
	return drain(ch)
}

func TestOllamaStreamText(t *testing.T) {
	body := `{"message":{"role":"assistant","content":"Hello"},"done":false}
{"message":{"role":"assistant","content":", world"},"done":false}
{"message":{"role":"assistant","content":""},"done":true,"prompt_eval_count":42,"eval_count":7}
`
	text, _, calls, usage, err := collectOllama(t, body)
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	if text != "Hello, world" {
		t.Errorf("text = %q", text)
	}
	if len(calls) != 0 {
		t.Errorf("calls = %v, want none", calls)
	}
	if usage == nil || usage.PromptTokens != 42 || usage.CompletionTokens != 7 {
		t.Errorf("usage = %+v, want 42/7", usage)
	}
}

func TestJSONIntRejectsFractionsAndOverflow(t *testing.T) {
	for _, value := range []any{
		1.5,
		json.Number("9223372036854775808"),
		json.Number("not-a-number"),
	} {
		if got, ok := jsonInt(value); ok {
			t.Errorf("jsonInt(%v) = %d, true; want rejection", value, got)
		}
	}
	if got, ok := jsonInt(json.Number("42000000000")); !ok || got != 42000000000 {
		t.Errorf("jsonInt(valid count) = %d, %v", got, ok)
	}
}

func TestOllamaStreamThinking(t *testing.T) {
	body := `{"message":{"role":"assistant","thinking":"let me "},"done":false}
{"message":{"role":"assistant","thinking":"check"},"done":false}
{"message":{"role":"assistant","content":"Yes."},"done":true}
`
	text, reasoning, _, _, err := collectOllama(t, body)
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	if reasoning != "let me check" {
		t.Errorf("reasoning = %q", reasoning)
	}
	if text != "Yes." {
		t.Errorf("text = %q", text)
	}
}

func TestOllamaToolCallArrivesWhole(t *testing.T) {
	// Some models emit the entire tool call in one message with no preceding
	// text deltas (plan.md Part V).
	body := `{"message":{"role":"assistant","content":"","tool_calls":[{"function":{"name":"read","arguments":{"path":"main.go"}}}]},"done":false}
{"message":{"role":"assistant","content":""},"done":true}
`
	text, _, calls, _, err := collectOllama(t, body)
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	if text != "" {
		t.Errorf("text = %q, want empty", text)
	}
	if len(calls) != 1 {
		t.Fatalf("calls = %d, want 1", len(calls))
	}
	if calls[0].Name != "read" {
		t.Errorf("name = %q", calls[0].Name)
	}
	if calls[0].ID == "" {
		t.Error("Ollama supplies no call ID; the client must synthesize one")
	}
	var args struct {
		Path string `json:"path"`
	}
	if err := json.Unmarshal(calls[0].Args, &args); err != nil || args.Path != "main.go" {
		t.Errorf("args = %s (%v)", calls[0].Args, err)
	}
}

func TestOllamaSynthesizedIDsAreUnique(t *testing.T) {
	body := `{"message":{"tool_calls":[{"function":{"name":"a","arguments":{}}},{"function":{"name":"b","arguments":{}}}]},"done":false}
{"message":{"tool_calls":[{"function":{"name":"c","arguments":{}}}]},"done":true}
`
	_, _, calls, _, err := collectOllama(t, body)
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	if len(calls) != 3 {
		t.Fatalf("calls = %d, want 3", len(calls))
	}
	seen := map[string]bool{}
	for _, c := range calls {
		if seen[c.ID] {
			t.Fatalf("duplicate call ID %q — results would pair to the wrong call", c.ID)
		}
		seen[c.ID] = true
	}
}

func TestOllamaToolCallIDsAreUniqueAcrossTurns(t *testing.T) {
	// Regression for H5.7: the synthesized ID counter must not reset on every
	// ChatStream call, or a resumed multi-turn session ends up with the same
	// tool_call_id in two different turns — harmless to Ollama, but it breaks
	// correlation if the session is later resumed against an OpenAI-kind
	// provider that relies on tool_call_id uniqueness.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"message":{"tool_calls":[{"function":{"name":"read","arguments":{}}}]},"done":true}` + "\n"))
	}))
	defer srv.Close()

	o := NewOllama("ollama", srv.URL, "")
	req := Req{Model: "m", Messages: []Message{{Role: RoleUser, Content: "hi"}}}

	seen := map[string]bool{}
	for turn := 0; turn < 2; turn++ {
		ch, err := o.ChatStream(context.Background(), req)
		if err != nil {
			t.Fatalf("turn %d: %v", turn, err)
		}
		_, _, calls, _, err := drain(ch)
		if err != nil {
			t.Fatalf("turn %d: %v", turn, err)
		}
		if len(calls) != 1 {
			t.Fatalf("turn %d: calls = %d, want 1", turn, len(calls))
		}
		if seen[calls[0].ID] {
			t.Fatalf("turn %d: tool_call_id %q reused from an earlier turn", turn, calls[0].ID)
		}
		seen[calls[0].ID] = true
	}
}

func TestOllamaStreamError(t *testing.T) {
	body := `{"error":"model 'nope' not found"}` + "\n"
	_, _, _, _, err := collectOllama(t, body)
	if err == nil || !strings.Contains(err.Error(), "not found") {
		t.Fatalf("err = %v, want the model-not-found message", err)
	}
}

func TestOllamaStopsAtDone(t *testing.T) {
	// Anything after done:true is not ours to read — a shared connection could
	// carry the next response.
	body := `{"message":{"content":"a"},"done":true}
{"message":{"content":"LEAKED"},"done":false}
`
	text, _, _, _, err := collectOllama(t, body)
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	if strings.Contains(text, "LEAKED") {
		t.Errorf("text = %q, want the stream to stop at done", text)
	}
}

func TestOpenAIStreamText(t *testing.T) {
	body := `data: {"choices":[{"delta":{"content":"Hello"}}]}

data: {"choices":[{"delta":{"content":", world"}}]}

data: {"usage":{"prompt_tokens":42,"completion_tokens":7},"choices":[]}

data: [DONE]

`
	text, _, _, usage, err := collectOpenAI(t, body)
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	if text != "Hello, world" {
		t.Errorf("text = %q", text)
	}
	if usage == nil || usage.PromptTokens != 42 || usage.CompletionTokens != 7 {
		t.Errorf("usage = %+v, want 42/7", usage)
	}
}

func TestOpenAIToolCallFragmentsAccumulate(t *testing.T) {
	// The defining OpenAI behavior: arguments arrive as string fragments that
	// only parse once concatenated.
	body := `data: {"choices":[{"delta":{"tool_calls":[{"index":0,"id":"call_abc","function":{"name":"read","arguments":"{\"pa"}}]}}]}

data: {"choices":[{"delta":{"tool_calls":[{"index":0,"function":{"arguments":"th\":\"ma"}}]}}]}

data: {"choices":[{"delta":{"tool_calls":[{"index":0,"function":{"arguments":"in.go\"}"}}]}}]}

data: [DONE]

`
	_, _, calls, _, err := collectOpenAI(t, body)
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	if len(calls) != 1 {
		t.Fatalf("calls = %d, want 1", len(calls))
	}
	if calls[0].ID != "call_abc" || calls[0].Name != "read" {
		t.Errorf("call = %+v", calls[0])
	}
	var args struct {
		Path string `json:"path"`
	}
	if err := json.Unmarshal(calls[0].Args, &args); err != nil {
		t.Fatalf("accumulated args do not parse: %s (%v)", calls[0].Args, err)
	}
	if args.Path != "main.go" {
		t.Errorf("path = %q, want main.go", args.Path)
	}
}

func TestOpenAIParallelToolCallsKeyByIndex(t *testing.T) {
	body := `data: {"choices":[{"delta":{"tool_calls":[{"index":0,"id":"a","function":{"name":"read","arguments":"{}"}},{"index":1,"id":"b","function":{"name":"grep","arguments":"{"}}]}}]}

data: {"choices":[{"delta":{"tool_calls":[{"index":1,"function":{"arguments":"}"}}]}}]}

data: [DONE]

`
	_, _, calls, _, err := collectOpenAI(t, body)
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	if len(calls) != 2 {
		t.Fatalf("calls = %d, want 2", len(calls))
	}
	if calls[0].Name != "read" || calls[1].Name != "grep" {
		t.Errorf("calls = %+v, want read then grep in index order", calls)
	}
	if string(calls[1].Args) != "{}" {
		t.Errorf("second call args = %s, want {}", calls[1].Args)
	}
}

func TestOpenAIMissingIDsAreSynthesized(t *testing.T) {
	body := `data: {"choices":[{"delta":{"tool_calls":[{"index":0,"function":{"name":"read","arguments":"{}"}}]}}]}

data: [DONE]

`
	_, _, calls, _, err := collectOpenAI(t, body)
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	if len(calls) != 1 || calls[0].ID == "" {
		t.Fatalf("calls = %+v, want one call with a synthesized ID", calls)
	}
}

func TestOpenAIReasoningFields(t *testing.T) {
	for _, field := range []string{"reasoning_content", "reasoning"} {
		body := `data: {"choices":[{"delta":{"` + field + `":"hmm"}}]}

data: [DONE]

`
		_, reasoning, _, _, err := collectOpenAI(t, body)
		if err != nil {
			t.Fatalf("%s: err = %v", field, err)
		}
		if reasoning != "hmm" {
			t.Errorf("%s: reasoning = %q, want hmm", field, reasoning)
		}
	}
}

func TestOpenAIIgnoresCommentsAndBlanks(t *testing.T) {
	body := `: keep-alive

data: {"choices":[{"delta":{"content":"x"}}]}

data: [DONE]

`
	text, _, _, _, err := collectOpenAI(t, body)
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	if text != "x" {
		t.Errorf("text = %q", text)
	}
}

func TestOpenAIStreamError(t *testing.T) {
	body := `data: {"error":{"message":"context length exceeded"}}

`
	_, _, _, _, err := collectOpenAI(t, body)
	if err == nil || !strings.Contains(err.Error(), "context length") {
		t.Fatalf("err = %v", err)
	}
}

func TestOllamaChatStreamAgainstServer(t *testing.T) {
	var gotPath, gotAuth string
	var gotBody ollamaChatReq
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath, gotAuth = r.URL.Path, r.Header.Get("Authorization")
		json.NewDecoder(r.Body).Decode(&gotBody)
		w.Write([]byte(`{"message":{"content":"hi"},"done":true,"prompt_eval_count":5,"eval_count":1}` + "\n"))
	}))
	defer srv.Close()

	o := NewOllama("ollama-cloud", srv.URL, "secret-key")
	ch, err := o.ChatStream(context.Background(), Req{
		Model:    "qwen3-coder:480b-cloud",
		Messages: []Message{{Role: RoleUser, Content: "hi"}},
		Tools:    []ToolDef{{Name: "read", Desc: "read a file", Schema: json.RawMessage(`{"type":"object"}`)}},
		NumCtx:   32768,
	})
	if err != nil {
		t.Fatal(err)
	}
	text, _, _, _, err := drain(ch)
	if err != nil {
		t.Fatal(err)
	}

	if gotPath != "/api/chat" {
		t.Errorf("path = %q", gotPath)
	}
	if gotAuth != "Bearer secret-key" {
		t.Errorf("auth = %q — cloud auth is a bearer token", gotAuth)
	}
	if gotBody.Model != "qwen3-coder:480b-cloud" || !gotBody.Stream {
		t.Errorf("body = %+v", gotBody)
	}
	if gotBody.Options["num_ctx"] != float64(32768) {
		t.Errorf("num_ctx = %v, want 32768", gotBody.Options["num_ctx"])
	}
	if len(gotBody.Tools) != 1 || gotBody.Tools[0].Function.Name != "read" {
		t.Errorf("tools = %+v", gotBody.Tools)
	}
	if text != "hi" {
		t.Errorf("text = %q", text)
	}
}

func TestOllamaReasoningEffortWire(t *testing.T) {
	var bodies []map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Errorf("decode request: %v", err)
		}
		bodies = append(bodies, body)
		w.Write([]byte(`{"message":{"content":"ok"},"done":true}` + "\n"))
	}))
	defer srv.Close()

	o := NewOllama("ollama", srv.URL, "")
	for _, req := range []Req{
		{Model: "qwen3", ReasoningEffort: ReasoningEffortNone},
		{Model: "gpt-oss:20b", ReasoningEffort: ReasoningEffortHigh},
		{Model: "qwen3", ReasoningEffort: ReasoningEffortMax},
	} {
		ch, err := o.ChatStream(context.Background(), req)
		if err != nil {
			t.Fatal(err)
		}
		if _, _, _, _, err := drain(ch); err != nil {
			t.Fatal(err)
		}
	}
	if got := bodies[0]["think"]; got != false {
		t.Errorf("Ollama disabled thinking = %#v, want false", got)
	}
	if got := bodies[1]["think"]; got != "high" {
		t.Errorf("Ollama gpt-oss thinking = %#v, want high", got)
	}
	if got := bodies[2]["think"]; got != "max" {
		t.Errorf("Ollama max thinking = %#v, want max", got)
	}
}

func TestOllamaOmitsAuthWhenNoKey(t *testing.T) {
	var gotAuth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		w.Write([]byte(`{"done":true}` + "\n"))
	}))
	defer srv.Close()

	ch, err := NewOllama("ollama-local", srv.URL, "").ChatStream(context.Background(), Req{Model: "m"})
	if err != nil {
		t.Fatal(err)
	}
	drain(ch)
	if gotAuth != "" {
		t.Errorf("auth = %q, want none for a local daemon", gotAuth)
	}
}

func TestHTTPErrorClassification(t *testing.T) {
	tests := []struct {
		status    int
		retryable bool
	}{
		{http.StatusTooManyRequests, true},
		{http.StatusInternalServerError, true},
		{http.StatusBadGateway, true},
		{http.StatusUnauthorized, false},
		{http.StatusNotFound, false},
		{http.StatusBadRequest, false},
	}
	for _, tt := range tests {
		e := &HTTPError{Status: tt.status}
		if got := e.Retryable(); got != tt.retryable {
			t.Errorf("status %d: Retryable() = %v, want %v", tt.status, got, tt.retryable)
		}
	}
}

func TestHTTPErrorUnwrapsProviderMessage(t *testing.T) {
	tests := []struct {
		name, body, want string
	}{
		{"string error", `{"error":"model not found"}`, "model not found"},
		{"object error", `{"error":{"message":"bad key"}}`, "bad key"},
		{"plain text", `something broke`, "something broke"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := httpError(400, strings.NewReader(tt.body))
			if !strings.Contains(err.Error(), tt.want) {
				t.Errorf("err = %q, want it to contain %q", err, tt.want)
			}
		})
	}
}

func TestOllamaChatStreamSurfacesHTTPError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		w.Write([]byte(`{"error":"invalid api key"}`))
	}))
	defer srv.Close()

	_, err := NewOllama("c", srv.URL, "bad").ChatStream(context.Background(), Req{Model: "m"})
	var he *HTTPError
	if err == nil {
		t.Fatal("want an error")
	}
	if !strings.Contains(err.Error(), "invalid api key") {
		t.Errorf("err = %v", err)
	}
	if he, _ = err.(*HTTPError); he == nil || he.Retryable() {
		t.Errorf("a 401 must be surfaced as non-retryable, got %v", err)
	}
}

func TestOllamaReasoningEffortFallbackRecognizesGLM(t *testing.T) {
	o := NewOllama("ollama-local", "", "")
	got := o.reasoningEffortLevelsForModel("glm-5.2:cloud")
	want := OllamaReasoningEfforts()
	if !slices.Equal(got, want) {
		t.Fatalf("GLM reasoning levels = %v, want %v", got, want)
	}
}

func TestOllamaShowMetadataWinsForReasoningLevels(t *testing.T) {
	// When the API advertises per-model levels, they must be used verbatim
	// instead of the generic capability vocabulary — this is the automatic
	// discovery path, with no model names in code.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"capabilities":["completion","thinking"],` +
			`"model_info":{"glm5_next.reasoning_efforts":["low","high","max"],` +
			`"glm5_next.context_length":131072}}`))
	}))
	defer srv.Close()

	o := NewOllama("ollama-cloud", srv.URL, "key")
	info, err := o.Show(context.Background(), "glm-5.3-flash:cloud")
	if err != nil {
		t.Fatal(err)
	}
	want := []ReasoningEffort{ReasoningEffortLow, ReasoningEffortHigh, ReasoningEffortMax}
	if !slices.Equal(info.ReasoningEfforts, want) {
		t.Fatalf("metadata levels = %v, want %v", info.ReasoningEfforts, want)
	}
	if got := ReasoningEffortLevelsForProvider(o, "glm-5.3-flash:cloud"); !slices.Equal(got, want) {
		t.Errorf("resolution levels = %v, want %v", got, want)
	}
}

func TestOllamaShowWithoutMetadataFallsBackToCapability(t *testing.T) {
	// Today's Ollama reports only the boolean capability; the generic
	// vocabulary is the best the API can express, and must not change.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"capabilities":["completion","thinking"],` +
			`"model_info":{"glm5_next.context_length":131072}}`))
	}))
	defer srv.Close()

	o := NewOllama("ollama-cloud", srv.URL, "key")
	info, err := o.Show(context.Background(), "glm-5.3-flash:cloud")
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(info.ReasoningEfforts, OllamaReasoningEfforts()) {
		t.Fatalf("capability levels = %v, want %v", info.ReasoningEfforts, OllamaReasoningEfforts())
	}
}

func TestOllamaEmbed(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/embed" {
			t.Errorf("path = %q", r.URL.Path)
		}
		w.Write([]byte(`{"embeddings":[[0.1,0.2],[0.3,0.4]]}`))
	}))
	defer srv.Close()

	vecs, err := NewOllama("local", srv.URL, "").Embed(context.Background(), []string{"a", "b"})
	if err != nil {
		t.Fatal(err)
	}
	if len(vecs) != 2 || len(vecs[0]) != 2 {
		t.Fatalf("vecs = %v", vecs)
	}
}

func TestOllamaModels(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"models":[{"name":"qwen3-coder:480b-cloud","details":{"parameter_size":"480B"}}]}`))
	}))
	defer srv.Close()

	models, err := NewOllama("c", srv.URL, "").Models(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(models) != 1 || models[0].Name != "qwen3-coder:480b-cloud" || models[0].Size != "480B" {
		t.Errorf("models = %+v", models)
	}
}

func TestOllamaModelsEnrichesFromShow(t *testing.T) {
	// /api/tags is a catalogue listing and carries no context window at all —
	// Ollama Cloud does not even fill parameter_size. Without the per-model
	// /api/show behind it, every cloud model needs a hand-written [[model]]
	// context_window block or the meter falls back to a guess.
	var shows int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/tags":
			w.Write([]byte(`{"models":[{"name":"glm-5.2"},{"name":"gemma4:31b"}]}`))
		case "/api/show":
			atomic.AddInt32(&shows, 1)
			var body struct {
				Model string `json:"model"`
			}
			json.NewDecoder(r.Body).Decode(&body)
			if body.Model == "gemma4:31b" {
				w.Write([]byte(`{"capabilities":["completion","vision"],
					"details":{"parameter_size":"0"},
					"model_info":{"gemma4.context_length":262144,"general.parameter_count":33000000000}}`))
				return
			}
			w.Write([]byte(`{"capabilities":["thinking","tools"],
				"details":{"parameter_size":"756162687872"},
				"model_info":{"glm5.2.context_length":1000000,"general.parameter_count":756000000000}}`))
		default:
			t.Errorf("unexpected path %q", r.URL.Path)
		}
	}))
	defer srv.Close()

	o := NewOllama("cloud", srv.URL, "key")
	models, err := o.Models(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	byName := map[string]ModelInfo{}
	for _, m := range models {
		byName[m.Name] = m
	}
	if got := byName["glm-5.2"]; got.ContextWindow != 1000000 || got.Vision || got.Size != "756B" {
		t.Errorf("glm-5.2 = %+v, want ctx 1000000, no vision, size 756B", got)
	}
	if got := byName["glm-5.2"].ReasoningEfforts; !slices.Equal(got, OllamaReasoningEfforts()) {
		t.Errorf("glm-5.2 reasoning efforts = %v, want %v", got, OllamaReasoningEfforts())
	}
	if got := byName["gemma4:31b"]; got.ContextWindow != 262144 || !got.Vision || got.Size != "33B" {
		t.Errorf("gemma4:31b = %+v, want ctx 262144, vision, size 33B", got)
	}

	// Memoized: opening the picker a second time must not re-fetch the whole
	// catalogue one request per model.
	before := atomic.LoadInt32(&shows)
	if _, err := o.Models(context.Background()); err != nil {
		t.Fatal(err)
	}
	if after := atomic.LoadInt32(&shows); after != before {
		t.Errorf("/api/show called %d more times on the second listing, want 0", after-before)
	}
}

func TestOllamaModelsSurviveAShowThatFails(t *testing.T) {
	// A catalogue missing a context window beats no catalogue: the picker must
	// still list a model whose detail lookup errored, including capabilities
	// reported directly by current /api/tags responses.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/tags" {
			w.Write([]byte(`{"models":[{"name":"mystery-model","details":{"parameter_size":"8B"},"capabilities":["completion","thinking"]}]}`))
			return
		}
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	models, err := NewOllama("c", srv.URL, "").Models(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(models) != 1 || models[0].Name != "mystery-model" || models[0].Size != "8B" {
		t.Errorf("models = %+v, want the listing kept", models)
	}
	if got := models[0].ReasoningEfforts; !slices.Equal(got, OllamaReasoningEfforts()) {
		t.Errorf("thinking capabilities = %v, want %v", got, OllamaReasoningEfforts())
	}
}

func TestOllamaDefaultsToLocalhost(t *testing.T) {
	if got := NewOllama("local", "", "").BaseURL; got != "http://localhost:11434" {
		t.Errorf("BaseURL = %q", got)
	}
}

func TestStreamRespectsCancellation(t *testing.T) {
	// A long stream must stop promptly when the context is cancelled, or an
	// interrupt leaves the UI lying about what the model is doing.
	var body strings.Builder
	for i := 0; i < 1000; i++ {
		body.WriteString(`{"message":{"content":"x"},"done":false}` + "\n")
	}
	ctx, cancel := context.WithCancel(context.Background())
	ch := make(chan Chunk)
	go func() {
		defer close(ch)
		var seq atomic.Int64
		streamOllamaNDJSON(ctx, strings.NewReader(body.String()), ch, &seq)
	}()

	<-ch // take one chunk, then walk away
	cancel()
	for range ch {
		// Drain until the producer notices; the test hangs (and fails by
		// timeout) if cancellation is not honored.
	}
}

func TestMockScenariosAllStream(t *testing.T) {
	names := MockScenarios()
	sort.Strings(names)
	if len(names) == 0 {
		t.Fatal("no scenarios registered")
	}
	for _, name := range names {
		t.Run(name, func(t *testing.T) {
			m := NewMock("mock", name)
			ch, err := m.ChatStream(context.Background(), Req{Model: "mock-large"})
			if err != nil {
				t.Fatal(err)
			}
			text, _, calls, _, streamErr := drain(ch)
			if name == "error" {
				if streamErr == nil {
					t.Error("the error scenario must yield an error")
				}
				return
			}
			if streamErr != nil {
				t.Fatalf("unexpected error: %v", streamErr)
			}
			if text == "" && len(calls) == 0 {
				t.Error("scenario produced neither text nor tool calls")
			}
		})
	}
}

func TestMockPlanFenceIsChunkedMidMarker(t *testing.T) {
	// The plan card has to grow while streaming, which is only exercised when a
	// chunk boundary falls inside a fence marker (plan.md §12.1).
	m := NewMock("mock", "plan")
	ch, _ := m.ChatStream(context.Background(), Req{})
	var chunks []string
	var full string
	for c := range ch {
		if c.Text != "" {
			chunks = append(chunks, c.Text)
			full += c.Text
		}
	}
	if !strings.Contains(full, "```plan") {
		t.Fatalf("assembled text has no plan fence:\n%s", full)
	}
	split := false
	for _, c := range chunks {
		if strings.HasSuffix(c, "``") {
			split = true
		}
	}
	if !split {
		t.Error("no chunk ends mid-fence; the streaming plan card would never be exercised")
	}
	if !strings.Contains(full, "```bash") {
		t.Error("plan body should contain a nested fence, which must not terminate the card")
	}
}

func TestMockAdvancesPerTurn(t *testing.T) {
	m := NewMock("mock", "tools")
	ch1, _ := m.ChatStream(context.Background(), Req{})
	_, _, calls, _, _ := drain(ch1)
	if len(calls) == 0 {
		t.Fatal("first turn should request a tool")
	}
	ch2, _ := m.ChatStream(context.Background(), Req{})
	_, _, calls2, _, _ := drain(ch2)
	if len(calls2) != 0 {
		t.Error("second turn should be prose, not another tool call")
	}
}

func TestMockPastEndKeepsWorking(t *testing.T) {
	m := NewMock("mock", "chat")
	for i := 0; i < 5; i++ {
		ch, err := m.ChatStream(context.Background(), Req{})
		if err != nil {
			t.Fatalf("turn %d: %v", i, err)
		}
		if _, _, _, _, err := drain(ch); err != nil {
			t.Fatalf("turn %d: %v", i, err)
		}
	}
}

func TestMockUnknownScenario(t *testing.T) {
	if _, err := NewMock("mock", "nope").ChatStream(context.Background(), Req{}); err == nil {
		t.Error("want an error for an unknown scenario")
	}
}

// Providers must satisfy the interface; a compile-time check is cheaper than
// discovering this at wiring time.
var (
	_ Provider = (*Ollama)(nil)
	_ Provider = (*OpenAI)(nil)
	_ Provider = (*Mock)(nil)
)
