package tui

import (
	"fmt"
	"strconv"
	"strings"

	"charm.land/lipgloss/v2"

	"evilcode/internal/theme"
)

// SendAction is what pressing Enter does right now (plan.md §6.3).
type SendAction int

const (
	// Submit starts a turn.
	Submit SendAction = iota

	// Queue holds the message until the current turn finishes.
	Queue

	// Interleave injects the message into the live turn at a safe point. It is
	// the KV-cache-friendly path: not a new request, just a user message
	// appended so the next loop iteration carries it with the cache intact.
	Interleave
)

func (s SendAction) String() string {
	switch s {
	case Queue:
		return "queue"
	case Interleave:
		return "interleave"
	default:
		return "submit"
	}
}

// SendActionFor decides what Enter (or Ctrl+Enter, via alternate) does.
//
// Ctrl+Enter always means "the opposite of my current mode", which is why the
// alternate branch inverts rather than picking a fixed action.
func SendActionFor(processing, queueMode bool, input string, alternate bool) SendAction {
	if !processing {
		return Submit
	}
	// A slash command or a shell escape is for the harness, not the model, so
	// it runs immediately regardless of mode.
	trimmed := strings.TrimSpace(input)
	if strings.HasPrefix(trimmed, "/") || strings.HasPrefix(trimmed, "!") {
		return Submit
	}
	if alternate {
		if queueMode {
			return Interleave
		}
		return Queue
	}
	if queueMode {
		return Queue
	}
	return Interleave
}

// ComposerState is everything the composer needs to draw itself.
type ComposerState struct {
	// Text is the current input.
	Text string

	// Cursor is the rune offset of the caret within Text.
	Cursor int

	// PromptNumber is how many prompts have been sent; the composer shows the
	// next one, so it prints PromptNumber+1.
	PromptNumber int

	Processing bool
	QueueMode  bool

	// Model, CtxUsed, CtxMax and Session feed the idle hint. At rest the row
	// is the only always-visible place to put live state, and it was spending
	// itself on a keybinding that does not apply when nothing is running.
	Model      string
	CtxUsed    int
	CtxMax     int
	Session    string
	ShellMode  bool
	SkillMode  bool
	NewSession bool

	// PaletteOpen hides the hint line, since the palette floats where it goes.
	PaletteOpen bool

	// Pending counts staged messages, for the send-mode indicator.
	Pending int
}

// MaxComposerRows caps the visible input height; beyond this it scrolls
// internally following the cursor (plan.md §6.1).
const MaxComposerRows = 10

// promptGlyph returns the composer's leading glyph and its color for the
// current state (plan.md §6.1).
func (r *Renderer) promptGlyph(s ComposerState) (string, lipgloss.Style) {
	switch {
	case s.ShellMode:
		return "$ ", rgbStyle(110, 214, 151)
	case s.Processing:
		return "… ", r.style(theme.RoleQueued)
	case s.SkillMode:
		return "» ", r.style(theme.RoleAccent)
	default:
		return "> ", r.style(theme.RoleUser)
	}
}

// sendModeGlyph is the single right-aligned glyph on the composer's last row.
func (r *Renderer) sendModeGlyph(s ComposerState) string {
	switch {
	case s.ShellMode:
		return rgbStyle(110, 214, 151).Render("$")
	case s.NewSession:
		return rgbStyle(120, 200, 255).Render("↗")
	case s.QueueMode:
		return r.style(theme.RoleQueued).Render("⏳")
	default:
		return ""
	}
}

// hintLine is the one row under the input. It is hidden while the palette is
// open, because the palette floats over exactly that space.
func (r *Renderer) hintLine(s ComposerState) string {
	if s.PaletteOpen {
		return ""
	}
	dim := r.style(theme.RoleDim)
	switch {
	case s.ShellMode:
		return rgbStyle(110, 214, 151).Render("  shell mode · Enter runs locally")
	case s.NewSession:
		return rgbStyle(120, 200, 255).Render("  ↗ Next prompt opens a new session")
	case s.Processing && s.QueueMode:
		return dim.Render("  Ctrl+Enter to send now")
	case s.Processing:
		return dim.Render("  Ctrl+Enter to queue")
	default:
		// Nothing is running, so there is nothing to queue behind — saying so
		// was both clutter and untrue. This row is the one piece of screen that
		// is always visible, so it carries what is always worth knowing.
		return dim.Render("  " + idleHint(s))
	}
}

// roundTokens renders a context window, which is always a round number and
// reads badly with a decimal: "200k", not "200.0k".
func roundTokens(n int) string {
	switch {
	case n >= 1_000_000 && n%1_000_000 == 0:
		return fmt.Sprintf("%dM", n/1_000_000)
	case n >= 1000 && n%1000 == 0:
		return fmt.Sprintf("%dk", n/1000)
	default:
		return humanTokens(n)
	}
}

// idleHint is the resting composer line: what model, how full the context is,
// and which session. Each part is omitted when it is not known yet rather than
// rendered as a zero.
func idleHint(s ComposerState) string {
	var parts []string
	if s.Model != "" {
		parts = append(parts, s.Model)
	}
	if s.CtxUsed > 0 && s.CtxMax > 0 {
		ctx := fmt.Sprintf("%s/%s ctx", humanTokens(s.CtxUsed), roundTokens(s.CtxMax))
		// The percentage only earns its space once it is worth watching. At 0%
		// it is noise beside the two numbers that already say the same thing.
		if pct := s.CtxUsed * 100 / s.CtxMax; pct >= 1 {
			ctx += fmt.Sprintf(" · %d%%", pct)
		}
		parts = append(parts, ctx)
	}
	if s.Session != "" {
		parts = append(parts, s.Session)
	}
	if len(parts) == 0 {
		// A fresh session with nothing resolved yet still needs the row to say
		// something, and this is the one binding a new reader most needs.
		return "Enter to send · Ctrl+J for a newline"
	}
	return strings.Join(parts, " · ")
}

// RenderComposer draws the input rows plus the hint line (plan.md §6.1).
func (r *Renderer) RenderComposer(s ComposerState) []string {
	glyph, glyphStyle := r.promptGlyph(s)
	numStyle := lipgloss.NewStyle().
		Foreground(lipgloss.Color(theme.Hex(theme.Rainbow(0)))).Bold(true)

	label := ""
	if !s.ShellMode {
		label = strconv.Itoa(s.PromptNumber + 1)
	}
	prefix := label + glyph
	prefixWidth := lipgloss.Width(prefix)
	indent := strings.Repeat(" ", prefixWidth)

	textStyle := r.style(theme.RoleUserText)
	body := s.Text

	wrapped := wrapPlain(body, max(r.Width-prefixWidth-1, 1))
	// Follow the cursor: keep the last rows visible rather than the first.
	if len(wrapped) > MaxComposerRows {
		wrapped = wrapped[len(wrapped)-MaxComposerRows:]
	}

	out := make([]string, 0, len(wrapped)+1)
	for i, line := range wrapped {
		head := indent
		if i == 0 {
			head = numStyle.Render(label) + glyphStyle.Render(glyph)
		}
		row := head + textStyle.Render(line)

		// The send-mode indicator rides the last row, right-aligned.
		if i == len(wrapped)-1 {
			if g := r.sendModeGlyph(s); g != "" {
				pad := r.Width - lipgloss.Width(row) - lipgloss.Width(g)
				if pad > 0 {
					row += strings.Repeat(" ", pad) + g
				}
			}
		}
		out = append(out, row)
	}

	if hint := r.hintLine(s); hint != "" {
		out = append(out, hint)
	}
	return out
}

// PendingKind classifies a staged message (plan.md §6.4).
type PendingKind int

const (
	// PendingSent has already gone in as a soft interrupt.
	PendingSent PendingKind = iota

	// PendingInterleave is staged to go in at the next safe point.
	PendingInterleave

	// PendingQueued waits for the turn to end.
	PendingQueued
)

// PendingMessage is one staged message row.
type PendingMessage struct {
	Kind PendingKind
	Text string
}

// MaxPendingRows is how many staged messages show at once.
const MaxPendingRows = 3

// RenderPending draws the queued-message rows. The number is rainbow-decayed by
// distance from the *front* of the queue, so the message going in next is the
// most saturated (plan.md §6.4).
func (r *Renderer) RenderPending(msgs []PendingMessage) []string {
	if len(msgs) == 0 {
		return nil
	}
	shown := msgs
	if len(shown) > MaxPendingRows {
		shown = shown[:MaxPendingRows]
	}

	out := make([]string, 0, len(shown))
	for i, m := range shown {
		var glyph string
		var glyphStyle, textStyle lipgloss.Style
		switch m.Kind {
		case PendingSent:
			glyph = "↻"
			glyphStyle = r.style(theme.RolePending)
			textStyle = r.style(theme.RoleAIText)
		case PendingInterleave:
			glyph = "⚡"
			glyphStyle = r.style(theme.RoleAsap)
			textStyle = r.style(theme.RoleAIText)
		default:
			glyph = "⏳"
			glyphStyle = r.style(theme.RoleQueued)
			textStyle = r.style(theme.RoleDim)
		}

		numStyle := lipgloss.NewStyle().
			Foreground(lipgloss.Color(theme.Hex(theme.Rainbow(len(shown) - 1 - i))))
		text := truncateCells(strings.ReplaceAll(m.Text, "\n", " "), max(r.Width-6, 8))
		out = append(out, numStyle.Render(strconv.Itoa(i+1))+" "+
			glyphStyle.Render(glyph)+" "+textStyle.Render(text))
	}
	return out
}
