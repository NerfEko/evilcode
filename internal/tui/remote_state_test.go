package tui

import (
	"errors"
	"testing"

	"evilcode/internal/agent"
	"evilcode/internal/provider"
	"evilcode/internal/tools"
)

func TestRemoteModelFailureLeavesTheMirrorUntouched(t *testing.T) {
	m := newTestModel(t)
	m.header.Model = "old"
	m.header.Provider = "mock"
	m.agent.Model = "old"
	m.remoteModel = func(string) error { return errors.New("busy") }

	m.applyModel(ModelEntry{Name: "new", Provider: "mock"})
	if m.header.Model != "old" || m.agent.Model != "old" {
		t.Fatalf("failed remote switch changed mirror to %q/%q", m.header.Model, m.agent.Model)
	}
}

func TestRemoteAskResolutionClearsEveryLocalPicker(t *testing.T) {
	m := newTestModel(t)
	m.SetRemoteAsk(agent.AskEvent{
		ID: "ask-1", Question: "choose", Options: []tools.AskOption{{Label: "a"}, {Label: "b"}},
	})
	m.applyEvent(agent.Event{Kind: agent.EventAskResolved, RequestID: "ask-1"})
	if m.pendingAsk.Get() != nil || m.ask != nil {
		t.Fatal("remote ask remained visible after resolution")
	}
}

func TestHiddenTurnStartDoesNotDrawAUserPrompt(t *testing.T) {
	m := newTestModel(t)
	m.hiddenPrompt = "private instruction"
	m.applyEvent(agent.Event{Kind: agent.EventTurnStart, Hidden: true})
	for _, block := range m.blocks {
		if block.Kind == BlockUser {
			t.Fatalf("hidden turn drew a user block: %+v", block)
		}
	}
	if m.hiddenPrompt != "" {
		t.Fatalf("hiddenPrompt = %q, want cleared", m.hiddenPrompt)
	}
}

func TestRemoteModelEventOwnsVisionAndReasoningState(t *testing.T) {
	m := newTestModel(t).WithVision(true)
	m.applyEvent(agent.Event{
		Kind: agent.EventModel, Model: "text-only", Provider: "mock",
		ReasoningEffortKnown: true, ReasoningEfforts: []string{string(provider.ReasoningEffortNone)},
		VisionKnown: true, Vision: false,
	})
	if m.visionOK() || m.header.Model != "text-only" || m.header.ReasoningEffort != provider.ReasoningEffortNone {
		t.Fatalf("remote model state = vision:%v model:%q effort:%q", m.visionOK(), m.header.Model, m.header.ReasoningEffort)
	}
}
