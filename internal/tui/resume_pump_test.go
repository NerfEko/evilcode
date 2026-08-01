package tui

import (
	"testing"
	"time"

	"evilcode/internal/agent"
	tea "charm.land/bubbletea/v2"
)

// TestLonePeriodPumpsNoUserBlock reproduces the live path: press "." on an
// empty composer, then drain the agent's events the way the message loop does
// and fold them in with applyEvent. The resume prompt must never render as a
// user block — the window should show only the model resuming.
func TestLonePeriodPumpsNoUserBlock(t *testing.T) {
	m := newTestModel(t)

	_, _ = m.Update(tea.KeyPressMsg(tea.Key{Code: '.', Text: "."}))

	if m.hiddenPrompt != "resume" {
		t.Fatalf("hiddenPrompt = %q, want %q", m.hiddenPrompt, "resume")
	}

	// Drain the agent's events until the turn ends, folding each into the view.
	deadline := time.After(2 * time.Second)
	for {
		select {
		case e := <-m.agent.Events():
			m.applyEvent(e)
			if e.Kind == agent.EventTurnEnd {
				goto done
			}
		case <-deadline:
			t.Fatal("timed out waiting for the turn to end")
		}
	}
done:

	for _, b := range m.blocks {
		if b.Kind == BlockUser {
			t.Errorf("resume gesture drew a user block: %+v", b)
		}
	}
}