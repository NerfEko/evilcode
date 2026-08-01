package tui

import "testing"

// The KV-cache widget only means something for DeepSeek. Ollama — local or
// cloud — has no prompt cache, so it must never show there even if cache
// counts somehow accumulated.
func TestKvCacheWidgetGatedByProvider(t *testing.T) {
	hasKvCache := func(provider string) bool {
		m := NewModel(nil, HeaderState{Model: "m", Provider: provider})
		m.cacheRead, m.cacheWrite = 8000, 2000
		for _, w := range m.activeWidgets() {
			if w.Kind == WidgetKvCache {
				return true
			}
		}
		return false
	}

	if !hasKvCache("deepseek") {
		t.Error("KV cache widget should show for deepseek")
	}
	for _, p := range []string{"ollama-local", "ollama-cloud", "mock", ""} {
		if hasKvCache(p) {
			t.Errorf("KV cache widget should NOT show for provider %q (no prompt cache)", p)
		}
	}
}