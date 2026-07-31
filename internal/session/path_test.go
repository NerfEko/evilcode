package session

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"evilcode/internal/provider"
)

// H4.2: session names are joined into paths with no validation. Only Rename
// checked. `--resume '../x'` or `/fork ../../tmp/evil` escapes the sessions
// directory entirely — reading, and then writing, wherever the name points.
func TestSessionNamesCannotEscapeTheSessionsDirectory(t *testing.T) {
	dir := t.TempDir()

	// Something to reach for outside the sessions directory.
	outside := filepath.Join(dir, "outside.jsonl")
	if err := os.WriteFile(outside, []byte("{}\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	for _, name := range []string{
		"../outside",
		"../../etc/passwd",
		"sub/nested",
		"/etc/passwd",
		"",
		".",
		"..",
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := Open(dir, name); err == nil {
				t.Errorf("Open accepted %q", name)
			}
			if _, _, err := Resume(dir, name); err == nil {
				t.Errorf("Resume accepted %q", name)
			}
			if err := Fork(dir, "anything", name); err == nil {
				t.Errorf("Fork accepted %q as a destination", name)
			}
			if _, err := CreateNamed(dir, name); err == nil {
				t.Errorf("CreateNamed accepted %q", name)
			}
		})
	}
}

// H4.3: a session holds every prompt, every tool result and anything a model
// echoed. It has no business being world-readable.
func TestSessionFilesAreNotWorldReadable(t *testing.T) {
	dir := t.TempDir()
	store, err := Create(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	info, err := os.Stat(Dir(dir))
	if err != nil {
		t.Fatal(err)
	}
	if perm := info.Mode().Perm(); perm != 0o700 {
		t.Errorf("the sessions directory is %v, want 0700", perm)
	}

	info, err = os.Stat(store.Path)
	if err != nil {
		t.Fatal(err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Errorf("the session log is %v, want 0600", perm)
	}
}

// The backup and temp files a rewrite leaves behind hold the same content.
func TestRewriteArtefactsAreNotWorldReadable(t *testing.T) {
	dir := t.TempDir()
	store, err := Create(dir)
	if err != nil {
		t.Fatal(err)
	}
	store.WriteMessage(provider.Message{Role: provider.RoleUser, Content: "a secret prompt"})
	name := store.Name
	if _, err := store.Compact(dir, "a summary"); err != nil {
		t.Fatal(err)
	}
	store.Close()

	entries, err := os.ReadDir(Dir(dir))
	if err != nil {
		t.Fatal(err)
	}
	var checked int
	for _, e := range entries {
		if !strings.HasPrefix(e.Name(), name) {
			continue
		}
		info, err := e.Info()
		if err != nil {
			t.Fatal(err)
		}
		if e.IsDir() {
			continue
		}
		checked++
		if perm := info.Mode().Perm(); perm&0o077 != 0 {
			t.Errorf("%s is %v, readable outside the owner", e.Name(), perm)
		}
	}
	if checked < 2 {
		t.Fatalf("expected a log and a backup, checked %d files", checked)
	}
}
