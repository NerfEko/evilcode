package tui

import (
	"strings"

	"charm.land/lipgloss/v2"

	"evilcode/internal/theme"
)

// PlanPrompt is the one-shot synthetic user turn `/plan` injects. It is a
// prompt, not a mode: there is no flag and no permission gate, which is what
// keeps the plan→execution handoff conversational (plan.md §12.1).
const PlanPrompt = `You are entering explicit planning-only mode for this request.

Goal: %s

Your job is to produce a clear, concrete, actionable plan. Do NOT implement anything yet: do not edit files, write patches, run mutating commands, or change git state. Read only the relevant code and project instructions so the plan is grounded in how things actually work; avoid an exhaustive repository tour.

When the plan is ready, present it directly in your reply inside a fenced code block whose language is ` + "`plan`" + ` (` + "```plan ... ```" + `). The UI renders that block as a dedicated plan card. Structure the plan inside the block with these sections: a top-level ` + "`# <short plan title>`" + ` heading, then Goal, Scope / affected areas, Approach (concrete ordered steps), Validation (how each part will be verified), and Open questions / decisions.

Keep it tight and high-signal. Avoid speculative rewrites and busywork. After presenting the plan card, stop and wait for the user. Do not start implementing.

Only once the user approves, use the ` + "`todo`" + ` tool if the work is genuinely multi-stage, then immediately begin the first actionable item. Do not treat the plan or todo list as implementation.`

// BarePlanGoal is substituted when `/plan` is called with no argument.
const BarePlanGoal = "produce a plan for the task or request currently in focus in this session. " +
	"If the goal is ambiguous, infer the most useful interpretation from the recent conversation " +
	"and repo state, and state your assumption."

// PlanFence is the opening marker the card renderer looks for.
const PlanFence = "```plan"

// PlanSegment is a plan block found in assistant text.
type PlanSegment struct {
	// Start and End are byte offsets into the source: the whole fenced region.
	Start, End int

	// Body is the plan content between the fences.
	Body string

	// Open marks a card whose closing fence has not arrived. It renders
	// anyway, growing as it streams — that behavior is what sells the feature
	// (plan.md §12.1).
	Open bool
}

// FindPlanSegments scans assistant content for plan blocks.
//
// The rules that matter, all of which have a failure mode if skipped:
//   - the opening fence must be exactly ```plan with nothing after it;
//   - a nested fence inside the plan (a ```bash example, say) must NOT
//     terminate the card — its body belongs inside the borders;
//   - an opening plan fence inside another fence is ignored;
//   - an unterminated fence renders anyway.
func FindPlanSegments(src string) []PlanSegment {
	// Fast path: the overwhelming majority of messages have no plan block, and
	// this runs on every frame of a streaming message.
	if !strings.Contains(src, PlanFence) {
		return nil
	}

	var out []PlanSegment
	lines := strings.SplitAfter(src, "\n")

	offset := 0
	inPlan := false
	inOtherFence := false
	nested := 0
	var body []string
	start := 0

	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		lineStart := offset
		offset += len(line)

		switch {
		case !inPlan && !inOtherFence && trimmed == PlanFence:
			inPlan = true
			start = lineStart
			body = nil
			nested = 0

		case !inPlan && strings.HasPrefix(trimmed, "```"):
			// Track other fences so a plan fence inside one is ignored.
			inOtherFence = !inOtherFence

		case inPlan && strings.HasPrefix(trimmed, "```"):
			if trimmed == "```" && nested == 0 {
				out = append(out, PlanSegment{
					Start: start, End: offset,
					Body: strings.Join(body, ""),
				})
				inPlan = false
				body = nil
				continue
			}
			// A nested fence toggles rather than closing the card.
			if trimmed == "```" {
				nested--
			} else {
				nested++
			}
			body = append(body, line)

		case inPlan:
			body = append(body, line)
		}
	}

	if inPlan {
		out = append(out, PlanSegment{
			Start: start, End: len(src),
			Body: strings.Join(body, ""),
			Open: true,
		})
	}
	return out
}

// PlanBorder is the violet the card is drawn in.
var PlanBorder = "#9e87ff"

// PlanCardWidth clamps the card, per §12.1.
const (
	PlanCardMin = 28
	PlanCardMax = 100
)

// RenderPlanCard draws the violet plan card.
//
// The body is markdown-rendered and then hard-wrapped BEFORE boxing: the box
// truncates, so unwrapped text would clip at the border rather than flowing.
func (r *Renderer) RenderPlanCard(seg PlanSegment) []string {
	width := clamp(r.Width-4, PlanCardMin, PlanCardMax)
	inner := max(width-4, 8)

	title, body := splitPlanTitle(seg.Body)
	if title == "" {
		title = "Plan"
	}

	rendered := body
	if trimmed := strings.TrimSpace(body); trimmed != "" {
		saved := r.Markdown.Width()
		r.Markdown.SetWidth(inner)
		rendered = r.Markdown.Render(trimmed, !seg.Open)
		r.Markdown.SetWidth(saved)
	}

	var content []string
	for _, line := range strings.Split(rendered, "\n") {
		// Hard-wrap before boxing so nothing clips at the border.
		for _, w := range wrapStyled(line, inner) {
			content = append(content, w)
		}
	}
	// Trim blank rows at both ends; the box provides its own spacing.
	for len(content) > 0 && strings.TrimSpace(plainText(content[0])) == "" {
		content = content[1:]
	}
	for len(content) > 0 && strings.TrimSpace(plainText(content[len(content)-1])) == "" {
		content = content[:len(content)-1]
	}
	if len(content) == 0 {
		content = []string{r.style(theme.RoleDim).Render("(empty plan)")}
	}

	return r.BoxTitled("⛭ "+title, content, PlanBorder)
}

// splitPlanTitle pulls the first heading out of a plan body to use as the card
// title, removing it from the body so it is not shown twice.
func splitPlanTitle(body string) (title, rest string) {
	lines := strings.Split(body, "\n")
	for i, line := range lines {
		trimmed := strings.TrimSpace(line)
		if !strings.HasPrefix(trimmed, "#") {
			continue
		}
		heading := strings.TrimSpace(strings.TrimLeft(trimmed, "#"))
		if heading == "" {
			continue
		}
		// Only a top-level-ish heading becomes the title.
		level := len(trimmed) - len(strings.TrimLeft(trimmed, "#"))
		if level > 3 {
			continue
		}
		remaining := append(append([]string{}, lines[:i]...), lines[i+1:]...)
		return heading, strings.Join(remaining, "\n")
	}
	return "", body
}

// wrapStyled wraps a line that may contain ANSI, measuring in cells.
func wrapStyled(line string, width int) []string {
	if lipgloss.Width(line) <= width {
		return []string{line}
	}
	// Styled wrapping is only needed for overlong rendered lines, which are
	// rare; falling back to the plain wrapper keeps the common path simple and
	// costs only the styling on those lines.
	return wrapPlain(plainText(line), width)
}

// plainText strips ANSI so widths and blank checks measure content.
func plainText(s string) string {
	var b strings.Builder
	inEscape := false
	for i := 0; i < len(s); i++ {
		switch {
		case s[i] == 0x1b:
			inEscape = true
		case inEscape && (s[i] == 'm' || s[i] == 'K'):
			inEscape = false
		case !inEscape:
			b.WriteByte(s[i])
		}
	}
	return b.String()
}
