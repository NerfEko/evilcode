package tui

import (
	"fmt"
	"image/color"
	"strings"

	"charm.land/lipgloss/v2"

	"evilcode/internal/theme"
	"evilcode/internal/todo"
)

// Todo card colors (plan.md §12.5). These are deliberately brighter than the
// global dim: cards sit on the bare background with no border, so they need
// their own contrast. Cool colors carry structure, amber carries priority and
// blocking.
var (
	todoBlocked    = theme.RGB(225, 165, 90)
	todoDone       = theme.RGB(105, 190, 125)
	todoCancelled  = theme.RGB(190, 105, 115)
	todoPending    = theme.RGB(135, 145, 160)
	todoTextDone   = theme.RGB(135, 150, 145)
	todoTextCancel = theme.RGB(145, 130, 135)
	todoTextActive = theme.RGB(225, 232, 240)
	todoTextIdle   = theme.RGB(195, 202, 212)
	todoGroupHot   = theme.RGB(255, 210, 130)
	todoGroupCool  = theme.RGB(170, 175, 205)
	todoMeta       = theme.RGB(140, 140, 150)
	todoScoreGood  = theme.RGB(105, 190, 125)
	todoScoreWarn  = theme.RGB(220, 190, 100)
	todoScoreBad   = theme.RGB(220, 120, 100)
)

// scoreStyle bands a 0-100 score. The bands are keyed to the quality gate, so
// "green" means "would satisfy the gate" rather than an arbitrary threshold.
func scoreStyle(v uint8) lipgloss.Style {
	c := todoScoreBad
	switch {
	case v >= todo.QualityGate:
		c = todoScoreGood
	case v >= 76:
		c = todoScoreWarn
	}
	return lipgloss.NewStyle().Foreground(lipgloss.Color(theme.Hex(c)))
}

// TodoCardState is what the inline card draws from.
type TodoCardState struct {
	Items []todo.Item
	Plan  todo.Plan
	Goals []todo.Goal
}

// RenderTodoCard draws the inline chat card of plan.md §12.5 surface 1.
func (r *Renderer) RenderTodoCard(s TodoCardState) []string {
	if len(s.Items) == 0 {
		return []string{r.style(theme.RoleDim).
			Render("No tasks yet. The model populates them as work is planned.")}
	}

	width := r.Width
	if r.Centered {
		width = min(width-4, 120)
	} else {
		width = min(width-2, 100)
	}
	meta := lipgloss.NewStyle().Foreground(lipgloss.Color(theme.Hex(todoMeta)))

	var out []string

	if v := s.Plan.UnderstandsUserIntent; v != nil {
		out = append(out, meta.Render("Understands user intent ")+
			scoreStyle(*v).Render(fmt.Sprintf("%d%%", *v)))
	}
	if s.Plan.UserIntention != nil {
		label := meta.Render("  User intention · ")
		indent := strings.Repeat(" ", lipgloss.Width(plainText(label)))
		for i, line := range wrapPlain(*s.Plan.UserIntention, max(width-19, 20)) {
			if i == 0 {
				out = append(out, label+meta.Render(line))
			} else {
				out = append(out, indent+meta.Render(line))
			}
		}
	}

	goalByGroup := map[string]todo.Goal{}
	for _, g := range s.Goals {
		goalByGroup[g.Group] = g
	}

	groups := todo.Groups(s.Items)
	flat := len(groups) == 1 && groups[0] == ""

	for _, group := range groups {
		items := itemsInGroup(s.Items, group)
		if len(items) == 0 {
			continue
		}

		if !flat {
			out = append(out, r.todoGroupHeader(group, items))
		} else {
			done, total := todoBucketCounts(items)
			row := r.todoPips(items)
			if total > 0 {
				row += meta.Render(todoProgressSuffix(done, total))
			}
			out = append(out, row)
		}

		if g, ok := goalByGroup[group]; ok {
			if line := r.todoGoalLine(g); line != "" {
				out = append(out, line)
			}
			if g.FeedbackLoop != nil {
				out = append(out, meta.Render("  Feedback · "+*g.FeedbackLoop))
			}
		}

		for _, item := range todo.SortItems(items) {
			out = append(out, r.todoItemRow(item, width))
		}
	}
	return out
}

func itemsInGroup(items []todo.Item, group string) []todo.Item {
	var out []todo.Item
	for _, i := range items {
		g := ""
		if i.Group != nil {
			g = *i.Group
		}
		if g == group {
			out = append(out, i)
		}
	}
	return out
}

// todoGroupHeader draws `auth flow  ●●●○○ 2/3 done · 67%`, hot when anything
// is active. The exact fraction rides the pips row so the header's progress
// never depends on the per-item confidence scores.
func (r *Renderer) todoGroupHeader(group string, items []todo.Item) string {
	style := lipgloss.NewStyle().
		Foreground(lipgloss.Color(theme.Hex(todoGroupCool))).Bold(true)
	for _, i := range items {
		if i.Status == todo.StatusInProgress {
			style = lipgloss.NewStyle().
				Foreground(lipgloss.Color(theme.Hex(todoGroupHot))).Bold(true)
			break
		}
	}
	done, total := todoBucketCounts(items)
	meta := lipgloss.NewStyle().Foreground(lipgloss.Color(theme.Hex(todoMeta)))
	line := style.Render(group) + "  " + r.todoPips(items)
	if total > 0 {
		line += meta.Render(todoProgressSuffix(done, total))
	}
	return line
}

// todoPips renders the progress meter. One pip per item while they fit; past
// that it goes proportional but keeps at least one pip per non-empty bucket,
// so a bucket never disappears entirely (plan.md §8.4).
func (r *Renderer) todoPips(items []todo.Item) string {
	done, active, open := 0, 0, 0
	for _, i := range items {
		switch i.Status {
		case todo.StatusCompleted, todo.StatusCancelled:
			done++
		case todo.StatusInProgress:
			active++
		default:
			open++
		}
	}
	total := done + active + open
	if total == 0 {
		return ""
	}

	budget := total
	const maxPips = 12
	if budget > maxPips {
		budget = maxPips
	}

	scale := func(n int) int {
		if n == 0 {
			return 0
		}
		if total <= budget {
			return n
		}
		return max(n*budget/total, 1)
	}
	dp, ap, op := scale(done), scale(active), scale(open)

	doneStyle := lipgloss.NewStyle().Foreground(lipgloss.Color(theme.Hex(theme.RGB(100, 180, 100))))
	activeStyle := lipgloss.NewStyle().Foreground(lipgloss.Color(theme.Hex(theme.RGB(255, 200, 100))))
	openStyle := lipgloss.NewStyle().Foreground(lipgloss.Color(theme.Hex(theme.RGB(90, 90, 105))))

	return doneStyle.Render(strings.Repeat("●", dp)) +
		activeStyle.Render(strings.Repeat("●", ap)) +
		openStyle.Render(strings.Repeat("○", op))
}

// todoProgressSuffix is the exact `N/M done · P%` figure for a bucket,
// rendered from statuses. The pips can under-read a tiny bucket and the
// per-item confidence scores are not progress, so the row that shows either
// carries this too — and an all-closed group reads 100%.
func todoProgressSuffix(done, total int) string {
	return fmt.Sprintf(" %d/%d done · %d%%", done, total, percentOf(done, total))
}

// todoBucketCounts tallies closed (completed/cancelled) vs total for the
// progress suffix.
func todoBucketCounts(items []todo.Item) (done, total int) {
	for _, i := range items {
		total++
		if i.Status == todo.StatusCompleted || i.Status == todo.StatusCancelled {
			done++
		}
	}
	return done, total
}

func (r *Renderer) todoGoalLine(g todo.Goal) string {
	meta := lipgloss.NewStyle().Foreground(lipgloss.Color(theme.Hex(todoMeta)))
	var parts []string
	if v := g.ClosedFeedbackLoop; v != nil {
		parts = append(parts, meta.Render("Closed feedback loop ")+
			scoreStyle(*v).Render(fmt.Sprintf("%d%%", *v)))
	}
	if v := g.EndToEndOwnership; v != nil {
		parts = append(parts, meta.Render("Ownership ")+
			scoreStyle(*v).Render(fmt.Sprintf("%d%%", *v)))
	}
	if len(parts) == 0 {
		return ""
	}
	return "  " + strings.Join(parts, meta.Render(" · "))
}

// todoItemRow draws one item. The `75→100%` arrow on a completed item whose
// planning and completion confidence differ is the anti-gaming tell: it makes
// a bulk end-stamp visible next to an evidence-driven rise.
func (r *Renderer) todoItemRow(item todo.Item, width int) string {
	glyph, glyphColor := todoGlyph(item)
	textColor := todoTextIdle
	switch item.Status {
	case todo.StatusCompleted:
		textColor = todoTextDone
	case todo.StatusCancelled:
		textColor = todoTextCancel
	case todo.StatusInProgress:
		textColor = todoTextActive
	}
	if item.Blocked() && item.Status != todo.StatusCompleted {
		textColor = theme.RGB(120, 120, 130)
	}

	glyphStyle := lipgloss.NewStyle().Foreground(lipgloss.Color(theme.Hex(glyphColor)))
	textStyle := lipgloss.NewStyle().Foreground(lipgloss.Color(theme.Hex(textColor)))
	meta := lipgloss.NewStyle().Foreground(lipgloss.Color(theme.Hex(todoMeta)))

	suffix := ""
	if label := todo.ArrowLabel(item.Confidence, item.CompletionConfidence); label != "" {
		score := uint8(0)
		if item.CompletionConfidence != nil {
			score = *item.CompletionConfidence
		}
		suffix = meta.Render(" · ") + scoreStyle(score).Render(label)
	}
	if item.Blocked() && item.Status != todo.StatusCompleted {
		suffix += lipgloss.NewStyle().
			Foreground(lipgloss.Color(theme.Hex(todoBlocked))).Render(" (blocked)")
	}

	content := truncateCells(item.Content, max(width-14, 12))
	return "  " + glyphStyle.Render(glyph) + " " + textStyle.Render(content) + suffix
}

// todoGlyph picks the status marker. Blocked wins over everything except
// completed: a blocked item you already finished is just finished.
func todoGlyph(item todo.Item) (string, color.RGBA) {
	if item.Blocked() && item.Status != todo.StatusCompleted {
		return "⊳", todoBlocked
	}
	switch item.Status {
	case todo.StatusCompleted:
		return "✓", todoDone
	case todo.StatusInProgress:
		return "●", theme.RGB(0x8b, 0xe9, 0xfd)
	case todo.StatusCancelled:
		return "✗", todoCancelled
	default:
		return "○", todoPending
	}
}

// RenderAssessmentDelta draws surface 2: a write that only moved scores shows
// the movement rather than redrawing the whole plan (plan.md §12.5).
func (r *Renderer) RenderAssessmentDelta(deltas []todo.AssessmentDelta) []string {
	if len(deltas) == 0 {
		return nil
	}
	label := lipgloss.NewStyle().Foreground(lipgloss.Color(theme.Hex(todoGroupCool))).Bold(true)
	meta := lipgloss.NewStyle().Foreground(lipgloss.Color(theme.Hex(todoMeta)))
	newStyle := lipgloss.NewStyle().Foreground(lipgloss.Color(theme.Hex(todoScoreGood)))

	var out []string
	lastLabel := ""
	for _, d := range deltas {
		if d.Label != lastLabel {
			out = append(out, label.Render(d.Label)+meta.Render("  updated"))
			lastLabel = d.Label
		}
		out = append(out, "  "+meta.Render(d.Metric+" ")+
			meta.Render(fmt.Sprintf("%d%%", d.Old))+
			label.Render(" → ")+
			newStyle.Render(fmt.Sprintf("%d%%", d.New)))
	}
	return out
}

// RenderTodoDelta draws surface 3: the what-changed lines under a todo tool
// call. It is display-only and costs no tokens.
func (r *Renderer) RenderTodoDelta(d todo.Delta) []string {
	if d.Empty() {
		return nil
	}
	dim := r.style(theme.RoleDim)
	summary := fmt.Sprintf("  ↳ %s  (%d/%d)", d.Summary(), d.Done, d.Total)

	if !d.UsesFormB() {
		// Form A: a single flip reads better as the item itself than as a count.
		c := d.Changes[0]
		return []string{dim.Render("  ↳ ") +
			changeStyle(c.Change).Render(c.Change.Glyph()+" "+c.Item.Content) +
			dim.Render(fmt.Sprintf("  (%d/%d)", d.Done, d.Total))}
	}

	out := []string{dim.Render(summary)}
	const maxRows = 6
	shown := d.Changes
	if len(shown) > maxRows {
		shown = shown[:maxRows]
	}
	for _, c := range shown {
		out = append(out, "    "+
			changeStyle(c.Change).Render(c.Change.Glyph())+" "+
			dim.Render(truncateCells(c.Item.Content, max(r.Width-8, 12))))
	}
	if len(d.Changes) > len(shown) {
		out = append(out, dim.Render(fmt.Sprintf("    +%d more", len(d.Changes)-len(shown))))
	}
	return out
}

func changeStyle(c todo.Change) lipgloss.Style {
	switch c {
	case todo.ChangeAdded:
		return lipgloss.NewStyle().Foreground(lipgloss.Color(theme.Hex(theme.DiffAdd)))
	case todo.ChangeRemoved:
		return lipgloss.NewStyle().Foreground(lipgloss.Color(theme.Hex(theme.DiffDel)))
	case todo.ChangeDone:
		return lipgloss.NewStyle().Foreground(lipgloss.Color(theme.Hex(todoDone)))
	case todo.ChangeStarted:
		return lipgloss.NewStyle().Foreground(lipgloss.Color(theme.Hex(theme.RGB(255, 200, 100))))
	case todo.ChangeCancelled:
		return lipgloss.NewStyle().Foreground(lipgloss.Color(theme.Hex(todoCancelled)))
	default:
		return lipgloss.NewStyle().Foreground(lipgloss.Color(theme.Hex(todoMeta)))
	}
}
