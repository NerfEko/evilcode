package tui

import (
	"fmt"
	"strings"

	"charm.land/lipgloss/v2"

	"evilcode/internal/theme"
)

// RenderHelp draws the full-screen help overlay (plan.md §5.5). The scroll
// percentage lives in the title, which keeps the footer free for the keys that
// actually do something.
// helpFooter picks the longest key summary that fits the box, so a narrow
// terminal loses detail rather than losing the border.
func helpFooter(width int) string {
	for _, f := range []string{
		" Esc to close · j/k scroll · Space page · /help <cmd> for details ",
		" Esc close · j/k scroll · /help <cmd> ",
		" Esc · j/k ",
	} {
		if lipgloss.Width(f) <= width {
			return f
		}
	}
	return ""
}

func (r *Renderer) RenderHelp(scroll, width, height int) []string {
	accent := lipgloss.NewStyle().
		Foreground(lipgloss.Color(r.Palette.Hex(theme.RoleAccent))).Bold(true)
	name := lipgloss.NewStyle().Bold(true)
	dim := r.style(theme.RoleDim)
	border := lipgloss.NewStyle().Foreground(lipgloss.Color(theme.Hex(theme.RGB(80, 80, 90))))

	var body []string
	add := func(s string) { body = append(body, s) }

	for _, sec := range HelpSections {
		add(accent.Render(sec.Title))
		for _, n := range sec.Names {
			c, ok := FindCommand(n)
			if !ok {
				continue
			}
			add("  " + name.Render("/"+c.Name) +
				strings.Repeat(" ", max(18-len(c.Name), 1)) + dim.Render(c.Help))
		}
		add("")
	}

	// Anything the curated sections missed, so drift is visible rather than
	// silent.
	if extra := UncoveredCommands(); len(extra) > 0 {
		add(accent.Render("More commands"))
		for _, c := range extra {
			add("  " + name.Render("/"+c.Name) +
				strings.Repeat(" ", max(18-len(c.Name), 1)) + dim.Render(c.Help))
		}
		add("")
	}

	add(accent.Render("Keys"))
	for _, k := range HelpKeys {
		add("  " + name.Render(k[0]) +
			strings.Repeat(" ", max(26-len(k[0]), 1)) + dim.Render(k[1]))
	}

	inner := max(width-4, 20)
	viewport := max(height-4, 3)

	maxScroll := max(len(body)-viewport, 0)
	scroll = clamp(scroll, 0, maxScroll)
	pct := 100
	if maxScroll > 0 {
		pct = scroll * 100 / maxScroll
	}

	visible := body[scroll:min(scroll+viewport, len(body))]

	// Widths are measured in cells, not bytes: the border runs contain
	// multi-byte separators, and len() would leave the box ragged.
	title := fmt.Sprintf(" Help  %d%%  ", pct)
	top := "┌" + title + strings.Repeat("─", max(inner+2-lipgloss.Width(title), 0)) + "┐"

	out := []string{border.Render(top)}
	for _, line := range visible {
		// Truncated to the box, not merely padded to it. A help line longer
		// than the terminal does not clip — it wraps, and every row below it
		// shifts, which tears the box apart on any narrow screen.
		line = truncateCells(line, inner)
		pad := max(inner-lipgloss.Width(line), 0)
		out = append(out, border.Render("│")+" "+line+strings.Repeat(" ", pad)+" "+border.Render("│"))
	}
	for len(out) < viewport+1 {
		out = append(out, border.Render("│")+strings.Repeat(" ", inner+2)+border.Render("│"))
	}

	footer := helpFooter(inner + 2)
	bottom := "└" + footer + strings.Repeat("─", max(inner+2-lipgloss.Width(footer), 0)) + "┘"
	out = append(out, border.Render(bottom))
	return out
}
