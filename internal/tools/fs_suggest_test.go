package tools

import (
	"strings"
	"testing"
)

// J1.3: read on a missing path scans the parent directory and names up to three
// near matches, instead of returning the bare "no such file" error.
func TestReadMissingPathSuggestsNearMatches(t *testing.T) {
	f := tempFS(t, map[string]string{
		"fs.go": "x", "fs_test.go": "x", "fx.go": "x", "other.go": "x",
	})
	_, err := run(t, f.Tools(), "read", map[string]any{"path": "fs.goo"})
	if err == nil {
		t.Fatal("want an error for a missing path")
	}
	if !strings.Contains(err.Error(), "Did you mean:") {
		t.Errorf("error = %q, want a 'Did you mean' suggestion", err)
	}
	// "fs.go" is a near match (the typo "fs.goo" contains "fs.go"); it should
	// be among the candidates.
	if !strings.Contains(err.Error(), "fs.go") {
		t.Errorf("error = %q, want fs.go among the suggestions", err)
	}
}

// The suggestion list is capped at three, even when many entries match.
func TestReadMissingPathCapsSuggestionsAtThree(t *testing.T) {
	files := map[string]string{}
	for _, n := range []string{"api1.go", "api2.go", "api3.go", "api4.go", "api5.go"} {
		files[n] = "x"
	}
	f := tempFS(t, files)
	_, err := run(t, f.Tools(), "read", map[string]any{"path": "api"})
	if err == nil {
		t.Fatal("want an error for a missing path")
	}
	idx := strings.Index(err.Error(), "Did you mean:")
	if idx < 0 {
		t.Fatalf("error = %q, want suggestions", err)
	}
	list := strings.TrimSpace(err.Error()[idx+len("Did you mean:"):])
	count := strings.Count(list, ",") + 1
	if count > 3 {
		t.Errorf("suggested %d names, want at most 3: %s", count, list)
	}
}

// A missing path whose parent has no near match still errors, without a
// misleading "Did you mean".
func TestReadMissingPathWithNoNearMatch(t *testing.T) {
	f := tempFS(t, map[string]string{"completely_unrelated.go": "x"})
	_, err := run(t, f.Tools(), "read", map[string]any{"path": "zzz.go"})
	if err == nil {
		t.Fatal("want an error for a missing path")
	}
	if strings.Contains(err.Error(), "Did you mean:") {
		t.Errorf("error = %q, no near match should not suggest", err)
	}
}

// A missing path whose parent does not exist does not panic or mislead: ReadDir
// fails and the bare error stands.
func TestReadMissingPathParentMissing(t *testing.T) {
	f := tempFS(t, nil)
	_, err := run(t, f.Tools(), "read", map[string]any{"path": "nodir/missing.go"})
	if err == nil {
		t.Fatal("want an error for a missing path")
	}
	if strings.Contains(err.Error(), "Did you mean:") {
		t.Errorf("error = %q, a missing parent should not suggest", err)
	}
}

// Sanity: an existing file still reads, unaffected by the suggestion path.
func TestReadExistingFileUnaffectedBySuggestions(t *testing.T) {
	f := tempFS(t, map[string]string{"real.go": "package main\n"})
	res, err := run(t, f.Tools(), "read", map[string]any{"path": "real.go"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(res.Output, "package main") {
		t.Errorf("output = %q, want the file's contents", res.Output)
	}
}

// A case-only typo (FS.GO when fs.go exists on a case-sensitive filesystem) is
// suggested: the skip is on the exact name, not the case-folded one.
func TestReadMissingPathSuggestsCaseTypo(t *testing.T) {
	f := tempFS(t, map[string]string{"fs.go": "x"})
	_, err := run(t, f.Tools(), "read", map[string]any{"path": "FS.GO"})
	if err == nil {
		t.Fatal("want an error for the case-typo path")
	}
	if !strings.Contains(err.Error(), "Did you mean:") || !strings.Contains(err.Error(), "fs.go") {
		t.Errorf("error = %q, want fs.go suggested for the case typo", err)
	}
}

// Suggestions still work under confinement, scanning through the confined open.
func TestReadMissingPathSuggestsUnderConfine(t *testing.T) {
	f := tempFS(t, map[string]string{"fs.go": "x", "fs_test.go": "x"}).WithConfine(true)
	_, err := run(t, f.Tools(), "read", map[string]any{"path": "fs.goo"})
	if err == nil {
		t.Fatal("want an error for a missing path")
	}
	if !strings.Contains(err.Error(), "Did you mean:") || !strings.Contains(err.Error(), "fs.go") {
		t.Errorf("error = %q, want fs.go suggested under confine", err)
	}
}