package tui

import (
	"context"
	"fmt"
	"image/color"
	"strconv"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"evilcode/internal/memory"
	"evilcode/internal/theme"
)

// Memory pipeline badge colors (plan.md §8.8).
var (
	MemoryIdleColor   = theme.RGB(120, 120, 130)
	MemorySearchColor = theme.RGB(140, 180, 255)
	MemoryVerifyColor = theme.RGB(255, 200, 100)
	MemoryDoneColor   = theme.RGB(100, 200, 100)
	MemorySaveColor   = theme.RGB(200, 150, 255)
	MemoryUpdateColor = theme.RGB(120, 220, 180)
	MemoryFailedColor = theme.RGB(255, 100, 100)
)

// memoryStepColor is the badge color for a step at rest.
func memoryStepColor(s memory.Stage) color.RGBA {
	switch s {
	case memory.StageFind:
		return MemorySearchColor
	case memory.StageCheck:
		return MemoryVerifyColor
	case memory.StageInject:
		return MemorySaveColor
	case memory.StageUpdate:
		return MemoryUpdateColor
	default:
		return MemoryIdleColor
	}
}

// memoryStep is one row of the bracket pipeline.
type memoryStep struct {
	stage memory.Stage
	label string
}

var memorySteps = []memoryStep{
	{memory.StageFind, "Find matches"},
	{memory.StageCheck, "Check relevance"},
	{memory.StageInject, "Inject context"},
	{memory.StageUpdate, "Update memory"},
}

// memoryStepDetail is the right-hand column for a step, given the activity.
func memoryStepDetail(s memory.Stage, a memory.Activity) string {
	switch s {
	case memory.StageFind:
		return plural(a.Candidates, "candidate")
	case memory.StageCheck:
		return fmt.Sprintf("%d above threshold", a.Relevant)
	case memory.StageInject:
		return fmt.Sprintf("%d tok", a.Tokens)
	default:
		return fmt.Sprintf("%d saved", a.Saved)
	}
}

// MemoryActivityWidget renders the memory pipeline (plan.md §8.8).
//
// It is absent while idle with nothing to report. A permanently docked box
// reading "idle" is the kind of clutter §8.3 exists to prevent — but a failed
// embedder still shows, because silence there is indistinguishable from a
// memory bank that simply had nothing to say.
func (r *Renderer) MemoryActivityWidget(a memory.Activity, elapsed int) Widget {
	if a.Stage == memory.StageIdle && a.Failed == "" && a.Saved == 0 {
		return Widget{Kind: WidgetMemoryActivity}
	}

	dim := lipgloss.NewStyle().Foreground(lipgloss.Color(theme.Hex(MemoryIdleColor)))
	label := lipgloss.NewStyle().Foreground(lipgloss.Color(theme.Hex(theme.RGB(200, 200, 210))))

	var head string
	if a.Failed != "" {
		head = lipgloss.NewStyle().
			Foreground(lipgloss.Color(theme.Hex(MemoryFailedColor))).
			Render("Now: memory unavailable")
	} else {
		status := a.Stage
		if status == memory.StageIdle {
			// Idle-with-saves is the tail of a completed pass, not nothing.
			head = lipgloss.NewStyle().
				Foreground(lipgloss.Color(theme.Hex(MemoryDoneColor))).
				Render("Now: memory up to date")
		} else {
			head = lipgloss.NewStyle().
				Foreground(lipgloss.Color(theme.Hex(memoryStepColor(status)))).
				Render("Now: "+status.String()) +
				dim.Render(fmt.Sprintf(" · %ds", elapsed))
		}
	}

	lines := []string{head}
	for i, step := range memorySteps {
		bracket := "├"
		switch i {
		case 0:
			bracket = "╭"
		case len(memorySteps) - 1:
			bracket = "╰"
		}

		style := dim
		switch {
		case a.Failed != "":
			// Nothing after the failure point can be trusted, so the whole
			// pipeline reads as inactive rather than as partially green.
		case step.stage < a.Stage || a.Stage == memory.StageIdle:
			style = lipgloss.NewStyle().Foreground(lipgloss.Color(theme.Hex(MemoryDoneColor)))
		case step.stage == a.Stage:
			style = lipgloss.NewStyle().
				Foreground(lipgloss.Color(theme.Hex(memoryStepColor(step.stage)))).Bold(true)
		}

		name := padCells(step.label, 17)
		lines = append(lines,
			style.Render(bracket+" ")+label.Render(name)+dim.Render(memoryStepDetail(step.stage, a)))
	}
	return Widget{Kind: WidgetMemoryActivity, Lines: lines}
}

// MemoryTileBorder is the recall tile's border color (plan.md §9.5).
var MemoryTileBorder = theme.RGB(150, 180, 255)

// RenderMemoryTile draws what passive recall injected:
//
//	╭─────────────────────────────────╮
//	│ 🧠 recalled 4 memories · 820 tok │
//	│  · the user prefers tabs        │
//	╰─────────────────────────────────╯
//
// The memories themselves are listed because an injection the user cannot see
// is an injection they cannot correct — the whole failure mode of a memory
// bank is a wrong fact quietly steering every answer.
func (r *Renderer) RenderMemoryTile(hits []memory.Hit) []string {
	if len(hits) == 0 {
		return nil
	}
	border := lipgloss.NewStyle().Foreground(lipgloss.Color(theme.Hex(MemoryTileBorder)))
	head := lipgloss.NewStyle().Foreground(lipgloss.Color(theme.Hex(MemoryTileBorder)))
	dim := r.style(theme.RoleDim)

	tokens := memory.EstimateTokens(memory.FormatMemories(hits))
	rows := []string{head.Render(fmt.Sprintf("🧠 recalled %s · %d tok",
		memoryCount(len(hits)), tokens))}
	for _, h := range hits {
		rows = append(rows, dim.Render(" · "+h.Text))
	}

	// The box hugs its content rather than the transcript width: a full-width
	// rule around one short line reads as a section break, not a tile.
	inner := 0
	for _, row := range rows {
		inner = max(inner, lipgloss.Width(row))
	}
	inner = min(inner, max(r.Width-4, 20))

	out := []string{border.Render("╭" + strings.Repeat("─", inner+2) + "╮")}
	for _, row := range rows {
		text := truncateCells(row, inner)
		out = append(out, border.Render("│")+" "+text+
			strings.Repeat(" ", max(inner-lipgloss.Width(text), 0))+" "+border.Render("│"))
	}
	return append(out, border.Render("╰"+strings.Repeat("─", inner+2)+"╯"))
}

// padCells right-pads to a cell width, so the pipeline's two columns line up
// whatever the label. lipgloss.Width, not len: the labels are ASCII today but
// the detail column is not.
func padCells(s string, width int) string {
	s = truncateCells(s, width)
	return s + strings.Repeat(" ", max(width-lipgloss.Width(s), 0))
}

// memoryCount renders a memory count with the right noun. "memory" is the one
// word here that does not take a plain -s, and it is the word every memory
// surface prints, so it gets its own helper rather than a plural() special case.
func memoryCount(n int) string {
	if n == 1 {
		return "1 memory"
	}
	return fmt.Sprintf("%d memories", n)
}

// plural renders a count with its noun. "memory" is spelled out because it does
// not take a plain -s, and the widget and the status line both need it.
func plural(n int, noun string) string {
	if n == 1 {
		return fmt.Sprintf("%d %s", n, noun)
	}
	if noun == "memory" {
		return fmt.Sprintf("%d memories", n)
	}
	return fmt.Sprintf("%d %ss", n, noun)
}

// memoryCommand implements `/memory` (plan.md §19).
//
// The bare form reports rather than toggling. Memory is the one subsystem whose
// state the user cannot see at a glance, so the default has to be "tell me what
// you know", not "flip a switch I then have to check".
func (m *Model) memoryCommand(arg string) tea.Cmd {
	if m.memory == nil || m.memory.Store == nil {
		m.notice = "memory is not configured for this session"
		return nil
	}

	verb, rest, _ := strings.Cut(arg, " ")
	switch strings.ToLower(strings.TrimSpace(verb)) {
	case "on":
		m.memory.SetEnabled(true)
		m.notice = "🧠 Memory ON"
	case "off":
		m.memory.SetEnabled(false)
		m.notice = "🧠 Memory OFF · nothing recalled or remembered until /memory on"

	case "forget":
		id, err := strconv.ParseInt(strings.TrimSpace(rest), 10, 64)
		if err != nil {
			m.notice = "usage: /memory forget <id> — /memory list shows the ids"
			break
		}
		found, err := m.memory.Store.Forget(id)
		switch {
		case err != nil:
			m.notice = "could not forget #" + rest + ": " + err.Error()
		case !found:
			m.notice = fmt.Sprintf("no memory #%d", id)
		default:
			m.notice = fmt.Sprintf("🧠 Forgot #%d", id)
		}

	case "list":
		m.blocks = append(m.blocks, Block{Kind: BlockNotice, Text: m.memoryList()})
		m.scroll.FollowBottom()

	default:
		m.blocks = append(m.blocks, Block{Kind: BlockNotice, Text: m.memoryStatus()})
		m.scroll.FollowBottom()
	}
	return nil
}

// memoryStatus is the report the bare `/memory` prints.
//
// Every line stands on its own rather than lining up in columns: notices are
// re-wrapped for the pane width, which eats leading and repeated spaces, so an
// aligned table here renders as a ragged one.
func (m *Model) memoryStatus() string {
	state := "ON"
	if !m.memory.Enabled() {
		state = "OFF"
	}
	all := m.memory.Store.All()

	counts := map[memory.Kind]int{}
	unembedded := 0
	for _, r := range all {
		counts[r.Kind]++
		if len(r.Vec) == 0 {
			unembedded++
		}
	}

	var parts []string
	for _, k := range []memory.Kind{memory.KindFact, memory.KindPreference,
		memory.KindProject, memory.KindEpisode} {
		if counts[k] > 0 {
			parts = append(parts, fmt.Sprintf("%d %s", counts[k], k))
		}
	}

	var b strings.Builder
	fmt.Fprintf(&b, "🧠 Memory %s · %d remembered\n", state, len(all))
	if len(parts) > 0 {
		b.WriteString(strings.Join(parts, " · ") + "\n")
	}
	if unembedded > 0 {
		// Worth saying plainly: these are findable by substring and invisible
		// to semantic recall, which looks like memory silently not working.
		fmt.Fprintf(&b, "⚠ %d without embeddings — lexical recall only\n", unembedded)
	}
	if act := m.memory.Activity(); act.Failed != "" {
		fmt.Fprintf(&b, "⚠ last error: %s\n", act.Failed)
	}
	b.WriteString("\n/memory list · /memory forget <id> · /memory off")
	return b.String()
}

// memoryList prints the most recent memories with their ids.
func (m *Model) memoryList() string {
	all := m.memory.Store.All()
	if len(all) == 0 {
		return "🧠 Nothing remembered yet."
	}
	const show = 20
	var b strings.Builder
	fmt.Fprintf(&b, "🧠 %s, newest first:\n", plural(len(all), "memory"))
	for i, r := range all {
		if i == show {
			fmt.Fprintf(&b, "… and %d more\n", len(all)-show)
			break
		}
		fmt.Fprintf(&b, "#%d (%s) %s\n", r.ID, r.Kind, memory.Truncate(r.Text, 72))
	}
	return strings.TrimRight(b.String(), "\n")
}

// ConsolidateMemory summarizes the session on the way out (plan.md §19).
//
// It is bounded rather than unbounded: a summary is worth a moment on exit, but
// a shell that hangs because a model is slow to answer is not a trade anyone
// would make. Whatever does not finish in time is simply not remembered.
func (m *Model) ConsolidateMemory() {
	if m.memory == nil || !m.memory.Enabled() || m.agent == nil {
		return
	}
	msgs := m.agent.Conv.Messages()
	if len(msgs) < 4 {
		// Too short to have been about anything; a summary of it would be noise
		// in the picker's search rather than a way to find this session again.
		return
	}
	var b strings.Builder
	for _, msg := range msgs {
		if txt := strings.TrimSpace(msg.Content); txt != "" {
			b.WriteString(string(msg.Role) + ": " + txt + "\n")
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), ConsolidateTimeout)
	defer cancel()
	m.memory.Consolidate(ctx, b.String())
}

// ConsolidateTimeout bounds the on-exit summary.
const ConsolidateTimeout = 20 * time.Second
