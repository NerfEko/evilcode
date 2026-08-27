package tui

import (
	"testing"

	"evilcode/internal/agent"
	"evilcode/internal/config"
	"evilcode/internal/provider"
)

// TestRefreshModelListClearsCacheAndRefetches guards the one command that can
// surface models released after evilcode started. The picker cache (m.models)
// is otherwise held for the whole session, so reopening the picker shows stale
// entries. /refresh-model-list must drop the cache and schedule a fresh fetch.
func TestRefreshModelListClearsCacheAndRefetches(t *testing.T) {
	if _, ok := FindCommand("refresh-model-list"); !ok {
		t.Fatal("refresh-model-list not registered")
	}

	a := agent.New("s", provider.NewMock("ollama-local", "chat"), "mock-large", nil,
		agent.NewConversation(agent.BuildSystemPrompt(agent.ProjectContext{}, nil, "")))
	t.Cleanup(a.Close)
	m := NewModel(a, HeaderState{SessionName: "s", Model: "mock-large", Provider: "ollama-local"})
	m.providers = []config.ProviderConfig{
		{Name: "ollama-local", Kind: config.KindMock},
	}

	// Seed a stale cache: a name the mock provider never lists.
	m.models = []ModelEntry{{Name: "ghost-model", Provider: "ollama-local"}}

	_, cmd := m.runCommandWithArg("refresh-model-list", "")
	if cmd == nil {
		t.Fatal("refresh-model-list returned no command; want a fetch scheduled")
	}
	if m.models != nil {
		t.Fatalf("cache not cleared: %+v", m.models)
	}
	if !m.modelsPending {
		t.Error("modelsPending not set while the refresh fetch is in flight")
	}

	// Execute the scheduled fetch and apply the result.
	msg := cmd()
	loaded, ok := msg.(modelsLoaded)
	if !ok {
		t.Fatalf("cmd msg = %T, want modelsLoaded", msg)
	}
	m.applyModels(loaded)
	if len(m.models) == 0 {
		t.Fatal("model list empty after refresh")
	}
	for _, e := range m.models {
		if e.Name == "ghost-model" {
			t.Errorf("stale ghost-model survived refresh: %+v", m.models)
		}
	}
}

// The plain /model command, by contrast, must NOT refetch once the cache is
// warm — that is the behavior refresh-model-list exists to override.
func TestModelCommandDoesNotRefetchWarmCache(t *testing.T) {
	a := agent.New("s", provider.NewMock("ollama-local", "chat"), "mock-large", nil,
		agent.NewConversation(agent.BuildSystemPrompt(agent.ProjectContext{}, nil, "")))
	t.Cleanup(a.Close)
	m := NewModel(a, HeaderState{SessionName: "s", Model: "mock-large", Provider: "ollama-local"})
	m.providers = []config.ProviderConfig{
		{Name: "ollama-local", Kind: config.KindMock},
	}
	m.models = []ModelEntry{{Name: "ghost-model", Provider: "ollama-local"}}

	_, cmd := m.runCommandWithArg("model", "")
	if cmd != nil {
		t.Errorf("warm cache triggered a fetch; /model should show the cache as-is")
	}
	if m.models == nil {
		t.Error("/model nilled the warm cache; it should preserve it")
	}
}
