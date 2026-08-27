package session

import (
	"os"
	"path/filepath"
	"testing"

	"evilcode/internal/provider"
)

func TestStandaloneRenameLeavesLiveStoreStale(t *testing.T) {
	// The live store's Name and Path point at the file it has open. The
	// standalone Rename moves that file on disk but never tells the store,
	// so appends with images write blobs to the orphaned old blob dir and
	// /rewind, /fork, /save resolve a path that no longer exists. This is the
	// behaviour the TUI used to rely on; kept as a regression guard for the
	// lower-level helper.
	dir := t.TempDir()
	st, err := CreateNamed(dir, "old")
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()

	if err := Rename(dir, st.Name, "new"); err != nil {
		t.Fatal(err)
	}
	if st.Name != "old" {
		t.Errorf("Name = %q, the standalone helper must not touch the live store", st.Name)
	}
}

func TestStoreRenameKeepsIdentityAndBlobsInSync(t *testing.T) {
	// Store.Rename moves the log and blobs and updates the live store's Name
	// and Path in one locked step, so the running session keeps writing to the
	// renamed file: an image-bearing append lands its blob beside the renamed
	// log, not the orphaned old one.
	dir := t.TempDir()
	st, err := CreateNamed(dir, "old")
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()

	if err := st.Rename(dir, "new"); err != nil {
		t.Fatalf("rename: %v", err)
	}
	if st.Name != "new" {
		t.Errorf("Name = %q, want %q", st.Name, "new")
	}
	want, _ := pathFor(dir, "new")
	if st.Path != want {
		t.Errorf("Path = %q, want %q", st.Path, want)
	}
	if _, err := os.Stat(filepath.Join(Dir(dir), "old.jsonl")); err == nil {
		t.Error("old log still on disk after rename")
	}
	if _, err := os.Stat(st.Path); err != nil {
		t.Errorf("renamed log missing: %v", err)
	}
	if err := st.WriteMessage(provider.Message{
		Role:    provider.RoleUser,
		Content: "after rename",
		Images:  [][]byte{[]byte("png-bytes")},
	}); err != nil {
		t.Fatal(err)
	}
	// The blob must land beside the renamed log, and the old blob dir must
	// not have been re-created by a stale-path append.
	newBlobs, err := os.ReadDir(blobDir(st.Path))
	if err != nil || len(newBlobs) == 0 {
		t.Errorf("blob not beside renamed log: %v", err)
	}
	if _, err := os.Stat(blobDir(filepath.Join(Dir(dir), "old.jsonl"))); err == nil {
		t.Error("blob written to the orphaned old blob dir — the live store is stale")
	}
}
