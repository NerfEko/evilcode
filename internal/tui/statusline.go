package tui

import (
	"fmt"
	"strings"
	"time"

	"charm.land/lipgloss/v2"

	"evilcode/internal/theme"
)

// SpinnerFrames is a circular spin, not a grow-and-recede. A test asserts the
// exact sequence, because the two read completely differently and it is easy to
// "fix" one into the other by reordering (plan.md §8.1).
var SpinnerFrames = []string{"⠋", "⠙", "⠹", "⠸", "⠼", "⠴", "⠦", "⠧", "⠇", "⠏"}

// SpinnerInterval is 80ms — 12.5 fps, one frame per tick.
const SpinnerInterval = 80 * time.Millisecond

// SpinnerFrame returns the frame for an elapsed duration.
func SpinnerFrame(elapsed time.Duration) string {
	idx := int(elapsed/SpinnerInterval) % len(SpinnerFrames)
	if idx < 0 {
		idx += len(SpinnerFrames)
	}
	return SpinnerFrames[idx]
}

// Phase is what the agent is doing, for the status line's priority ordering.
type Phase int

const (
	PhaseIdle Phase = iota
	PhaseSending
	PhaseConnecting
	PhaseThinking
	PhaseStreaming
	PhaseRunningTool
	PhaseWaitingNetwork
	PhaseRateLimited
)

// StatusState is everything the status line needs.
type StatusState struct {
	Phase   Phase
	Elapsed time.Duration

	// ToolName and ToolIntent describe the running tool.
	ToolName   string
	ToolIntent string

	// Batch progress, when more than one tool is running.
	BatchDone, BatchTotal int
	BatchRunning          []string
	BatchLastDone         string

	// Streaming stats.
	TokensPerSecond float64
	TokensIn        int
	TokensOut       int

	// CacheMiss flags an unexpectedly large prompt evaluation on a warm
	// session, which is the visible symptom of a lost KV cache.
	CacheMiss     bool
	CacheMissSize int

	// RetryIn is the rate-limit countdown.
	RetryIn time.Duration

	// Queued is the number of staged messages.
	Queued int

	// Tip is the idle rotating tip, already selected.
	Tip string

	// Warning is an idle-state history warning.
	Warning       string
	WarningSevere bool

	// ConnectPhase names the connection step, and Attempt counts retries.
	ConnectPhase string
	Attempt      int

	// Animate gates the tool color animation.
	Animate bool
}

// SlowConnectThreshold is when a single connection attempt starts looking
// wrong and the label turns amber.
const SlowConnectThreshold = 10 * time.Second

// RenderStatus draws the status line (plan.md §8.2), priority-ordered.
func (r *Renderer) RenderStatus(s StatusState) string {
	line := r.statusBody(s)
	if s.Queued > 0 {
		line += r.style(theme.RoleQueued).Render(fmt.Sprintf(" · +%d queued", s.Queued))
	}
	return line
}

func (r *Renderer) statusBody(s StatusState) string {
	dim := r.style(theme.RoleDim)
	spinner := SpinnerFrame(s.Elapsed)
	secs := formatElapsed(s.Elapsed)

	switch s.Phase {
	case PhaseRateLimited:
		amber := rgbStyle(255, 193, 7)
		return amber.Render(fmt.Sprintf("%s Rate limited. Auto-retry in %s...",
			spinner, formatCountdown(s.RetryIn)))

	case PhaseWaitingNetwork:
		return rgbStyle(255, 193, 7).Render(
			fmt.Sprintf("↻ network disconnected, waiting to retry · %s", secs))

	case PhaseSending:
		return r.style(theme.RoleAI).Render(spinner) + dim.Render(" sending… "+secs)

	case PhaseConnecting:
		label := fmt.Sprintf(" %s… %s", s.ConnectPhase, secs)
		style := dim
		// A retry, or one attempt dragging on, is worth flagging before the
		// user starts wondering whether it is hung.
		if s.Attempt > 0 || s.Elapsed > SlowConnectThreshold {
			style = rgbStyle(255, 193, 7)
		}
		return r.style(theme.RoleAI).Render(spinner) + style.Render(label)

	case PhaseThinking:
		return r.style(theme.RoleAI).Render(spinner) + dim.Render(" thinking… "+secs)

	case PhaseStreaming:
		// The prompt count is omitted until the provider reports it: it arrives
		// with the final chunk, and "↑0" for the whole of a streaming response
		// reads as a broken counter rather than as an unknown one.
		counts := fmt.Sprintf("↓%s", humanTokens(s.TokensOut))
		if s.TokensIn > 0 {
			counts = fmt.Sprintf("↑%s %s", humanTokens(s.TokensIn), counts)
		}
		body := fmt.Sprintf(" streaming… %s · %.1f tps · %s", secs, s.TokensPerSecond, counts)
		if s.CacheMiss {
			// The whole line goes amber: a cache miss is a cost event, and a
			// subtle marker would be missed.
			amber := rgbStyle(255, 193, 7)
			return amber.Render(fmt.Sprintf("⚠ %s cache miss · %s%s",
				humanTokens(s.CacheMissSize), spinner, body))
		}
		return r.style(theme.RoleAI).Render(spinner) + dim.Render(body)

	case PhaseRunningTool:
		return r.knightRider(s)

	default:
		if s.Warning != "" {
			style := rgbStyle(255, 184, 108)
			if s.WarningSevere {
				style = rgbStyle(255, 100, 100)
			}
			return style.Render("⚠ " + s.Warning)
		}
		if s.Tip != "" {
			return dim.Render("💡 " + s.Tip)
		}
		return ""
	}
}

// KnightRiderCells is the width of each mirrored bar.
const KnightRiderCells = 3

// knightRider draws the running-tool indicator of §8.2:
//
//	·●· bash ·●·  · reading foo.go · 4s · Alt+B bg
//
// The two bars mirror each other, which is what makes it read as a single
// sweeping object rather than two independent animations.
func (r *Renderer) knightRider(s StatusState) string {
	color := theme.AnimatedTool(s.Elapsed.Seconds(), s.Animate, r.Palette.Get(theme.RoleTool))
	style := lipgloss.NewStyle().Foreground(lipgloss.Color(theme.Hex(color)))
	dim := r.style(theme.RoleDim)

	// The sweep position advances with the spinner so the whole status line
	// animates on one clock.
	pos := int(s.Elapsed/SpinnerInterval) % KnightRiderCells

	bar := func(filled int) string {
		var b strings.Builder
		for i := 0; i < KnightRiderCells; i++ {
			if i == filled {
				b.WriteString("●")
			} else {
				b.WriteString("·")
			}
		}
		return b.String()
	}

	left := bar(pos)
	right := bar(KnightRiderCells - 1 - pos)

	name := s.ToolName
	if s.BatchTotal > 1 {
		name = "batch"
	}

	var out strings.Builder
	out.WriteString(style.Render(left) + " " +
		style.Bold(true).Render(name) + " " + style.Render(right))

	if s.BatchTotal > 1 {
		detail := fmt.Sprintf(" · %d/%d done", s.BatchDone, s.BatchTotal)
		if len(s.BatchRunning) > 0 {
			detail += ", running: " + strings.Join(s.BatchRunning, ", ")
		}
		if s.BatchLastDone != "" {
			detail += ", last done: " + s.BatchLastDone
		}
		out.WriteString(dim.Render(detail))
	} else if s.ToolIntent != "" {
		out.WriteString(dim.Render(" · " + s.ToolIntent))
	}

	out.WriteString(dim.Render(" · " + formatElapsed(s.Elapsed)))
	// Alt+B, not ⌥B: the binding is alt+b, evilcode is Linux-only (§1), and the
	// Mac option glyph is neither on this keyboard nor in §9.5's inventory.
	out.WriteString(rgbStyle(100, 100, 100).Render(" · Alt+B bg"))
	return out.String()
}

func formatElapsed(d time.Duration) string {
	secs := int(d.Seconds())
	if secs < 60 {
		return fmt.Sprintf("%ds", secs)
	}
	return fmt.Sprintf("%dm %ds", secs/60, secs%60)
}

func formatCountdown(d time.Duration) string {
	secs := int(d.Seconds())
	if secs < 60 {
		return fmt.Sprintf("%ds", secs)
	}
	return fmt.Sprintf("%dm %ds", secs/60, secs%60)
}

// TipPeriod and TipVisible describe the idle tip rotation (plan.md §8.2).
const (
	TipPeriod  = 90 * time.Second
	TipVisible = 34 * time.Second

	// MinTipWidth suppresses tips on a narrow terminal, where they would
	// crowd out everything that matters.
	MinTipWidth = 16
)

// Tips are the idle hints.
var Tips = []string{
	"Ctrl+R searches your prompt history",
	"Ctrl+G bookmarks your scroll position",
	"Alt+C toggles centered layout",
	"Ctrl+Enter does the opposite of your current send mode",
	"/help lists everything",
}

// TipAt returns the tip to show at a given uptime, or "" when the rotation is
// in its quiet phase or the terminal is too narrow.
func TipAt(uptime time.Duration, width int) string {
	if width < MinTipWidth || len(Tips) == 0 {
		return ""
	}
	phase := uptime % TipPeriod
	if phase >= TipVisible {
		return ""
	}
	idx := int(uptime/TipPeriod) % len(Tips)
	return Tips[idx]
}
