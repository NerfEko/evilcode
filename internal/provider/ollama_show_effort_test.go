package provider

import (
	"context"
	"net/http"
	"net/http/httptest"
	"slices"
	"sync/atomic"
	"testing"
)

// TestOllamaShowEnrichesReasoningEffortForHeuristicMiss is the mechanism the
// daemon's setModel relies on to stop rejecting reasoning efforts for thinking
// models whose family is not in the name heuristic.
//
// The daemon decides reasoning support with ReasoningEffortLevelsForProvider,
// which for Ollama falls back to a name heuristic matching only gpt-oss, think,
// reason, r1, qwen3, glm, qwq. A model like deepseek-v4-flash:0731 advertises a
// "thinking" capability over /api/show but matches none of those, so the
// heuristic returns nil and every effort is rejected as "not supported" — while
// the client picker, which derives levels from capabilities, offered them.
//
// The fix enriches via Show when the heuristic misses. This test pins the
// mechanism: before Show the heuristic returns nothing for the model; after
// Show the same call returns the capability-derived levels from the cache.
func TestOllamaShowEnrichesReasoningEffortForHeuristicMiss(t *testing.T) {
	var shows int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/show" {
			t.Errorf("unexpected path %q", r.URL.Path)
		}
		atomic.AddInt32(&shows, 1)
		w.Write([]byte(`{"capabilities":["completion","tools","thinking"],` +
			`"details":{"parameter_size":"0"},` +
			`"model_info":{"deepseek.context_length":131072}}`))
	}))
	defer srv.Close()

	o := NewOllama("ollama-cloud", srv.URL, "key")
	const model = "deepseek-v4-flash:0731"

	// Heuristic fallback alone: "deepseek" is not a recognized thinking family,
	// so no levels are advertised and the daemon would reject any effort.
	if got := ReasoningEffortLevelsForProvider(o, model); len(got) != 0 {
		t.Fatalf("heuristic returned %v for %q, want none (the miss that causes the bug)", got, model)
	}

	// Enrich via Show, exactly as setModel now does when the heuristic misses.
	if _, err := o.Show(context.Background(), model); err != nil {
		t.Fatalf("Show: %v", err)
	}

	// Now the same call returns the model's advertised levels from the cache,
	// so the daemon agrees with the client picker and accepts the effort.
	got := ReasoningEffortLevelsForProvider(o, model)
	if !slices.Equal(got, OllamaReasoningEfforts()) {
		t.Errorf("after Show, levels = %v, want %v (capability-derived)", got, OllamaReasoningEfforts())
	}
	if atomic.LoadInt32(&shows) != 1 {
		t.Errorf("Show called %d times, want 1 (cache must serve the second lookup)", shows)
	}
}