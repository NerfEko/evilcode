package session

import (
	"fmt"
	"path/filepath"
	"strings"
	"sync"
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

// An append racing the rewrite must not fall into the hole between the rewrite
// reading the log and the store reopening on the result. Codex found this
// reviewing the first fix: reopening afterwards closes the window for later
// appends, not for concurrent ones.
//
// The detector is the backup. Compaction copies the log to `.bak` before
// replacing it, so every message it erases is still recorded there. A message
// that is in neither the backup nor the compacted file was never erased — it
// was written to the orphaned inode and is simply gone.
func TestAppendsRacingCompactAreNotLost(t *testing.T) {
	dir := t.TempDir()
	store, err := Create(dir)
	if err != nil {
		t.Fatal(err)
	}
	name := store.Name
	path := filepath.Join(Dir(dir), name+".jsonl")

	// A log with some bulk in it: the rewrite has to read, re-encode and write
	// every entry, and that is the window an append can fall into.
	for i := range 4000 {
		if err := store.WriteMessage(provider.Message{
			Role:    provider.RoleUser,
			Content: fmt.Sprintf("history %d %s", i, strings.Repeat("x", 200)),
		}); err != nil {
			t.Fatal(err)
		}
	}

	var mu sync.Mutex
	var wrote []string
	stop := make(chan struct{})
	done := make(chan struct{})
	go func() {
		defer close(done)
		for i := 0; ; i++ {
			select {
			case <-stop:
				return
			default:
			}
			c := fmt.Sprintf("racer %04d", i)
			if err := store.WriteMessage(provider.Message{Role: provider.RoleUser, Content: c}); err != nil {
				return
			}
			mu.Lock()
			wrote = append(wrote, c)
			mu.Unlock()
		}
	}()

	if _, err := store.Compact(dir, "summary"); err != nil {
		t.Fatal(err)
	}
	close(stop)
	<-done
	store.Close()

	seen := map[string]bool{}
	for _, p := range []string{path, path + ".bak"} {
		msgs, err := Messages(p)
		if err != nil {
			t.Fatal(err)
		}
		for _, m := range msgs {
			seen[m.Content] = true
		}
	}

	mu.Lock()
	defer mu.Unlock()
	for _, c := range wrote {
		if !seen[c] {
			t.Fatalf("%q was appended successfully but is in neither the compacted "+
				"log nor its backup: it went to the orphaned inode (%d of %d racers)",
				c, len(wrote), len(wrote))
		}
	}
}
