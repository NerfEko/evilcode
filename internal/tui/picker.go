package tui

import (
	"fmt"
	"strings"

	"charm.land/lipgloss/v2"

	"evilcode/internal/theme"
)

// Picker chrome (plan.md §5.3). Unlike the slash palette, the picker *does*
// reserve layout height — it is a surface you interact with, not a hint that
// floats over one.
var (
	pickerBorder     = "#55556e"
	pickerBackground = "#12121a"
	pickerSelectedBg = "#3c3c50"
)

// ModelEntry is one row in the model picker.
type ModelEntry struct {
	Name     string
	Provider string
	Via      string

	// Detail is trailing dim text, or the reason it is unavailable.
	Detail string

	Current     bool
	Favorite    bool
	Recommended bool
	New         bool
	Old         bool
	Default     bool

	// Unavailable and Limited drive the marker gutter.
	Unavailable bool
	Limited     bool
}

// PickerColumn is which column the left/right keys are focused on.
type PickerColumn int

const (
	ColModel PickerColumn = iota
	ColProvider
	ColVia
)

// PickerState is the model picker's state.
type PickerState struct {
	Entries  []ModelEntry
	Filter   string
	Selected int
	Column   PickerColumn

	// Height is how many rows of the list are visible.
	Height int
}

// DefaultPickerHeight is the visible row count.
const DefaultPickerHeight = 10

// Filtered returns the entries matching the filter, and the matched rune
// indexes per entry for the underline highlight.
func (s PickerState) Filtered() ([]ModelEntry, [][]int) {
	if s.Filter == "" {
		return s.Entries, make([][]int, len(s.Entries))
	}
	var out []ModelEntry
	var matches [][]int
	q := strings.ToLower(s.Filter)
	for _, e := range s.Entries {
		if idx, _, ok := fuzzyMatch(q, strings.ToLower(e.Name)); ok {
			out = append(out, e)
			matches = append(matches, idx)
		}
	}
	return out, matches
}

// RenderPicker draws the inline picker box (plan.md §5.3).
//
// The key hints live outside the box, above it, because they describe what the
// box does rather than being part of its content.
func (r *Renderer) RenderPicker(s PickerState) []string {
	entries, matches := s.Filtered()

	height := s.Height
	if height <= 0 {
		height = DefaultPickerHeight
	}

	hint := lipgloss.NewStyle().
		Foreground(lipgloss.Color(theme.Hex(theme.RGB(120, 120, 150)))).Italic(true).
		Render("keys: Ctrl+O set default · Ctrl+N favorite · Shift+Tab switch active model to next favorite")

	var body []string
	body = append(body, r.pickerHeader(s, len(entries)))

	if len(entries) == 0 {
		body = append(body, lipgloss.NewStyle().
			Foreground(lipgloss.Color(r.Palette.Hex(theme.RoleDim))).Italic(true).
			Render("   no matches"))
	} else {
		sel := clamp(s.Selected, 0, len(entries)-1)
		// Keep the selection centered rather than merely visible: a selection
		// pinned to an edge gives no sense of where you are in the list.
		start := clamp(sel-height/2, 0, max(len(entries)-height, 0))
		end := min(start+height, len(entries))

		for i := start; i < end; i++ {
			body = append(body, r.pickerRow(s, entries[i], matches[i], i == sel))
		}
		if note := pickerNotice(entries[sel]); note != "" {
			body = append(body, note)
		}
	}

	return append([]string{hint}, r.roundedBox(body)...)
}

func (r *Renderer) pickerHeader(s PickerState, count int) string {
	accent := lipgloss.NewStyle().
		Foreground(lipgloss.Color(r.Palette.Hex(theme.RoleAccent))).Bold(true)
	dim := r.style(theme.RoleDim)

	col := func(name string, c PickerColumn) string {
		if s.Column == c {
			return accent.Render(name)
		}
		return dim.Render(name)
	}

	head := col("model", ColModel) + dim.Render(" | ") +
		col("provider", ColProvider) + dim.Render(" | ") + col("via", ColVia)

	if s.Filter != "" {
		head += dim.Render(fmt.Sprintf("   %q", s.Filter))
	}
	head += dim.Render(fmt.Sprintf(" (%d/%d)", count, len(s.Entries)))
	return head
}

// pickerRow draws one entry. The style cascade is first-match-wins, exactly as
// §5.3 lists it: unavailable, then the selection highlight, then current,
// favorite, recommended, old, default.
func (r *Renderer) pickerRow(s PickerState, e ModelEntry, matched []int, selected bool) string {
	marker, markerStyle := " ", r.style(theme.RoleDim)
	switch {
	case e.Unavailable:
		marker = "×"
		markerStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color(theme.Hex(theme.RGB(180, 120, 120)))).Bold(true)
	case e.Limited:
		marker = "⚠"
		markerStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color(theme.Hex(theme.RGB(214, 184, 92))))
	case selected:
		marker = "▸"
		markerStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("#ffffff")).Bold(true)
	}

	nameStyle := pickerNameStyle(e, selected && s.Column == ColModel)
	name := e.Name + pickerSuffixes(e)

	cell := func(text string, c PickerColumn, style lipgloss.Style) string {
		if selected && s.Column == c {
			return lipgloss.NewStyle().
				Foreground(lipgloss.Color("#ffffff")).
				Background(lipgloss.Color(pickerSelectedBg)).Bold(true).Render(text)
		}
		return style.Render(text)
	}

	var b strings.Builder
	b.WriteString(" " + markerStyle.Render(marker) + " ")
	// The selection highlight and the filter underline carry different
	// information — which row is selected, and which characters matched — so
	// the underline is applied on top of the highlight rather than instead.
	b.WriteString(underlineMatch(name, matched, nameStyle))
	if e.Provider != "" {
		b.WriteString("  " + cell(e.Provider, ColProvider,
			lipgloss.NewStyle().Foreground(lipgloss.Color(theme.Hex(theme.RGB(140, 180, 255))))))
	}
	if e.Via != "" {
		b.WriteString("  " + cell(e.Via, ColVia,
			lipgloss.NewStyle().Foreground(lipgloss.Color(theme.Hex(theme.RGB(220, 190, 120))))))
	}
	if e.Detail != "" {
		if e.Unavailable {
			b.WriteString("  " + lipgloss.NewStyle().
				Foreground(lipgloss.Color(theme.Hex(theme.RGB(180, 120, 120)))).
				Italic(true).Render(e.Detail))
		} else {
			b.WriteString("  " + r.style(theme.RoleDim).Render(e.Detail))
		}
	}
	return b.String()
}

// pickerNameStyle is the §5.3 cascade, first match wins.
func pickerNameStyle(e ModelEntry, selectedInNameColumn bool) lipgloss.Style {
	switch {
	case e.Unavailable:
		return lipgloss.NewStyle().Foreground(lipgloss.Color(theme.Hex(theme.RGB(80, 80, 80))))
	case selectedInNameColumn:
		return lipgloss.NewStyle().
			Foreground(lipgloss.Color("#ffffff")).
			Background(lipgloss.Color(pickerSelectedBg)).Bold(true)
	case e.Current:
		return lipgloss.NewStyle().Foreground(lipgloss.Color("#ff79c6"))
	case e.Favorite:
		return lipgloss.NewStyle().
			Foreground(lipgloss.Color(theme.Hex(theme.RGB(255, 160, 210)))).Bold(true)
	case e.Recommended:
		return lipgloss.NewStyle().Foreground(lipgloss.Color(theme.Hex(theme.RGB(255, 220, 120))))
	case e.Old:
		return lipgloss.NewStyle().Foreground(lipgloss.Color(theme.Hex(theme.RGB(120, 120, 130))))
	default:
		return lipgloss.NewStyle().Foreground(lipgloss.Color(theme.Hex(theme.RGB(200, 200, 220))))
	}
}

func pickerSuffixes(e ModelEntry) string {
	var b strings.Builder
	if e.New {
		b.WriteString(" new")
	}
	if e.Favorite {
		b.WriteString(" ♥")
	}
	if e.Recommended {
		b.WriteString(" ★")
	}
	if e.Old {
		b.WriteString(" old")
	}
	if e.Default {
		b.WriteString(" default")
	}
	return b.String()
}

// pickerNotice is the caveat line under the header when the selection has one.
func pickerNotice(e ModelEntry) string {
	switch {
	case e.Unavailable:
		s := "× unavailable"
		if e.Detail != "" {
			s += " · " + e.Detail
		}
		return lipgloss.NewStyle().
			Foreground(lipgloss.Color(theme.Hex(theme.RGB(210, 150, 110)))).Italic(true).Render(s)
	case e.Limited && e.Detail != "":
		return lipgloss.NewStyle().
			Foreground(lipgloss.Color(theme.Hex(theme.RGB(210, 150, 110)))).Italic(true).
			Render("⚠ " + e.Detail)
	default:
		return ""
	}
}

// underlineMatch highlights filter matches with an underline. This is
// deliberately different from the palette's recolor (§5.1): the picker's rows
// already carry meaning in their color, so an underline is the only mark left
// that does not collide with the style cascade.
func underlineMatch(text string, matched []int, base lipgloss.Style) string {
	if len(matched) == 0 {
		return base.Render(text)
	}
	hit := make(map[int]bool, len(matched))
	for _, i := range matched {
		hit[i] = true
	}
	var b strings.Builder
	for i, r := range []rune(text) {
		if hit[i] {
			b.WriteString(base.Underline(true).Render(string(r)))
		} else {
			b.WriteString(base.Render(string(r)))
		}
	}
	return b.String()
}

// roundedBox wraps content lines in the rounded chrome of §3.3: the box width
// is content-derived and every row is padded to it, so the right border is
// straight regardless of what is inside.
func (r *Renderer) roundedBox(content []string) []string {
	inner := 0
	for _, l := range content {
		inner = max(inner, lipgloss.Width(l))
	}
	inner = min(inner, max(r.Width-4, 8))

	border := lipgloss.NewStyle().Foreground(lipgloss.Color(pickerBorder))
	fill := lipgloss.NewStyle().Background(lipgloss.Color(pickerBackground))

	out := make([]string, 0, len(content)+2)
	out = append(out, border.Render("╭"+strings.Repeat("─", inner+2)+"╮"))
	for _, l := range content {
		pad := max(inner-lipgloss.Width(l), 0)
		out = append(out, border.Render("│")+fill.Render(" "+l+strings.Repeat(" ", pad)+" ")+border.Render("│"))
	}
	out = append(out, border.Render("╰"+strings.Repeat("─", inner+2)+"╯"))
	return out
}

// BoxTitled draws a rounded box with a centered title in its top border, the
// ad-hoc helper §3.3 describes for plan cards, update boxes, and memory tiles.
func (r *Renderer) BoxTitled(title string, content []string, borderColor string) []string {
	inner := 0
	for _, l := range content {
		inner = max(inner, lipgloss.Width(l))
	}
	inner = max(inner, lipgloss.Width(title)+4)
	inner = min(inner, max(r.Width-4, 8))

	border := lipgloss.NewStyle().Foreground(lipgloss.Color(borderColor))

	// The title sits inside the border run, centered. A title wider than the
	// box is truncated: the box has to stay rectangular, and a title that
	// pushes the border off screen is worse than an abbreviated one.
	if lipgloss.Width(title)+4 > inner {
		title = truncateCells(title, max(inner-5, 1)) + "…"
	}
	label := " " + title + " "
	dashes := inner + 2 - lipgloss.Width(label)
	left := max(dashes/2, 1)
	right := max(dashes-left, 1)
	top := "╭" + strings.Repeat("─", left) + label + strings.Repeat("─", right) + "╮"

	out := []string{border.Render(top)}
	for _, l := range content {
		pad := max(inner-lipgloss.Width(l), 0)
		out = append(out, border.Render("│")+" "+l+strings.Repeat(" ", pad)+" "+border.Render("│"))
	}
	out = append(out, border.Render("╰"+strings.Repeat("─", inner+2)+"╯"))
	return out
}
