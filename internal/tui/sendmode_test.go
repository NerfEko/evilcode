package tui

import (
	"context"
	"testing"
	"time"

	"evilcode/internal/agent"
)

// TestSendWhileProcessingQueues is the regression for the duplicate-message
// bug: sending while a turn ran used to deliver the text into the live turn
// immediately *and* stage it as pending, so the agent saw it twice. The
// immediate path is gone — while processing, send() only stages, and the
// message reaches the model once, at turn end.
func TestSendWhileProcessingQueues(t *testing.T) {
	m := newTestModel(t)
	m.processing = true
	m.editor.Text = "hello"
	m.editor.Cursor = len([]rune("hello"))

	m.send()

	if len(m.pending) != 1 || m.pending[0].Kind != PendingQueued {
		t.Fatalf("pending = %+v, want one PendingQueued", m.pending)
	}
	if m.pending[0].Text != "hello" {
		t.Errorf("staged text = %q, want %q", m.pending[0].Text, "hello")
	}
	// Nothing reached the live turn.
	if got := m.agent.PendingInterrupts(); got != 0 {
		t.Fatalf("agent has %d queued interrupts; processing must only stage", got)
	}
	if m.editor.Text != "" {
		t.Errorf("composer not cleared: %q", m.editor.Text)
	}

	count := m.promptCount
	m.flushPending()
	if m.promptCount != count+1 {
		t.Errorf("promptCount = %d, want %d (queued message submitted once at turn end)",
			m.promptCount, count+1)
	}
	if len(m.pending) != 0 {
		t.Errorf("pending = %+v, want cleared after flush", m.pending)
	}
}

// An attached window sends queued input to the daemon immediately. Keeping it
// in the TUI's pending slice would make closing the window discard text the
// daemon had never received.
func TestRemoteSendWhileProcessingUsesServerQueue(t *testing.T) {
	m := newTestModel(t)
	m.processing = true
	m.remoteCommand = func(string, string, string) error { return nil }
	received := make(chan string, 1)
	m.agent.Forward = func(_ context.Context, text string) error {
		received <- text
		return nil
	}
	m.editor.Text = "keep working"
	m.editor.Cursor = len([]rune(m.editor.Text))

	m.send()

	select {
	case got := <-received:
		if got != "keep working" {
			t.Fatalf("forwarded text = %q, want queued prompt", got)
		}
	case <-time.After(time.Second):
		t.Fatal("remote queue was not sent to the daemon")
	}
	if len(m.pending) != 0 {
		t.Fatalf("remote pending = %+v, want the daemon to own the queue", m.pending)
	}
}

func TestRemoteInterruptUsesServerBridge(t *testing.T) {
	m := newTestModel(t)
	m.processing = true
	called := false
	m.remoteInterrupt = func(disarm bool) error {
		called = disarm
		return nil
	}

	m.interrupt(true)

	if !called {
		t.Fatal("remote interrupt bridge was not called")
	}
	if m.notice != "Interrupting..." {
		t.Fatalf("notice = %q, want interrupt status", m.notice)
	}
}

// TestMultipleQueuedMessagesFlushTogether pins the batch: everything staged
// during a turn goes out in one prompt at turn end, in the order staged.
func TestMultipleQueuedMessagesFlushTogether(t *testing.T) {
	m := newTestModel(t)
	m.processing = true

	for _, text := range []string{"first", "second"} {
		m.editor.Text = text
		m.editor.Cursor = len([]rune(text))
		m.send()
	}
	if len(m.pending) != 2 {
		t.Fatalf("pending = %+v, want 2 staged", m.pending)
	}

	count := m.promptCount
	m.flushPending()
	if m.promptCount != count+1 {
		t.Fatalf("promptCount = %d, want %d (one batched prompt)", m.promptCount, count+1)
	}
	last := m.blocks[len(m.blocks)-1]
	if last.Kind != BlockUser || last.Text != "first\n\nsecond" {
		t.Errorf("flushed block = kind %v text %q, want user \"first\\n\\nsecond\"",
			last.Kind, last.Text)
	}
}

// TestSendWhileIdleSubmits pins the other half: queueing only applies while a
// turn is running; at rest Enter starts a turn as before.
func TestSendWhileIdleSubmits(t *testing.T) {
	m := newTestModel(t)
	m.editor.Text = "go"
	m.editor.Cursor = len([]rune("go"))

	count := m.promptCount
	m.send()

	if len(m.pending) != 0 {
		t.Errorf("idle send staged %d message(s), want none", len(m.pending))
	}
	if m.promptCount != count+1 {
		t.Errorf("promptCount = %d, want %d (idle send must submit)", m.promptCount, count+1)
	}
}

// TestInterruptKeepsQueuedMessages pins that Esc does not drop staged text:
// the messages were never delivered, and the interrupted turn's TurnEnd flushes
// them as the next turn.
func TestInterruptKeepsQueuedMessages(t *testing.T) {
	m := newTestModel(t)
	m.processing = true
	m.editor.Text = "fix the test"
	m.editor.Cursor = len([]rune("fix the test"))
	m.send()
	if len(m.pending) != 1 {
		t.Fatalf("pending = %+v, want 1 staged", m.pending)
	}

	m.interrupt(false)
	if len(m.pending) != 1 {
		t.Fatalf("interrupt cleared pending: %+v", m.pending)
	}

	count := m.promptCount
	m.applyEvent(agent.Event{Kind: agent.EventTurnEnd})
	if m.promptCount != count+1 {
		t.Errorf("promptCount = %d, want %d (queued message flushed after interrupt)",
			m.promptCount, count+1)
	}
}

// TestQueueModeBindingIsGone pins the removal: the mode toggle no longer
// exists because there is only one send mode while processing — queue.
func TestQueueModeBindingIsGone(t *testing.T) {
	km, problems := NewKeymap(nil)
	if len(problems) != 0 {
		t.Fatalf("keymap problems: %v", problems)
	}
	for _, k := range []string{"ctrl+t", "ctrl+tab", "ctrl+enter"} {
		if _, ok := km.Lookup(k); ok {
			t.Errorf("%s is still bound to a queue-mode toggle", k)
		}
	}
}
