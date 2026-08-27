package tui

import (
	"testing"

	"evilcode/internal/agent"
	"evilcode/internal/config"
	"evilcode/internal/provider"
	"evilcode/internal/session"
)

// TestPickerSwitchRecordsModelMeta drives the model picker the way a keypress
// does and checks that the switch is written to the session log, so a later
// /resume resolves to the new model rather than the one the session started on.
func TestPickerSwitchRecordsModelMeta(t *testing.T) {
	dir := t.TempDir()
	st, err := session.Open(dir, "bat")
	if err != nil {
		t.Fatal(err)
	}
	// The session began on mock-large@mock.
	if err := st.WriteModel("mock-large@mock"); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })

	a := agent.New("bat", provider.NewMock("mock", "chat"), "mock-large", nil,
		agent.NewConversation("system"))
	t.Cleanup(a.Close)
	m := NewModel(a, HeaderState{SessionName: "bat", Model: "mock-large", Provider: "mock"})
	m.width, m.height = 100, 40
	m.WithSessions(dir, "", st)
	// The target must be buildable: a switch to a provider that cannot build
	// is refused, not half-applied (R2-13). Mock providers keep the flow on
	// the direct-apply path — no reasoning menu, no effort state to set up.
	m.providers = []config.ProviderConfig{
		{Name: "mock", Kind: config.KindMock},
		{Name: "other", Kind: config.KindMock},
	}

	// Open the picker on a different model and select it.
	m.pickerOpen = true
	m.picker.Entries = []ModelEntry{
		{Name: "mock-large", Provider: "mock", Current: true},
		{Name: "mock-small", Provider: "other"},
	}
	m.picker.Selected = 1

	if _, _ = m.handlePickerKey("enter"); m.header.Model != "mock-small" {
		t.Fatalf("header.Model = %q, want mock-small", m.header.Model)
	}

	// The switch must be on disk: Describe reads the last model meta.
	info, err := session.Describe(dir, "bat")
	if err != nil {
		t.Fatal(err)
	}
	if info.Model != "mock-small@other" {
		t.Errorf("remembered model = %q, want mock-small@other", info.Model)
	}
}
