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
