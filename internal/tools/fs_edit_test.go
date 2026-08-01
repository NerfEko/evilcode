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