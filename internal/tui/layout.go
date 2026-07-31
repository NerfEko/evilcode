// Package tui is evilcode's terminal interface. It is the only package allowed
// to import bubbletea (plan.md invariant 1).
package tui

// Slot names the rows of the vertical stack inside the chat column, in the
// order plan.md §3.2 lays them out.
type Slot int

const (
	SlotTranscript Slot = iota
	SlotQueued
	SlotSwarm
	SlotStatus
	SlotNotice
	SlotPicker
	SlotPickerGap
	SlotComposer
	SlotOverscroll
	SlotIdleArt

	numSlots
)

// MinScrollingTranscript is the floor for the transcript once it is scrolling.
const MinScrollingTranscript = 3

// Stack holds the height each slot wants. The transcript's height is computed,
// never requested — it is whatever is left, or exactly its content.
type Stack struct {
	// Available is the chat column's total height.
	Available int

	// ContentHeight is how many rows the transcript's content occupies.
	ContentHeight int

	// Heights are the requested heights of every non-transcript slot.
	Heights [numSlots]int
}

// Result is a resolved layout.
type Result struct {
	// Transcript is the height given to the transcript.
	Transcript int

	// Scrolling reports whether the transcript became a viewport. When false
	// the layout is "packed": the transcript is exactly as tall as its
	// content, so content hugs the composer with no dead gutter above it.
	Scrolling bool

	// Heights is the final height of every slot, transcript included.
	Heights [numSlots]int
}

// Fixed returns the total height of everything except the transcript.
func (s Stack) Fixed() int {
	total := 0
	for slot, h := range s.Heights {
		if Slot(slot) == SlotTranscript {
			continue
		}
		if h > 0 {
			total += h
		}
	}
	return total
}

// Resolve computes the layout. This is the signature structural trick of
// plan.md §3.2, and everything else sits on it:
//
// While content plus the fixed rows fit, the transcript gets its *exact*
// content height, so a short conversation sits right above the composer
// instead of floating at the top of an empty screen. Once it overflows, the
// transcript becomes a scrolling viewport with a floor of three rows.
func (s Stack) Resolve() Result {
	res := Result{Heights: s.Heights}

	fixed := s.Fixed()
	room := s.Available - fixed

	switch {
	case room <= 0:
		// The fixed rows alone do not fit. The transcript collapses rather
		// than pushing the composer off the bottom of the screen — losing
		// history is recoverable, losing the input box is not.
		res.Transcript = 0
		res.Scrolling = true

	case s.ContentHeight <= room:
		// Packed: hug the composer.
		res.Transcript = s.ContentHeight
		res.Scrolling = false

	default:
		res.Transcript = max(room, MinScrollingTranscript)
		res.Scrolling = true
	}

	res.Heights[SlotTranscript] = res.Transcript
	return res
}

// Total reports the resolved stack's total height, which may exceed Available
// when even the floors do not fit.
func (r Result) Total() int {
	total := 0
	for _, h := range r.Heights {
		if h > 0 {
			total += h
		}
	}
	return total
}

// Horizontal splits the terminal width into a side pane and the chat column
// (plan.md §3.1). Carving happens right to left; the chat column is whatever
// remains.
type Horizontal struct {
	Width int

	// SidePaneRatio is the requested side-pane percentage, clamped 25..100.
	SidePaneRatio int

	// SidePaneOpen requests the diff/markdown pane.
	SidePaneOpen bool
}

// Split widths.
const (
	MinDiffWidth = 30
	MinChatWidth = 20

	// SidePaneBorder is the single left border column the side pane draws.
	SidePaneBorder = 1
)

// Split resolves the horizontal layout. The side pane is dropped entirely
// rather than squeezed below its minimum: half a diff is worse than none.
func (h Horizontal) Split() (chatWidth, sideWidth int) {
	if !h.SidePaneOpen || h.Width <= 0 {
		return h.Width, 0
	}

	ratio := clamp(h.SidePaneRatio, 25, 100)
	side := h.Width * ratio / 100
	side = max(side, MinDiffWidth)

	chat := h.Width - side - SidePaneBorder
	if chat < MinChatWidth || side < MinDiffWidth {
		return h.Width, 0
	}
	return chat, side
}

// LeftInset is the gutter column in left-aligned mode. Centered mode has none,
// because centering provides its own margins (plan.md §3.4).
const LeftInset = 1

// Inset returns the left gutter for the given alignment.
func Inset(centered bool) int {
	if centered {
		return 0
	}
	return LeftInset
}

// CenteredCap is the maximum content width in centered mode.
const CenteredCap = 96

// ContentWidth returns the width text should wrap to, and the left padding
// that centers it. Centering is done with literal left padding rather than
// per-line centering, which keeps copy and column math sane (plan.md Phase 2).
func ContentWidth(total int, centered bool) (width, leftPad int) {
	if !centered {
		w := total - LeftInset
		return max(w, 1), LeftInset
	}
	w := min(total, CenteredCap)
	return max(w, 1), max((total-w)/2, 0)
}

func clamp(v, lo, hi int) int {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}
