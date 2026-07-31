package session

import (
	"os"
	"path/filepath"
	"testing"
)

// A session written before attachments moved out of line still holds `images`
// as inline base64. Decoding must not fail on it — a message that fails to
// decode is skipped, so an old session with a screenshot in it would come back
// missing that turn entirely.
func TestLegacyInlineImagesStillDecode(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(Dir(dir), 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(Dir(dir), "old.jsonl")

	// Exactly what the old code wrote: images inline as base64.
	lines := `{"ts":"2026-07-30T10:00:00Z","type":"user","data":{"role":"user","content":"what is this?","images":["aGVsbG8="]}}
{"ts":"2026-07-30T10:00:01Z","type":"assistant","data":{"role":"assistant","content":"a greeting"}}
`
	if err := os.WriteFile(path, []byte(lines), 0o644); err != nil {
		t.Fatal(err)
	}

	msgs, err := Messages(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(msgs) != 2 {
		t.Fatalf("replayed %d of 2 messages; a legacy inline attachment dropped its turn", len(msgs))
	}
	if msgs[0].Content != "what is this?" {
		t.Errorf("first message = %q", msgs[0].Content)
	}
	if len(msgs[0].Images) != 1 || string(msgs[0].Images[0]) != "hello" {
		t.Errorf("the inline image did not survive: %v", msgs[0].Images)
	}
}
