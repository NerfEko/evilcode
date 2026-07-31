package tui

import (
	"strings"
	"testing"

	"evilcode/internal/agent"
)

// H2.16: /compact and /rewind rewrite the session log and reset the
// conversation. Neither checked whether a turn was in flight, so an in-flight
// turn kept appending across the reset and its messages landed after the
// rewrite — in a conversation that no longer contains what they answer.
func TestCompactAndRewindRefuseDuringATurn(t *testing.T) {
	for _, tc := range []struct {
		name string
		run  func(m *Model)
	}{
		{"compact", func(m *Model) { m.runCompact() }},
		{"rewind", func(m *Model) { m.runRewind("1") }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m := newTestModel(t)
			m.processing = true
			before := m.agent.Conv.Len()

			tc.run(m)

			if m.agent.Conv.Len() != before {
				t.Errorf("the conversation changed while a turn was in flight")
			}
			if !strings.Contains(strings.ToLower(m.notice), "turn") {
				t.Errorf("notice = %q, want it to say a turn is in flight", m.notice)
			}
		})
	}
}

// H2.17: /plan cancelled the turn in flight and submitted its own immediately,
// without waiting for the cancelled one to end. Since the agent refuses a second
// concurrent turn, the plan prompt was dropped — and a hidden prompt has nobody
// to report the refusal to.
func TestPlanDuringATurnWaitsForItToEnd(t *testing.T) {
	m := newTestModel(t)
	m.processing = true

	m.commandArg = "the auth flow"
	m.runCommand("plan")

	if m.queuedHidden == "" {
		t.Fatal("the plan prompt was sent into a running turn rather than queued")
	}
	if !strings.Contains(m.queuedHidden, "planning mode") {
		t.Errorf("queued prompt is not the plan prompt: %q", oneLine(m.queuedHidden))
	}

	// The turn it interrupted ends: now it starts.
	m.processing = false
	m.applyEvent(agent.Event{Kind: agent.EventTurnEnd, Reason: agent.EndInterrupted})

	if m.queuedHidden != "" {
		t.Error("the queued prompt is still waiting after the turn ended")
	}
	if !strings.Contains(m.hiddenPrompt, "planning mode") {
		t.Errorf("the queued plan did not start; hidden prompt = %q", oneLine(m.hiddenPrompt))
	}
}
