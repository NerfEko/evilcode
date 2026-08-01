package tools

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
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