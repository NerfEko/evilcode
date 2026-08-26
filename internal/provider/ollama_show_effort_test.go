package provider

import (
	"net/http"
	"net/http/httptest"
	"slices"
	"sync/atomic"
	"testing"
)

// TestOllamaShowEnrichesReasoningEffortForHeuristicMiss pins the mechanism that
// makes reasoning levels come from the API for every caller, not just the
// daemon's setModel.
//
// The name heuristic matches only gpt-oss, think, reason, r1, qwen3, glm, qwq.
// A model like deepseek-v4-flash:0731 advertises a "thinking" capability over
// /api/show but matches none of those, so the heuristic alone returns nothing
// and every effort would be rejected as "not supported". The provider's level
// resolution enriches via Show when the heuristic misses, so the daemon
// snapshot, SetReasoningEffort, wiring, and the TUI fallbacks all see the
// capability-derived levels on a cold cache.
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

	// Cold cache: the heuristic alone would return nothing for this model, so
	// the provider asks /api/show and returns the advertised levels.
	got := ReasoningEffortLevelsForProvider(o, model)
	if !slices.Equal(got, OllamaReasoningEfforts()) {
		t.Errorf("cold-cache levels = %v, want %v (enriched from /api/show)", got, OllamaReasoningEfforts())
	}
	if atomic.LoadInt32(&shows) != 1 {
		t.Errorf("Show called %d times, want 1 (enrichment must be memoized)", shows)
	}

	// A second resolution is served from the cache without another request.
	got = ReasoningEffortLevelsForProvider(o, model)
	if !slices.Equal(got, OllamaReasoningEfforts()) {
		t.Errorf("second resolution levels = %v, want %v", got, OllamaReasoningEfforts())
	}
	if atomic.LoadInt32(&shows) != 1 {
		t.Errorf("Show called %d times after second resolution, want 1", shows)
	}
}

// TestOllamaReasoningLevelsHeuristicMissShowFailureLeavesNil pins the failure
// path: when /api/show is unreachable, a heuristic miss must leave no levels
// rather than fabricating them, so behavior is unchanged from before the
// enrichment.
func TestOllamaReasoningLevelsHeuristicMissShowFailureLeavesNil(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	o := NewOllama("ollama-cloud", srv.URL, "key")
	if got := ReasoningEffortLevelsForProvider(o, "deepseek-v4-flash:0731"); len(got) != 0 {
		t.Errorf("levels = %v, want none when /api/show fails", got)
	}
}
