package tools

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"unicode/utf8"
)

// J1.2: a single line over 2000 characters is truncated with a marker rather
// than consuming the whole read budget, and the count of truncated lines is
// said once at the end.
func TestReadTruncatesLongLines(t *testing.T) {
	long := strings.Repeat("x", 5000)
	body := "short one\n" + long + "\nshort two\n" + long + "\n"
	f := tempFS(t, map[string]string{"min.js": body})

	res, err := run(t, f.Tools(), "read", map[string]any{"path": "min.js"})
	if err != nil {
		t.Fatal(err)
	}
	// The two long lines are capped; the marker shows the cut.
	if c := strings.Count(res.Output, "..."); c != 2 {
		t.Errorf("want 2 truncated-line markers, got %d in:\n%s", c, res.Output)
	}
	// The short lines survive intact and in order.
	if !strings.Contains(res.Output, "1\tshort one") || !strings.Contains(res.Output, "3\tshort two") {
		t.Errorf("output lost the short lines:\n%s", res.Output)
	}
	// No single output line still carries the full 5000-char payload.
	for _, line := range strings.Split(res.Output, "\n") {
		if len(line) > MaxLineLen+50 { // marker + line number headroom
			t.Errorf("a line is %d chars long, past the %d cap", len(line), MaxLineLen)
		}
	}
	if !strings.Contains(res.Output, "2 line(s) truncated at") {
		t.Errorf("output = %q, want a one-line truncation summary", res.Output)
	}
}

// A paged read of a minified file (offset/limit on an over-cap file) also
// truncates long lines, so paging into a giant bundle line does not drown the
// window.
func TestPagedReadTruncatesLongLines(t *testing.T) {
	long := strings.Repeat("y", 4000)
	var b strings.Builder
	for range 10 {
		b.WriteString(long)
		b.WriteString("\n")
	}
	f := tempFS(t, nil)
	f.MaxReadBytes = 8 * 1024
	full := filepath.Join(f.Root, "big.js")
	if err := os.WriteFile(full, []byte(b.String()), 0o644); err != nil {
		t.Fatal(err)
	}
	res, err := run(t, f.Tools(), "read", map[string]any{"path": "big.js", "offset": 1, "limit": 3})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(res.Output, "truncated at") {
		t.Errorf("paged output = %q, want a truncation summary", res.Output)
	}
}

// Lines at or under the cap are untouched: no marker, no summary.
func TestReadLeavesShortLinesAlone(t *testing.T) {
	f := tempFS(t, map[string]string{"a.txt": strings.Repeat("z", 2000) + "\n"})
	res, err := run(t, f.Tools(), "read", map[string]any{"path": "a.txt"})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(res.Output, "truncated") {
		t.Errorf("a 2000-char line should not be truncated:\n%s", res.Output)
	}
}

// Truncation cuts at a UTF-8 rune boundary, not the middle of a multibyte
// character, so the result is valid UTF-8 (no U+FFFD from the provider edge).
func TestReadTruncatesAtRuneBoundary(t *testing.T) {
	// 2000 'a' bytes, then a 3-byte rune '世' straddling the cut point: byte
	// 2000 lands inside the rune.
	prefix := strings.Repeat("a", 2000)
	body := prefix + "世" + strings.Repeat("b", 100) + "\n"
	f := tempFS(t, map[string]string{"u.txt": body})
	res, err := run(t, f.Tools(), "read", map[string]any{"path": "u.txt"})
	if err != nil {
		t.Fatal(err)
	}
	// The output must be valid UTF-8: a cut mid-rune would leave invalid bytes
	// that utf8.Valid rejects.
	if !utf8.ValidString(res.Output) {
		t.Errorf("truncated output is not valid UTF-8")
	}
	if !strings.Contains(res.Output, "...") {
		t.Errorf("want a truncation marker:\n%s", res.Output)
	}
}

// With anchors on, a truncated line's anchor is hashed from the original line,
// so an edit quoting it validates against the version read — not from the
// truncated text the edit path would reject.
func TestAnchoredReadHashesOriginalLongLine(t *testing.T) {
	long := strings.Repeat("x", 3000)
	f := tempFS(t, map[string]string{"a.txt": long + "\n"}).WithAnchors(true)
	res, err := run(t, f.Tools(), "read", map[string]any{"path": "a.txt"})
	if err != nil {
		t.Fatal(err)
	}
	wantAnchor := LineAnchor(long)
	if !strings.Contains(res.Output, wantAnchor) {
		t.Errorf("output missing the original line's anchor %q:\n%s", wantAnchor, res.Output)
	}
	badAnchor := LineAnchor(long[:2000] + "...")
	if strings.Contains(res.Output, badAnchor) {
		t.Errorf("output carries the truncated-text anchor %q, which an edit would reject", badAnchor)
	}
}

// A paged read of a file whose single line is larger than the read cap still
// emits it (truncated) and advances past it, rather than returning
// "re-read with offset=1" forever or erroring "token too long".
func TestPagedReadEmitsASingleLineLargerThanTheCap(t *testing.T) {
	f := tempFS(t, nil)
	f.MaxReadBytes = 4096
	giant := strings.Repeat("c", 50_000) + "\n"
	full := filepath.Join(f.Root, "giant.js")
	if err := os.WriteFile(full, []byte(giant), 0o644); err != nil {
		t.Fatal(err)
	}
	res, err := run(t, f.Tools(), "read", map[string]any{"path": "giant.js", "offset": 1, "limit": 5})
	if err != nil {
		t.Fatalf("a single over-cap line should page, not error: %v", err)
	}
	if !strings.Contains(res.Output, "...") {
		t.Errorf("want the long line truncated with a marker:\n%s", res.Output)
	}
	// Paging advanced past the line: the continuation hint points beyond 1.
	if strings.Contains(res.Output, "offset=1\n") && !strings.Contains(res.Output, "offset=2") {
		t.Errorf("paging did not advance past the long line:\n%s", res.Output)
	}
}
