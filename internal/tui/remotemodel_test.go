package tui

import (
	"testing"

	"evilcode/internal/agent"
	"evilcode/internal/provider"
)

// An attached client switches models through the daemon: the picker sends
// MsgModel and returns before the local picker tail that persists last_model
// ever runs (attach wires WithRemoteModelEffort, WithRemoteModel stays unset).
// Persistence therefore rides entirely on the mirror in applyEvent — the
// daemon's canonical EventModel must reach rememberModel even though only the
// combined hook is wired. A guard on remoteModel alone meant every pick was
// forgotten on quit, and the next launch fell back to the stale last_model
// (glm5.2 forever, whatever the user chose).
func TestAttachedModelEventRemembersTheModel(t *testing.T) {
	a := agent.New("bat", provider.NewMock("mock", "chat"), "mock-large", nil,
		agent.NewConversation("system"))
	t.Cleanup(a.Close)
	m := NewModel(a, HeaderState{SessionName: "bat", Model: "mock-large", Provider: "mock"})
	m.width, m.height = 100, 40

	remembered := ""
	m.WithPersistentModelState("mock-large@mock", nil,
		func(ref string) error { remembered = ref; return nil },
		func(string, provider.ReasoningEffort) error { return nil })
	// Attach mode: only the combined model+effort hook is wired.
	m.WithRemoteModelEffort(func(ref string, effort provider.ReasoningEffort) error { return nil })

	// The daemon applied the switch and published the canonical model event.
	m.applyEvent(agent.Event{Kind: agent.EventModel, Model: "mock-small", Provider: "mock"})

	if remembered != "mock-small@mock" {
		t.Errorf("last_model saved = %q, want mock-small@mock", remembered)
	}
	if m.lastModel != "mock-small@mock" {
		t.Errorf("in-memory last model = %q, want mock-small@mock", m.lastModel)
	}
}

// The standalone TUI persists the pick through the picker tail itself, so the
// EventModel mirror must stay inert there: without a remote hook the local
// agent's own events are the source of truth, and re-saving from them would
// only duplicate the picker's write.
func TestStandaloneModelEventDoesNotDoubleRemember(t *testing.T) {
	a := agent.New("bat", provider.NewMock("mock", "chat"), "mock-large", nil,
		agent.NewConversation("system"))
	t.Cleanup(a.Close)
	m := NewModel(a, HeaderState{SessionName: "bat", Model: "mock-large", Provider: "mock"})
	m.width, m.height = 100, 40

	calls := 0
	m.WithPersistentModelState("mock-large@mock", nil,
		func(ref string) error { calls++; return nil },
		func(string, provider.ReasoningEffort) error { return nil })

	m.applyEvent(agent.Event{Kind: agent.EventModel, Model: "mock-small", Provider: "mock"})

	if calls != 0 {
		t.Errorf("saveLastModel called %d times without a remote hook, want 0", calls)
	}
}
