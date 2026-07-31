package tui

import (
	"image/color"
	"math"
	"strings"
	"time"

	"charm.land/lipgloss/v2"

	"evilcode/internal/theme"
)

// Prompt-entry animation (plan.md §10.2). Three effects run over the same
// ~600ms window on the row that was just submitted: a warm pulse, a spotlight
// behind it, and a light sweep across it. Together they read as "this was
// received" without a spinner or a status line saying so.
const (
	EntryDuration = 600 * time.Millisecond

	// PulseStrength, SpotlightStrength, and ShimmerStrength scale each effect.
	// None reaches full: the row has to stay readable while it animates.
	PulseStrength     = 0.70
	SpotlightStrength = 0.85
	ShimmerStrength   = 0.70

	// ShimmerWidth is the sweep band's width as a fraction of the row.
	ShimmerWidth = 0.18
)

var (
	pulseTarget     = theme.RGB(255, 230, 120)
	spotlightTarget = theme.RGB(58, 66, 82)
	shimmerTarget   = theme.RGB(255, 248, 210)
)

// EntryAnimation tracks the effect on one submitted row.
type EntryAnimation struct {
	// Block is the index of the block being animated, or -1 for none.
	Block int

	// Started is when the prompt was submitted.
	Started time.Time
}

// NewEntryAnimation starts the effect on a block.
func NewEntryAnimation(block int) EntryAnimation {
	return EntryAnimation{Block: block, Started: time.Now()}
}

// Progress returns 0..1 through the animation, and whether it is still running.
func (e EntryAnimation) Progress(now time.Time) (float64, bool) {
	if e.Block < 0 || e.Started.IsZero() {
		return 0, false
	}
	elapsed := now.Sub(e.Started)
	if elapsed >= EntryDuration {
		return 1, false
	}
	return float64(elapsed) / float64(EntryDuration), true
}

// pulsePhase is a triangular envelope: in fast, out fast, brightest at the
// midpoint.
func pulsePhase(t float64) float64 {
	if t < 0.5 {
		return 2 * t
	}
	return 2 * (1 - t)
}

// spotlightPhase rises fast and decays smooth — an ease-in multiplied by an
// ease-out. The asymmetry is the point: arriving should feel immediate and
// leaving should feel unhurried.
func spotlightPhase(t float64) float64 {
	easeIn := 1 - math.Pow(1-t, 3)
	easeOut := math.Pow(1-t, 2)
	return clampUnit(easeIn * easeOut * 1.65)
}

// shimmerIntensity returns how strongly a horizontal position is lit by the
// sweep at time t. Position is 0..1 across the row.
func shimmerIntensity(t, position float64) float64 {
	travel := clampUnit(t * 1.15)
	dist := math.Abs(position - travel)
	if dist > ShimmerWidth {
		return 0
	}
	local := math.Pow(1-dist/ShimmerWidth, 2.2)
	// The whole sweep fades as the animation ends, so it does not stop dead.
	fade := math.Pow(1-t, 0.55)
	return local * fade
}

func clampUnit(v float64) float64 {
	if v < 0 {
		return 0
	}
	if v > 1 {
		return 1
	}
	return v
}

// ApplyEntryAnimation re-colors a row's plain text for the animation frame.
//
// It works on plain text and re-styles from scratch rather than trying to
// modify existing escape sequences: the row it animates is the user band, whose
// styling it already owns, and rewriting SGR in place would be fragile for no
// benefit.
func ApplyEntryAnimation(text string, t float64, fg, bg color.RGBA) string {
	pulse := pulsePhase(t) * PulseStrength
	spot := spotlightPhase(t) * SpotlightStrength

	rowFg := theme.Blend(fg, pulseTarget, pulse)
	rowBg := theme.Blend(bg, spotlightTarget, spot)

	runes := []rune(text)
	if len(runes) == 0 {
		return text
	}

	var b strings.Builder
	for i, r := range runes {
		position := float64(i) / float64(max(len(runes)-1, 1))
		cellFg := rowFg
		if s := shimmerIntensity(t, position) * ShimmerStrength; s > 0 {
			cellFg = theme.Blend(cellFg, shimmerTarget, s)
		}
		b.WriteString(lipgloss.NewStyle().
			Foreground(lipgloss.Color(theme.Hex(cellFg))).
			Background(lipgloss.Color(theme.Hex(rowBg))).
			Render(string(r)))
	}
	return b.String()
}

// animateEntry re-renders a block's rows for one animation frame.
func (r *Renderer) animateEntry(lines []string, t float64) []string {
	fg := r.Palette.Get(theme.RoleUserText)
	bg := r.Palette.Get(theme.RoleUserBg)

	out := make([]string, len(lines))
	for i, line := range lines {
		out[i] = ApplyEntryAnimation(plainText(line), t, fg, bg)
	}
	return out
}
