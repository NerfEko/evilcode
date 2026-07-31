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

	// SettleMargin is the guard band above the live tail: a widget may not be
	// placed within this many rows of the first row owned by a still-streaming
	// block, so the next line to arrive does not grow into it (§2.3). It is a
	// feel value — the right number depends on stream speed and terminal height
	// and will want tuning against a real session.
	SettleMargin = 4
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

	// lastHeight is the transcript's content height last frame. An anchor is an
	// absolute content line, so anything that removes lines *above* a widget
	// silently changes what its anchor points at — and the widget lurches. A
	// collapsing thinking trace does exactly that, going from nine lines to one
	// the instant the answer starts.
	lastHeight int
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

// Layout places widgets and returns where each landed.
//
// The two-phase structure is what keeps the screen still (plan.md §8.3):
// already-placed widgets hold their slot and merely shrink or hide in place
// when a wide line slides under them, and only re-home after the slot has been
// unusable for RehomeFrames consecutive frames.
//
// owner and kindOf carry the per-line block provenance of §1.2, which is what
// implements the settled-region policy of §2.3: a widget may only sit on rows
// the content has finished changing, and never beside model prose. When owner
// is nil (the synthetic-row unit tests, and any caller without provenance) the
// settled constraint is dropped and every row is a candidate — the legacy
// behavior, so those tests stay meaningful as free-width/overlay checks.
func (d *Dock) Layout(widgets []Widget, rows []string, owner []int, kindOf func(int) BlockKind, streamingBlock, totalWidth, scrollTop, contentHeight int, centered bool) []Placement {
	if len(widgets) == 0 || totalWidth <= WidgetMinWidth+WidgetGap {
		return nil
	}

	// The transcript got shorter, so lines were removed rather than added and
	// every anchor below the removal now names the wrong content. Re-home
	// instead of holding a stale line: a widget that lands somewhere sensible
	// reads better than one that lurches to wherever its old number now points.
	//
	// NOTE: F2.4 replaces this wholesale wipe with block-index anchors that do
	// not drift; it is kept here only until that task lands.
	if contentHeight < d.lastHeight {
		d.anchors = map[WidgetKind]*anchor{}
	}
	d.lastHeight = contentHeight

	free := FreeWidth(rows, totalWidth)

	// settledEnd is the first row that is off-limits for placement: the first
	// row owned by the still-streaming tail (the live block), or — when nothing
	// is streaming — the first row past the content (the slack/padding below the
	// text), minus a guard band. Rows at or below it are where the content is
	// still changing or about to arrive, so a widget placed there is covered next
	// frame. That is the root cause of the flashing (§2.2).
	settledEnd := len(rows)
	if owner != nil {
		if streamingBlock >= 0 {
			// The first visible row owned by the streaming tail is where the
			// unsettled region begins.
			first := -1
			for i, o := range owner {
				if o == streamingBlock {
					first = i
					break
				}
			}
			if first < 0 {
				// The streaming tail is outside this viewport (scrolled past it),
				// so every visible row is settled.
				settledEnd = len(rows)
			} else {
				settledEnd = first - SettleMargin
			}
		} else {
			// Nothing streaming: the content is finished. The first row past the
			// content in this window is where slack begins.
			contentRows := contentHeight - scrollTop
			if contentRows < 0 {
				contentRows = 0
			}
			if contentRows > len(rows) {
				contentRows = len(rows)
			}
			settledEnd = contentRows - SettleMargin
		}
	}
	if settledEnd < 0 {
		settledEnd = 0
	}

	// dockable reports whether a run of rows may hold a widget: each row is in
	// the settled region and not owned by model prose (BlockAssistant). Chrome
	// (owner -1) is dockable — the header and inter-block gaps are content
	// nothing will rewrite.
	dockable := func(row, height int) bool {
		if owner == nil {
			return true
		}
		if row+height > settledEnd {
			return false
		}
		for i := row; i < row+height; i++ {
			o := owner[i]
			if o != -1 && kindOf(o) == BlockAssistant {
				return false
			}
		}
		return true
	}

	// At most one widget is placed (§2.5: rule 5). Zero is a legitimate
	// outcome — there is no fallback placement, no pinning a box somewhere bad
	// just to have one. With one widget there is no second widget to overlap,
	// so the cross-widget `occupied` tracker is gone.
	var out []Placement

	place := func(w Widget, a *anchor, row, height int) Placement {
		a.everPlaced = true
		a.BadFrames = 0
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

			case fits(free, row, height, w.Width()) && dockable(row, height):
				// Still in the settled region and still fits: hold the slot.
				// Settled rows do not change, so this is the common case.
				out = append(out, place(w, a, row, height))
				continue

			default:
				// On screen but the slot is taken or left the settled region.
				// This is the one case hide-in-place is for, and only if it was
				// visible.
				a.BadFrames++
				if a.everPlaced && a.BadFrames < RehomeFrames {
					continue
				}
			}
		}

		row, ok := findSlot(free, owner, dockable, height, w.Width())
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
	// One slot: keep only the first placement. The widget that holds the slot
	// is chosen by list order today; F2.5's salience score reorders that list so
	// the slot rotates and urgency preempts.
	if len(out) > 1 {
		out = out[:1]
	}
	return out
}

// fits reports whether a widget can sit at row: the rows are in range and the
// text there leaves enough clear columns.
//
// Overlaying text was tried and reverted. It made widgets appear constantly —
// there is always *somewhere* to put a box if you are willing to cover prose —
// and a box sitting over a paragraph is harder to read past than a missing box
// is to live without.
func fits(free []int, row, height, width int) bool {
	if row < 0 || row+height > len(free) {
		return false
	}
	for i := row; i < row+height; i++ {
		if free[i] < width+WidgetGap {
			return false
		}
	}
	return true
}

// findSlot picks the topmost row where a widget fits, so a fresh placement is
// not covered by the next line that arrives.
//
// The look-ahead profile (reliableWidth) is gone: it looked ahead in *space*
// over rows that were blank because the content had not arrived yet, which is
// weakest at the bottom edge and — once placement is settled-only — actively
// harmful, predicting a slot will not stay clear when the settled region
// guarantees it will (§2.3). The settled-region check (dockable) and fits'
// instantaneous free-width test are enough: a settled row does not change, so
// the width it has now is the width it keeps.
func findSlot(free []int, owner []int, dockable func(row, height int) bool, height, width int) (int, bool) {
	for row := 0; row+height <= len(free); row++ {
		if !dockable(row, height) {
			continue
		}
		if fits(free, row, height, width) {
			return row, true
		}
	}
	return 0, false
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
