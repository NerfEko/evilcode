package tui

import (
	"strings"
	"testing"
)

// E2: submitting with staged images on a model that cannot see them must
// block the submission and retain everything — the editor text, the
// attachments, and the prompt count. The old path cleared the attachments,
// sent the [image N] placeholders to a model that would never receive the
// bytes, and started a turn anyway.
func TestSubmitBlocksWhenVisionUnavailable(t *testing.T) {
	m := newTestModel(t)
	if _, err := m.attachImage(testPNG, "shot.png"); err != nil {
		t.Fatal(err)
	}
	m.editor.Insert("what is in this image? [image 1]")
	before := m.promptCount
	m.submit(m.editor.Text, 60)

	if m.promptCount != before {
		t.Errorf("prompt count advanced %d -> %d, want the submission blocked", before, m.promptCount)
	}
	if len(m.blocks) != 0 {
		t.Errorf("blocks = %+v, want none — nothing may be sent", m.blocks)
	}
	if len(m.attachments) != 1 {
		t.Errorf("attachments = %d, want the staged image retained", len(m.attachments))
	}
	if got := m.editor.Text; !strings.Contains(got, "[image 1]") {
		t.Errorf("editor text = %q, want the placeholder retained so the user can detach it", got)
	}
	if !strings.Contains(m.notice, "cannot see images") {
		t.Errorf("notice = %q, want the vision guidance", m.notice)
	}
}

// With vision enabled the same submission proceeds and clears the
// attachments, as before.
func TestSubmitProceedsWithVisionEnabled(t *testing.T) {
	m := newTestModel(t)
	m.WithVision(true)
	if _, err := m.attachImage(testPNG, "shot.png"); err != nil {
		t.Fatal(err)
	}
	m.editor.Insert("what is this? [image 1]")
	m.submit(m.editor.Text, 60)

	if m.promptCount != 1 {
		t.Errorf("prompt count = %d, want 1", m.promptCount)
	}
	if len(m.attachments) != 0 {
		t.Errorf("attachments = %d, want cleared after take", len(m.attachments))
	}
	if len(m.blocks) != 1 || m.blocks[0].Kind != BlockUser {
		t.Errorf("blocks = %+v, want the user block", m.blocks)
	}
}
