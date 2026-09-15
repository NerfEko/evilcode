package tui

import (
	"testing"

	"evilcode/internal/agent"
	"evilcode/internal/provider"
)

// TestSnapshotKeepLocalPreservesTheFirstPrompt is the feedback loop for the
// deferred attach's materialization snapshot: the snapshot that names a
// session created by this window's own prompt must not wipe the prompt the
// window just drew (and its image blocks with it).
func TestSnapshotKeepLocalPreservesTheFirstPrompt(t *testing.T) {
	m := NewModel(nil, HeaderState{Model: "mock-large"})
	m.width, m.height = 90, 24
	m.dataDir = t.TempDir()
	m.blocks = []Block{{Kind: BlockUser, Text: "hello", Number: 1}}
	m.promptCount = 1

	// The materialization snapshot: a fresh session with an empty mirror.
	m.ApplyRemoteState("fresh-1", "mock-large", "mock", false, nil, nil, nil, true)
	if len(m.blocks) != 1 || m.blocks[0].Kind != BlockUser || m.blocks[0].Text != "hello" {
		t.Fatalf("keepLocal wiped the locally drawn prompt: %+v", m.blocks)
	}
	if m.header.SessionName != "fresh-1" {
		t.Errorf("session name = %q, want fresh-1", m.header.SessionName)
	}
	if m.todos == nil {
		t.Error("the first real snapshot did not bind the todo mirror")
	}

	// Without the flag the snapshot is a conversation rewrite and rebuilds,
	// which is what /rename and /compact have always needed.
	m.ApplyRemoteState("fresh-1", "mock-large", "mock", false, nil, nil, nil, false)
	if len(m.blocks) != 0 {
		t.Fatalf("a plain snapshot did not rebuild the transcript: %+v", m.blocks)
	}
}

// TestPreviewModelEventMirrorsThePicker covers the synthetic EventModel a
// pre-session model pick replies with: the header and the effort levels move
// as they would after a session-owned switch.
func TestPreviewModelEventMirrorsThePicker(t *testing.T) {
	m := NewModel(nil, HeaderState{Model: "mock-large", Provider: "mock"})
	m.width, m.height = 90, 24

	m.applyEvent(agent.Event{
		Kind:                 agent.EventModel,
		Model:                "mock-small",
		Provider:             "mock",
		ReasoningEffortKnown: true,
		ReasoningEffort:      provider.ReasoningEffortLow,
		ReasoningEfforts:     []string{"minimal", "low", "medium", "high"},
	})
	if m.header.Model != "mock-small" {
		t.Errorf("header model = %q, want mock-small", m.header.Model)
	}
	if m.reasoningEffort != provider.ReasoningEffortLow {
		t.Errorf("effort = %q, want low", m.reasoningEffort)
	}
}
