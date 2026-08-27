package tui

import (
	"testing"

	"evilcode/internal/agent"
	"evilcode/internal/config"
	"evilcode/internal/provider"
)

// TestModelSwitchUpdatesPickerCurrentMarker guards the user-visible symptom
// "I changed the model in /model but the picker still highlights the old one."
// After a successful switch, the Current marker on both the cached m.models
// list and the live picker.Entries must follow the newly active model, not the
// one that was current when the catalogue was fetched.
func TestModelSwitchUpdatesPickerCurrentMarker(t *testing.T) {
	a := agent.New("s", provider.NewMock("start", "chat"), "mock-small", nil,
		agent.NewConversation(agent.BuildSystemPrompt(agent.ProjectContext{}, nil, "")))
	t.Cleanup(a.Close)
	m := NewModel(a, HeaderState{SessionName: "s", Model: "mock-small", Provider: "start"})
	m.providers = []config.ProviderConfig{
		{Name: "start", Kind: config.KindMock},
		{Name: "next", Kind: config.KindMock},
	}

	// Seed the picker cache the way fetchAllModels would: mock-small is current.
	m.models = []ModelEntry{
		{Name: "mock-small", Provider: "start", Current: true},
		{Name: "mock-large", Provider: "next"},
	}
	m.picker.Entries = m.models
	m.pickerOpen = true
	m.picker.Selected = 1 // hovering mock-large@next

	if _, _ = m.handlePickerKey("enter"); m.header.Model != "mock-large" {
		t.Fatalf("switch did not apply: header.Model = %q, want mock-large", m.header.Model)
	}

	// The cached list must now mark the new model current and the old one not.
	var startSmall, nextLarge *ModelEntry
	for i := range m.models {
		switch m.models[i].Name + "@" + m.models[i].Provider {
		case "mock-small@start":
			startSmall = &m.models[i]
		case "mock-large@next":
			nextLarge = &m.models[i]
		}
	}
	if nextLarge == nil || !nextLarge.Current {
		t.Errorf("m.models: mock-large@next Current = %v, want true", nextLarge != nil && nextLarge.Current)
	}
	if startSmall != nil && startSmall.Current {
		t.Errorf("m.models: mock-small@start still marked Current after switch")
	}

	// The live picker.Entries must agree with the cache.
	for i := range m.picker.Entries {
		if m.picker.Entries[i].Name == "mock-small" && m.picker.Entries[i].Provider == "start" && m.picker.Entries[i].Current {
			t.Errorf("picker.Entries: mock-small@start still marked Current after switch")
		}
		if m.picker.Entries[i].Name == "mock-large" && m.picker.Entries[i].Provider == "next" && !m.picker.Entries[i].Current {
			t.Errorf("picker.Entries: mock-large@next Current = false, want true")
		}
	}
}
