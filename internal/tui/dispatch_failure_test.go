package tui

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

// R2-03: a dispatch the runtime refuses must not look like a submitted turn.
// submit used to commit the user row, clear the editor, and consume the staged
// attachments before the send, and dispatchTurn dropped every non-busy error —
// so a refused daemon connection presented as a lost prompt with lost images.

func TestFailedDispatchRestoresPromptAndAttachments(t *testing.T) {
	m := newTestModel(t).WithVision(true)
	boom := errors.New("the daemon refused the frame")
	m.agent.Forward = func(ctx context.Context, text string) error { return boom }

	if _, err := m.attachImage(testPNG, "shot.png"); err != nil {
		t.Fatal(err)
	}
	m.editor.Insert("look at this [image 1]")
	m.submit(m.editor.Text, 60)

	// submit commits first; the rollback arrives through the failure channel.
	select {
	case f := <-m.dispatchFailures:
		if f.err != boom || f.queued {
			t.Fatalf("failure = %+v", f)
		}
		if len(f.images) != 1 {
			t.Fatalf("failure carried %d images, want the staged one", len(f.images))
		}
		nm, _ := m.Update(f)
		m = nm.(*Model)
	case <-time.After(5 * time.Second):
		t.Fatal("dispatch failure never arrived")
	}

	if len(m.blocks) != 0 {
		t.Errorf("the refused prompt's user row is still committed: %+v", m.blocks)
	}
	if m.promptCount != 0 {
		t.Errorf("prompt count = %d, want the row uncommitted", m.promptCount)
	}
	if !strings.Contains(m.editor.Text, "look at this") {
		t.Errorf("editor = %q, want the prompt text restored", m.editor.Text)
	}
	if len(m.attachments) != 1 || !strings.Contains(m.editor.Text, "[image 1]") {
		t.Errorf("attachments = %d, editor = %q; want the image staged again", len(m.attachments), m.editor.Text)
	}
	if !strings.Contains(m.notice, "never reached the model") || !strings.Contains(m.notice, "refused the frame") {
		t.Errorf("notice = %q, want the failure surfaced", m.notice)
	}
}

func TestFailedQueuedDispatchLeavesNoGhostEntry(t *testing.T) {
	m := newTestModel(t)
	boom := errors.New("the socket is gone")
	m.agent.Forward = func(ctx context.Context, text string) error { return boom }
	m.agent.SetRunning(true) // the submit takes the queued path

	m.editor.Insert("follow up")
	m.submit("follow up", 60)
	if len(m.queuedTexts) != 1 {
		t.Fatalf("submit did not queue: %+v", m.queuedTexts)
	}

	select {
	case f := <-m.dispatchFailures:
		nm, _ := m.Update(f)
		m = nm.(*Model)
	case <-time.After(5 * time.Second):
		t.Fatal("dispatch failure never arrived")
	}

	if len(m.queuedTexts) != 0 {
		t.Errorf("queued entries = %+v, want the failed prompt removed", m.queuedTexts)
	}
	if got := m.editor.Text; got != "follow up" {
		t.Errorf("editor = %q, want the queued text restored", got)
	}
	if !strings.Contains(m.notice, "never reached the model") {
		t.Errorf("notice = %q, want the failure surfaced", m.notice)
	}
}

// A dispatch failure must not disturb a prompt that was fine: the rollback
// only removes the row it committed.
func TestFailedDispatchKeepsEarlierTranscript(t *testing.T) {
	m := newTestModel(t).WithVision(true)
	boom := errors.New("nope")
	m.agent.Forward = func(ctx context.Context, text string) error { return boom }
	m.submit("earlier prompt", 0)
	// Let the first (also failing) dispatch drain, then resend.
	<-m.dispatchFailures

	if _, err := m.attachImage(testPNG, "shot.png"); err != nil {
		t.Fatal(err)
	}
	m.editor.Insert("second prompt [image 1]")
	m.submit(m.editor.Text, 60)
	select {
	case f := <-m.dispatchFailures:
		nm, _ := m.Update(f)
		m = nm.(*Model)
	case <-time.After(5 * time.Second):
		t.Fatal("dispatch failure never arrived")
	}

	if len(m.blocks) != 1 || m.blocks[0].Text != "earlier prompt" {
		t.Errorf("blocks = %+v, want only the earlier prompt left", m.blocks)
	}
	if m.promptCount != 1 {
		t.Errorf("prompt count = %d, want 1", m.promptCount)
	}
}
