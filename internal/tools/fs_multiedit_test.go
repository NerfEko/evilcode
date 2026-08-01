package tools

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// J1.6: multiedit applies an ordered list of edits to one file in one pass,
// against the accumulating content, with a per-edit report and one write.
func TestMultiEditAppliesOrderedEdits(t *testing.T) {
	f := tempFS(t, map[string]string{"a.txt": "one\ntwo\nthree\n"})
	res, err := run(t, f.Tools(), "multiedit", map[string]any{
		"path": "a.txt",
		"edits": []map[string]any{
			{"old": "one", "new": "ONE"},
			{"old": "two", "new": "TWO"},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(res.Output, "2 applied, 0 failed") {
		t.Errorf("output = %q, want 2 applied 0 failed", res.Output)
	}
	got, _ := os.ReadFile(filepath.Join(f.Root, "a.txt"))
	if string(got) != "ONE\nTWO\nthree\n" {
		t.Errorf("file = %q, want ONE\\nTWO\\nthree\\n", got)
	}
}

// A later edit can touch text an earlier edit produced (accumulating content).
func TestMultiEditSeesEarlierEdits(t *testing.T) {
	f := tempFS(t, map[string]string{"a.txt": "x\n"})
	res, err := run(t, f.Tools(), "multiedit", map[string]any{
		"path": "a.txt",
		"edits": []map[string]any{
			{"old": "x", "new": "y"},
			{"old": "y", "new": "z"},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(res.Output, "2 applied") {
		t.Errorf("output = %q, want 2 applied", res.Output)
	}
	got, _ := os.ReadFile(filepath.Join(f.Root, "a.txt"))
	if string(got) != "z\n" {
		t.Errorf("file = %q, want z\\n (second edit touched the first's result)", got)
	}
}

// A failed edit is reported and skipped; it does not roll back the ones before
// it, and the rest continue. Partial application is the correct outcome.
func TestMultiEditPartialApplication(t *testing.T) {
	f := tempFS(t, map[string]string{"a.txt": "one\ntwo\nthree\n"})
	res, err := run(t, f.Tools(), "multiedit", map[string]any{
		"path": "a.txt",
		"edits": []map[string]any{
			{"old": "one", "new": "ONE"},
			{"old": "nope", "new": "X"},   // fails: not found
			{"old": "two", "new": "TWO"}, // still applies
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(res.Output, "2 applied, 1 failed") {
		t.Errorf("output = %q, want 2 applied 1 failed", res.Output)
	}
	if !strings.Contains(res.Output, "edit 2: old string not found") {
		t.Errorf("output = %q, want edit 2 reported as not found", res.Output)
	}
	got, _ := os.ReadFile(filepath.Join(f.Root, "a.txt"))
	if string(got) != "ONE\nTWO\nthree\n" {
		t.Errorf("file = %q, want the two applied edits to persist", got)
	}
}

// A non-unique old without all=true fails that edit only.
func TestMultiEditNonUniqueFailsThatEdit(t *testing.T) {
	f := tempFS(t, map[string]string{"a.txt": "dup\ndup\n"})
	res, err := run(t, f.Tools(), "multiedit", map[string]any{
		"path": "a.txt",
		"edits": []map[string]any{
			{"old": "dup", "new": "x"},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(res.Output, "0 applied, 1 failed") {
		t.Errorf("output = %q, want 0 applied 1 failed", res.Output)
	}
	got, _ := os.ReadFile(filepath.Join(f.Root, "a.txt"))
	if string(got) != "dup\ndup\n" {
		t.Errorf("file = %q, a fully-failed multiedit must not rewrite it", got)
	}
}

// all=true replaces every occurrence in one edit.
func TestMultiEditReplaceAll(t *testing.T) {
	f := tempFS(t, map[string]string{"a.txt": "dup\ndup\ndup\n"})
	_, err := run(t, f.Tools(), "multiedit", map[string]any{
		"path": "a.txt",
		"edits": []map[string]any{
			{"old": "dup", "new": "X", "all": true},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	got, _ := os.ReadFile(filepath.Join(f.Root, "a.txt"))
	if string(got) != "X\nX\nX\n" {
		t.Errorf("file = %q, want all three replaced", got)
	}
}

// A fully-failed multiedit does not rewrite the file (no mtime churn, no-op).
func TestMultiEditAllFailedDoesNotWrite(t *testing.T) {
	f := tempFS(t, map[string]string{"a.txt": "one\n"})
	full := filepath.Join(f.Root, "a.txt")
	info, _ := os.Stat(full)
	before := info.ModTime()
	// Sleep is unnecessary: a no-op must not touch mtime at all.
	_, err := run(t, f.Tools(), "multiedit", map[string]any{
		"path": "a.txt",
		"edits": []map[string]any{
			{"old": "nope", "new": "X"},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	info2, _ := os.Stat(full)
	if !info2.ModTime().Equal(before) {
		t.Errorf("a fully-failed multiedit rewrote the file (mtime changed)")
	}
}