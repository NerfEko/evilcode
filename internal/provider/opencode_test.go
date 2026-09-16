package provider

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"slices"
	"testing"
)

// sseHello is the smallest legal chat completions stream, shared by the wire
// tests below.
const sseHello = "data: {\"choices\":[{\"delta\":{\"content\":\"hi\"}}]}\n\ndata: [DONE]\n\n"

func sseServer(t *testing.T, capture func(r *http.Request, body map[string]any)) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Errorf("decode request: %v", err)
		}
		if capture != nil {
			capture(r, body)
		}
		w.Header().Set("Content-Type", "text/event-stream")
		w.Write([]byte(sseHello))
	}))
	t.Cleanup(srv.Close)
	return srv
}

// The gateway asks clients to identify themselves and to send a stable
// conversation id; both must ride on every chat request.
func TestOpenCodeGoSendsSessionHeaderAndUserAgent(t *testing.T) {
	var ua, session string
	srv := sseServer(t, func(r *http.Request, _ map[string]any) {
		ua = r.Header.Get("User-Agent")
		session = r.Header.Get("x-opencode-session")
	})
	o := NewOpenCodeGo("opencode-go", srv.URL, "k")
	ch, err := o.ChatStream(context.Background(), Req{Model: "glm-5.3", SessionID: "sess-7"})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, _, _, err := drain(ch); err != nil {
		t.Fatal(err)
	}
	if session != "sess-7" {
		t.Errorf("x-opencode-session = %q, want the request's session id", session)
	}
	if ua != openCodeGoUserAgent {
		t.Errorf("user agent = %q, want %q", ua, openCodeGoUserAgent)
	}
}

// A request without a session id must not send the header, and the default
// base URL must be the gateway's.
func TestOpenCodeGoDefaultsAndHeaderlessRequest(t *testing.T) {
	var sawHeader bool
	srv := sseServer(t, func(r *http.Request, _ map[string]any) {
		sawHeader = r.Header.Get("x-opencode-session") != ""
	})
	o := NewOpenCodeGo("opencode-go", "", "")
	if o.BaseURL != DefaultOpenCodeGoBaseURL {
		t.Errorf("base URL = %q, want %q", o.BaseURL, DefaultOpenCodeGoBaseURL)
	}
	o.HTTP = srv.Client()
	// Point the transport at the test server while keeping every other
	// default, including the session header.
	o.BaseURL = srv.URL
	ch, err := o.ChatStream(context.Background(), Req{Model: "glm-5.3"})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, _, _, err := drain(ch); err != nil {
		t.Fatal(err)
	}
	if sawHeader {
		t.Error("x-opencode-session sent for a request without a session id")
	}
}

// The live listing is what the picker shows; the bundled table only supplies
// metadata for it, and a model the gateway delisted disappears with it.
func TestOpenCodeGoModelsMergeBundledMetadata(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/models" {
			http.NotFound(w, r)
			return
		}
		w.Write([]byte(`{"data":[{"id":"deepseek-v4-flash"},{"id":"brand-new-model"},{"id":"ox-alpha-free"}]}`))
	}))
	defer srv.Close()

	models, err := NewOpenCodeGo("opencode-go", srv.URL, "k").Models(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	byName := map[string]ModelInfo{}
	for _, m := range models {
		byName[m.Name] = m
	}
	got, ok := byName["deepseek-v4-flash"]
	if !ok {
		t.Fatal("bundled model missing from live listing")
	}
	if got.ContextWindow != 1000000 || got.Vision {
		t.Errorf("deepseek-v4-flash = %+v, want a 1M text-only window", got)
	}
	if want := []ReasoningEffort{ReasoningEffortLow, ReasoningEffortHigh, ReasoningEffortMax}; !slices.Equal(got.ReasoningEfforts, want) {
		t.Errorf("deepseek-v4-flash efforts = %v, want %v", got.ReasoningEfforts, want)
	}
	if got, ok := byName["brand-new-model"]; !ok {
		t.Error("live-only model missing")
	} else if got.ContextWindow != 0 {
		t.Errorf("live-only model window = %d, want 0 (no bundled metadata)", got.ContextWindow)
	}
	if _, ok := byName["glm-5.3-flash"]; ok {
		t.Error("bundled-only model present although the gateway does not list it")
	}
}

// Without the gateway the bundled catalogue still fills the picker.
func TestOpenCodeGoModelsFallBackToBundledCatalogue(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	models, err := NewOpenCodeGo("opencode-go", srv.URL, "k").Models(context.Background())
	if err != nil {
		t.Fatalf("fallback listing: %v", err)
	}
	if len(models) != len(opencodeGoCatalogue) {
		t.Fatalf("models = %d, want %d", len(models), len(opencodeGoCatalogue))
	}
	byName := map[string]ModelInfo{}
	for _, m := range models {
		byName[m.Name] = m
	}
	glm, ok := byName["glm-5.3-flash"]
	if !ok {
		t.Fatal("catalogue missing glm-5.3-flash")
	}
	if glm.ContextWindow != 1000000 || !glm.Vision {
		t.Errorf("glm-5.3-flash = %+v, want 1M vision window", glm)
	}
}

// A model's reasoning control must follow its catalogue entry, and the wire
// field must be omitted when the effort is not one the model advertises.
func TestOpenCodeGoReasoningEffortGatedByCatalogue(t *testing.T) {
	var bodies []map[string]any
	srv := sseServer(t, func(_ *http.Request, body map[string]any) {
		bodies = append(bodies, body)
	})
	o := NewOpenCodeGo("opencode-go", srv.URL, "k")

	// glm-5.3-flash advertises low/high/max; a saved "medium" preference is
	// clamped away instead of risking a 400.
	for _, effort := range []ReasoningEffort{ReasoningEffortHigh, ReasoningEffortMedium, ReasoningEffortNone} {
		ch, err := o.ChatStream(context.Background(), Req{Model: "glm-5.3-flash", ReasoningEffort: effort})
		if err != nil {
			t.Fatal(err)
		}
		if _, _, _, _, err := drain(ch); err != nil {
			t.Fatal(err)
		}
	}
	// glm-5 advertises no levels at all; the field must stay off even for a
	// valid value.
	ch, err := o.ChatStream(context.Background(), Req{Model: "glm-5", ReasoningEffort: ReasoningEffortHigh})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, _, _, err := drain(ch); err != nil {
		t.Fatal(err)
	}

	if got := bodies[0]["reasoning_effort"]; got != "high" {
		t.Errorf("advertised effort = %v, want high", got)
	}
	if got, ok := bodies[1]["reasoning_effort"]; ok {
		t.Errorf("unadvertised effort sent: %v", got)
	}
	if got, ok := bodies[2]["reasoning_effort"]; ok {
		t.Errorf("none sent as reasoning_effort: %v", got)
	}
	if got, ok := bodies[3]["reasoning_effort"]; ok {
		t.Errorf("effort sent for a model with no advertised levels: %v", got)
	}
}

// ReasoningEffortLevelsForProvider must serve the catalogue without the
// OpenAI-compatible name heuristics: an unknown id gets no control.
func TestOpenCodeGoReasoningEffortLevelsForModel(t *testing.T) {
	o := NewOpenCodeGo("opencode-go", "http://example.invalid", "")
	if want := []ReasoningEffort{ReasoningEffortMinimal, ReasoningEffortLow, ReasoningEffortMedium, ReasoningEffortHigh, ReasoningEffortXHigh}; !slices.Equal(o.reasoningEffortLevelsForModel("muse-spark-1.3-contributor"), want) {
		t.Errorf("muse-spark-1.3 efforts = %v", o.reasoningEffortLevelsForModel("muse-spark-1.3-contributor"))
	}
	if got := o.reasoningEffortLevelsForModel("brand-new-model"); got != nil {
		t.Errorf("unknown model efforts = %v, want nil", got)
	}
	if !SupportsReasoningEffort(o) {
		t.Error("SupportsReasoningEffort(OpenCodeGo) = false")
	}
}

// The window config resolves for the meter must come from the same table.
func TestOpenCodeGoContextWindow(t *testing.T) {
	if got := OpenCodeGoContextWindow("kimi-k3"); got != 1048576 {
		t.Errorf("kimi-k3 window = %d, want 1048576", got)
	}
	if got := OpenCodeGoContextWindow("brand-new-model"); got != 0 {
		t.Errorf("unknown model window = %d, want 0", got)
	}
}

// Embeddings are not part of the gateway's contract; the error should say so
// under the right provider name.
// muse-spark, grok, and gpt-5.6 ids must go through the Responses wire: the
// gateway returns 500 or "not supported for format oa-compat" on chat
// completions for them, and serves them normally on /responses.
func TestOpenCodeGoRoutesResponsesFamilies(t *testing.T) {
	var paths []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		paths = append(paths, r.URL.Path)
		w.Header().Set("Content-Type", "text/event-stream")
		if r.URL.Path == "/v1/responses" {
			w.Write([]byte("event: response.completed\n" +
				`data: {"type":"response.completed","response":{"status":"completed","output":[],"usage":{"input_tokens":5,"output_tokens":2}}}` + "\n\n"))
			return
		}
		w.Write([]byte(sseHello))
	}))
	defer srv.Close()
	o := NewOpenCodeGo("opencode-go", srv.URL, "k")

	for model, want := range map[string]string{
		"muse-spark-1.3-contributor": "/v1/responses",
		"grok-4.6":                   "/v1/responses",
		"gpt-5.6-luna":               "/v1/responses",
		"grok-4.5":                   "/v1/responses",
		"glm-5.3-flash":              "/v1/chat/completions",
		"kimi-k3":                    "/v1/chat/completions",
	} {
		ch, err := o.ChatStream(context.Background(), Req{Model: model})
		if err != nil {
			t.Fatalf("%s: %v", model, err)
		}
		if _, _, _, _, err := drain(ch); err != nil {
			t.Fatalf("%s: %v", model, err)
		}
		if got := paths[len(paths)-1]; got != want {
			t.Errorf("%s posted %q, want %q", model, got, want)
		}
	}
}

// The Responses payload mirrors the conversation: system text in
// instructions, messages in input, tools declared as functions, and the
// catalogue-gated effort (an unadvertised value must not reach the wire).
func TestOpenCodeGoResponsesPayload(t *testing.T) {
	var bodies []map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Errorf("decode request: %v", err)
		}
		bodies = append(bodies, body)
		w.Header().Set("Content-Type", "text/event-stream")
		w.Write([]byte("event: response.completed\n" +
			`data: {"type":"response.completed","response":{"status":"completed","output":[],"usage":{"input_tokens":5,"output_tokens":2}}}` + "\n\n"))
	}))
	defer srv.Close()
	o := NewOpenCodeGo("opencode-go", srv.URL, "k")
	req := Req{
		Model: "muse-spark-1.3-contributor",
		Messages: []Message{
			{Role: RoleSystem, Content: "You are terse."},
			{Role: RoleUser, Content: "hi"},
		},
		Tools: []ToolDef{{Name: "get_time", Desc: "time", Schema: json.RawMessage(`{"type":"object"}`)}},
	}
	ch, err := o.ChatStream(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, _, _, err := drain(ch); err != nil {
		t.Fatal(err)
	}
	first := bodies[0]
	if first["instructions"] != "You are terse." {
		t.Errorf("instructions = %v", first["instructions"])
	}
	input, _ := first["input"].([]any)
	if len(input) != 1 {
		t.Fatalf("input = %v, want one user message item", first["input"])
	}
	tools, _ := first["tools"].([]any)
	if len(tools) != 1 {
		t.Errorf("tools = %v, want the declared function", first["tools"])
	}
	if _, ok := first["reasoning"]; ok {
		t.Error("reasoning sent although no effort was requested")
	}

	// An advertised effort rides the wire; an unadvertised one is clamped.
	req.ReasoningEffort = ReasoningEffortLow
	ch, err = o.ChatStream(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, _, _, err := drain(ch); err != nil {
		t.Fatal(err)
	}
	reasoning, _ := bodies[1]["reasoning"].(map[string]any)
	if reasoning["effort"] != "low" {
		t.Errorf("advertised effort = %v, want low", reasoning)
	}
	req.ReasoningEffort = ReasoningEffortMax
	ch, err = o.ChatStream(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, _, _, err := drain(ch); err != nil {
		t.Fatal(err)
	}
	if _, ok := bodies[2]["reasoning"]; ok {
		t.Error("unadvertised effort reached the Responses wire")
	}
}

// The Responses stream decoder must carry text, tool calls (with call ids the
// tool result replay correlates on), and usage from response.completed.
func TestOpenCodeGoResponsesStreamDecoded(t *testing.T) {
	fixture := "event: response.created\n" +
		`data: {"type":"response.created","response":{"id":"resp_1","status":"in_progress"}}` + "\n\n" +
		"event: response.output_item.added\n" +
		`data: {"type":"response.output_item.added","output_index":0,"item":{"id":"rs_1","type":"reasoning","status":"in_progress"}}` + "\n\n" +
		"event: response.output_text.delta\n" +
		`data: {"type":"response.output_text.delta","delta":"checking the time"}` + "\n\n" +
		"event: response.output_item.added\n" +
		`data: {"type":"response.output_item.added","output_index":1,"item":{"id":"fc_1","type":"function_call","status":"in_progress","name":"get_time","call_id":"call_9"}}` + "\n\n" +
		"event: response.function_call_arguments.delta\n" +
		`data: {"type":"response.function_call_arguments.delta","item_id":"fc_1","call_id":"call_9","delta":"{\"tz\":\"utc\"}"}` + "\n\n" +
		"event: response.output_item.done\n" +
		`data: {"type":"response.output_item.done","output_index":1,"item":{"id":"fc_1","type":"function_call","status":"completed","name":"get_time","call_id":"call_9","arguments":"{\"tz\":\"utc\"}"}}` + "\n\n" +
		"event: ping\n" +
		`data: {"type":"ping"}` + "\n\n" +
		"event: response.completed\n" +
		`data: {"type":"response.completed","response":{"status":"completed","output":[{"type":"reasoning","id":"rs_1","status":"completed","encrypted_content":"abc"},{"type":"function_call","id":"fc_1","type":"function_call","name":"get_time","call_id":"call_9","arguments":"{\"tz\":\"utc\"}"}],"usage":{"input_tokens":12,"output_tokens":3,"input_tokens_details":{"cached_tokens":7}}}}` + "\n\n"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/responses" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		w.Write([]byte(fixture))
	}))
	defer srv.Close()

	o := NewOpenCodeGo("opencode-go", srv.URL, "k")
	ch, err := o.ChatStream(context.Background(), Req{
		Model:           "muse-spark-1.3-contributor",
		SessionID:       "sess-9",
		ReasoningEffort: ReasoningEffortLow,
		Messages:        []Message{{Role: RoleUser, Content: "time?"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	text, _, calls, usage, err := drain(ch)
	if err != nil {
		t.Fatal(err)
	}
	if text != "checking the time" {
		t.Errorf("text = %q", text)
	}
	if len(calls) != 1 || calls[0].Name != "get_time" || calls[0].ID != "call_9" || string(calls[0].Args) != `{"tz":"utc"}` {
		t.Errorf("tool calls = %+v", calls)
	}
	if usage == nil || usage.PromptTokens != 12 || usage.CompletionTokens != 3 || usage.CacheReadTokens != 7 {
		t.Errorf("usage = %+v", usage)
	}
}

func TestOpenCodeGoEmbedNamesProvider(t *testing.T) {
	o := NewOpenCodeGo("opencode-go", "http://example.invalid", "")
	_, err := o.Embed(context.Background(), []string{"x"})
	if err == nil || o.name == "" {
		t.Fatalf("Embed = %v, want a named-provider error", err)
	}
}
