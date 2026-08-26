package tui

import (
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
)

func TestTypingTimerStartsOnFirstKeyAndRestartsAfterEmpty(t *testing.T) {
	m := newTestModel(t)
	if _, _ = m.handleKey(tea.KeyPressMsg(tea.Key{Code: 'a', Text: "a"})); m.typingStarted.IsZero() {
		t.Fatal("typing timer did not start on the first keystroke")
	}
	first := m.typingStarted

	_, _ = m.handleKey(tea.KeyPressMsg(tea.Key{Code: tea.KeyBackspace}))
	if m.editor.Text != "" {
		t.Fatalf("editor = %q after deleting its only character", m.editor.Text)
	}
	if !m.typingStarted.IsZero() {
		t.Fatal("typing timer survived an empty composer")
	}

	_, _ = m.handleKey(tea.KeyPressMsg(tea.Key{Code: 'b', Text: "b"}))
	if m.typingStarted.IsZero() {
		t.Fatal("typing timer did not restart after the composer became non-empty")
	}
	if !m.typingStarted.After(first) {
		t.Fatalf("restarted timer = %v, first timer = %v", m.typingStarted, first)
	}
}

func TestTypedPromptCarriesWPMToTheTranscript(t *testing.T) {
	m := newTestModel(t)
	m.typingStarted = time.Now().Add(-time.Minute)
	m.editor.Text = "one two three four five"
	m.editor.Cursor = len([]rune(m.editor.Text))

	_, _ = m.send()
	last := m.blocks[len(m.blocks)-1]
	if last.TypingWPM != 5 {
		t.Errorf("TypingWPM = %d, want 5 for five words typed in one minute", last.TypingWPM)
	}
}

func TestShortPromptDoesNotShowWPM(t *testing.T) {
	m := newTestModel(t)
	m.typingStarted = time.Now().Add(-time.Minute)
	m.editor.Text = "one two three four"
	m.editor.Cursor = len([]rune(m.editor.Text))

	_, _ = m.send()
	last := m.blocks[len(m.blocks)-1]
	if last.TypingWPM != 0 {
		t.Errorf("TypingWPM = %d, want 0 for a prompt shorter than five words", last.TypingWPM)
	}
}

func TestQueuedPromptCarriesWPMThroughFlush(t *testing.T) {
	m := newTestModel(t)
	m.processing = true
	m.typingStarted = time.Now().Add(-time.Minute)
	m.editor.Text = "one two three four five"
	m.editor.Cursor = len([]rune(m.editor.Text))

	_, _ = m.send()
	if len(m.pending) != 1 || m.pending[0].WPM != 5 {
		t.Fatalf("pending = %+v, want one message with 5 WPM", m.pending)
	}

	m.processing = false // flush runs from the TurnEnd handler, after processing clears
	m.flushPending()
	last := m.blocks[len(m.blocks)-1]
	if last.TypingWPM != 5 {
		t.Errorf("flushed TypingWPM = %d, want 5", last.TypingWPM)
	}
}

func TestUserTranscriptRendersWPMAfterMessage(t *testing.T) {
	r := testRenderer(80)
	rows := plainLines(r.Lines(&Block{
		Kind:      BlockUser,
		Text:      "one two three four five",
		Number:    1,
		TypingWPM: 87,
	}))
	joined := strings.Join(rows, "\n")
	if !strings.Contains(joined, "(87 wpm)") {
		t.Fatalf("user rows = %q, want the WPM marker after the message", joined)
	}
}

func TestDeterministicModeDoesNotExposeWallClockWPM(t *testing.T) {
	t.Setenv("EVILCODE_DETERMINISTIC", "1")
	m := newTestModel(t)
	m.typingStarted = time.Now().Add(-time.Second)
	if got := m.typingWPM("one two three four five"); got != 0 {
		t.Errorf("deterministic WPM = %d, want no wall-clock value", got)
	}
}
