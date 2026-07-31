package tui

import (
	"os"
	"strings"
	"testing"
)

func writeFile(path string, data []byte) error { return os.WriteFile(path, data, 0o644) }

var testPNG = []byte("\x89PNG\r\n\x1a\n" + strings.Repeat("x", 64))

func TestAttachStagesAndClears(t *testing.T) {
	// An attachment travels with exactly one message; a second prompt must not
	// silently resend the first one's images.
	m := newTestModel(t)
	if _, err := m.attachImage(testPNG, "shot.png"); err != nil {
		t.Fatal(err)
	}
	if got := m.TakeAttachments(); len(got) != 1 {
		t.Fatalf("took %d images, want 1", len(got))
	}
	if got := m.TakeAttachments(); got != nil {
		t.Errorf("a second take returned %d images", len(got))
	}
}

func TestAttachNumbersPlaceholders(t *testing.T) {
	m := newTestModel(t)
	for i, want := range []string{"[image 1]", "[image 2]"} {
		got, err := m.attachImage(testPNG, "")
		if err != nil {
			t.Fatal(err)
		}
		if got != want {
			t.Errorf("attachment %d placeholder = %q, want %q", i+1, got, want)
		}
	}
}

func TestAttachRefusesAnOversizeImage(t *testing.T) {
	// Big images are slow to *transmit* over a pty, not slow to encode, and
	// there is no way to interrupt a stalled render.
	m := newTestModel(t)
	if _, err := m.attachImage(make([]byte, MaxImageBytes+1), ""); err == nil {
		t.Error("an oversize image was accepted")
	}
}

func TestAttachIsBounded(t *testing.T) {
	m := newTestModel(t)
	for i := 0; i < MaxAttachments; i++ {
		if _, err := m.attachImage(testPNG, ""); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := m.attachImage(testPNG, ""); err == nil {
		t.Errorf("accepted more than %d attachments", MaxAttachments)
	}
}

func TestDropNoticeNamesWhatArrived(t *testing.T) {
	cases := map[string]string{
		dropNotice(2, 1): "Dropped 2 images and 1 file",
		dropNotice(1, 0): "Dropped 1 image",
		dropNotice(0, 3): "Dropped 3 files",
		dropNotice(0, 0): "",
	}
	for got, want := range cases {
		if got != want {
			t.Errorf("notice = %q, want %q", got, want)
		}
	}
}

func TestDropAttachesImagesAndInsertsOtherPaths(t *testing.T) {
	m := newTestModel(t)
	dir := t.TempDir()
	img := dir + "/shot.png"
	if err := writeFile(img, testPNG); err != nil {
		t.Fatal(err)
	}
	m.DropPaths([]string{img, "/etc/hosts"})

	if len(m.attachments) != 1 {
		t.Errorf("attached %d images, want the png only", len(m.attachments))
	}
	if got := m.editor.Text; !strings.Contains(got, "/etc/hosts") {
		t.Errorf("the non-image path was not inserted: %q", got)
	}
	if got := m.editor.Text; !strings.Contains(got, "[image 1]") {
		t.Errorf("the image placeholder was not inserted: %q", got)
	}
}

func TestVisionGuardDefaultsOff(t *testing.T) {
	// Sending bytes to a text-only model fails deep inside the provider with a
	// message that explains nothing, so this is opt-in per model.
	m := newTestModel(t)
	if m.visionOK() {
		t.Error("vision is on by default")
	}
	m.WithVision(true)
	if !m.visionOK() {
		t.Error("WithVision did not take")
	}
}
