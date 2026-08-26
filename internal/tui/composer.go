package tui

import (
	"fmt"
	"strconv"
	"strings"
	"unicode"

	"charm.land/lipgloss/v2"

	"evilcode/internal/provider"
	"evilcode/internal/theme"
)

// SendAction is what pressing Enter does right now (plan.md §6.3).
type SendAction int

const (
	// Submit starts a turn.
	Submit SendAction = iota

	// Queue holds the message until the current turn finishes.
	Queue
)

func (s SendAction) String() string {
	switch s {
	case Queue:
		return "queue"
	default:
		return "submit"
	}
}

// SendActionFor decides what Enter does.
//
// While a turn is running, every message queues until it ends — there is no
// immediate-send path (plan.md §6.3). A slash command is for the harness, not
// the model, so it runs immediately regardless.
func SendActionFor(processing bool, input string) SendAction {
	if !processing {
		return Submit
	}
	trimmed := strings.TrimSpace(input)
	if strings.HasPrefix(trimmed, "/") {
		return Submit
	}
	return Queue
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

	// Model, ReasoningEffort, CtxUsed, CtxMax and Session feed the idle hint. At rest the row
	// is the only always-visible place to put live state, and it was spending
	// itself on a keybinding that does not apply when nothing is running.
	Model           string
	ReasoningEffort provider.ReasoningEffort
	CtxUsed         int
	CtxMax          int
	Session         string
	SkillMode       bool
	NewSession      bool

	// PaletteOpen hides the hint line, since the palette floats where it goes.
	PaletteOpen bool

	// Masked keeps secrets in the composer while making every rendered frame
	// safe to screenshot.
	Masked bool
}

// MaxComposerRows caps the visible input height; beyond this it scrolls
// internally following the cursor (plan.md §6.1).
const MaxComposerRows = 10

// promptGlyph returns the composer's leading glyph and its color for the
// current state (plan.md §6.1).
func (r *Renderer) promptGlyph(s ComposerState) (string, lipgloss.Style) {
	switch {
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
	if s.NewSession {
		return rgbStyle(120, 200, 255).Render("↗")
	}
	return ""
}

// hintLine is the one row under the input. It is hidden while the palette is
// open, because the palette floats over exactly that space.
func (r *Renderer) hintLine(s ComposerState) string {
	if s.PaletteOpen {
		return ""
	}
	dim := r.style(theme.RoleDim)
	switch {
	case s.NewSession:
		return rgbStyle(120, 200, 255).Render("  ↗ Next prompt opens a new session")
	case s.Processing:
		return dim.Render("  Enter queues until the turn ends")
	default:
		// Nothing is running, so there is nothing to queue behind. This row is
		// the one piece of screen that is always visible, so it carries what
		// is always worth knowing.
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
		model := s.Model
		if s.ReasoningEffort.Valid() {
			model += " " + string(s.ReasoningEffort)
		}
		parts = append(parts, model)
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

// RenderComposer draws the input rows plus the hint line (plan.md §6.1). The
// caret is drawn as a reverse-video block on its cell so the input box has a
// visible cursor even in the alternate screen, where the terminal's own caret
// is hidden.
func (r *Renderer) RenderComposer(s ComposerState) []string {
	glyph, glyphStyle := r.promptGlyph(s)
	numStyle := lipgloss.NewStyle().
		Foreground(lipgloss.Color(theme.Hex(theme.Rainbow(0)))).Bold(true)

	label := strconv.Itoa(s.PromptNumber + 1)
	prefix := label + glyph
	prefixWidth := lipgloss.Width(prefix)
	indent := strings.Repeat(" ", prefixWidth)

	textStyle := r.style(theme.RoleUserText)
	cursorStyle := textStyle.Reverse(true)
	body := s.Text
	if s.Masked {
		var masked strings.Builder
		for _, ch := range body {
			if ch == '\n' {
				masked.WriteRune(ch)
			} else {
				masked.WriteRune('•')
			}
		}
		body = masked.String()
	}

	wrapWidth := max(r.Width-prefixWidth-1, 1)
	wrapped, caretLine, caretCol := wrapPlainWithCursor(body, s.Cursor, wrapWidth)
	// Follow the cursor: keep the last rows visible rather than the first.
	visibleCaret := caretLine
	if len(wrapped) > MaxComposerRows {
		offset := len(wrapped) - MaxComposerRows
		wrapped = wrapped[offset:]
		visibleCaret = caretLine - offset
	}

	out := make([]string, 0, len(wrapped)+1)
	for i, line := range wrapped {
		head := indent
		if i == 0 {
			head = numStyle.Render(label) + glyphStyle.Render(glyph)
		}

		var row string
		if i == visibleCaret {
			pre, cell, post, cellW := splitCellsPlain(line, caretCol)
			if cellW == 0 {
				// The caret sits on an empty cell (end of line or blank line):
				// draw a solid block where the next character will land.
				row = head + textStyle.Render(pre) + cursorStyle.Render(" ") + textStyle.Render(post)
			} else {
				row = head + textStyle.Render(pre) + cursorStyle.Render(cell) + textStyle.Render(post)
			}
		} else {
			row = head + textStyle.Render(line)
		}

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

// wrapPlainWithCursor wraps body exactly like [wrapPlain] and also reports the
// caret's position in the wrapped output. caretLine/caretCol are cell offsets
// relative to the full wrapped slice (before any MaxComposerRows windowing).
//
// The composer used to render its text with no caret: in the alternate screen
// the terminal cursor is hidden, so there was nothing showing where typed text
// would land. Tracking the caret through the same word-wrap that produces the
// rows keeps the block aligned with the text without a second wrapping pass.
func wrapPlainWithCursor(body string, cursor int, width int) (lines []string, caretLine, caretCol int) {
	if width < 1 {
		width = 1
	}
	if cursor < 0 {
		cursor = 0
	}
	if max := len([]rune(body)); cursor > max {
		cursor = max
	}
	caretLine, caretCol = 0, 0
	paraStart := 0
	for _, para := range strings.Split(body, "\n") {
		runes := []rune(para)
		paraLen := len(runes)
		paraEnd := paraStart + paraLen
		cInPara := -1
		if cursor >= paraStart && cursor <= paraEnd {
			cInPara = cursor - paraStart
			if cInPara > paraLen {
				cInPara = paraLen
			}
		}
		plines, line, col := wrapParagraphWithCursor(runes, cInPara, width)
		if cInPara >= 0 {
			caretLine = len(lines) + line
			caretCol = col
		}
		lines = append(lines, plines...)
		paraStart = paraEnd + 1 // skip the newline rune
	}
	if len(lines) == 0 {
		lines = []string{""}
	}
	return lines, caretLine, caretCol
}

// wrapPiece is one rendered segment of a composer line: a whole word or a
// cell-chunk of a word too long to fit, with the rune span it covers and whether
// a single space precedes it on the line.
type wrapPiece struct {
	text  string
	start int  // rune index within the paragraph
	end   int  // rune index within the paragraph (exclusive)
	space bool // a single space is rendered before this piece
}

// wrapParagraphWithCursor wraps one paragraph (no newlines) like [wrapPlain]
// and locates the caret. caret is a rune offset within the paragraph, or -1 when
// the caret is not in this paragraph.
func wrapParagraphWithCursor(runes []rune, caret int, width int) (lines []string, caretLine, caretCol int) {
	caretLine = -1
	if len(runes) == 0 {
		return []string{""}, 0, 0
	}
	type wordSpan struct{ start, end int }
	var words []wordSpan
	i := 0
	for i < len(runes) {
		for i < len(runes) && unicode.IsSpace(runes[i]) {
			i++
		}
		if i >= len(runes) {
			break
		}
		s := i
		for i < len(runes) && !unicode.IsSpace(runes[i]) {
			i++
		}
		words = append(words, wordSpan{s, i})
	}

	type lineInfo struct {
		text   string
		pieces []wrapPiece
		start  int
	}
	var infos []lineInfo

	var lineBuf strings.Builder
	var pieces []wrapPiece
	lineStart := -1
	flush := func() {
		infos = append(infos, lineInfo{text: lineBuf.String(), pieces: pieces, start: lineStart})
		lineBuf.Reset()
		pieces = nil
		lineStart = -1
	}

	for _, w := range words {
		wordStr := string(runes[w.start:w.end])
		if lineBuf.Len() == 0 {
			lineBuf.WriteString(wordStr)
			pieces = []wrapPiece{{text: wordStr, start: w.start, end: w.end, space: false}}
			lineStart = w.start
		} else if lipgloss.Width(lineBuf.String())+1+lipgloss.Width(wordStr) <= width {
			lineBuf.WriteByte(' ')
			lineBuf.WriteString(wordStr)
			pieces = append(pieces, wrapPiece{text: wordStr, start: w.start, end: w.end, space: true})
		} else {
			flush()
			lineBuf.WriteString(wordStr)
			pieces = []wrapPiece{{text: wordStr, start: w.start, end: w.end, space: false}}
			lineStart = w.start
		}
		// Break a single word longer than the width into cell chunks, matching
		// wrapPlain's splitPlainCells path.
		for lipgloss.Width(lineBuf.String()) > width {
			cur := lineBuf.String()
			chunk, remainder := splitPlainCells(cur, width)
			cRunes := len([]rune(chunk))
			wp := pieces[0]
			pieces[0] = wrapPiece{text: chunk, start: wp.start, end: wp.start + cRunes, space: wp.space}
			lineBuf.Reset()
			lineBuf.WriteString(chunk)
			flush()
			lineBuf.WriteString(remainder)
			pieces = []wrapPiece{{text: remainder, start: wp.start + cRunes, end: wp.end, space: false}}
			lineStart = wp.start + cRunes
		}
	}
	// Trailing whitespace after the last word is preserved on the final line
	// so the live caret on a trailing space stays visible while typing.
	// wrapPlain collapses it for completed transcript text, but the composer
	// is live input: without it, pressing space changes nothing on screen
	// until the next character lands, because the caret block already sat at
	// the end of the last word.
	trailingStart := 0
	if len(words) > 0 {
		trailingStart = words[len(words)-1].end
	}
	if trailingStart < len(runes) {
		trailing := string(runes[trailingStart:])
		if lineBuf.Len() == 0 {
			lineBuf.WriteString(trailing)
			pieces = []wrapPiece{{text: trailing, start: trailingStart, end: len(runes), space: false}}
			lineStart = trailingStart
		} else {
			lineBuf.WriteString(trailing)
			pieces = append(pieces, wrapPiece{text: trailing, start: trailingStart, end: len(runes), space: false})
		}
		for lipgloss.Width(lineBuf.String()) > width {
			cur := lineBuf.String()
			chunk, remainder := splitPlainCells(cur, width)
			cRunes := len([]rune(chunk))
			wp := pieces[0]
			pieces[0] = wrapPiece{text: chunk, start: wp.start, end: wp.start + cRunes, space: wp.space}
			lineBuf.Reset()
			lineBuf.WriteString(chunk)
			flush()
			lineBuf.WriteString(remainder)
			pieces = []wrapPiece{{text: remainder, start: wp.start + cRunes, end: wp.end, space: false}}
			lineStart = wp.start + cRunes
		}
	}
	if lineBuf.Len() > 0 || len(pieces) > 0 {
		flush()
	}
	if len(infos) == 0 {
		infos = []lineInfo{{text: ""}}
	}

	// Locate the caret across the assembled lines. Line k owns the rune range
	// [lo, nextStart): the first line also owns any leading whitespace, so its
	// lower bound is 0. The caret on an empty trailing cell or in the wrapping
	// gap before the next line maps to the end of this line.
	for k, li := range infos {
		lo := li.start
		if k == 0 {
			lo = 0
		}
		nextStart := len(runes)
		if k+1 < len(infos) {
			nextStart = infos[k+1].start
		}
		if caret >= lo && caret < nextStart {
			caretLine = k
			caretCol = colForCaret(li.pieces, caret)
			break
		}
	}
	if caretLine < 0 {
		// caret == paraLen (the newline position): end of the last line.
		caretLine = len(infos) - 1
		last := infos[caretLine]
		caretCol = lipgloss.Width(last.text)
	}

	out := make([]string, len(infos))
	for k, li := range infos {
		out[k] = li.text
	}
	return out, caretLine, caretCol
}

// colForCaret maps a caret rune offset to a cell column within one wrapped line
// given its pieces. Whitespace runs collapse to the single space wrapPlain
// renders, so a caret in a collapsed gap lands at the end of the previous word.
func colForCaret(pieces []wrapPiece, caret int) int {
	col := 0
	for j, p := range pieces {
		if j > 0 && p.space {
			prevEnd := pieces[j-1].end
			if caret >= prevEnd && caret < p.start {
				return col // end of the previous word, before the rendered space
			}
			col++
		}
		if caret >= p.start && caret < p.end {
			off := caret - p.start
			return col + lipgloss.Width(string([]rune(p.text)[:off]))
		}
		col += lipgloss.Width(p.text)
	}
	return col // trailing: end of the line
}

// splitCellsPlain splits plain (unstyled) text at a cell column, returning the
// prefix, the single cell at that column (or empty when the column is past the
// end), the suffix, and the cell's width.
func splitCellsPlain(s string, col int) (pre, cell, post string, cellW int) {
	if col < 0 {
		col = 0
	}
	runes := []rune(s)
	w := 0
	i := 0
	for i < len(runes) {
		rw := lipgloss.Width(string(runes[i]))
		if w == col || w+rw > col {
			break
		}
		w += rw
		i++
	}
	pre = string(runes[:i])
	if i < len(runes) {
		cell = string(runes[i])
		cellW = lipgloss.Width(cell)
		post = string(runes[i+1:])
	}
	return pre, cell, post, cellW
}

// PendingKind classifies a staged message (plan.md §6.4).
type PendingKind int

const (
	// PendingQueued waits for the turn to end. It is the only kind: while a
	// turn runs, every message queues; nothing is sent into the live turn.
	PendingQueued PendingKind = iota
)

// PendingMessage is one staged message row.
type PendingMessage struct {
	Kind PendingKind
	Text string
	WPM  int
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
		glyph := "⏳"
		glyphStyle := r.style(theme.RoleQueued)
		textStyle := r.style(theme.RoleDim)

		numStyle := lipgloss.NewStyle().
			Foreground(lipgloss.Color(theme.Hex(theme.Rainbow(len(shown) - 1 - i))))
		text := truncateCells(strings.ReplaceAll(m.Text, "\n", " "), max(r.Width-6, 8))
		out = append(out, numStyle.Render(strconv.Itoa(i+1))+" "+
			glyphStyle.Render(glyph)+" "+textStyle.Render(text))
	}
	return out
}
