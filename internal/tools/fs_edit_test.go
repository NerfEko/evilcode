package tools

import (
	"strings"
	"testing"
)

// J1.4: a failed exact match that would succeed after trimming whitespace says
// so and where, rather than a bare "not found". The model supplied surrounding
// whitespace the file lacks.
func TestEditFailedMatchTrimmed(t *testing.T) {
	f := tempFS(t, map[string]string{"a.txt": "line one\ntarget\nline three\n"})
	_, err := run(t, f.Tools(), "edit", map[string]any{
		"path": "a.txt", "old": "  target  ", "new": "hit",
	})
	if err == nil {
		t.Fatal("want an error: the padded '  target  ' is not in the file")
	}
	if !strings.Contains(err.Error(), "trimming") {
		t.Errorf("error = %q, want it to say the match was found after trimming", err)
	}
	if !strings.Contains(err.Error(), "line 2") {
		t.Errorf("error = %q, want the line number of the trimmed match", err)
	}
}

// A failed match that differs only in indentation says so and at what line.
func TestEditFailedMatchIndentation(t *testing.T) {
	f := tempFS(t, map[string]string{"a.txt": "func main() {\n\tx()\n}\n"})
	_, err := run(t, f.Tools(), "edit", map[string]any{
		"path": "a.txt", "old": "func main() {\n    x()\n}", "new": "func main() {\n\ty()\n}",
	})
	if err == nil {
		t.Fatal("want an error: the indentation differs")
	}
	if !strings.Contains(err.Error(), "indentation") || !strings.Contains(err.Error(), "line 1") {
		t.Errorf("error = %q, want 'different indentation around line 1'", err)
	}
}

// A failed match with no looser form keeps the plain not-found error.
func TestEditFailedMatchNotFound(t *testing.T) {
	f := tempFS(t, map[string]string{"a.txt": "one\ntwo\n"})
	_, err := run(t, f.Tools(), "edit", map[string]any{
		"path": "a.txt", "old": "nope", "new": "x",
	})
	if err == nil {
		t.Fatal("want an error")
	}
	if strings.Contains(err.Error(), "found after trimming") || strings.Contains(err.Error(), "found with different indentation") {
		t.Errorf("error = %q, a true miss should not claim a looser match", err)
	}
}