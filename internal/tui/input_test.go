package tui

import (
	"strings"
	"testing"
)

func TestEscapedNewlineParity(t *testing.T) {
	// The universal newline fallback for terminals with no kitty keyboard
	// protocol. Parity is the whole rule: an odd number of trailing
	// backslashes escapes the Enter, an even number does not — so `\\` is a
	// literal backslash that still submits (plan.md §6.2).
	tests := []struct {
		in   string
		want bool
	}{
		{"hello", false},
		{`hello\`, true},
		{`hello\\`, false},
		{`hello\\\`, true},
		{`hello\\\\`, false},
		{`\`, true},
		{"", false},
		{`C:\path\`, true},
	}
	for _, tt := range tests {
		if got := EndsWithEscapedNewline(tt.in); got != tt.want {
			t.Errorf("EndsWithEscapedNewline(%q) = %v, want %v", tt.in, got, tt.want)
		}
	}
}

func TestStripEscapedNewline(t *testing.T) {
	if got := StripEscapedNewline(`hello\`); got != "hello" {
		t.Errorf("got %q, want %q", got, "hello")
	}
	// An even count is content, not an instruction, and must survive intact.
	if got := StripEscapedNewline(`hello\\`); got != `hello\\` {
		t.Errorf("got %q, want it unchanged", got)
	}
}

func TestCollapseLargePaste(t *testing.T) {
	small := "one\ntwo"
	insert, stored := CollapsePaste(small)
	if insert != small || stored != nil {
		t.Errorf("a small paste should go in verbatim, got %q / %+v", insert, stored)
	}

	big := strings.Repeat("line\n", 20)
	insert, stored = CollapsePaste(big)
	if stored == nil {
		t.Fatal("a large paste should be collapsed")
	}
	if !strings.HasPrefix(insert, "[pasted ") {
		t.Errorf("placeholder = %q", insert)
	}
	if stored.Content != big {
		t.Error("the stored content should be the original paste")
	}
}

func TestExpandPastesReplacesLastOccurrence(t *testing.T) {
	// Two pastes of the same size share a placeholder string. Replacing the
	// first occurrence each time would give both the same content.
	a := strings.Repeat("a\n", 10)
	b := strings.Repeat("b\n", 10)
	pa, _ := CollapsePaste(a)
	pb, _ := CollapsePaste(b)
	if pa != pb {
		t.Skip("placeholders differ; this test only matters when they collide")
	}

	_, storedA := CollapsePaste(a)
	_, storedB := CollapsePaste(b)
	input := pa + " and " + pb
	got := ExpandPastes(input, []Paste{*storedA, *storedB})

	if !strings.Contains(got, "a\n") || !strings.Contains(got, "b\n") {
		t.Errorf("both pastes should be restored:\n%s", got)
	}
	if strings.Contains(got, "[pasted") {
		t.Errorf("a placeholder survived:\n%s", got)
	}
}

func TestExpandPastesIgnoresRemovedPlaceholders(t *testing.T) {
	// The user may have deleted the placeholder before sending.
	_, stored := CollapsePaste(strings.Repeat("x\n", 10))
	got := ExpandPastes("nothing here", []Paste{*stored})
	if got != "nothing here" {
		t.Errorf("got %q", got)
	}
}

func TestImagePathDetection(t *testing.T) {
	for _, p := range []string{"a.png", "b.JPG", "c.jpeg", "d.webp"} {
		if !IsImagePath(p) {
			t.Errorf("%q should be an image", p)
		}
	}
	for _, p := range []string{"a.go", "b.txt", "noext", "a.png.txt"} {
		if IsImagePath(p) {
			t.Errorf("%q should not be an image", p)
		}
	}
}

func TestQuoteIfNeeded(t *testing.T) {
	if got := QuoteIfNeeded("/a/b.go"); got != "/a/b.go" {
		t.Errorf("got %q", got)
	}
	if got := QuoteIfNeeded("/a b/c.go"); got != `"/a b/c.go"` {
		t.Errorf("got %q", got)
	}
}

func TestEditorInsertAndDelete(t *testing.T) {
	var e Editor
	e.Insert("hello")
	if e.Text != "hello" || e.Cursor != 5 {
		t.Fatalf("text=%q cursor=%d", e.Text, e.Cursor)
	}
	e.Home()
	e.Insert("say ")
	if e.Text != "say hello" || e.Cursor != 4 {
		t.Fatalf("text=%q cursor=%d", e.Text, e.Cursor)
	}
	e.Backspace()
	if e.Text != "sayhello" {
		t.Errorf("text=%q", e.Text)
	}
	e.Delete()
	if e.Text != "sayello" {
		t.Errorf("text=%q", e.Text)
	}
}

func TestEditorHandlesWideRunes(t *testing.T) {
	// Cursor arithmetic is in runes, not bytes; a multibyte glyph would
	// otherwise split.
	var e Editor
	e.Insert("🦇bat")
	e.Home()
	e.Right()
	e.Insert("X")
	if e.Text != "🦇Xbat" {
		t.Errorf("text=%q, want the insert after the whole glyph", e.Text)
	}
	e.Home()
	e.Delete()
	if e.Text != "Xbat" {
		t.Errorf("text=%q, want the whole glyph deleted", e.Text)
	}
}

func TestEditorKillAndUndo(t *testing.T) {
	var e Editor
	e.Insert("hello world")
	e.KillToStart()
	if e.Text != "" {
		t.Errorf("text=%q", e.Text)
	}
	if !e.Undo() {
		t.Fatal("undo should be available after a destructive edit")
	}
	if e.Text != "hello world" {
		t.Errorf("undo restored %q", e.Text)
	}

	e.Home()
	e.KillToEnd()
	if e.Text != "" {
		t.Errorf("text=%q", e.Text)
	}
	e.Undo()
	if e.Text != "hello world" {
		t.Errorf("undo restored %q", e.Text)
	}
}

func TestEditorDeleteWord(t *testing.T) {
	var e Editor
	e.Insert("one two three")
	e.DeleteWord()
	if e.Text != "one two " {
		t.Errorf("text=%q", e.Text)
	}
	e.DeleteWord()
	if e.Text != "one " {
		t.Errorf("text=%q", e.Text)
	}
	// Trailing whitespace goes with the word, so repeated presses converge.
	e.DeleteWord()
	if e.Text != "" {
		t.Errorf("text=%q", e.Text)
	}
	// At the start it is a no-op rather than an error.
	e.DeleteWord()
}

func TestEditorWordMovement(t *testing.T) {
	var e Editor
	e.Insert("alpha beta gamma")
	e.WordLeft()
	if e.Cursor != 11 {
		t.Errorf("cursor=%d, want the start of 'gamma'", e.Cursor)
	}
	e.WordLeft()
	if e.Cursor != 6 {
		t.Errorf("cursor=%d, want the start of 'beta'", e.Cursor)
	}
	e.WordRight()
	if e.Cursor != 10 {
		t.Errorf("cursor=%d, want the end of 'beta'", e.Cursor)
	}
	e.Home()
	e.WordLeft()
	if e.Cursor != 0 {
		t.Errorf("cursor=%d, want it to stop at the start", e.Cursor)
	}
	e.End()
	e.WordRight()
	if e.Cursor != len([]rune(e.Text)) {
		t.Errorf("cursor=%d, want it to stop at the end", e.Cursor)
	}
}

func TestEditorClearIsUndoable(t *testing.T) {
	// Esc clears the input, and the notice promises Ctrl+Z brings it back.
	var e Editor
	e.Insert("something valuable")
	e.Clear()
	if e.Text != "" {
		t.Fatalf("text=%q", e.Text)
	}
	if !e.Undo() || e.Text != "something valuable" {
		t.Errorf("clearing must be undoable, got %q", e.Text)
	}
}

func TestEditorStashRoundTrip(t *testing.T) {
	// One key both directions is what makes Ctrl+S useful mid-thought.
	var e Editor
	e.Insert("draft")
	if n := e.Stash(); n == "" {
		t.Error("stashing should announce itself")
	}
	if e.Text != "" {
		t.Errorf("text=%q, want it set aside", e.Text)
	}
	if n := e.Stash(); n == "" {
		t.Error("restoring should announce itself")
	}
	if e.Text != "draft" {
		t.Errorf("text=%q, want the draft back", e.Text)
	}
	// Nothing stashed and nothing typed is a no-op.
	e.Text, e.Cursor = "", 0
	if n := e.Stash(); n != "" {
		t.Errorf("notice=%q, want none", n)
	}
}

func TestEditorCursorClamping(t *testing.T) {
	// A cursor left past the end by an earlier edit must not panic.
	e := Editor{Text: "ab", Cursor: 99}
	e.Insert("c")
	if e.Text != "abc" {
		t.Errorf("text=%q", e.Text)
	}
	e = Editor{Text: "ab", Cursor: -5}
	e.Backspace()
	if e.Text != "ab" {
		t.Errorf("text=%q", e.Text)
	}
}

func TestNewlineKeys(t *testing.T) {
	for _, k := range []string{"shift+enter", "alt+enter"} {
		if !NewlineKeys[k] {
			t.Errorf("%s should insert a newline", k)
		}
	}
	if NewlineKeys["enter"] {
		t.Error("plain Enter must submit, not insert a newline")
	}
}
