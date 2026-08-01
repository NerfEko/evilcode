package tui

import (
	"reflect"
	"testing"
)

// TestFlushPendingDoesNotResendDeliveredInterleaves is the regression for the
// duplicate-message bug: sending while a turn runs used to interject the text
// immediately *and* stage it as pending, then flushPending submitted the
// staged copy again at turn end, so the agent saw the message twice.
func TestFlushPendingDoesNotResendDeliveredInterleaves(t *testing.T) {
	m := newTestModel(t)
	m.processing = true
	m.editor.Text = "hello"
	m.editor.Cursor = len([]rune("hello"))

	m.send(false) // Enter in default mode while processing → Interleave

	if len(m.pending) != 1 || m.pending[0].Kind != PendingSent {
		t.Fatalf("pending = %+v, want one PendingSent receipt", m.pending)
	}
	if got := m.agent.PendingInterrupts(); got != 1 {
		t.Fatalf("agent has %d queued interrupts, want 1 (delivered exactly once)", got)
	}

	count := m.promptCount
	m.flushPending()
	if m.promptCount != count {
		t.Errorf("flushPending resubmitted a delivered interleave: prompt %d → %d",
			count, m.promptCount)
	}
	if len(m.pending) != 0 {
		t.Errorf("pending = %+v, want cleared after flush", m.pending)
	}
}

// TestFlushPendingSubmitsQueuedMessages pins the other half: messages staged
// with queue mode *were* never delivered, so they must go out when the turn
// ends.
func TestFlushPendingSubmitsQueuedMessages(t *testing.T) {
	m := newTestModel(t)
	m.processing = true
	m.queueMode = true
	m.editor.Text = "later"
	m.editor.Cursor = len([]rune("later"))

	m.send(false) // queue mode + Enter → Queue

	if len(m.pending) != 1 || m.pending[0].Kind != PendingQueued {
		t.Fatalf("pending = %+v, want one PendingQueued", m.pending)
	}
	if got := m.agent.PendingInterrupts(); got != 0 {
		t.Fatalf("agent has %d interrupts; queuing must not touch the live turn", got)
	}

	count := m.promptCount
	m.flushPending()
	if m.promptCount != count+1 {
		t.Errorf("promptCount = %d, want %d (queued message submitted once)",
			m.promptCount, count+1)
	}
}

func TestPendingToResendSkipsReceipts(t *testing.T) {
	got := pendingToResend([]PendingMessage{
		{Kind: PendingSent, Text: "delivered"},
		{Kind: PendingQueued, Text: "waits"},
		{Kind: PendingInterleave, Text: "staged"},
	})
	want := []string{"waits", "staged"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("pendingToResend = %v, want %v", got, want)
	}
	if got := pendingToResend(nil); len(got) != 0 {
		t.Errorf("pendingToResend(nil) = %v, want empty", got)
	}
}

// TestCtrlEnterTogglesQueueMode is the control-path fix: Ctrl+Enter changes the
// send mode (queue on/off), giving a persistent way to keep messages from
// reaching the live turn, instead of only a per-message opposite.
func TestCtrlEnterTogglesQueueMode(t *testing.T) {
	km, problems := NewKeymap(nil)
	if len(problems) != 0 {
		t.Fatalf("keymap problems: %v", problems)
	}
	for _, k := range []string{"ctrl+enter", "ctrl+t", "ctrl+tab"} {
		b, ok := km.Lookup(k)
		if !ok {
			t.Errorf("%s is not bound", k)
			continue
		}
		if b.Action != ActionQueueMode {
			t.Errorf("%s → %s, want %s", k, b.Action, ActionQueueMode)
		}
	}
}
