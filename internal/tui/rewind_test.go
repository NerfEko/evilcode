package tui

import (
	"strings"
	"testing"

	"evilcode/internal/agent"
	"evilcode/internal/provider"
	"evilcode/internal/session"
)

// H5.9: the collapse summary computed discarded := before[len(kept):] where
// before came from Conv.Messages() (system message prepended at index 0) and
// kept came from the on-disk file (no system message). The indices are one
// apart, so the slice boundary lands one message too early and folds a
// message that was actually kept into "discarded" instead.
func TestRewindCollapseSummaryDoesNotMisattributeAKeptMessage(t *testing.T) {
	dir := t.TempDir()
	st, err := session.Open(dir, "bat")
	if err != nil {
		t.Fatal(err)
	}
	msgs := []provider.Message{
		{Role: provider.RoleUser, Content: "one"},
		{Role: provider.RoleTool, Content: "kept tool result", ToolCallID: "c1", ToolName: "read"},
		{Role: provider.RoleUser, Content: "two"},
		{Role: provider.RoleAssistant, Content: "b"},
	}
	for _, msg := range msgs {
		if err := st.WriteMessage(msg); err != nil {
			t.Fatal(err)
		}
	}
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}
	st, err = session.Open(dir, "bat")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })

	a := agent.New("bat", provider.NewMock("mock", "chat"), "mock-large", nil,
		agent.NewConversation("system"))
	t.Cleanup(a.Close)
	a.Conv.Append(msgs...)

	m := NewModel(a, HeaderState{SessionName: "bat", Model: "mock-large"})
	m.width, m.height = 100, 40
	m.WithSessions(dir, "", st)

	// Rewind to the "two" point: this keeps "one" and its tool result, and
	// discards "two" and "b" — zero tool calls in the pruned stretch.
	m.runRewind("2")

	got := m.agent.Conv.Messages()
	summary := got[len(got)-1].Content
	if !strings.Contains(summary, "0 tool call(s)") {
		t.Fatalf("collapse summary miscounted a kept tool result as discarded: %q", summary)
	}
}
