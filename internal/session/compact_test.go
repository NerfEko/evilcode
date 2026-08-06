package session

import (
	"strings"
	"testing"

	"evilcode/internal/provider"
)

func TestCompactSurvivesResume(t *testing.T) {
	// The bug this exists for: Conversation.Compact assigned the message slice
	// directly, bypassing the session sink. The summary never reached the file
	// and the old messages stayed in it, so resuming a compacted session
	// replayed the entire uncompacted history and dropped the summary.
	dir := t.TempDir()
	store, err := Create(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, m := range []provider.Message{
		{Role: provider.RoleUser, Content: "wire the auth flow"},
		{Role: provider.RoleAssistant, Content: "reading the callback handler"},
		{Role: provider.RoleUser, Content: "now the retry gate"},
	} {
		if err := store.WriteMessage(m); err != nil {
			t.Fatal(err)
		}
	}
	name := store.Name
	store.Close()

	replay, err := Compact(dir, name, "we wired auth and added a retry gate")
	if err != nil {
		t.Fatal(err)
	}
	if len(replay) != 1 {
		t.Fatalf("compaction returned %d messages, want the summary alone", len(replay))
	}

	// The part that was broken: read it back off disk.
	_, resumed, err := Resume(dir, name)
	if err != nil {
		t.Fatal(err)
	}
	if len(resumed) != 1 {
		t.Fatalf("resume replayed %d messages, want the summary alone: %v", len(resumed), resumed)
	}
	if !strings.Contains(resumed[0].Content, "retry gate") {
		t.Errorf("resumed message = %q", resumed[0].Content)
	}
	for _, m := range resumed {
		if strings.Contains(m.Content, "reading the callback handler") {
			t.Error("pre-compaction history came back on resume")
		}
	}
}

func TestCompactWithTailSurvivesResume(t *testing.T) {
	dir := t.TempDir()
	store, err := Create(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, m := range []provider.Message{
		{Role: provider.RoleUser, Content: "old prompt"},
		{Role: provider.RoleAssistant, Content: "old answer"},
	} {
		if err := store.WriteMessage(m); err != nil {
			t.Fatal(err)
		}
	}
	name := store.Name
	store.Close()

	tail := []provider.Message{
		{Role: provider.RoleUser, Content: "current prompt"},
		{Role: provider.RoleAssistant, Content: "current answer", Images: [][]byte{[]byte("vision-tail")}},
	}
	replay, err := CompactWithTail(dir, name, "old work summary", tail)
	if err != nil {
		t.Fatal(err)
	}
	if len(replay) != 3 {
		t.Fatalf("compaction returned %d messages, want summary + two tail messages", len(replay))
	}
	if !strings.Contains(replay[0].Content, "old work summary") || replay[1].Content != "current prompt" {
		t.Fatalf("replay = %#v, want summary followed by the preserved tail", replay)
	}

	_, resumed, err := Resume(dir, name)
	if err != nil {
		t.Fatal(err)
	}
	if len(resumed) != 3 || resumed[1].Content != "current prompt" || resumed[2].Content != "current answer" {
		t.Fatalf("resumed = %#v, want summary + preserved tail", resumed)
	}
	if len(resumed[2].Images) != 1 || string(resumed[2].Images[0]) != "vision-tail" {
		t.Fatalf("resumed tail images = %#v, want the preserved image", resumed[2].Images)
	}
}

func TestCompactIsCountedForThePicker(t *testing.T) {
	// §5.4's 📦 glyph and §8.2's "≥3 compactions" warning both need this.
	dir := t.TempDir()
	store, _ := Create(dir)
	store.WriteMessage(provider.Message{Role: provider.RoleUser, Content: "hi"})
	name := store.Name
	store.Close()

	if _, err := Compact(dir, name, "first"); err != nil {
		t.Fatal(err)
	}
	if _, err := Compact(dir, name, "second"); err != nil {
		t.Fatal(err)
	}

	info, err := Describe(dir, name)
	if err != nil {
		t.Fatal(err)
	}
	if info.Compactions != 2 {
		t.Errorf("compactions = %d, want 2", info.Compactions)
	}
}

func TestCompactKeepsTheMetaHistory(t *testing.T) {
	// Losing the model and cwd would make a compacted session unresumable
	// rather than merely shorter.
	dir := t.TempDir()
	store, _ := Create(dir)
	store.WriteMeta(Meta{Kind: MetaStart, Cwd: "/some/where"})
	store.WriteMessage(provider.Message{Role: provider.RoleUser, Content: "hi"})
	name := store.Name
	store.Close()

	if _, err := Compact(dir, name, "a summary"); err != nil {
		t.Fatal(err)
	}
	info, err := Describe(dir, name)
	if err != nil {
		t.Fatal(err)
	}
	if info.Cwd != "/some/where" {
		t.Errorf("cwd = %q, want it kept through compaction", info.Cwd)
	}
}
