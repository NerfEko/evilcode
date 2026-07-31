package tui

import (
	"fmt"
	"strings"

	"charm.land/lipgloss/v2"

	"evilcode/internal/theme"
)

// HistoryRows is the visible window (plan.md §5.2).
const HistoryRows = 8

// HistorySearch is the Ctrl+R overlay's state.
//
// It is readline's reverse-i-search: the selected match is written into the
// composer as you navigate, and cancelling restores the draft you had before
// opening it. That live preview is what makes it usable without a second
// preview pane.
type HistorySearch struct {
	Active bool
	Query  string

	// Matches is the current result set, best first.
	Matches []string

	// Selected indexes Matches.
	Selected int

	// savedDraft and savedCursor hold the composer contents from before the
	// search opened, so cancelling is lossless.
	savedDraft  string
	savedCursor int
}

// Open starts a search, remembering the current draft.
func (h *HistorySearch) Open(draft string, cursor int) {
	h.Active = true
	h.Query = ""
	h.Matches = nil
	h.Selected = 0
	h.savedDraft, h.savedCursor = draft, cursor
}

// Close ends the search and reports the draft to restore when cancelling.
func (h *HistorySearch) Close() (draft string, cursor int) {
	h.Active = false
	h.Query = ""
	h.Matches = nil
	h.Selected = 0
	return h.savedDraft, h.savedCursor
}

// Current returns the selected match, or "" when there is none.
func (h *HistorySearch) Current() string {
	if len(h.Matches) == 0 {
		return ""
	}
	return h.Matches[clamp(h.Selected, 0, len(h.Matches)-1)]
}

// Move advances the selection. Older is Up and Ctrl+R again; newer is Down.
func (h *HistorySearch) Move(delta int) {
	if len(h.Matches) == 0 {
		return
	}
	h.Selected = clamp(h.Selected+delta, 0, len(h.Matches)-1)
}

// RenderHistorySearch draws the floating search overlay. Like the slash
// palette it reserves no layout height (plan.md §5.2), and it is drawn after
// the palette so it wins when both could apply.
func (r *Renderer) RenderHistorySearch(h *HistorySearch) []string {
	if !h.Active {
		return nil
	}

	dim := r.style(theme.RoleDim)
	gold := lipgloss.NewStyle().Foreground(lipgloss.Color(theme.Hex(paletteGold)))
	teal := lipgloss.NewStyle().Foreground(lipgloss.Color(theme.Hex(paletteTeal)))

	header := dim.Render("(history search) ") +
		gold.Render(h.Query) + gold.Render("█")
	// The key hints are the part worth dropping when the terminal is narrow:
	// the query you are typing is not.
	if hints := "  ↑↓ select · ↵ insert · Esc cancel"; lipgloss.Width(header)+
		lipgloss.Width(hints) <= r.Width {
		header += dim.Render(hints)
	}

	out := []string{header}

	switch {
	case h.Query == "":
		// An empty query matches nothing, as readline does; saying so is
		// better than showing the whole history.
		return append(out, dim.Render("  type to search history"))
	case len(h.Matches) == 0:
		return append(out, dim.Render("  no matches"))
	}

	sel := clamp(h.Selected, 0, len(h.Matches)-1)
	start := 0
	if sel >= HistoryRows {
		start = sel - HistoryRows + 1
	}
	end := min(start+HistoryRows, len(h.Matches))

	for i := start; i < end; i++ {
		text := truncateCells(strings.ReplaceAll(h.Matches[i], "\n", " "), max(r.Width-6, 20))
		if i == sel {
			out = append(out, gold.Render("  ▸ "+text))
		} else {
			out = append(out, teal.Render("    "+text))
		}
	}
	if end < len(h.Matches) {
		out = append(out, dim.Render(fmt.Sprintf("    ...  +%d more", len(h.Matches)-end)))
	}
	return out
}
