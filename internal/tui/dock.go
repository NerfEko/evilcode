package tui

import (
	"charm.land/lipgloss/v2"
	"strings"
)

// Widget sizing (plan.md §8.3).
const (
	WidgetMinWidth  = 24
	WidgetMaxWidth  = 40
	WidgetMinHeight = 5

	// WidgetGap is the clearance between the transcript text and a widget, so
	// a docked box never looks glued to a line of prose.
	WidgetGap = 2

	// ScrollbarReserve is the column the scrollbar takes plus its gap. The dock
	// runs before the bar is painted, so it has to leave room for it.
	ScrollbarReserve = 2

	// RehomeFrames is how long a slot must be unusable before a widget moves.
	// Re-homing on the first bad frame makes widgets skitter as text streams
	// under them; this is the hysteresis that keeps them still (invariant 4).
	RehomeFrames = 120

	// LookAheadRows is how far ahead the reliable-width profile looks when
	// choosing a new home, so a fresh placement is not covered next frame.
	LookAheadRows = 12
)

// WidgetKind identifies a dockable widget. The order is the priority order of
// plan.md §8.3: lower wins when two want the same space.
type WidgetKind int

const (
	WidgetDiagrams WidgetKind = iota
	WidgetWorkspaceMap
	WidgetOverview
	WidgetTodos
	WidgetContextUsage
	WidgetUsageLimits
	WidgetKvCache
	WidgetMemoryActivity
	WidgetModelInfo
	WidgetCompaction
	WidgetBackgroundTasks
	WidgetGitStatus
	WidgetSwarmStatus
	WidgetAmbientMode
	WidgetTips
)

// Side is which margin a widget prefers.
type Side int

const (
	SideRight Side = iota
	SideLeft
)

// PreferredSide reports which margin a widget wants. Left-side widgets only
// exist in centered mode, because only it has a left margin to dock into.
func (k WidgetKind) PreferredSide() Side {
	switch k {
	case WidgetUsageLimits, WidgetKvCache, WidgetCompaction,
		WidgetBackgroundTasks, WidgetSwarmStatus, WidgetAmbientMode:
		return SideLeft
	default:
		return SideRight
	}
}

// Widget is a dockable box of content.
type Widget struct {
	Kind WidgetKind

	// Title is drawn in the top border. Only WorkspaceMap uses one; the rest
	// are recognizable by their content, and a title on each would turn the
	// margin into a wall of labels.
	Title string

	// Lines is the rendered content, already styled and within MaxWidth.
	Lines []string
}

// Height is the widget's total height including its border.
func (w Widget) Height() int { return len(w.Lines) + 2 }

// Width is the widget's total width including its border.
func (w Widget) Width() int {
	inner := 0
	for _, l := range w.Lines {
		inner = max(inner, lipgloss.Width(l))
	}
	return clamp(inner+4, WidgetMinWidth, WidgetMaxWidth)
}

// Placement is where a widget ended up.
type Placement struct {
	Kind WidgetKind

	// Row is the frame row the widget's top border sits on.
	Row int

	// Col is the frame column its left border sits on.
	Col int

	Width, Height int
}

// anchor remembers where a widget lives between frames, which is what lets it
// scroll with the text rather than snapping to a fresh slot each frame.
type anchor struct {
	// ContentTop is the absolute transcript line the widget's top row rides.
	ContentTop int

	Side Side

	// BadFrames counts consecutive frames the slot has been unusable.
	BadFrames int

	// everPlaced records that this widget has actually reached the screen at
	// some point. Hide-in-place is only correct for a widget the reader has
	// seen; applying it to one that was never visible is what kept widgets
	// hidden for 120 frames after they briefly dropped out of the list.
	//
	// Sticky rather than per-frame: hiding is itself a not-placed frame, so a
	// per-frame flag made the widget re-home on the very next tick and the
	// hysteresis never lasted longer than one frame.
	everPlaced bool
}

// Dock places widgets into the blank space beside the transcript.
type Dock struct {
	anchors map[WidgetKind]*anchor
}

// NewDock builds an empty dock.
func NewDock() *Dock { return &Dock{anchors: map[WidgetKind]*anchor{}} }

// FreeWidth reports, per row, how many trailing columns are blank. This is what
// the dock measures: widgets go where the text is not, so a long line of prose
// simply pushes them aside rather than overlapping.
func FreeWidth(rows []string, totalWidth int) []int {
	out := make([]int, len(rows))
	for i, row := range rows {
		// Trailing spaces are padding, not content. glamour writes its padding
		// *after* the styled text, so those rows genuinely end in plain spaces;
		// the user prompt band pads *inside* its style, so its row ends with the
		// reset and this trim is a no-op — background-painted padding is real
		// and still counts as occupied. The escape ordering does the work, so no
		// ANSI parsing is needed.
		out[i] = max(totalWidth-lipgloss.Width(strings.TrimRight(row, " ")), 0)
	}
	return out
}

// reliableWidth returns the width free across a band of upcoming rows, so a
// fresh placement is not covered by the next line that arrives. Using the
// instantaneous width instead is what makes widgets flicker in and out during
// streaming.
func reliableWidth(free []int, start, height int) int {
	end := min(start+height+LookAheadRows, len(free))
	if start >= end {
		return 0
	}
	w := free[start]
	for i := start + 1; i < end; i++ {
		w = min(w, free[i])
	}
	return w
}

// Layout places widgets and returns where each landed.
//
// The two-phase structure is what keeps the screen still (plan.md §8.3):
// already-placed widgets hold their slot and merely shrink or hide in place
// when a wide line slides under them, and only re-home after the slot has been
// unusable for RehomeFrames consecutive frames.
func (d *Dock) Layout(widgets []Widget, rows []string, totalWidth, scrollTop int, centered bool) []Placement {
	if len(widgets) == 0 || totalWidth <= WidgetMinWidth+WidgetGap {
		return nil
	}
	free := FreeWidth(rows, totalWidth)

	// occupied tracks rows already claimed, so two widgets never overlap each
	// other. Text underneath is fair game — see the overlay note below.
	occupied := make([]bool, len(rows))
	var out []Placement

	place := func(w Widget, a *anchor, row, height int) Placement {
		a.everPlaced = true
		a.BadFrames = 0
		claim(occupied, row, height)
		return Placement{
			Kind: w.Kind, Row: row, Col: totalWidth - w.Width(),
			Width: w.Width(), Height: height,
		}
	}

	for _, w := range widgets {
		side := w.Kind.PreferredSide()
		if side == SideLeft && !centered {
			// Fall back to the right margin rather than skipping. Honouring the
			// preference meant six widget kinds that could never render at all,
			// and the centered left margin is narrower than WidgetMinWidth at
			// every real terminal width anyway (see DEVIATIONS).
			side = SideRight
		}

		a := d.anchors[w.Kind]
		height := w.Height()

		if a != nil {
			row := a.ContentTop - scrollTop

			switch {
			case row < 0 || row+height > len(rows):
				// The anchored content scrolled out of the viewport. It was not
				// on screen, so re-homing now cannot be seen as a jump — fall
				// through without aging. This is what lets a widget scroll with
				// the text *and* stay visible, which used to be in conflict.

			case fits(occupied, row, height):
				out = append(out, place(w, a, row, height))
				continue

			default:
				// On screen but the slot is taken by another widget. This is the
				// one case hide-in-place is for, and only if it was visible.
				a.BadFrames++
				if a.everPlaced && a.BadFrames < RehomeFrames {
					continue
				}
			}
		}

		row, ok := findSlot(free, occupied, height, w.Width())
		if !ok {
			continue
		}
		if a == nil {
			a = &anchor{}
			d.anchors[w.Kind] = a
		}
		a.ContentTop, a.Side = row+scrollTop, side
		out = append(out, place(w, a, row, height))
	}
	return out
}

// fits reports whether a widget of the given size can sit at row without
// colliding with another widget.
//
// It deliberately does not consult free width. Widgets may overlay text: prose
// wraps to the full measure, so requiring clear columns meant boxes only ever
// appeared beside short rows — tool calls and lists — and never beside a
// paragraph. They arrive after a delay and can be dismissed with a click, so
// covering text is an acceptable trade for being visible at all.
func fits(occupied []bool, row, height int) bool {
	if row < 0 || row+height > len(occupied) {
		return false
	}
	for i := row; i < row+height; i++ {
		if occupied[i] {
			return false
		}
	}
	return true
}

// findSlot picks where a widget goes: the topmost run of rows with genuinely
// free columns if there is one, otherwise the topmost free run at all.
//
// Clear space is preferred, not required — the second pass is what makes a
// widget appear beside prose instead of not appearing.
func findSlot(free []int, occupied []bool, height, width int) (int, bool) {
	for row := 0; row+height <= len(occupied); row++ {
		if !fits(occupied, row, height) {
			continue
		}
		if reliableWidth(free, row, height) >= width+WidgetGap {
			return row, true
		}
	}
	for row := 0; row+height <= len(occupied); row++ {
		if fits(occupied, row, height) {
			return row, true
		}
	}
	return 0, false
}

func claim(occupied []bool, row, height int) {
	for i := row; i < row+height && i < len(occupied); i++ {
		occupied[i] = true
	}
}

// Forget drops a widget's anchor, so a widget dismissed by clicking does not
// come back to the same slot when it is restored.
//
// Was dead code for a long time: the aging path in Layout covers a widget that
// merely stops rendering, so this is only for a deliberate dismissal.
func (d *Dock) Forget(kind WidgetKind) { delete(d.anchors, kind) }

// Reset clears every anchor, for a resize or an alignment change where holding
// old positions would be worse than starting over.
func (d *Dock) Reset() { d.anchors = map[WidgetKind]*anchor{} }

// Hit reports the widget whose box covers a screen cell, if any.
func (d *Dock) Hit(placements []Placement, col, row int) (WidgetKind, bool) {
	for _, p := range placements {
		if row >= p.Row && row < p.Row+p.Height &&
			col >= p.Col && col < p.Col+p.Width {
			return p.Kind, true
		}
	}
	return 0, false
}
