// Package ansirender turns captured terminal output (ANSI escapes and all) into a
// cell grid, and that grid into a PNG. It exists so a building agent can *look* at
// what the TUI drew without a human watching a terminal (plan.md §14).
package ansirender

import (
	"image/color"
	"strings"

	"github.com/rivo/uniseg"
)

// Defaults for cells the stream never colored. The background is normative
// (plan.md §14); the foreground is a plain light gray.
var (
	DefaultFG = color.RGBA{R: 204, G: 204, B: 204, A: 255}
	DefaultBG = color.RGBA{R: 18, G: 18, B: 24, A: 255}
)

// Cell is one terminal cell. Text is a whole grapheme cluster so that a wide
// glyph stays intact; the trailing half of a double-width glyph is a Cell with
// an empty Text carrying only the background.
type Cell struct {
	Text                string
	FG, BG              color.RGBA
	Bold, Faint, Italic bool
}

// Screen is a rectangular grid of cells, row-major.
type Screen struct {
	Rows [][]Cell
}

// Size reports the grid dimensions in cells.
func (s *Screen) Size() (cols, rows int) {
	for _, r := range s.Rows {
		if len(r) > cols {
			cols = len(r)
		}
	}
	return cols, len(s.Rows)
}

// At returns the cell at (col, row), or a blank default-styled cell when the
// coordinates fall outside the written region.
func (s *Screen) At(col, row int) Cell {
	if row < 0 || row >= len(s.Rows) || col < 0 || col >= len(s.Rows[row]) {
		return Cell{FG: DefaultFG, BG: DefaultBG}
	}
	return s.Rows[row][col]
}

// Text renders the grid back to plain text, one line per row, trailing blanks
// trimmed. This is what golden-frame comparisons diff against.
func (s *Screen) Text() string {
	var b strings.Builder
	for i, row := range s.Rows {
		if i > 0 {
			b.WriteByte('\n')
		}
		b.WriteString(strings.TrimRight(rowText(row), " "))
	}
	return b.String()
}

func rowText(row []Cell) string {
	var b strings.Builder
	for i := 0; i < len(row); i++ {
		if row[i].Text == "" {
			b.WriteByte(' ')
			continue
		}
		b.WriteString(row[i].Text)
		// A wide glyph owns the following cell too; that cell is empty and must
		// not also emit a padding space.
		i += uniseg.StringWidth(row[i].Text) - 1
	}
	return b.String()
}

// style is the SGR state machine's accumulated attributes.
type style struct {
	fg, bg                       color.RGBA
	bold, faint, italic, reverse bool
}

func newStyle() style {
	return style{fg: DefaultFG, bg: DefaultBG}
}

// cell materializes the current style into a cell, resolving reverse video by
// swapping the resolved colors (so a reversed default cell is dark-on-light).
func (st style) cell(text string) Cell {
	fg, bg := st.fg, st.bg
	if st.reverse {
		fg, bg = bg, fg
	}
	return Cell{Text: text, FG: fg, BG: bg, Bold: st.bold, Faint: st.faint, Italic: st.italic}
}

// TabWidth is the column stop interval used for a literal tab.
const TabWidth = 8

// Parse converts terminal output into a cell grid. It understands SGR (`CSI…m`)
// per plan.md §14 and skips every other escape sequence rather than letting its
// bytes leak into the grid as text.
func Parse(input string) *Screen {
	scr := &Screen{}
	st := newStyle()
	row, col := 0, 0

	blank := Cell{FG: DefaultFG, BG: DefaultBG}
	put := func(text string, width int) {
		for len(scr.Rows) <= row {
			scr.Rows = append(scr.Rows, nil)
		}
		// Cells skipped over (by \r or \t) were never written, so they keep the
		// default style rather than inheriting whatever is active now.
		for len(scr.Rows[row]) < col+width {
			scr.Rows[row] = append(scr.Rows[row], blank)
		}
		scr.Rows[row][col] = st.cell(text)
		// The continuation cells of a wide glyph carry background only.
		for i := 1; i < width; i++ {
			scr.Rows[row][col+i] = st.cell("")
		}
		col += width
	}

	// touch guarantees a row exists even if nothing was ever written to it, so a
	// trailing blank line survives into the grid.
	touch := func() {
		for len(scr.Rows) <= row {
			scr.Rows = append(scr.Rows, nil)
		}
	}

	state := -1 // uniseg grapheme iterator state
	rest := input
	for len(rest) > 0 {
		switch rest[0] {
		case 0x1b:
			n := skipEscape(rest, &st)
			rest = rest[n:]
			state = -1
			continue
		case '\n':
			touch()
			row++
			col = 0
			rest = rest[1:]
			state = -1
			continue
		case '\r':
			col = 0
			rest = rest[1:]
			state = -1
			continue
		case '\t':
			touch()
			next := (col/TabWidth + 1) * TabWidth
			for col < next {
				put(" ", 1)
			}
			rest = rest[1:]
			state = -1
			continue
		}

		// StepString's third result is a boundary bitmask; the display width
		// lives in its high bits, not in the value itself.
		cluster, remainder, boundaries, newState := uniseg.StepString(rest, state)
		width := boundaries >> uniseg.ShiftWidth
		state, rest = newState, remainder
		if cluster == "" {
			continue
		}
		// Control characters other than the ones handled above have no cell.
		if len(cluster) == 1 && cluster[0] < 0x20 {
			continue
		}
		if width < 1 {
			// Zero-width joiners and combining marks ride along with the
			// preceding cell rather than claiming one of their own.
			if col > 0 && row < len(scr.Rows) && col-1 < len(scr.Rows[row]) {
				scr.Rows[row][col-1].Text += cluster
			}
			continue
		}
		touch()
		put(cluster, width)
	}
	touch()

	// Square off the grid so every row is addressable at every column.
	cols, _ := scr.Size()
	for i := range scr.Rows {
		for len(scr.Rows[i]) < cols {
			scr.Rows[i] = append(scr.Rows[i], Cell{FG: DefaultFG, BG: DefaultBG})
		}
	}
	return scr
}

// skipEscape consumes one escape sequence starting at s[0] == ESC, applying it
// if it is an SGR sequence, and returns how many bytes it swallowed.
func skipEscape(s string, st *style) int {
	if len(s) < 2 {
		return len(s)
	}
	switch s[1] {
	case '[': // CSI: parameters, then a final byte in @..~
		i := 2
		for i < len(s) && (s[i] < '@' || s[i] > '~') {
			i++
		}
		if i >= len(s) {
			return len(s)
		}
		if s[i] == 'm' {
			applySGR(s[2:i], st)
		}
		return i + 1
	case ']': // OSC: runs until BEL or ST (ESC \)
		i := 2
		for i < len(s) {
			if s[i] == 0x07 {
				return i + 1
			}
			if s[i] == 0x1b && i+1 < len(s) && s[i+1] == '\\' {
				return i + 2
			}
			i++
		}
		return len(s)
	case 'P', 'X', '^', '_': // DCS/SOS/PM/APC: run until ST
		i := 2
		for i < len(s) {
			if s[i] == 0x1b && i+1 < len(s) && s[i+1] == '\\' {
				return i + 2
			}
			i++
		}
		return len(s)
	case '(', ')', '*', '+': // charset designation: ESC ( B and friends
		if len(s) < 3 {
			return len(s)
		}
		return 3
	default: // two-byte escape
		return 2
	}
}

// applySGR mutates the style by the semicolon-separated parameters of a `CSI…m`
// sequence. Unknown codes are ignored rather than aborting the run.
func applySGR(params string, st *style) {
	codes := splitParams(params)
	for i := 0; i < len(codes); i++ {
		switch n := codes[i]; {
		case n == 0:
			*st = newStyle()
		case n == 1:
			st.bold = true
		case n == 2:
			st.faint = true
		case n == 3:
			st.italic = true
		case n == 7:
			st.reverse = true
		case n == 22:
			st.bold, st.faint = false, false
		case n == 23:
			st.italic = false
		case n == 27:
			st.reverse = false
		case n >= 30 && n <= 37:
			st.fg = palette256(n - 30)
		case n == 38:
			if c, used, ok := extendedColor(codes[i:]); ok {
				st.fg = c
				i += used - 1
			}
		case n == 39:
			st.fg = DefaultFG
		case n >= 40 && n <= 47:
			st.bg = palette256(n - 40)
		case n == 48:
			if c, used, ok := extendedColor(codes[i:]); ok {
				st.bg = c
				i += used - 1
			}
		case n == 49:
			st.bg = DefaultBG
		case n >= 90 && n <= 97:
			st.fg = palette256(n - 90 + 8)
		case n >= 100 && n <= 107:
			st.bg = palette256(n - 100 + 8)
		}
	}
}

// extendedColor decodes `38;5;n` / `38;2;r;g;b` (and the 48 background twins)
// starting at codes[0], returning the color and how many parameters it ate.
func extendedColor(codes []int) (c color.RGBA, used int, ok bool) {
	if len(codes) < 2 {
		return c, 0, false
	}
	switch codes[1] {
	case 5:
		if len(codes) < 3 {
			return c, 0, false
		}
		return palette256(codes[2]), 3, true
	case 2:
		if len(codes) < 5 {
			return c, 0, false
		}
		return color.RGBA{R: clamp8(codes[2]), G: clamp8(codes[3]), B: clamp8(codes[4]), A: 255}, 5, true
	}
	return c, 0, false
}

func clamp8(v int) uint8 {
	if v < 0 {
		return 0
	}
	if v > 255 {
		return 255
	}
	return uint8(v)
}

func splitParams(s string) []int {
	if s == "" {
		return []int{0} // bare `CSI m` means reset
	}
	// Some emitters use colons inside 38:2:… — treat both separators alike.
	fields := strings.FieldsFunc(s, func(r rune) bool { return r == ';' || r == ':' })
	if len(fields) == 0 {
		return []int{0}
	}
	out := make([]int, 0, len(fields))
	for _, f := range fields {
		n := 0
		for _, r := range f {
			if r < '0' || r > '9' {
				n = 0
				break
			}
			n = n*10 + int(r-'0')
		}
		out = append(out, n)
	}
	return out
}

// base16 is the conventional xterm rendering of the first sixteen palette slots.
var base16 = [16]color.RGBA{
	{0, 0, 0, 255}, {128, 0, 0, 255}, {0, 128, 0, 255}, {128, 128, 0, 255},
	{0, 0, 128, 255}, {128, 0, 128, 255}, {0, 128, 128, 255}, {192, 192, 192, 255},
	{128, 128, 128, 255}, {255, 0, 0, 255}, {0, 255, 0, 255}, {255, 255, 0, 255},
	{0, 0, 255, 255}, {255, 0, 255, 255}, {0, 255, 255, 255}, {255, 255, 255, 255},
}

// palette256 resolves an xterm-256 index: sixteen base colors, a 6×6×6 cube
// whose levels are 0 then 55+40x, then a 24-step grayscale ramp of 8+10n.
func palette256(n int) color.RGBA {
	switch {
	case n < 0 || n > 255:
		return DefaultFG
	case n < 16:
		return base16[n]
	case n < 232:
		n -= 16
		level := func(x int) uint8 {
			if x == 0 {
				return 0
			}
			return uint8(55 + 40*x)
		}
		return color.RGBA{R: level(n / 36), G: level((n / 6) % 6), B: level(n % 6), A: 255}
	default:
		v := uint8(8 + 10*(n-232))
		return color.RGBA{R: v, G: v, B: v, A: 255}
	}
}
