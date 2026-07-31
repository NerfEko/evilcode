package session

import (
	"strings"
	"testing"

	"evilcode/internal/provider"
)

// H1.1: Compact and Rewind rewrite the log through a temp file and a rename, so
// a Store held open across the call keeps an O_APPEND descriptor pointing at the
// orphaned pre-rename inode. Everything appended afterwards goes to a file with
// no name and vanishes when the descriptor closes.
func TestAppendAfterCompactSurvivesResume(t *testing.T) {
	dir := t.TempDir()
	store, err := Create(dir)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.WriteMessage(provider.Message{Role: provider.RoleUser, Content: "wire the auth flow"}); err != nil {
		t.Fatal(err)
	}
	name := store.Name

	if _, err := Compact(dir, name, "we wired auth"); err != nil {
		t.Fatal(err)
	}
	if err := store.Reopen(); err != nil {
		t.Fatal(err)
	}
	if err := store.WriteMessage(provider.Message{Role: provider.RoleUser, Content: "now the retry gate"}); err != nil {
		t.Fatal(err)
	}
	store.Close()

	_, resumed, err := Resume(dir, name)
	if err != nil {
		t.Fatal(err)
	}
	var found bool
	for _, m := range resumed {
		if strings.Contains(m.Content, "retry gate") {
			found = true
		}
	}
	if !found {
		t.Fatalf("message appended after compaction is gone; resumed = %v", resumed)
	}
}

func TestAppendAfterRewindSurvivesResume(t *testing.T) {
	dir := t.TempDir()
	store, err := Create(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range []string{"first", "second", "third"} {
		if err := store.WriteMessage(provider.Message{Role: provider.RoleUser, Content: c}); err != nil {
			t.Fatal(err)
		}
	}
	name := store.Name

	points, err := RewindPoints(store.Path)
	if err != nil {
		t.Fatal(err)
	}
	if len(points) != 3 {
		t.Fatalf("rewind points = %d, want 3", len(points))
	}
	if _, err := Rewind(dir, name, points[1].Entry); err != nil {
		t.Fatal(err)
	}
	if err := store.Reopen(); err != nil {
		t.Fatal(err)
	}
	if err := store.WriteMessage(provider.Message{Role: provider.RoleUser, Content: "after the rewind"}); err != nil {
		t.Fatal(err)
	}
	store.Close()

	_, resumed, err := Resume(dir, name)
	if err != nil {
		t.Fatal(err)
	}
	var found bool
	for _, m := range resumed {
		if m.Content == "after the rewind" {
			found = true
		}
	}
	if !found {
		t.Fatalf("message appended after rewind is gone; resumed = %v", resumed)
	}
}
