package session

import (
	"bytes"
	"os"
	"strings"
	"testing"

	"evilcode/internal/provider"
)

// H3.5: raw image bytes were persisted into the transcript. Four attachments at
// the 4 MiB limit exceed the session reader's 16 MiB record cap, so the line
// cannot be parsed back — and a session that cannot be read is a session that
// cannot be resumed. The images are not the loss; everything after them is.
func TestALargeAttachmentDoesNotMakeASessionUnresumable(t *testing.T) {
	dir := t.TempDir()
	store, err := Create(dir)
	if err != nil {
		t.Fatal(err)
	}
	name := store.Name

	// Four maximum-size attachments, as the TUI would allow.
	images := make([][]byte, 4)
	for i := range images {
		images[i] = bytes.Repeat([]byte{byte('a' + i)}, 4<<20)
	}
	if err := store.WriteMessage(provider.Message{
		Role: provider.RoleUser, Content: "what is in these?", Images: images,
	}); err != nil {
		t.Fatal(err)
	}
	if err := store.WriteMessage(provider.Message{
		Role: provider.RoleAssistant, Content: "four solid colours",
	}); err != nil {
		t.Fatal(err)
	}
	store.Close()

	_, msgs, err := Resume(dir, name)
	if err != nil {
		t.Fatal(err)
	}
	if len(msgs) != 2 {
		t.Fatalf("resume replayed %d of 2 messages; the record with the images "+
			"could not be read back", len(msgs))
	}
	if msgs[1].Content != "four solid colours" {
		t.Errorf("the message after the attachments is %q", msgs[1].Content)
	}

	// The bytes live beside the log rather than in it.
	blobs, err := os.ReadDir(strings.TrimSuffix(store.Path, ".jsonl") + ".blobs")
	if err != nil {
		t.Fatalf("the attachments were not written beside the log: %v", err)
	}
	if len(blobs) != len(images) {
		t.Errorf("%d blobs on disk for %d attachments", len(blobs), len(images))
	}

	// The attachments themselves survive as references the reader can resolve.
	if got := len(msgs[0].Images); got != len(images) {
		t.Fatalf("resumed with %d of %d images", got, len(images))
	}
	for i, img := range msgs[0].Images {
		if !bytes.Equal(img, images[i]) {
			t.Errorf("image %d came back with %d bytes, want %d", i, len(img), len(images[i]))
		}
	}
}
