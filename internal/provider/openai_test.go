package provider

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
)

// H5.5: completed tool calls must come out in protocol index order, not the
// order their fragments finished arriving. A gateway that interleaves two
// concurrent tool calls can easily finish index 1 before index 0; if the
// accumulator emits by first-arrival order instead of by index, the caller
// matching results back to calls pairs the wrong result with the wrong call.
func TestToolCallsEmitInIndexOrder(t *testing.T) {
	body := "data: {\"choices\":[{\"delta\":{\"tool_calls\":[" +
		"{\"index\":1,\"id\":\"call_b\",\"type\":\"function\",\"function\":{\"name\":\"second\",\"arguments\":\"{}\"}}" +
		"]}}]}\n\n" +
		"data: {\"choices\":[{\"delta\":{\"tool_calls\":[" +
		"{\"index\":0,\"id\":\"call_a\",\"type\":\"function\",\"function\":{\"name\":\"first\",\"arguments\":\"{}\"}}" +
		"]}}]}\n\n" +
		"data: [DONE]\n\n"

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(body))
	}))
	defer srv.Close()

	ch, err := NewOpenAI("oai", srv.URL, "k").ChatStream(context.Background(),
		Req{Model: "m", Messages: []Message{{Role: RoleUser, Content: "hi"}}})
	if err != nil {
		t.Fatal(err)
	}

	var got Chunk
	for c := range ch {
		if c.Err != nil {
			t.Fatalf("unexpected stream error: %v", c.Err)
		}
		if c.Done {
			got = c
		}
	}

	if len(got.ToolCalls) != 2 {
		t.Fatalf("want 2 tool calls, got %d: %+v", len(got.ToolCalls), got.ToolCalls)
	}
	if got.ToolCalls[0].ID != "call_a" || got.ToolCalls[1].ID != "call_b" {
		t.Errorf("tool calls out of index order: got [%s, %s], want [call_a, call_b] "+
			"(index 1's fragment arrived first but must still sort after index 0)",
			got.ToolCalls[0].ID, got.ToolCalls[1].ID)
	}
}

func TestOpenAIReasoningEffortWire(t *testing.T) {
	var bodies []map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Errorf("decode request: %v", err)
		}
		bodies = append(bodies, body)
		w.Write([]byte("data: [DONE]\n\n"))
	}))
	defer srv.Close()

	oai := NewOpenAI("oai", srv.URL, "k")
	ch, err := oai.ChatStream(context.Background(), Req{
		Model: "gpt-5.2", ReasoningEffort: ReasoningEffortHigh,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, _, _, err := drain(ch); err != nil {
		t.Fatal(err)
	}
	if got := bodies[0]["reasoning_effort"]; got != "high" {
		t.Errorf("reasoning_effort = %v, want high", got)
	}
	if _, ok := bodies[0]["thinking"]; ok {
		t.Error("ordinary OpenAI request unexpectedly included DeepSeek thinking")
	}
}

func TestDeepSeekReasoningEffortWire(t *testing.T) {
	var bodies []map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Errorf("decode request: %v", err)
		}
		bodies = append(bodies, body)
		w.Write([]byte("data: [DONE]\n\n"))
	}))
	defer srv.Close()

	oai := NewOpenAI("deepseek", srv.URL, "k").WithDeepSeekReasoning()
	for _, effort := range []ReasoningEffort{ReasoningEffortMax, ReasoningEffortNone} {
		ch, err := oai.ChatStream(context.Background(), Req{
			Model: "deepseek-reasoner", ReasoningEffort: effort,
		})
		if err != nil {
			t.Fatal(err)
		}
		if _, _, _, _, err := drain(ch); err != nil {
			t.Fatal(err)
		}
	}
	if got := bodies[0]["reasoning_effort"]; got != "max" {
		t.Errorf("DeepSeek max effort = %v, want max", got)
	}
	thinking, ok := bodies[0]["thinking"].(map[string]any)
	if !ok || thinking["type"] != "enabled" {
		t.Errorf("DeepSeek enabled thinking = %#v", bodies[0]["thinking"])
	}
	if _, ok := bodies[1]["reasoning_effort"]; ok {
		t.Error("DeepSeek disabled thinking unexpectedly included reasoning_effort")
	}
	thinking, ok = bodies[1]["thinking"].(map[string]any)
	if !ok || thinking["type"] != "disabled" {
		t.Errorf("DeepSeek disabled thinking = %#v", bodies[1]["thinking"])
	}
}

func TestOpenAIModelsExposeReasoningEfforts(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"data":[
			{"id":"gpt-5.2","reasoning_efforts":["none","low","high"]},
			{"id":"gateway-reasoner","reasoning":{"effort":{"levels":["low","medium"]}}},
			{"id":"o3-mini"}
		]}`))
	}))
	defer srv.Close()

	models, err := NewOpenAI("oai", srv.URL, "k").Models(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(models) != 3 {
		t.Fatalf("models = %+v", models)
	}
	if got := models[0].ReasoningEfforts; len(got) != 3 || got[0] != ReasoningEffortNone || got[2] != ReasoningEffortHigh {
		t.Errorf("gpt-5.2 efforts = %v", got)
	}
	if got := models[1].ReasoningEfforts; len(got) != 2 || got[0] != ReasoningEffortLow || got[1] != ReasoningEffortMedium {
		t.Errorf("gateway efforts = %v", got)
	}
	if got := models[2].ReasoningEfforts; len(got) != 3 || got[0] != ReasoningEffortLow || got[2] != ReasoningEffortHigh {
		t.Errorf("o3-mini fallback efforts = %v", got)
	}
}

func TestOpenAIGPT56FallbackIncludesMax(t *testing.T) {
	oai := NewOpenAI("oai", "http://example.invalid", "")
	want := OpenAIGPT56ReasoningEfforts()
	for _, model := range []string{"gpt-5.6-sol", "gpt-5.6-terra", "gpt-5.6-luna", "gpt-5.6"} {
		if got := oai.reasoningEffortLevelsForModel(model); !slices.Equal(got, want) {
			t.Errorf("%s efforts = %v, want %v", model, got, want)
		}
	}
}

// DeepSeek requires the previous assistant response's reasoning_content to be
// passed back on the next request when thinking is enabled. A two-round fixture
// asserts the second request carries the first response's reasoning verbatim.
func TestDeepSeekReasoningReplayedAcrossToolRounds(t *testing.T) {
	var bodies []map[string]any
	round := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Errorf("decode request: %v", err)
		}
		bodies = append(bodies, body)
		round++
		if round == 1 {
			// First round: thinking trace plus a tool call.
			w.Write([]byte("data: {\"choices\":[{\"delta\":{\"reasoning_content\":\"trace-one\",\"content\":\"\"}}]}\n\n" +
				"data: {\"choices\":[{\"delta\":{\"tool_calls\":[{\"index\":0,\"id\":\"call_x\",\"function\":{\"name\":\"read\",\"arguments\":\"{}\"}}]}}]}\n\n" +
				"data: {\"choices\":[{\"delta\":{},\"finish_reason\":\"tool_calls\"}]}\n\n" +
				"data: [DONE]\n\n"))
			return
		}
		// Second round: the tool result is present, so the assistant message
		// must carry the replayed reasoning.
		w.Write([]byte("data: {\"choices\":[{\"delta\":{\"content\":\"done\"}}]}\n\n" +
			"data: {\"choices\":[{\"delta\":{},\"finish_reason\":\"stop\"}]}\n\n" +
			"data: [DONE]\n\n"))
	}))
	defer srv.Close()

	oai := NewOpenAI("deepseek", srv.URL, "k").WithDeepSeekReasoning()
	req := Req{Model: "deepseek-v4-flash", Messages: []Message{
		{Role: RoleUser, Content: "hi"},
	}}

	// Round one: reasoning streamed and stored on the message.
	ch, err := oai.ChatStream(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	var reasoning string
	for c := range ch {
		if c.Err != nil {
			t.Fatalf("round 1 stream error: %v", c.Err)
		}
		reasoning += c.Reasoning
	}
	if reasoning != "trace-one" {
		t.Fatalf("round 1 reasoning = %q", reasoning)
	}

	// Round two replays the assistant message carrying that reasoning.
	req.Messages = append(req.Messages,
		Message{Role: RoleAssistant, Content: "", Reasoning: "reasoning_content",
			ToolCalls: []ToolCall{{ID: "call_x", Name: "read", Args: json.RawMessage(`{}`)}}},
		Message{Role: RoleTool, Content: "result", ToolCallID: "call_x", ToolName: "read"},
	)
	ch, err = oai.ChatStream(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	for c := range ch {
		if c.Err != nil {
			t.Fatalf("round 2 stream error: %v", c.Err)
		}
	}
	if len(bodies) != 2 {
		t.Fatalf("requests = %d, want 2", len(bodies))
	}
	msgs, ok := bodies[1]["messages"].([]any)
	if !ok {
		t.Fatalf("second request messages = %#v", bodies[1]["messages"])
	}
	var assistant map[string]any
	for _, raw := range msgs {
		m := raw.(map[string]any)
		if m["role"] == "assistant" {
			assistant = m
		}
	}
	if assistant == nil {
		t.Fatal("no assistant message in the second request")
	}
	if got := assistant["reasoning_content"]; got != "reasoning_content" {
		t.Errorf("replayed reasoning_content = %#v, want the first response's reasoning", got)
	}
}

// A stream that ends without the [DONE] terminal marker must be reported as a
// truncation error, not committed as a complete answer (B2).
func TestOpenAITruncatedStreamIsAnError(t *testing.T) {
	body := "data: {\"choices\":[{\"delta\":{\"content\":\"partial\"}}]}\n\n" +
		"data: {\"choices\":[{\"delta\":{\"content\":\" answer\"}}]}\n\n"

	_, _, _, _, err := collectOpenAI(t, body)
	if err == nil {
		t.Fatal("want an error for a stream that never reached [DONE]")
	}
	if !errors.Is(err, ErrStreamTruncated) {
		t.Errorf("err = %v, want ErrStreamTruncated", err)
	}
}

// A finish_reason of length/content_filter means the answer is clipped; it must
// surface as an error rather than save a truncated turn as complete (B3).
func TestOpenAINonNormalFinishReasonIsAnError(t *testing.T) {
	for _, reason := range []string{"length", "content_filter"} {
		body := "data: {\"choices\":[{\"delta\":{\"content\":\"clipped\"}}]}\n\n" +
			"data: {\"choices\":[{\"delta\":{},\"finish_reason\":\"" + reason + "\"}]}\n\n" +
			"data: [DONE]\n\n"
		text, _, _, _, err := collectOpenAI(t, body)
		if err == nil {
			t.Fatalf("%s: want an error, got text %q", reason, text)
		}
		if !strings.Contains(err.Error(), reason) {
			t.Errorf("%s: err = %v, want it to name the finish reason", reason, err)
		}
	}
}

// Synthesized tool IDs must not collide between responses: gateways that omit
// IDs are common, and a repeated id pairs the next turn's result to the wrong
// call (B4).
func TestOpenAISynthesizedIDsAreUniqueAcrossResponses(t *testing.T) {
	body := "data: {\"choices\":[{\"delta\":{\"tool_calls\":[{\"index\":0,\"function\":{\"name\":\"read\",\"arguments\":\"{}\"}}]}}]}\n\n" +
		"data: [DONE]\n\n"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(body))
	}))
	defer srv.Close()

	oai := NewOpenAI("oai", srv.URL, "k")
	req := Req{Model: "m", Messages: []Message{{Role: RoleUser, Content: "hi"}}}
	seen := map[string]bool{}
	for turn := 0; turn < 2; turn++ {
		ch, err := oai.ChatStream(context.Background(), req)
		if err != nil {
			t.Fatalf("turn %d: %v", turn, err)
		}
		var got Chunk
		for c := range ch {
			if c.Err != nil {
				t.Fatalf("turn %d: %v", turn, c.Err)
			}
			if c.Done {
				got = c
			}
		}
		if len(got.ToolCalls) != 1 {
			t.Fatalf("turn %d: calls = %d, want 1", turn, len(got.ToolCalls))
		}
		if seen[got.ToolCalls[0].ID] {
			t.Fatalf("turn %d: tool_call_id %q reused from an earlier turn", turn, got.ToolCalls[0].ID)
		}
		seen[got.ToolCalls[0].ID] = true
	}
}

// H5.6: some OpenAI-compatible gateways require role:"tool" messages to carry
// the tool's name, not just the call ID. toOAIMessages must copy it from
// Message.ToolName.
func TestToOAIMessagesSetsToolName(t *testing.T) {
	out := (&OpenAI{}).toOAIMessages([]Message{
		{Role: RoleTool, Content: "result", ToolCallID: "call_a", ToolName: "get_weather"},
	})
	if len(out) != 1 {
		t.Fatalf("want 1 message, got %d", len(out))
	}
	if out[0].Name != "get_weather" {
		t.Errorf("Name = %q, want %q", out[0].Name, "get_weather")
	}
}

// DeepSeek reports its KV cache as prompt_cache_hit_tokens / _miss_tokens in
// the final usage chunk. The OpenAI provider reuses the same decoder, so the
// cache counts must come through on the Done chunk's Usage.
func TestDeepSeekCacheTokensParsed(t *testing.T) {
	body := "data: {\"choices\":[{\"delta\":{\"content\":\"hi\"}}]}\n\n" +
		"data: {\"usage\":{\"prompt_tokens\":100,\"completion_tokens\":20," +
		"\"prompt_cache_hit_tokens\":80,\"prompt_cache_miss_tokens\":20}}\n\n" +
		"data: [DONE]\n\n"

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(body))
	}))
	defer srv.Close()

	ch, err := NewOpenAI("deepseek", srv.URL, "k").ChatStream(context.Background(),
		Req{Model: "deepseek-chat", Messages: []Message{{Role: RoleUser, Content: "hi"}}})
	if err != nil {
		t.Fatal(err)
	}

	var got Chunk
	for c := range ch {
		if c.Err != nil {
			t.Fatalf("unexpected stream error: %v", c.Err)
		}
		if c.Done {
			got = c
		}
	}
	if got.Usage == nil {
		t.Fatal("missing usage on done chunk")
	}
	if got.Usage.CacheReadTokens != 80 {
		t.Errorf("CacheReadTokens = %d, want 80", got.Usage.CacheReadTokens)
	}
	if got.Usage.CacheWriteTokens != 20 {
		t.Errorf("CacheWriteTokens = %d, want 20", got.Usage.CacheWriteTokens)
	}
}
