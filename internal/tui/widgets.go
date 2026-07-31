package tui

import (
	"fmt"
	"strings"

	"charm.land/lipgloss/v2"

	"evilcode/internal/theme"
	"evilcode/internal/todo"
)

// WidgetBorder is the dimmed chrome every docked box wears (plan.md §8.3).
var WidgetBorder = theme.RGB(70, 70, 80)

// RenderWidget wraps content in the dock's rounded border. A widget with no
// content bails before drawing anything: an empty box is worse than an absent
// one, because it claims space to say nothing.
func (r *Renderer) RenderWidget(w Widget) []string {
	if len(w.Lines) == 0 {
		return nil
	}
	border := lipgloss.NewStyle().Foreground(lipgloss.Color(theme.Hex(WidgetBorder)))
	inner := w.Width() - 4

	var out []string
	if w.Title != "" {
		label := " " + w.Title + " "
		dashes := inner + 2 - lipgloss.Width(label)
		out = append(out, border.Render("╭"+label+strings.Repeat("─", max(dashes, 0))+"╮"))
	} else {
		out = append(out, border.Render("╭"+strings.Repeat("─", inner+2)+"╮"))
	}
	for _, line := range w.Lines {
		text := truncateCells(line, inner)
		pad := max(inner-lipgloss.Width(text), 0)
		out = append(out, border.Render("│")+" "+text+strings.Repeat(" ", pad)+" "+border.Render("│"))
	}
	out = append(out, border.Render("╰"+strings.Repeat("─", inner+2)+"╯"))
	return out
}

// SegmentedBar draws the pill meter `▰▰▰▰▱▱` used for quota and context.
//
// The fill color is driven by what REMAINS, not what is used: a meter that
// reddens as it fills is a progress bar, and a meter that reddens as it empties
// is a warning. The spec wants the warning (plan.md §8.5).
func SegmentedBar(used, total, cells int) string {
	if total <= 0 || cells <= 0 {
		return ""
	}
	remaining := float64(total-used) / float64(total)
	filled := clamp(used*cells/total, 0, cells)

	fill := lipgloss.NewStyle().
		Foreground(lipgloss.Color(theme.Hex(theme.MeterColor(remaining))))
	track := lipgloss.NewStyle().
		Foreground(lipgloss.Color(theme.Hex(theme.RGB(50, 50, 60))))

	return fill.Render(strings.Repeat("▰", filled)) +
		track.Render(strings.Repeat("▱", cells-filled))
}

// SolidBar is the `█░` variant used for budget bars.
func SolidBar(used, total, cells int) string {
	if total <= 0 || cells <= 0 {
		return ""
	}
	remaining := float64(total-used) / float64(total)
	filled := clamp(used*cells/total, 0, cells)

	fill := lipgloss.NewStyle().
		Foreground(lipgloss.Color(theme.Hex(theme.MeterColor(remaining))))
	track := lipgloss.NewStyle().
		Foreground(lipgloss.Color(theme.Hex(theme.RGB(50, 50, 60))))

	return fill.Render(strings.Repeat("█", filled)) +
		track.Render(strings.Repeat("░", cells-filled))
}

// ContextWidget renders the context meter (plan.md §8.5).
func (r *Renderer) ContextWidget(used, total int) Widget {
	if total <= 0 {
		return Widget{Kind: WidgetContextUsage}
	}
	label := lipgloss.NewStyle().Foreground(lipgloss.Color(theme.Hex(theme.RGB(140, 140, 150))))
	remaining := float64(total-used) / float64(total)
	counts := lipgloss.NewStyle().
		Foreground(lipgloss.Color(theme.Hex(theme.MeterColor(remaining)))).Bold(true)

	return Widget{
		Kind: WidgetContextUsage,
		Lines: []string{
			label.Render("Context ") +
				counts.Render(fmt.Sprintf("%s/%s", humanTokens(used), humanTokens(total))),
			SegmentedBar(used, total, 10) +
				label.Render(fmt.Sprintf(" %d%%", int(remaining*100))),
		},
	}
}

// ModelInfoWidget renders the §8.9 widget.
func (r *Renderer) ModelInfoWidget(h HeaderState, sessions int) Widget {
	icon := lipgloss.NewStyle().Foreground(lipgloss.Color(theme.Hex(theme.RGB(140, 180, 255))))
	name := lipgloss.NewStyle().
		Foreground(lipgloss.Color(theme.Hex(theme.RGB(255, 150, 200)))).Bold(true)
	meta := lipgloss.NewStyle().Foreground(lipgloss.Color(theme.Hex(theme.RGB(140, 140, 150))))
	branch := lipgloss.NewStyle().Foreground(lipgloss.Color(theme.Hex(theme.RGB(150, 170, 140))))

	lines := []string{icon.Render("⚡ ") + name.Render(truncateCells(h.Model, 28))}
	if h.SessionName != "" {
		lines = append(lines, meta.Render(fmt.Sprintf("%d sessions · %s", sessions, h.SessionName)))
	}
	if h.Cwd != "" {
		// Elided from the left, so what survives is the end of the path — the
		// directory you are actually in. Truncating from the right kept the
		// leading slashes and dropped the project name, which is both useless
		// and, in a golden, an unscrubbable machine-specific prefix.
		row := meta.Render(elideLeft(h.Cwd, 30))
		if h.Branch != "" {
			row += branch.Render("  " + h.Branch)
		}
		lines = append(lines, row)
	}
	if h.Provider != "" {
		lines = append(lines, meta.Render("☁ "+h.Provider))
	}
	return Widget{Kind: WidgetModelInfo, Lines: lines}
}

// elideLeft trims a path to a cell width from the left, marking the cut.
func elideLeft(s string, width int) string {
	if lipgloss.Width(s) <= width {
		return s
	}
	runes := []rune(s)
	// One cell for the ellipsis; walk back from the end until the rest fits.
	keep := len(runes)
	for keep > 0 && lipgloss.Width(string(runes[len(runes)-keep:])) > width-1 {
		keep--
	}
	return "…" + string(runes[len(runes)-keep:])
}

// TodosWidget is the compact dock form of the todo list (plan.md §8.4).
func (r *Renderer) TodosWidget(items []todo.Item, goals []todo.Goal, available int) Widget {
	if len(items) == 0 {
		return Widget{Kind: WidgetTodos}
	}
	label := lipgloss.NewStyle().
		Foreground(lipgloss.Color(theme.Hex(theme.RGB(180, 180, 190)))).Bold(true)
	counter := lipgloss.NewStyle().Foreground(lipgloss.Color(theme.Hex(theme.RGB(140, 140, 150))))

	done := 0
	for _, i := range items {
		if i.Status == todo.StatusCompleted {
			done++
		}
	}

	header := label.Render("Todos ") +
		counter.Render(fmt.Sprintf("%d/%d ", done, len(items))) + r.todoPips(items)
	if avg, ok := todo.AggregateConfidence(items); ok {
		header += counter.Render(" · ") + scoreStyle(uint8(avg)).Render(fmt.Sprintf("%d%%", avg))
	}
	lines := []string{header}

	// The compact form shows only what fits; the inline card is where the
	// whole list lives.
	budget := clamp(available, 1, 5)
	sorted := todo.SortItems(items)
	shown := 0
	for _, item := range sorted {
		if shown >= budget {
			break
		}
		if item.Status == todo.StatusCompleted || item.Status == todo.StatusCancelled {
			continue
		}
		lines = append(lines, r.todoItemRow(item, WidgetMaxWidth-4))
		shown++
	}
	if remaining := len(items) - done - shown; remaining > 0 {
		lines = append(lines, counter.Render(fmt.Sprintf("  +%d more", remaining)))
	} else if done > 0 {
		lines = append(lines, counter.Render(fmt.Sprintf("  +%d done", done)))
	}
	return Widget{Kind: WidgetTodos, Lines: lines}
}

// GitStatusWidget summarizes the working tree.
func (r *Renderer) GitStatusWidget(branch string, staged, unstaged, untracked int) Widget {
	if branch == "" {
		return Widget{Kind: WidgetGitStatus}
	}
	label := lipgloss.NewStyle().Foreground(lipgloss.Color(theme.Hex(theme.RGB(150, 170, 140))))
	meta := lipgloss.NewStyle().Foreground(lipgloss.Color(theme.Hex(theme.RGB(140, 140, 150))))

	lines := []string{label.Render(truncateCells(branch, 30))}
	if staged+unstaged+untracked == 0 {
		lines = append(lines, meta.Render("clean"))
	} else {
		add := lipgloss.NewStyle().Foreground(lipgloss.Color(theme.Hex(theme.DiffAdd)))
		del := lipgloss.NewStyle().Foreground(lipgloss.Color(theme.Hex(theme.DiffDel)))
		lines = append(lines,
			add.Render(fmt.Sprintf("%d staged", staged))+meta.Render(" · ")+
				del.Render(fmt.Sprintf("%d unstaged", unstaged)))
		if untracked > 0 {
			lines = append(lines, meta.Render(fmt.Sprintf("%d untracked", untracked)))
		}
	}
	return Widget{Kind: WidgetGitStatus, Lines: lines}
}

// BackgroundTask is one entry in the background-task widget.
type BackgroundTask struct {
	Label string
	Done  bool
	Err   bool
}

// BackgroundTasksWidget lists running and finished background commands.
func (r *Renderer) BackgroundTasksWidget(tasks []BackgroundTask, elapsedFrame int) Widget {
	if len(tasks) == 0 {
		return Widget{Kind: WidgetBackgroundTasks}
	}
	meta := lipgloss.NewStyle().Foreground(lipgloss.Color(theme.Hex(theme.RGB(140, 140, 150))))
	ok := lipgloss.NewStyle().Foreground(lipgloss.Color(r.Palette.Hex(theme.RoleSuccess)))
	bad := lipgloss.NewStyle().Foreground(lipgloss.Color(r.Palette.Hex(theme.RoleError)))

	lines := []string{meta.Render("Background")}
	for _, t := range tasks {
		var glyph string
		switch {
		case t.Err:
			glyph = bad.Render("✗")
		case t.Done:
			glyph = ok.Render("✓")
		default:
			glyph = meta.Render(SpinnerFrames[elapsedFrame%len(SpinnerFrames)])
		}
		lines = append(lines, glyph+" "+meta.Render(truncateCells(t.Label, 28)))
	}
	return Widget{Kind: WidgetBackgroundTasks, Lines: lines}
}

// TipsWidget is the lowest-priority widget, filling space nothing else wanted.
func (r *Renderer) TipsWidget(tip string) Widget {
	if tip == "" {
		return Widget{Kind: WidgetTips}
	}
	dim := r.style(theme.RoleDim)
	var lines []string
	for _, line := range wrapPlain("💡 "+tip, WidgetMaxWidth-4) {
		lines = append(lines, dim.Render(line))
	}
	return Widget{Kind: WidgetTips, Lines: lines}
}

// FactStack is the quiet always-on HUD of plan.md §8.6: four rows of context
// that are always true and never urgent.
type FactStack struct {
	Provider string
	Auth     string
	Model    string
	Cwd      string
	Branch   string
	Used     int
	Total    int
}

// RenderFactStack draws the bottom-right fact rows.
//
// It is one visual object: any collision slides the whole block rather than
// splitting it, because half a fact stack reads as a rendering bug.
func (r *Renderer) RenderFactStack(f FactStack) []string {
	meta := lipgloss.NewStyle().Foreground(lipgloss.Color(theme.Hex(theme.RGB(140, 140, 150))))
	sep := meta.Render(" · ")

	var rows []string
	if f.Provider != "" {
		row := meta.Render(f.Provider)
		if f.Auth != "" {
			row += sep + meta.Render(f.Auth)
		}
		rows = append(rows, row)
	}
	if f.Model != "" {
		rows = append(rows, meta.Render(truncateCells(f.Model, 34)))
	}
	if f.Cwd != "" {
		row := meta.Render(truncateCells(f.Cwd, 28))
		if f.Branch != "" {
			row += meta.Render("   " + f.Branch)
		}
		rows = append(rows, row)
	}
	if f.Total > 0 {
		remaining := float64(f.Total-f.Used) / float64(f.Total)
		rows = append(rows, meta.Render(
			fmt.Sprintf("%s/%s ", humanTokens(f.Used), humanTokens(f.Total)))+
			SegmentedBar(f.Used, f.Total, 6)+
			meta.Render(fmt.Sprintf(" %d%%", int(remaining*100))))
	}
	return rows
}

// RenderOverscrollFacts draws the one-row elastic facts line with its live
// countdown (plan.md §4.4). It is the same information as the fact stack,
// which is why only one of them shows at a time.
func (r *Renderer) RenderOverscrollFacts(f FactStack, remaining float64) string {
	meta := lipgloss.NewStyle().Foreground(lipgloss.Color(theme.Hex(theme.RGB(140, 140, 150))))
	countdown := lipgloss.NewStyle().
		Foreground(lipgloss.Color(theme.Hex(theme.RGB(150, 150, 165)))).Italic(true)

	var parts []string
	if f.Model != "" {
		parts = append(parts, f.Model)
	}
	if f.Provider != "" {
		parts = append(parts, f.Provider)
	}
	if f.Auth != "" {
		parts = append(parts, f.Auth)
	}
	if f.Total > 0 {
		parts = append(parts, fmt.Sprintf("%s/%s", humanTokens(f.Used), humanTokens(f.Total)))
	}
	if f.Cwd != "" {
		parts = append(parts, f.Cwd)
	}

	left := meta.Render(strings.Join(parts, " · "))
	right := countdown.Render(fmt.Sprintf("(overscroll %.1f)", remaining))

	gap := max(r.Width-lipgloss.Width(left)-lipgloss.Width(right), 1)
	return left + strings.Repeat(" ", gap) + right
}
