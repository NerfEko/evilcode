package tools

import (
	"path/filepath"
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

// A trailing newline on `old` must not break the indentation diagnosis: the
// synthetic empty final element would inflate the window and compare the
// block's last line against the line after it.
func TestEditFailedMatchIndentationTrailingNewline(t *testing.T) {
	f := tempFS(t, map[string]string{"a.txt": "func main() {\n\tx()\n}\nnext\n"})
	_, err := run(t, f.Tools(), "edit", map[string]any{
		"path": "a.txt", "old": "func main() {\n    x()\n}\n", "new": "func main() {\n\ty()\n}\n",
	})
	if err == nil {
		t.Fatal("want an error: the indentation differs")
	}
	if !strings.Contains(err.Error(), "indentation") || !strings.Contains(err.Error(), "line 1") {
		t.Errorf("error = %q, want 'different indentation around line 1' despite the trailing newline", err)
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

// J1.5: a successful edit returns three lines of context either side, so a
// consecutive edit to the same region needs no re-read.
func TestEditReturnsContextAroundChange(t *testing.T) {
	var b strings.Builder
	for i := 1; i <= 20; i++ {
		b.WriteString("line ")
		b.WriteString(itoa(i))
		b.WriteString("\n")
	}
	f := tempFS(t, map[string]string{"a.txt": b.String()})
	res, err := run(t, f.Tools(), "edit", map[string]any{
		"path": "a.txt", "old": "line 10", "new": "line ten",
	})
	if err != nil {
		t.Fatal(err)
	}
	// The change is at line 10; context spans lines 7-13 (3 either side),
	// and the changed line reads "line ten".
	if !strings.Contains(res.Output, "10\tline ten") {
		t.Errorf("output missing the changed line 10:\n%s", res.Output)
	}
	if !strings.Contains(res.Output, "7\tline 7") || !strings.Contains(res.Output, "13\tline 13") {
		t.Errorf("output = %q, want context lines 7 and 13", res.Output)
	}
	if strings.Contains(res.Output, "6\tline 6") || strings.Contains(res.Output, "14\tline 14") {
		t.Errorf("output = %q, context must be exactly 3 lines either side", res.Output)
	}
}

// itoa avoids pulling strconv for a tiny test helper.
func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var buf [4]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	return string(buf[i:])
}
// A trailing newline on `new` is a delimiter, not a changed line: the context
// after the change is three lines, not four.
func TestEditContextTrailingNewlineOnNew(t *testing.T) {
	var b strings.Builder
	for i := 1; i <= 20; i++ {
		b.WriteString("line ")
		b.WriteString(itoa(i))
		b.WriteString("\n")
	}
	f := tempFS(t, map[string]string{"a.txt": b.String()})
	res, err := run(t, f.Tools(), "edit", map[string]any{
		"path": "a.txt", "old": "line 10", "new": "line ten\n",
	})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(res.Output, "14\tline 14") {
		t.Errorf("output = %q, a trailing newline on new must not add a 4th context line", res.Output)
	}
}

// A newline-terminated file edited near EOF does not print a phantom numbered
// empty final line.
func TestEditContextNoPhantomEOFLine(t *testing.T) {
	f := tempFS(t, map[string]string{"a.txt": "a\nb\nc\n"})
	res, err := run(t, f.Tools(), "edit", map[string]any{
		"path": "a.txt", "old": "c", "new": "C",
	})
	if err != nil {
		t.Fatal(err)
	}
	// No "4\t" line (the file has only 3 lines).
	if strings.Contains(res.Output, "\n4\t") {
		t.Errorf("output printed a phantom 4th line:\n%s", res.Output)
	}
}

// A context line over MaxLineLen is truncated, so a minified neighbour does not
// consume the result budget and push the changed line out.
func TestEditContextTruncatesLongLines(t *testing.T) {
	long := strings.Repeat("z", 5000)
	f := tempFS(t, map[string]string{"a.txt": long + "\ntarget\n" + long + "\n"})
	res, err := run(t, f.Tools(), "edit", map[string]any{
		"path": "a.txt", "old": "target", "new": "hit",
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, line := range strings.Split(res.Output, "\n") {
		if len(line) > MaxLineLen+50 {
			t.Errorf("a context line is %d chars, past the cap", len(line))
		}
	}
}

// An anchored edit also returns context, so an anchored model need not re-read.
func TestAnchoredEditReturnsContext(t *testing.T) {
	body := "line one\nline two\nline three\n"
	f := tempFS(t, map[string]string{"a.txt": body}).WithAnchors(true)
	// Read first so anchors are recorded.
	if _, err := run(t, f.Tools(), "read", map[string]any{"path": "a.txt"}); err != nil {
		t.Fatal(err)
	}
	st, _ := f.anchors.lookup(filepath.Join(f.Root, "a.txt"))
	anchor := ""
	for a, nums := range st.Anchors {
		if len(nums) == 1 && nums[0] == 2 { // "line two"
			anchor = a
			break
		}
	}
	if anchor == "" {
		t.Fatal("could not find the anchor for line 2")
	}
	res, err := run(t, f.Tools(), "edit", map[string]any{
		"path":    "a.txt",
		"patches": []map[string]any{{"anchor": anchor, "op": "replace", "lines": []string{"line TWO"}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(res.Output, "line TWO") {
		t.Errorf("anchored edit output missing the changed line:\n%s", res.Output)
	}
	// With anchors on, the context carries fresh anchors (anchor|line| text)
	// the model can quote for a follow-up edit, not plain "N\t" rows.
	if !strings.Contains(res.Output, "|1| line one") || !strings.Contains(res.Output, "|3| line three") {
		t.Errorf("anchored edit output = %q, want annotated context lines 1 and 3", res.Output)
	}
}

// When `old` ends in a newline but the matched block is at EOF without one, the
// diagnosis names the missing newline, not (only) the indentation.
func TestEditFailedMatchTrailingNewlineMissingAtEOF(t *testing.T) {
	f := tempFS(t, map[string]string{"a.txt": "func main() {\n\tx()\n}"}) // no trailing newline
	_, err := run(t, f.Tools(), "edit", map[string]any{
		"path": "a.txt", "old": "func main() {\n    x()\n}\n", "new": "func main() {\n\ty()\n}\n",
	})
	if err == nil {
		t.Fatal("want an error")
	}
	if !strings.Contains(err.Error(), "missing the trailing newline") {
		t.Errorf("error = %q, want it to name the missing trailing newline", err)
	}
}

// After an anchored edit, a follow-up anchored edit on a neighbouring line
// succeeds without a re-read: the post-write anchor state was re-recorded.
func TestAnchoredEditFollowUpNeedsNoReread(t *testing.T) {
	body := "line one\nline two\nline three\n"
	f := tempFS(t, map[string]string{"a.txt": body}).WithAnchors(true)
	if _, err := run(t, f.Tools(), "read", map[string]any{"path": "a.txt"}); err != nil {
		t.Fatal(err)
	}
	st, _ := f.anchors.lookup(filepath.Join(f.Root, "a.txt"))
	anchorFor := func(line int) string {
		for a, nums := range st.Anchors {
			if len(nums) == 1 && nums[0] == line {
				return a
			}
		}
		return ""
	}
	a2 := anchorFor(2)
	if a2 == "" {
		t.Fatal("no anchor for line 2")
	}
	// First anchored edit on line 2.
	if _, err := run(t, f.Tools(), "edit", map[string]any{
		"path": "a.txt", "patches": []map[string]any{{"anchor": a2, "op": "replace", "lines": []string{"line TWO"}}},
	}); err != nil {
		t.Fatalf("first anchored edit: %v", err)
	}
	// A follow-up anchored edit on line 3 — no re-read in between.
	a3 := anchorFor(3)
	if a3 == "" {
		t.Fatal("no anchor for line 3")
	}
	if _, err := run(t, f.Tools(), "edit", map[string]any{
		"path": "a.txt", "patches": []map[string]any{{"anchor": a3, "op": "replace", "lines": []string{"line THREE"}}},
	}); err != nil {
		t.Fatalf("follow-up anchored edit without re-read failed: %v", err)
	}
}
