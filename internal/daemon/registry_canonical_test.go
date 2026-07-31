package daemon

import (
	"path/filepath"
	"testing"
)

func TestRereadingClearsAConflictKeyedOnCanonicalPath(t *testing.T) {
	// With a workspace root set, Write stores the display (root-relative) path
	// on the Conflict but Read's clearing loop matches on the normalized
	// absolute path. If the delivered key is built from the display path, the
	// two never agree and a re-read never re-arms notification — the
	// coordination feature degrades to fire-once-per-file-pair.
	dir := t.TempDir()
	r := newRegistryAt(dir)
	rel := filepath.Join("internal", "tui", "app.go")
	abs := filepath.Join(dir, rel)

	r.Read("bat", abs, 1)
	r.Pending("bat", r.Write("crypt", abs, 2))

	r.Read("bat", abs, 3)
	again := r.Pending("bat", r.Write("crypt", abs, 4))
	if len(again) != 1 {
		t.Errorf("a second write after a re-read should be reported; got %d", len(again))
	}
}
