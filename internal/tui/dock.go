package tui

import (
	"charm.land/lipgloss/v2"
)

// Widget sizing (plan.md §8.3).
const (
	WidgetMinWidth  = 24
	WidgetMaxWidth  = 40
	WidgetMinHeight = 5

	// WidgetGap is the clearance between the transcript text and a widget, so
	// a docked box never looks glued to a line of prose.
	WidgetGap = 2

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
		out[i] = max(totalWidth-lipgloss.Width(row), 0)
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

	// occupied tracks rows already claimed, so two widgets never overlap.
	occupied := make([]bool, len(rows))
	var out []Placement

	for _, w := range widgets {
		side := w.Kind.PreferredSide()
		if side == SideLeft && !centered {
			// Only centered mode has a left margin to dock into.
			continue
		}

		a := d.anchors[w.Kind]
		height := w.Height()

		// Phase 1: try to hold the existing anchor.
		if a != nil {
			row := a.ContentTop - scrollTop
			if fits(free, occupied, row, height, w.Width()) {
				a.BadFrames = 0
				claim(occupied, row, height)
				out = append(out, Placement{
					Kind: w.Kind, Row: row, Col: totalWidth - w.Width(),
					Width: w.Width(), Height: height,
				})
				continue
			}
			a.BadFrames++
			if a.BadFrames < RehomeFrames {
				// Hide in place rather than moving. A widget that jumps the
				// moment a long line streams under it is worse than one that
				// briefly disappears.
				continue
			}
		}

		// Phase 2: find a new home against the look-ahead profile.
		row, ok := findSlot(free, occupied, height, w.Width())
		if !ok {
			continue
		}
		d.anchors[w.Kind] = &anchor{ContentTop: row + scrollTop, Side: side}
		claim(occupied, row, height)
		out = append(out, Placement{
			Kind: w.Kind, Row: row, Col: totalWidth - w.Width(),
			Width: w.Width(), Height: height,
		})
	}
	return out
}

// fits reports whether a widget of the given size can sit at row.
func fits(free []int, occupied []bool, row, height, width int) bool {
	if row < 0 || row+height > len(free) {
		return false
	}
	for i := row; i < row+height; i++ {
		if occupied[i] || free[i] < width+WidgetGap {
			return false
		}
	}
	return true
}

// findSlot picks the topmost row where the widget fits reliably.
func findSlot(free []int, occupied []bool, height, width int) (int, bool) {
	for row := 0; row+height <= len(free); row++ {
		if occupied[row] {
			continue
		}
		if reliableWidth(free, row, height) < width+WidgetGap {
			continue
		}
		if fits(free, occupied, row, height, width) {
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

// Forget drops a widget's anchor, which happens when it stops rendering.
func (d *Dock) Forget(kind WidgetKind) { delete(d.anchors, kind) }

// Reset clears every anchor, for a resize or an alignment change where holding
// old positions would be worse than starting over.
func (d *Dock) Reset() { d.anchors = map[WidgetKind]*anchor{} }
