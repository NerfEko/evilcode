package tui

import (
	"fmt"
	"strings"
	"time"
)

// TrailingEnterGuard swallows a bare Enter arriving right after a paste. Many
// terminals end a bracketed paste with a newline, and without this guard
// pasting a block submits it immediately (plan.md §6.6).
const TrailingEnterGuard = 150 * time.Millisecond

// PasteCollapseLines is the size at which a paste collapses to a placeholder
// rather than filling the composer.
const PasteCollapseLines = 5

// EndsWithEscapedNewline reports whether a trailing backslash means "insert a
// newline" rather than "submit".
//
// The rule is parity: an odd number of trailing backslashes escapes the Enter,
// an even number does not — so `\` continues the line and `\\` is a literal
// backslash that still submits. This is the universal fallback for terminals
// with no kitty keyboard protocol, and it has to keep working forever
// (plan.md §6.2).
func EndsWithEscapedNewline(s string) bool {
	n := 0
	for i := len(s) - 1; i >= 0 && s[i] == '\\'; i-- {
		n++
	}
	return n%2 == 1
}

// StripEscapedNewline removes the escaping backslash that EndsWithEscapedNewline
// detected, since it is an instruction rather than content.
func StripEscapedNewline(s string) string {
	if !EndsWithEscapedNewline(s) {
		return s
	}
	return s[:len(s)-1]
}

// NewlineKeys are the chords that insert a newline instead of submitting
// (plan.md §6.2). Shift+Enter needs the kitty keyboard protocol; Alt+Enter
// arrives as ESC+CR and works everywhere.
var NewlineKeys = map[string]bool{
	"shift+enter": true,
	"alt+enter":   true,
}

// Paste is a stored paste whose contents were collapsed in the composer.
type Paste struct {
	Placeholder string
	Content     string
	Lines       int
}

// CollapsePaste decides how a paste enters the composer. A large paste becomes
// a placeholder so the composer stays readable; the contents are stored and
// restored on send.
func CollapsePaste(content string) (insert string, stored *Paste) {
	lines := strings.Count(content, "\n") + 1
	if lines < PasteCollapseLines {
		return content, nil
	}
	placeholder := fmt.Sprintf("[pasted %d lines]", lines)
	return placeholder, &Paste{Placeholder: placeholder, Content: content, Lines: lines}
}

// ExpandPastes restores collapsed pastes before sending.
//
// Replacement walks the stored pastes in reverse and replaces the *last*
// occurrence of each placeholder. Two pastes of the same size share a
// placeholder string, and replacing the first occurrence each time would give
// both the same content.
func ExpandPastes(input string, pastes []Paste) string {
	for i := len(pastes) - 1; i >= 0; i-- {
		p := pastes[i]
		idx := strings.LastIndex(input, p.Placeholder)
		if idx < 0 {
			continue
		}
		input = input[:idx] + p.Content + input[idx+len(p.Placeholder):]
	}
	return input
}

// ImageExtensions are the file types a drop attaches as an image.
var ImageExtensions = map[string]bool{
	".png": true, ".jpg": true, ".jpeg": true,
	".gif": true, ".webp": true, ".bmp": true, ".tiff": true,
}

// IsImagePath reports whether a dropped path should be attached as an image.
func IsImagePath(path string) bool {
	if i := strings.LastIndex(path, "."); i >= 0 {
		return ImageExtensions[strings.ToLower(path[i:])]
	}
	return false
}

// QuoteIfNeeded wraps a dropped path in quotes when it contains whitespace, so
// it survives being pasted into a shell command.
func QuoteIfNeeded(path string) string {
	if strings.ContainsAny(path, " \t") {
		return `"` + path + `"`
	}
	return path
}

// Editor is the composer's text buffer with the readline edits of §6.5.
type Editor struct {
	Text   string
	Cursor int

	// undo holds the buffer before the last destructive edit. One level is
	// enough: this is a prompt box, not an editor.
	undoText   string
	undoCursor int
	hasUndo    bool

	// stash holds a set-aside draft for Ctrl+S.
	stash    string
	hasStash bool
}

func (e *Editor) runes() []rune { return []rune(e.Text) }

func (e *Editor) clampCursor() {
	n := len(e.runes())
	e.Cursor = clamp(e.Cursor, 0, n)
}

// saveUndo records the buffer before a destructive edit.
func (e *Editor) saveUndo() {
	e.undoText, e.undoCursor, e.hasUndo = e.Text, e.Cursor, true
}

// Undo restores the buffer from before the last destructive edit.
func (e *Editor) Undo() bool {
	if !e.hasUndo {
		return false
	}
	e.Text, e.Cursor = e.undoText, e.undoCursor
	e.hasUndo = false
	e.clampCursor()
	return true
}

// Insert adds text at the cursor.
func (e *Editor) Insert(s string) {
	e.clampCursor()
	r := e.runes()
	e.Text = string(r[:e.Cursor]) + s + string(r[e.Cursor:])
	e.Cursor += len([]rune(s))
}

// Backspace deletes the rune before the cursor.
func (e *Editor) Backspace() {
	e.clampCursor()
	if e.Cursor == 0 {
		return
	}
	r := e.runes()
	e.Text = string(r[:e.Cursor-1]) + string(r[e.Cursor:])
	e.Cursor--
}

// Delete removes the rune at the cursor.
func (e *Editor) Delete() {
	e.clampCursor()
	r := e.runes()
	if e.Cursor >= len(r) {
		return
	}
	e.Text = string(r[:e.Cursor]) + string(r[e.Cursor+1:])
}

// KillToStart is Ctrl+U.
func (e *Editor) KillToStart() {
	e.clampCursor()
	e.saveUndo()
	r := e.runes()
	e.Text = string(r[e.Cursor:])
	e.Cursor = 0
}

// KillToEnd is Ctrl+K.
func (e *Editor) KillToEnd() {
	e.clampCursor()
	e.saveUndo()
	e.Text = string(e.runes()[:e.Cursor])
}

// DeleteWord is Ctrl+W: remove the word before the cursor, including the
// whitespace that separates it.
func (e *Editor) DeleteWord() {
	e.clampCursor()
	if e.Cursor == 0 {
		return
	}
	e.saveUndo()
	r := e.runes()
	i := e.Cursor
	for i > 0 && isSpace(r[i-1]) {
		i--
	}
	for i > 0 && !isSpace(r[i-1]) {
		i--
	}
	e.Text = string(r[:i]) + string(r[e.Cursor:])
	e.Cursor = i
}

// Clear empties the buffer, keeping an undo point so Esc is recoverable.
func (e *Editor) Clear() {
	if e.Text == "" {
		return
	}
	e.saveUndo()
	e.Text, e.Cursor = "", 0
}

// Home and End move to the line's edges.
func (e *Editor) Home() { e.Cursor = 0 }
func (e *Editor) End()  { e.Cursor = len(e.runes()) }

// Left and Right move by one rune.
func (e *Editor) Left() {
	e.clampCursor()
	if e.Cursor > 0 {
		e.Cursor--
	}
}

func (e *Editor) Right() {
	e.clampCursor()
	if e.Cursor < len(e.runes()) {
		e.Cursor++
	}
}

// WordLeft and WordRight move by words, for Ctrl+B/F and Ctrl+←/→.
func (e *Editor) WordLeft() {
	e.clampCursor()
	r := e.runes()
	i := e.Cursor
	for i > 0 && isSpace(r[i-1]) {
		i--
	}
	for i > 0 && !isSpace(r[i-1]) {
		i--
	}
	e.Cursor = i
}

func (e *Editor) WordRight() {
	e.clampCursor()
	r := e.runes()
	i := e.Cursor
	for i < len(r) && isSpace(r[i]) {
		i++
	}
	for i < len(r) && !isSpace(r[i]) {
		i++
	}
	e.Cursor = i
}

// Stash sets the draft aside, or restores it when the buffer is empty. One key
// for both directions is what makes Ctrl+S useful mid-thought.
func (e *Editor) Stash() string {
	switch {
	case e.Text != "":
		e.stash, e.hasStash = e.Text, true
		e.Text, e.Cursor = "", 0
		return "Input stashed - Ctrl+S to restore"
	case e.hasStash:
		e.Text = e.stash
		e.Cursor = len(e.runes())
		e.hasStash = false
		return "Input restored"
	default:
		return ""
	}
}

func isSpace(r rune) bool { return r == ' ' || r == '\t' || r == '\n' }
