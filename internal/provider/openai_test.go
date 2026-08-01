package provider

import (
	"context"
	"net/http"
	"net/http/httptest"
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

// H5.6: some OpenAI-compatible gateways require role:"tool" messages to carry
// the tool's name, not just the call ID. toOAIMessages must copy it from
// Message.ToolName.
func TestToOAIMessagesSetsToolName(t *testing.T) {
	out := toOAIMessages([]Message{
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
