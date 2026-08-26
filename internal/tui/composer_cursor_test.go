package tui

import (
	"strings"
	"testing"

	"charm.land/lipgloss/v2"

	"evilcode/internal/theme"
)

// wrapPlainWithCursor must produce exactly the same wrapped lines as wrapPlain,
// otherwise the composer's layout would shift when the caret turns on.
func TestWrapPlainWithCursorMatchesWrapPlain(t *testing.T) {
	cases := []string{
		"",
		"hello",
		"hello world",
		"hello   world",      // collapsed spaces
		"  leading spaces",   // leading whitespace
		"a\n\nb",             // blank line
		"one two three four", // wrap
		strings.Repeat("x", 50),
		"abcdefghij klmnopqr",
		"emoji 🦇🦇 end",
		"line1\nline2\nline3",
	}
	widths := []int{1, 5, 10, 20, 40}
	for _, s := range cases {
		for _, w := range widths {
			got, _, _ := wrapPlainWithCursor(s, 0, w)
			want := wrapPlain(s, w)
			if strings.Join(got, "|") != strings.Join(want, "|") {
				t.Errorf("wrap mismatch body=%q width=%d:\n got=%q\nwant=%q", s, w, got, want)
			}
		}
	}
}

func TestCaretLineAndColumn(t *testing.T) {
	// Single line, caret at each rune position.
	const body = "hello   world"
	for c := 0; c <= len([]rune(body)); c++ {
		_, line, col := wrapPlainWithCursor(body, c, 80)
		if line != 0 {
			t.Errorf("caret %d on line %d, want 0", c, line)
		}
		// The three spaces between the words collapse to one cell; verify the
		// caret lands on the same cell the user sees.
		want := c
		if c >= 5 && c < 8 {
			want = 5
		}
		if c >= 8 {
			want = c - 2
		}
		if col != want {
			t.Errorf("caret %d col=%d want=%d", c, col, want)
		}
	}
}

func TestCaretWrapsToSecondLine(t *testing.T) {
	body := "aaaa bbbb cccc" // width 5 -> "aaaa","bbbb","cccc"
	lines, _, _ := wrapPlainWithCursor(body, 0, 5)
	if len(lines) != 3 {
		t.Fatalf("lines=%q", lines)
	}
	// "aaaa" is runes 0..3, caret at 4 (space) -> end of line 0.
	_, line, col := wrapPlainWithCursor(body, 4, 5)
	if line != 0 || col != 4 {
		t.Errorf("caret 4 -> line=%d col=%d, want 0,4", line, col)
	}
	// "bbbb" starts at rune 5; caret at 5 -> start of line 1, col 0.
	_, line, col = wrapPlainWithCursor(body, 5, 5)
	if line != 1 || col != 0 {
		t.Errorf("caret 5 -> line=%d col=%d, want 1,0", line, col)
	}
	// caret at end (rune 14) -> end of last line, col 4.
	_, line, col = wrapPlainWithCursor(body, 14, 5)
	if line != 2 || col != 4 {
		t.Errorf("caret end -> line=%d col=%d, want 2,4", line, col)
	}
}

func TestCaretOnEmptyInput(t *testing.T) {
	lines, line, col := wrapPlainWithCursor("", 0, 20)
	if len(lines) != 1 || lines[0] != "" || line != 0 || col != 0 {
		t.Fatalf("empty input: lines=%q line=%d col=%d", lines, line, col)
	}
	// caret clamps to rune length when out of range.
	_, line, col = wrapPlainWithCursor("abc", 99, 20)
	if line != 0 || col != 3 {
		t.Fatalf("clamped caret: line=%d col=%d", line, col)
	}
}

// RenderComposer must draw a visible block cursor: the reverse-video SGR (7)
// must appear in the rendered row, and the caret cell must not leak the plain
// text twice.
func TestRenderComposerShowsBlockCursor(t *testing.T) {
	r := NewRenderer(theme.Dracula(), 80)
	rows := r.RenderComposer(ComposerState{Text: "hello", Cursor: 2})
	joined := strings.Join(rows, "\n")
	if !hasReverseSGR(joined) {
		t.Fatalf("composer row has no reverse-video cursor:\n%s", joined)
	}
	if !strings.Contains(joined, "h") || !strings.Contains(joined, "e") {
		t.Fatalf("composer dropped text:\n%s", joined)
	}
	// The cell under the caret (the first 'l') is rendered once, so "hello"
	// still contains exactly its original two l characters.
	if strings.Count(rows[0], "l") != 2 {
		t.Fatalf("caret cell rendered more than once:\n%s", joined)
	}
}

func TestRenderComposerCursorAtEnd(t *testing.T) {
	r := NewRenderer(theme.Dracula(), 80)
	rows := r.RenderComposer(ComposerState{Text: "abc", Cursor: 3})
	joined := strings.Join(rows, "\n")
	if !hasReverseSGR(joined) {
		t.Fatalf("end-of-line cursor not drawn:\n%s", joined)
	}
	// A block cursor at the end renders a space cell; the row width is
	// unchanged (prefix + 3 chars + 1 cursor cell).
	if w := lipgloss.Width(rows[0]); w < 4 {
		t.Fatalf("end cursor row too narrow: width=%d row=%q", w, rows[0])
	}
}

func hasReverseSGR(s string) bool {
	return strings.Contains(s, "\x1b[7m") || strings.Contains(s, "\x1b[7;")
}

// Trailing whitespace at the caret must stay visible while typing. wrapPlain
// collapses it for completed transcript text, but the composer is live input:
// without preserving it, pressing space changes nothing on screen until the
// next character lands, because the caret block already sat at the end of the
// last word.
func TestTrailingSpacePreserved(t *testing.T) {
	cases := []struct {
		body   string
		cursor int
		width  int
		want   string
		col    int
	}{
		{"hello ", 6, 80, "hello ", 6},     // one trailing space
		{"hello   ", 8, 80, "hello   ", 8}, // several trailing spaces
		{"hello ", 5, 80, "hello ", 5},     // caret on the space itself
		{" ", 1, 80, " ", 1},               // a lone space on an empty line
		{"   ", 3, 80, "   ", 3},           // only spaces
	}
	for _, c := range cases {
		lines, line, col := wrapPlainWithCursor(c.body, c.cursor, c.width)
		if len(lines) != 1 || lines[0] != c.want {
			t.Errorf("body=%q cursor=%d: lines=%q want %q", c.body, c.cursor, lines, []string{c.want})
		}
		if line != 0 || col != c.col {
			t.Errorf("body=%q cursor=%d: line=%d col=%d want 0,%d", c.body, c.cursor, line, col, c.col)
		}
	}
}

// Completed text without trailing whitespace still matches wrapPlain, so the
// transcript invariant (no layout shift) holds where it matters.
func TestNoTrailingStillMatchesWrapPlain(t *testing.T) {
	for _, s := range []string{"hello", "hello world", "hello   world", "  leading spaces"} {
		got, _, _ := wrapPlainWithCursor(s, len([]rune(s)), 20)
		want := wrapPlain(s, 20)
		if strings.Join(got, "|") != strings.Join(want, "|") {
			t.Errorf("trailing invariant broken for %q:\n got=%q\nwant=%q", s, got, want)
		}
	}
}

// RenderComposer must show a typed trailing space: the space lands in the row
// and the block cursor sits after it, not on the last word.
func TestRenderComposerShowsTrailingSpace(t *testing.T) {
	r := NewRenderer(theme.Dracula(), 80)
	rows := r.RenderComposer(ComposerState{Text: "hello ", Cursor: 6})
	joined := strings.Join(rows, "\n")
	if !hasReverseSGR(joined) {
		t.Fatalf("no cursor drawn:\n%s", joined)
	}
	// The row contains the trailing space followed by the cursor cell. Strip
	// ANSI to check the visible text: it must end with "hello " (the space).
	plain := stripAnsi(rows[0])
	if !strings.Contains(plain, "hello ") {
		t.Fatalf("trailing space not rendered:\n%s", joined)
	}
	// The cursor block is one cell past the space, so the visible width grew by
	// the prefix plus 6 (5 letters + space) plus 1 cursor cell.
	if w := lipgloss.Width(rows[0]); w < 7 {
		t.Fatalf("cursor row too narrow after trailing space: width=%d row=%q", w, joined)
	}
}

func stripAnsi(s string) string {
	var b strings.Builder
	in := false
	for _, r := range s {
		if r == '\x1b' {
			in = true
			continue
		}
		if in {
			if r == 'm' {
				in = false
			}
			continue
		}
		b.WriteRune(r)
	}
	return b.String()
}
