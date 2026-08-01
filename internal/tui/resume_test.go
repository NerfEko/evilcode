package tui

import (
	"testing"

	tea "charm.land/bubbletea/v2"
)

// periodKey synthesizes a lone "." keypress, the way a terminal delivers a
// printable character.
func periodKey() tea.KeyPressMsg {
	return tea.KeyPressMsg(tea.Key{Code: '.', Text: "."})
}

// TestLonePeriodQuickResumes sends "resume" as an invisible prompt when the
// composer is empty, without drawing a user block or leaving the period in the
// composer.
func TestLonePeriodQuickResumes(t *testing.T) {
	m := newTestModel(t)
	// A running turn makes submitHidden queue the prompt instead of starting a
	// second concurrent one (H2.3), so the assertion is deterministic.
	m.processing = true

	_, _ = m.Update(periodKey())

	if m.queuedHidden != "resume" {
		t.Fatalf("queuedHidden = %q, want %q", m.queuedHidden, "resume")
	}
	if m.editor.Text != "" {
		t.Errorf("composer = %q, want empty (the period must be swallowed)", m.editor.Text)
	}
	for _, b := range m.blocks {
		if b.Kind == BlockUser {
			t.Errorf("a user block was drawn for the resume gesture: %+v", b)
		}
	}
}

// TestPeriodTypesNormallyWithExistingText keeps the period an ordinary
// character once the composer already has text, so "fix this." and "./path"
// still type.
func TestPeriodTypesNormallyWithExistingText(t *testing.T) {
	m := newTestModel(t)
	m.editor.Text = "fix this"
	m.editor.Cursor = len([]rune(m.editor.Text))

	_, _ = m.Update(periodKey())

	if m.editor.Text != "fix this." {
		t.Fatalf("composer = %q, want %q", m.editor.Text, "fix this.")
	}
	if m.queuedHidden != "" || m.hiddenPrompt != "" {
		t.Errorf("resume gesture fired mid-message: queued=%q hidden=%q",
			m.queuedHidden, m.hiddenPrompt)
	}
}

// TestLonePeriodResumesWhileIdle covers the common case — nothing running —
// and confirms the hidden prompt is set synchronously.
func TestLonePeriodResumesWhileIdle(t *testing.T) {
	m := newTestModel(t)

	_, _ = m.Update(periodKey())

	if m.hiddenPrompt != "resume" {
		t.Fatalf("hiddenPrompt = %q, want %q", m.hiddenPrompt, "resume")
	}
	if m.editor.Text != "" {
		t.Errorf("composer = %q, want empty", m.editor.Text)
	}
	for _, b := range m.blocks {
		if b.Kind == BlockUser {
			t.Errorf("resume gesture drew a user block: %+v", b)
		}
	}
}