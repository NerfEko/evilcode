package tui

import (
	"image/color"
	"sort"
	"strconv"
	"strings"

	"charm.land/lipgloss/v2"

	"evilcode/internal/theme"
)

// Palette colors (plan.md §5.1). These are ad-hoc literals rather than roles
// because the palette has its own visual identity: warm gold for the selection,
// teal for the alternatives.
var (
	paletteGold = theme.RGB(255, 213, 128)
	paletteTeal = theme.RGB(128, 203, 196)
)

// PaletteRows is the visible window height.
const PaletteRows = 8

// Suggestion is one ranked palette entry.
type Suggestion struct {
	Name string
	Desc string

	// Matched holds the indexes of Name's runes that the query matched, for
	// the recolor highlight.
	Matched []int

	// bucket is 1 for a literal prefix match and 0 for a fuzzy one. A prefix
	// match has absolute priority: exact typing must always beat fuzzy.
	bucket int
	score  int
}

// RankCommands filters and orders commands for a query. The query is the text
// after the leading slash.
//
// Ranking is two-bucket by design: anything the user literally prefixed sorts
// above every fuzzy match, however good the fuzzy score. Typing `/mod` must
// offer `/model` first even if some other command fuzzy-matches better.
func RankCommands(query string, cmds []Command) []Suggestion {
	q := strings.ToLower(strings.TrimSpace(query))
	out := make([]Suggestion, 0, len(cmds))

	for _, c := range cmds {
		name := strings.ToLower(c.Name)
		switch {
		case q == "":
			// No query: preserve registration order, which groups related
			// commands. Sorting an unfiltered list by length is just noise.
			out = append(out, Suggestion{
				Name: c.Name, Desc: c.Help, bucket: 1, score: len(cmds) - len(out),
			})

		case strings.HasPrefix(name, q):
			matched := make([]int, len([]rune(q)))
			for i := range matched {
				matched[i] = i
			}
			out = append(out, Suggestion{
				Name: c.Name, Desc: c.Help, Matched: matched,
				bucket: 1, score: 1000 - len(c.Name),
			})

		default:
			if idx, score, ok := fuzzyMatch(q, name); ok {
				out = append(out, Suggestion{
					Name: c.Name, Desc: c.Help, Matched: idx,
					bucket: 0, score: score,
				})
			}
		}
	}

	sort.SliceStable(out, func(i, j int) bool {
		a, b := out[i], out[j]
		if a.bucket != b.bucket {
			return a.bucket > b.bucket
		}
		if a.score != b.score {
			return a.score > b.score
		}
		if len(a.Name) != len(b.Name) {
			return len(a.Name) < len(b.Name)
		}
		return a.Name < b.Name
	})
	return out
}

// fuzzyMatch scores an anchored subsequence match, returning the matched rune
// indexes. Unlike history search (§5.2), this scorer rewards matches at word
// starts, because command names are short and structured.
func fuzzyMatch(query, text string) ([]int, int, bool) {
	qr, tr := []rune(query), []rune(text)
	var idx []int
	score, qi, last := 0, 0, -2

	for ti := 0; ti < len(tr) && qi < len(qr); ti++ {
		if tr[ti] != qr[qi] {
			continue
		}
		score++
		if ti == last+1 {
			score += 3
		}
		if ti == 0 || tr[ti-1] == '-' {
			score += 2
		}
		idx = append(idx, ti)
		last = ti
		qi++
	}
	if qi < len(qr) {
		return nil, 0, false
	}
	return idx, score - len(tr)/8, true
}

// PaletteState is what the palette needs to draw.
type PaletteState struct {
	// Query is the text after the leading slash.
	Query string

	// Selected is the index into the ranked list.
	Selected int

	// Suppressed hides the list entirely. It is set while an interactive
	// prompt owns the composer, or while a picker preview is the surface for
	// the command being typed (plan.md §5.1).
	Suppressed bool
}

// RenderPalette draws the floating command list. It returns rows only — the
// caller splices them over a finished frame, so opening the palette reserves no
// layout height and never moves the transcript (plan.md invariant 3).
func (r *Renderer) RenderPalette(s PaletteState, cmds []Command) []string {
	if s.Suppressed {
		return nil
	}
	ranked := RankCommands(s.Query, cmds)
	if len(ranked) == 0 {
		return nil
	}

	gold := lipgloss.NewStyle().Foreground(lipgloss.Color(theme.Hex(paletteGold)))
	dim := r.style(theme.RoleDim)

	// A single suggestion is the whole answer, so the entire row goes gold
	// rather than splitting into selected/unselected styling.
	if len(ranked) == 1 {
		s := ranked[0]
		return []string{gold.Render("/"+s.Name) + "  " + gold.Render(s.Desc)}
	}

	// Descriptions align to one column; a ragged edge reads as a list of
	// unrelated fragments rather than a table.
	nameWidth := 0
	for _, item := range ranked {
		nameWidth = max(nameWidth, lipgloss.Width(item.Name)+1)
	}

	sel := clamp(s.Selected, 0, len(ranked)-1)
	start := 0
	if sel >= PaletteRows {
		start = sel - PaletteRows + 1
	}
	end := min(start+PaletteRows, len(ranked))

	out := make([]string, 0, end-start)
	for i := start; i < end; i++ {
		item := ranked[i]
		var row string
		pad := strings.Repeat(" ", max(nameWidth-lipgloss.Width(item.Name)-1, 0)+2)
		if i == sel {
			row = highlightMatch("/"+item.Name, item.Matched, paletteGold) +
				pad + gold.Render(item.Desc)
		} else {
			row = highlightMatch("/"+item.Name, item.Matched, paletteTeal) +
				pad + dim.Render(item.Desc)
		}

		// Scroll affordances ride the first and last visible rows.
		if i == start && start > 0 {
			row += dim.Render("  ↑" + strconv.Itoa(start))
		}
		if i == end-1 && end < len(ranked) {
			row += dim.Render("  +" + strconv.Itoa(len(ranked)-end) + " more")
		}
		out = append(out, row)
	}
	return out
}

// highlightMatch recolors matched characters instead of underlining them: the
// match lifts toward white and goes bold while staying in the palette's hue,
// and unmatched characters dim (plan.md §5.1). An underline would fight the
// color; this reads as emphasis.
func highlightMatch(text string, matched []int, base color.RGBA) string {
	hit := make(map[int]bool, len(matched))
	for _, i := range matched {
		// Matched indexes are into the name; the rendered text has a leading
		// slash, so shift by one.
		hit[i+1] = true
	}

	lit := lipgloss.NewStyle().
		Foreground(lipgloss.Color(theme.Hex(theme.Lighten(base)))).Bold(true)
	dimmed := lipgloss.NewStyle().
		Foreground(lipgloss.Color(theme.Hex(theme.Darken(base))))

	var b strings.Builder
	for i, r := range []rune(text) {
		if hit[i] {
			b.WriteString(lit.Render(string(r)))
		} else {
			b.WriteString(dimmed.Render(string(r)))
		}
	}
	return b.String()
}

// MovePaletteSelection advances the selection with wrapping, which is what
// makes a short list feel like a ring rather than a dead end.
func MovePaletteSelection(selected, delta, count int) int {
	if count <= 0 {
		return 0
	}
	return ((selected+delta)%count + count) % count
}
