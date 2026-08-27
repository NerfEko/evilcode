package tui

import (
	"context"
	"strings"
	"testing"

	"evilcode/internal/agent"
	"evilcode/internal/config"
	"evilcode/internal/provider"
)

// fetchAllModels lists every configured provider's models so a DeepSeek
// catalog surfaces alongside Ollama without a config file. Mock providers
// stand in for the wire endpoints: the point is the aggregation, not the HTTP.
func TestFetchAllModelsAggregatesEveryProvider(t *testing.T) {
	provs := []config.ProviderConfig{
		{Name: "ollama-local", Kind: config.KindMock},
		{Name: "deepseek", Kind: config.KindMock, APIKeyEnv: "DEEPSEEK_API_KEY"},
	}
	t.Setenv("DEEPSEEK_API_KEY", "sk-test")

	got := fetchAllModels(provs, "mock-large", "ollama-local")
	if len(got) != 4 { // two mock models per provider
		t.Fatalf("entries = %d, want one per model per provider:\n%+v", len(got), got)
	}
	seen := map[string]bool{}
	for _, e := range got {
		seen[e.Provider+"/"+e.Name] = true
		if e.Provider == "deepseek" && e.Via != "api-key" {
			t.Errorf("deepseek entry via = %q, want api-key", e.Via)
		}
		if e.Provider == "ollama-local" && e.Via != "local" {
			t.Errorf("ollama-local entry via = %q, want local", e.Via)
		}
	}
	for _, want := range []string{"ollama-local/mock-small", "ollama-local/mock-large", "deepseek/mock-small", "deepseek/mock-large"} {
		if !seen[want] {
			t.Errorf("missing %q in %v", want, seen)
		}
	}
	// The active model on the active provider is marked current.
	for _, e := range got {
		if e.Name == "mock-large" && e.Provider == "ollama-local" && !e.Current {
			t.Errorf("active model not marked current: %+v", e)
		}
	}
}

// A provider with no key configured is reported as such rather than hidden, so
// the picker can show why a catalog is empty.
func TestFetchAllModelsMarksMissingKey(t *testing.T) {
	provs := []config.ProviderConfig{
		{Name: "deepseek", Kind: config.KindMock, APIKeyEnv: "DEEPSEEK_API_KEY"},
	}
	t.Setenv("DEEPSEEK_API_KEY", "")
	got := fetchAllModels(provs, "x", "deepseek")
	for _, e := range got {
		if e.Via != "no key" {
			t.Errorf("via = %q, want \"no key\" when the env var is unset", e.Via)
		}
	}
}

// providerConfig backs the picker's cross-provider rebuild.
func TestProviderConfigLookup(t *testing.T) {
	m := &Model{providers: []config.ProviderConfig{
		{Name: "ollama-local", Kind: config.KindMock},
		{Name: "deepseek", Kind: config.KindDeepSeek},
	}}
	if pc := m.providerConfig("deepseek"); pc == nil || pc.Kind != config.KindDeepSeek {
		t.Fatalf("deepseek lookup failed: %+v", pc)
	}
	if pc := m.providerConfig("nope"); pc != nil {
		t.Errorf("unknown provider returned non-nil: %+v", pc)
	}
}

// The compactor's embedder is a build-time decision (R2-11): a dedicated
// embedding backend keeps its provider and vector space across a chat switch.
// The old switch overwrote it with whichever chat provider the picker
// selected, silently re-coupling semantic relevance to the chat choice
// (R2-13).
func TestPickerSwitchKeepsTheBuildTimeCompactionEmbedder(t *testing.T) {
	a := agent.New("bat", provider.NewMock("start", "chat"), "mock-large", nil,
		agent.NewConversation("system"))
	t.Cleanup(a.Close)
	dedicated := provider.NewMock("start", "embed")
	c := &agent.Compactor{
		Summarize: func(context.Context, string, string) (string, error) { return "s", nil },
		Embedding: dedicated,
	}
	for range 2 {
		c.AddEmbeddingSnapshot([]float32{1, 0})
	}
	for range 2 {
		c.AddEmbeddingSnapshot([]float32{0, 1})
	}

	m := NewModel(a, HeaderState{SessionName: "bat", Model: "mock-large", Provider: "start"})
	m.providers = []config.ProviderConfig{
		{Name: "start", Kind: config.KindMock},
		{Name: "next", Kind: config.KindMock},
	}
	m.WithCompactor(c)
	m.pickerOpen = true
	m.picker.Entries = []ModelEntry{
		{Name: "mock-large", Provider: "start", Current: true},
		{Name: "mock-large", Provider: "next"},
	}
	m.picker.Selected = 1

	if _, _ = m.handlePickerKey("enter"); m.agent.Provider.Name() != "next" {
		t.Fatalf("active provider = %q, want next", m.agent.Provider.Name())
	}
	if c.Embedding != agent.EmbeddingProvider(dedicated) {
		t.Fatal("chat-provider switch replaced the compactor's build-time embedder")
	}
}

// fetchAllModels falls back to the current model when nothing answers, so the
// picker never opens empty.
func TestFetchAllModelsFallsBackToCurrent(t *testing.T) {
	got := fetchAllModels(nil, "deepseek-chat", "deepseek")
	if len(got) != 1 || got[0].Name != "deepseek-chat" || !got[0].Current {
		t.Fatalf("fallback = %+v, want the current model only", got)
	}
}

func TestPickerRendersMultipleProviders(t *testing.T) {
	entries := []ModelEntry{
		{Name: "mock-large", Provider: "ollama-local", Via: "local", Current: true},
		{Name: "deepseek-chat", Provider: "deepseek", Via: "api-key"},
	}
	joined := strings.Join(plainLines(testRenderer(120).RenderPicker(
		PickerState{Entries: entries})), "\n")
	for _, want := range []string{"ollama-local", "deepseek", "deepseek-chat", "api-key"} {
		if !strings.Contains(joined, want) {
			t.Errorf("picker missing %q:\n%s", want, joined)
		}
	}
}
