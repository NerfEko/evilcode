package todo

import (
	"os"
	"path/filepath"
	"testing"
)

// H1.11: a transaction mutated live state and then wrote four files in
// sequence. A failure on the third leaves memory holding a write that two files
// on disk record and two do not, and the store keeps serving the version that
// was never fully persisted.
func TestAFailedWriteLeavesMemoryMatchingDisk(t *testing.T) {
	dir := t.TempDir()
	store, err := NewStore(dir, "swarm")
	if err != nil {
		t.Fatal(err)
	}

	if _, err := store.Apply(Write{
		Items: []Item{{ID: "1", Content: "wire the auth flow", Status: StatusPending}},
	}); err != nil {
		t.Fatal(err)
	}

	// Nothing can be written from here on, and everything already written is
	// still readable — the same shape as a full disk, and unlike deleting the
	// files it leaves the on-disk state a restart would actually replay.
	todos := filepath.Join(dir, "todos")
	if err := os.Chmod(todos, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chmod(todos, 0o700) })

	if _, err := store.Apply(Write{
		Items: []Item{
			{ID: "1", Content: "wire the auth flow", Status: StatusPending},
			{ID: "2", Content: "add the retry gate", Status: StatusPending},
		},
	}); err == nil {
		t.Fatal("the write reported success even though a file could not be written")
	}

	// Whatever the store now serves must be what a restart would replay.
	reopened, err := NewStore(dir, "swarm")
	if err != nil {
		t.Fatal(err)
	}
	live, onDisk := store.Items(), reopened.Items()
	if len(live) != len(onDisk) {
		t.Fatalf("the store serves %d items, a restart replays %d: "+
			"the failed write is live in memory and absent from disk", len(live), len(onDisk))
	}
	for i := range live {
		if live[i].Content != onDisk[i].Content {
			t.Errorf("item %d: memory %q, disk %q", i, live[i].Content, onDisk[i].Content)
		}
	}
}

func TestRenameMovesStateWithoutChangingAnotherNamespace(t *testing.T) {
	dir := t.TempDir()
	first, err := NewStore(dir, "bat")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := first.Apply(Write{
		Items: []Item{{ID: "1", Content: "keep this plan", Status: StatusPending}},
	}); err != nil {
		t.Fatal(err)
	}
	second, err := NewStore(dir, "wolf")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := second.Apply(Write{
		Items: []Item{{ID: "2", Content: "do not inherit this", Status: StatusPending}},
	}); err != nil {
		t.Fatal(err)
	}

	if err := first.Rename("raven"); err != nil {
		t.Fatal(err)
	}
	if first.Session != "raven" {
		t.Fatalf("renamed store session = %q", first.Session)
	}
	reopened, err := NewStore(dir, "raven")
	if err != nil {
		t.Fatal(err)
	}
	if got := reopened.Items(); len(got) != 1 || got[0].Content != "keep this plan" {
		t.Fatalf("renamed state = %v", got)
	}
	if got := second.Items(); len(got) != 1 || got[0].Content != "do not inherit this" {
		t.Fatalf("other namespace changed = %v", got)
	}
	if _, err := os.Stat(filepath.Join(dir, "todos", "bat.json")); !os.IsNotExist(err) {
		t.Fatalf("old namespace still exists: %v", err)
	}
}
