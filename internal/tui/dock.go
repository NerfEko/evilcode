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

	// SettleMargin is the guard band above the live tail: a widget may not be
	// placed within this many rows of the first row owned by a still-streaming
	// block, so the next line to arrive does not grow into it (§2.3). It is a
	// feel value — the right number depends on stream speed and terminal height
	// and will want tuning against a real session.
	SettleMargin = 4

	// SpawnLift holds a new widget this many rows clear of the pocket floor it
	// is seated on. The floor sits just above the churning tail, and a widget
	// level with the live thinking bubble reads as having been parked next to
	// it. A couple of lines up is enough to break that reading.
	SpawnLift = 3

	// SpawnCooldown is how long the dock stays empty after a widget scrolls away
	// before it will seat another — roughly two seconds at the 80ms frame tick.
	// Without the pause a replacement arrives on the same frame the last one
	// left, which reads as the old widget jumping rather than a new one
	// arriving.
	SpawnCooldown = 25
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

	// Salience is the frame-local score used to choose the single dock slot.
	// It is UI state, not content, and is ignored by the painter.
	Salience float64

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

// anchor remembers where the resident lives between frames, which is what lets
// it scroll with the text rather than snapping to a fresh slot each frame.
type anchor struct {
	Kind WidgetKind

	// Block and Offset identify the transcript row the widget rides. Block is
	// -1 for chrome (header/gap/slack), where Offset is the absolute row. A
	// block-relative anchor survives that block gaining or losing lines above
	// it; an absolute content line did not — a reasoning trace collapsing above
	// the widget moved every line under it.
	Block  int
	Offset int

	Side Side
}

// Dock places one widget into the blank space beside the transcript.
//
// Widgets are residents, not a rotation. A widget spawns into a settled pocket
// of negative space, and from then on it belongs to that part of the
// conversation: it rides its anchor, scrolls up with the text, and eventually
// leaves the screen with the content it was placed beside. It is never swapped
// for another widget while it is up, and it never re-homes — a widget that
// re-homes has visibly teleported, and one that is exchanged on a timer reads
// as a clock.
//
// Once it has scrolled away the dock sits empty for SpawnCooldown frames before
// it will consider spawning another. That pause is what makes a new widget read
// as arriving rather than as the last one moving.
type Dock struct {
	// resident is the widget currently riding the transcript, or nil.
	resident *anchor

	// emptyFrames counts consecutive frames with no resident, so a fresh one
	// does not appear the instant the last one scrolls out. It starts already
	// past the cooldown: the pause is there to separate one widget from the
	// next, and there is nothing to separate the first one from.
	emptyFrames int
}

// NewDock builds an empty dock.
func NewDock() *Dock { return &Dock{emptyFrames: SpawnCooldown} }

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
// An existing anchor holds while its settled slot still fits. If the content
// makes that slot unusable, it rehomes on this frame; settled placement means
// ordinary streaming below it cannot churn the slot, so a long delay is not
// needed and would make displaced widgets disappear for seconds.
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
			for i := 0; i < len(rows); i++ {
				o := ownerAt(owner, scrollTop, i)
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
			o := ownerAt(owner, scrollTop, i)
			if o != -1 && kindOf(o) == BlockAssistant {
				return false
			}
		}
		return true
	}

	place := func(w Widget, row int) Placement {
		return Placement{
			Kind: w.Kind, Row: row, Col: totalWidth - w.Width(),
			Width: w.Width(), Height: w.Height(),
		}
	}
	offered := func(kind WidgetKind) (Widget, bool) {
		for _, w := range widgets {
			if w.Kind == kind {
				return w, true
			}
		}
		return Widget{}, false
	}

	// The resident rides its anchor. Nothing may take the slot from it and it
	// never re-homes: it belongs to the part of the conversation it was placed
	// beside, and it leaves the screen with it.
	if d.resident != nil {
		w, still := offered(d.resident.Kind)
		row, anchored := d.resident.screenRow(owner, scrollTop, len(rows))

		switch {
		case !still || !anchored || row < 0:
			// Its content has scrolled above the viewport — or the widget stopped
			// being offered at all, or the block it rode was compacted away. The
			// resident retires rather than re-homing. Re-homing is what made a
			// widget appear to teleport across the screen.
			d.resident = nil
			d.emptyFrames = 0

		case row+w.Height() > len(rows) || !fits(free, row, w.Height(), w.Width()):
			// Momentarily unusable — the viewport shrank, or a long line grew
			// under it. Hide in place with the anchor intact, so it comes back
			// exactly where it was instead of hunting for somewhere new.
			return nil

		default:
			return []Placement{place(w, row)}
		}
	}

	// Empty dock. Let the space stay empty for a moment first: a new widget
	// appearing the same frame the last one scrolled out reads as the old one
	// jumping, which is the whole effect this is here to avoid.
	d.emptyFrames++
	if d.emptyFrames < SpawnCooldown {
		return nil
	}

	// widgets arrives ranked by salience, so the highest-ranked candidate that
	// can find a pocket is the one that moves in. Salience only ever decides
	// this moment — once a widget is resident, ranking stops mattering.
	for _, w := range widgets {
		side := w.Kind.PreferredSide()
		if side == SideLeft && !centered {
			// Fall back to the right margin rather than skipping. Honouring the
			// preference meant six widget kinds that could never render at all,
			// and the centered left margin is narrower than WidgetMinWidth at
			// every real terminal width anyway (see DEVIATIONS).
			side = SideRight
		}
		row, ok := findSlot(free, dockable, w.Height(), w.Width())
		if !ok {
			continue
		}
		block, offset := anchorAt(owner, scrollTop, row)
		d.resident = &anchor{Kind: w.Kind, Block: block, Offset: offset, Side: side}
		d.emptyFrames = 0
		return []Placement{place(w, row)}
	}
	return nil
}

// ownerAt maps a visible row back to the full transcript provenance. Keeping
// the full Owner slice here matters: a block can be partly above the viewport,
// and its first visible row is not necessarily its first row.
func ownerAt(owner []int, scrollTop, row int) int {
	if owner == nil {
		return -1
	}
	i := scrollTop + row
	if i < 0 || i >= len(owner) {
		return -1
	}
	return owner[i]
}

// anchorAt converts a screen row into a block-relative anchor. Chrome has no
// block index, so its absolute content row is retained as the -1 fallback.
func anchorAt(owner []int, scrollTop, row int) (int, int) {
	global := scrollTop + row
	if owner == nil || global < 0 || global >= len(owner) || owner[global] < 0 {
		return -1, global
	}
	block := owner[global]
	first := global
	for first > 0 && owner[first-1] == block {
		first--
	}
	return block, global - first
}

// screenRow resolves a block-relative anchor against this frame's Owner data.
func (a *anchor) screenRow(owner []int, scrollTop, visible int) (int, bool) {
	if a.Block < 0 {
		return a.Offset - scrollTop, true
	}
	first := -1
	for i, block := range owner {
		if block == a.Block {
			first = i
			break
		}
	}
	if first < 0 {
		return 0, false
	}
	row := first + a.Offset - scrollTop
	return row, row >= -visible && row <= visible
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

// findSlot seats a new widget near the floor of the lowest usable pocket.
//
// Not the topmost row, which is what it used to return. A resident rides
// upward with the text and retires when it passes the top of the viewport, so
// the row it is born on *is* its whole lifespan: seated at the top of the
// screen it scrolls out almost immediately and the dock spends its life
// respawning, which is the slideshow. Seated low it has the entire viewport
// above it as runway.
//
// SpawnLift then holds it a few rows clear of the pocket floor. The floor sits
// just above the unsettled tail, which is where the live thinking bubble is, and
// a widget level with a bubble that is about to collapse looks like it was
// placed next to it on purpose.
//
// The look-ahead profile (reliableWidth) is gone: it looked ahead in *space*
// over rows that were blank because the content had not arrived yet, which is
// weakest at the bottom edge and — once placement is settled-only — actively
// harmful, predicting a slot will not stay clear when the settled region
// guarantees it will (§2.3). The settled-region check (dockable) and fits'
// instantaneous free-width test are enough: a settled row does not change, so
// the width it has now is the width it keeps.
func findSlot(free []int, dockable func(row, height int) bool, height, width int) (int, bool) {
	usable := func(row int) bool { return dockable(row, 1) && fits(free, row, 1, width) }

	// Walk pockets bottom-up and take the lowest one tall enough to hold the
	// widget plus its lift.
	for end := len(free); end > 0; end-- {
		if !usable(end - 1) {
			continue
		}
		start := end - 1
		for start > 0 && usable(start-1) {
			start--
		}
		// Seat at the pocket floor, lifted clear of it, but never above the
		// pocket's own ceiling: in a pocket only just tall enough, fitting at all
		// beats the extra clearance.
		row := max(end-height-SpawnLift, start)
		if row+height <= end && dockable(row, height) && fits(free, row, height, width) {
			return row, true
		}
		end = start + 1 // skip the rest of this pocket
	}
	return 0, false
}

// Forget retires the resident if it is the named kind, for a widget dismissed
// by clicking. The cooldown runs from here too, so swatting one away does not
// immediately produce another in its place.
func (d *Dock) Forget(kind WidgetKind) {
	if d.resident != nil && d.resident.Kind == kind {
		d.resident = nil
		d.emptyFrames = 0
	}
}

// Reset retires the resident, for a resize or an alignment change where every
// row has re-wrapped and holding an old position would be worse than starting
// over. No cooldown: nothing scrolled away, the frame was rebuilt under it, so
// a gap here would just read as the widget blinking out on resize.
func (d *Dock) Reset() { d.resident, d.emptyFrames = nil, SpawnCooldown }

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
