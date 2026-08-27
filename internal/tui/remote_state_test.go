package tui

import (
	"errors"
	"testing"

	"evilcode/internal/agent"
	"evilcode/internal/config"
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

// The daemon resolves the context window at model-set time (ContextWindowFor)
// and publishes it on EventModel; an attached TUI must mirror it or the meter
// renders the hardcoded 200k fallback for every model.
func TestRemoteModelEventCarriesContextWindow(t *testing.T) {
	m := newTestModel(t)
	m.applyEvent(agent.Event{
		Kind: agent.EventModel, Model: "deepseek-v4-flash:0731", Provider: "ollama-cloud",
		ContextWindow: 1048576, ContextWindowKnown: true,
	})
	if m.agent.NumCtx != 1048576 {
		t.Fatalf("agent NumCtx = %d, want 1048576", m.agent.NumCtx)
	}
	if got := m.contextMax(); got != 1048576 {
		t.Fatalf("contextMax = %d, want the event's window", got)
	}
}

// contextWindowMsg is the off-loop result of the provider ask after a model
// switch. Only the result for the model still active applies: a later switch
// makes an earlier lookup's answer stale.
func TestContextWindowMsgAppliesOnlyToCurrentModel(t *testing.T) {
	m := newTestModel(t)
	m.agent.Model = "deepseek-v4-flash:0731"
	m.header.Provider = "ollama-cloud"
	ref := config.ModelRef(m.agent.Model, m.header.Provider)

	m.Update(contextWindowMsg{ref: ref, window: 1048576})
	if m.agent.NumCtx != 1048576 {
		t.Fatalf("current model window = %d, want 1048576", m.agent.NumCtx)
	}

	// A result for a model the session has already left is discarded.
	other := config.ModelRef("glm-5.2", "ollama-cloud")
	m.Update(contextWindowMsg{ref: other, window: 262144})
	if m.agent.NumCtx != 1048576 {
		t.Fatalf("stale window overwrote the current model's: NumCtx = %d", m.agent.NumCtx)
	}
}

func TestResolveContextWindowUsesOverrideSynchronously(t *testing.T) {
	m := newTestModel(t)
	// The switch crosses to ollama-cloud, which must be buildable: a provider
	// that cannot build refuses the switch instead of half-applying (R2-13).
	m.providers = []config.ProviderConfig{
		{Name: "mock", Kind: config.KindMock},
		{Name: "ollama-cloud", Kind: config.KindOllama},
	}
	m.contextWindowOverride = func(ref string) int {
		if ref == "deepseek-v4-flash:0731@ollama-cloud" {
			return 262144
		}
		return 0
	}
	cmd := m.applyModel(ModelEntry{Name: "deepseek-v4-flash:0731", Provider: "ollama-cloud"})
	if cmd != nil {
		t.Fatal("an explicit override should resolve synchronously, not return a cmd")
	}
	if m.agent.NumCtx != 262144 {
		t.Fatalf("NumCtx = %d, want the override 262144", m.agent.NumCtx)
	}
}

func TestResolveContextWindowDefersProviderAskOffLoop(t *testing.T) {
	m := newTestModel(t) // mock provider, no override
	ref := config.ModelRef("deepseek-v4-flash:0731", "ollama-cloud")
	m.agent.Model = "deepseek-v4-flash:0731"
	m.header.Provider = "ollama-cloud"

	cmd := m.resolveContextWindow(ref)
	if cmd == nil {
		t.Fatal("no override: expected a deferred provider ask")
	}
	msg, ok := cmd().(contextWindowMsg)
	if !ok {
		t.Fatalf("cmd returned %T, want contextWindowMsg", msg)
	}
	if msg.ref != ref {
		t.Errorf("msg ref = %q, want %q", msg.ref, ref)
	}
}
