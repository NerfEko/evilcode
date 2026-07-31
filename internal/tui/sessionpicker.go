package tui

import (
	"fmt"
	"strings"
	"time"

	"charm.land/lipgloss/v2"

	"evilcode/internal/session"
	"evilcode/internal/theme"
)

// Session picker layout (plan.md §5.4).
const (
	// ListRatio is the percentage of width given to the list; the rest is the
	// preview.
	ListRatio = 40

	// SearchBarRows is the height of the filter row.
	SearchBarRows = 1
)

// SessionRow is one entry in the picker.
type SessionRow struct {
	Info session.Info

	// Marked is the multi-select state.
	Marked bool

	// Current flags the session being viewed right now.
	Current bool

	// Here flags a session started in this working directory.
	Here bool

	// Preview is the session's recent conversation, rendered as the transcript
	// would show it. Filled lazily for the selected row only — reading a JSONL
	// per arrow key would make the picker crawl.
	Preview []Block

	// Recalled is the remembered summary that matched, set when this row came
	// from semantic search rather than from the literal filter.
	Recalled string
}

// SessionPickerState is the picker's state.
type SessionPickerState struct {
	Rows     []SessionRow
	Filter   string
	Selected int

	// Editing is whether the search bar has focus.
	Editing bool

	// Confirm holds a pending confirmation prompt, or "" for none.
	Confirm string

	// Semantic holds session names matched by memory rather than by their text
	// (plan.md §19, session RAG), keyed to the summary that matched. It is
	// consulted only when the literal filter finds nothing, so typing a name
	// still behaves like typing a name.
	Semantic map[string]string
}

// Filtered returns the rows matching the filter.
func (s SessionPickerState) Filtered() []SessionRow {
	if s.Filter == "" {
		return s.Rows
	}
	q := strings.ToLower(s.Filter)
	var out []SessionRow
	for _, r := range s.Rows {
		hay := strings.ToLower(r.Info.Name + " " + r.Info.Title)
		if strings.Contains(hay, q) {
			out = append(out, r)
		}
	}
	if len(out) > 0 || len(s.Semantic) == 0 {
		return out
	}
	// Nothing matched literally, so fall back to what memory remembers about
	// each session. A search that finds the session you meant by describing it
	// is the whole point of storing episode summaries.
	for _, r := range s.Rows {
		if why, ok := s.Semantic[r.Info.Name]; ok {
			r.Recalled = why
			out = append(out, r)
		}
	}
	return out
}

// RenderSessionPicker draws the full-screen picker: a search bar, then a
// 40/60 list-and-preview split (plan.md §5.4).
func (r *Renderer) RenderSessionPicker(s SessionPickerState, width, height int) []string {
	rows := s.Filtered()

	out := []string{r.sessionSearchBar(s, width)}

	listWidth := max(width*ListRatio/100, 24)
	previewWidth := max(width-listWidth-1, 20)
	bodyHeight := max(height-SearchBarRows-1, 4)

	list := r.sessionList(s, rows, listWidth, bodyHeight)
	preview := r.sessionPreview(rows, s.Selected, previewWidth, bodyHeight)

	border := lipgloss.NewStyle().
		Foreground(lipgloss.Color(theme.Hex(theme.RGB(70, 70, 70))))

	for i := 0; i < bodyHeight; i++ {
		left := ""
		if i < len(list) {
			left = list[i]
		}
		right := ""
		if i < len(preview) {
			right = preview[i]
		}
		pad := max(listWidth-lipgloss.Width(left), 0)
		out = append(out, left+strings.Repeat(" ", pad)+border.Render("│")+right)
	}

	if s.Confirm != "" {
		return r.overlayConfirm(out, s.Confirm, width, height)
	}
	return out
}

// sessionSearchBar draws the filter row. The whole row carries a background so
// it reads as a control rather than as another line of list.
func (r *Renderer) sessionSearchBar(s SessionPickerState, width int) string {
	bg := lipgloss.Color(theme.Hex(theme.RGB(25, 25, 30)))
	accent := lipgloss.NewStyle().
		Foreground(lipgloss.Color(r.Palette.Hex(theme.RoleAccent))).Background(bg)
	query := lipgloss.NewStyle().Foreground(lipgloss.Color("#ffffff")).Background(bg).Bold(true)
	hint := lipgloss.NewStyle().
		Foreground(lipgloss.Color(theme.Hex(theme.RGB(60, 60, 60)))).Background(bg)
	fill := lipgloss.NewStyle().Background(bg)

	var body string
	if s.Editing {
		body = accent.Render("🔍 ") + query.Render(s.Filter) + accent.Render("▎") +
			hint.Render("  Esc to clear")
	} else {
		body = accent.Render("🔍 ") + query.Render(s.Filter) + hint.Render("  / to edit")
	}
	pad := max(width-lipgloss.Width(plainText(body)), 0)
	return body + fill.Render(strings.Repeat(" ", pad))
}

// sessionList draws the left column: two rows per session.
func (r *Renderer) sessionList(s SessionPickerState, rows []SessionRow, width, height int) []string {
	if len(rows) == 0 {
		return []string{r.style(theme.RoleDim).Render("  no sessions match")}
	}

	sel := clamp(s.Selected, 0, len(rows)-1)
	// Two rows per entry, so the window is half the height.
	perEntry := 2
	visible := max(height/perEntry, 1)
	start := clamp(sel-visible/2, 0, max(len(rows)-visible, 0))
	end := min(start+visible, len(rows))

	var out []string
	for i := start; i < end; i++ {
		out = append(out, r.sessionRow(rows[i], i == sel, s.Filter, width)...)
	}
	return out
}

func (r *Renderer) sessionRow(row SessionRow, selected bool, filter string, width int) []string {
	dim := r.style(theme.RoleDim)

	mark := lipgloss.NewStyle().
		Foreground(lipgloss.Color(theme.Hex(theme.RGB(90, 90, 90)))).Render("○ ")
	if row.Marked {
		mark = lipgloss.NewStyle().
			Foreground(lipgloss.Color(theme.Hex(theme.RGB(140, 220, 160)))).
			Bold(true).Render("● ")
	}

	emoji := lipgloss.NewStyle().
		Foreground(lipgloss.Color(theme.Hex(theme.RGB(110, 210, 255)))).
		Render(row.Info.Emoji)

	nameStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("#ffffff"))
	if selected {
		nameStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color(theme.Hex(theme.RGB(140, 220, 160)))).Bold(true)
	}

	var b strings.Builder
	b.WriteString(mark + emoji + " " + nameStyle.Render(row.Info.Name))

	if row.Info.Saved {
		b.WriteString(" " + lipgloss.NewStyle().
			Foreground(lipgloss.Color(theme.Hex(theme.RGB(255, 180, 100)))).Render("📌"))
	}
	b.WriteString(" " + r.sessionStatus(row))

	if row.Current {
		b.WriteString(" " + lipgloss.NewStyle().
			Foreground(lipgloss.Color(theme.Hex(theme.RGB(110, 210, 255)))).
			Bold(true).Render("◀ current"))
	}
	if row.Here {
		b.WriteString(" " + lipgloss.NewStyle().
			Foreground(lipgloss.Color(theme.Hex(theme.RGB(120, 200, 140)))).
			Bold(true).Render("▸ here"))
	}

	first := truncateCells(b.String(), width)

	// The second row is the quiet detail: what this session actually was.
	detail := fmt.Sprintf("%d messages", row.Info.Messages)
	if row.Recalled != "" {
		// A row the literal filter never would have found needs to say why it
		// is here, or it reads as the picker ignoring what was typed.
		detail += " · 🧠 " + row.Recalled
	} else if row.Info.Title != "" {
		detail += " · " + row.Info.Title
	}
	second := "     " + dim.Render(truncateCells(detail, max(width-6, 10)))

	return []string{first, second}
}

// sessionStatus renders the state glyph and label (plan.md §5.4).
func (r *Renderer) sessionStatus(row SessionRow) string {
	dim := r.style(theme.RoleDim)
	switch {
	case row.Info.Crashed:
		return lipgloss.NewStyle().
			Foreground(lipgloss.Color(theme.Hex(theme.RGB(220, 100, 100)))).Render("💥") +
			dim.Render(" crashed")
	case row.Current:
		return lipgloss.NewStyle().
			Foreground(lipgloss.Color(theme.Hex(theme.RGB(100, 220, 130)))).Render("●") +
			dim.Render(" ready")
	default:
		return lipgloss.NewStyle().
			Foreground(lipgloss.Color(theme.Hex(theme.RGB(100, 100, 100)))).Render("✓") +
			dim.Render(" closed "+humanAge(row.Info.Modified))
	}
}

// humanAge renders a duration the way someone would say it.
func humanAge(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	d := time.Since(t)
	switch {
	case d < time.Minute:
		return "just now"
	case d < time.Hour:
		return fmt.Sprintf("%dm ago", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh ago", int(d.Hours()))
	default:
		return fmt.Sprintf("%dd ago", int(d.Hours()/24))
	}
}

// sessionPreview draws the right column.
// sessionPreview draws the right column: what the session actually contains,
// rendered the way the transcript renders it.
//
// It used to show the name, the message count, the modified age and the title —
// every one of which is already on the row beside it, and the title was always
// empty because nothing ever wrote it. A preview that previews nothing is worse
// than no preview, because it takes 60% of the screen to say so.
func (r *Renderer) sessionPreview(rows []SessionRow, selected, width, height int) []string {
	border := lipgloss.NewStyle().
		Foreground(lipgloss.Color(theme.Hex(theme.RGB(130, 130, 160))))
	dim := r.style(theme.RoleDim)

	title := " Preview "
	top := "╭" + title + strings.Repeat("─", max(width-lipgloss.Width(title)-2, 0)) + "╮"
	out := []string{border.Render(top)}

	// Always close the box, even with nothing to show. Returning early left a
	// top border hanging with no sides and no bottom.
	closeBox := func() []string {
		for len(out) < height-1 {
			out = append(out, border.Render("│"))
		}
		return append(out, border.Render("╰"+strings.Repeat("─", max(width-2, 0))+"╯"))
	}
	if len(rows) == 0 {
		return closeBox()
	}

	row := rows[clamp(selected, 0, len(rows)-1)]
	head := lipgloss.NewStyle().Bold(true).Render(row.Info.Emoji+" "+row.Info.Name) +
		dim.Render(fmt.Sprintf("  %d messages · %s",
			row.Info.Messages, humanAge(row.Info.Modified)))
	body := []string{head}
	if row.Info.Crashed {
		body = append(body, lipgloss.NewStyle().
			Foreground(lipgloss.Color(theme.Hex(theme.RGB(220, 100, 100)))).
			Render("reason: no clean exit was recorded"))
	}
	body = append(body, "")

	// The conversation, rendered through the transcript's own renderer at the
	// preview's width, then tailed: the most recent context is what tells you
	// whether this is the session you meant.
	inner := max(width-4, 20)
	sub := r.AtWidth(inner)
	var convo []string
	for i := range row.Preview {
		convo = append(convo, sub.render(&row.Preview[i])...)
	}
	if fits := height - 2 - len(body); fits > 0 && len(convo) > fits {
		convo = convo[len(convo)-fits:]
	}
	body = append(body, convo...)

	for _, line := range body {
		if len(out) >= height-1 {
			break
		}
		out = append(out, border.Render("│")+" "+truncateCells(line, inner))
	}
	return closeBox()
}

// overlayConfirm draws the centered confirmation modal of §5.4.
//
// The rect is cleared before drawing rather than overlaid, because a modal you
// can read the transcript through is a modal nobody reads.
func (r *Renderer) overlayConfirm(rows []string, prompt string, width, height int) []string {
	boxW := min(width-4, 74)
	boxH := min(height-2, 8)
	top := max((height-boxH)/2, 0)
	left := max((width-boxW)/2, 0)

	amber := lipgloss.NewStyle().
		Foreground(lipgloss.Color(theme.Hex(theme.RGB(255, 193, 7))))
	action := amber.Bold(true)

	title := " Confirm "
	lines := []string{
		amber.Render("╭" + title + strings.Repeat("─", max(boxW-lipgloss.Width(title)-2, 0)) + "╮"),
	}
	for _, l := range wrapPlain(prompt, boxW-4) {
		if len(lines) >= boxH-2 {
			break
		}
		pad := max(boxW-4-lipgloss.Width(l), 0)
		lines = append(lines, amber.Render("│")+" "+l+strings.Repeat(" ", pad)+" "+amber.Render("│"))
	}
	for len(lines) < boxH-2 {
		lines = append(lines, amber.Render("│")+strings.Repeat(" ", boxW-2)+amber.Render("│"))
	}
	hint := "Enter/Y confirm · Esc/N cancel"
	pad := max(boxW-4-lipgloss.Width(hint), 0)
	lines = append(lines,
		amber.Render("│")+" "+action.Render(hint)+strings.Repeat(" ", pad)+" "+amber.Render("│"))
	lines = append(lines, amber.Render("╰"+strings.Repeat("─", boxW-2)+"╯"))

	out := append([]string(nil), rows...)
	for i, line := range lines {
		idx := top + i
		for len(out) <= idx {
			out = append(out, "")
		}
		// Clear the covered cells rather than drawing over them.
		out[idx] = strings.Repeat(" ", left) + line
	}
	return out
}
