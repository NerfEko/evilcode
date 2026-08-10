package tui

import (
	"strings"

	"charm.land/lipgloss/v2"
)

const (
	WidgetMinWidth   = 24
	WidgetMaxWidth   = 40
	WidgetMinHeight  = 5
	WidgetGap        = 2
	ScrollbarReserve = 2
	SettleMargin     = 4
	SpawnLift        = 3

	// SpawnSearchViewports bounds how far above the settled tail a spawn will
	// look for a pocket. Widgets spawn near the bottom of the settled content;
	// a slot ten screens up is history the user has already read past. It also
	// keeps the search off the whole transcript when there is no slot at all,
	// which is the ordinary case once prose fills the tail.
	SpawnSearchViewports = 2
)

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

// Widget is a dockable box of content.
type Widget struct {
	Kind     WidgetKind
	Salience float64
	Title    string
	Lines    []string
}

func (w Widget) Height() int { return len(w.Lines) + 2 }

func (w Widget) Width() int {
	inner := 0
	for _, l := range w.Lines {
		inner = max(inner, lipgloss.Width(l))
	}
	return clamp(inner+4, WidgetMinWidth, WidgetMaxWidth)
}

type Placement struct {
	Kind          WidgetKind
	Index         int
	Row           int
	Col           int
	Width, Height int
}

// instance is one spawned widget riding the transcript. It is removed only
// by a click, transcript replacement, or block compaction.
type instance struct {
	Kind   WidgetKind
	Block  int
	Offset int
}

type Dock struct {
	residents []*instance
	lastSpawn *instance
}

func NewDock() *Dock { return &Dock{} }

// FreeWidth reports how many trailing columns are blank on each row.
func FreeWidth(rows []string, totalWidth int) []int {
	out := make([]int, len(rows))
	for i, row := range rows {
		out[i] = max(totalWidth-lipgloss.Width(strings.TrimRight(row, " ")), 0)
	}
	return out
}

// freeWidths is FreeWidth measured on demand. Layout works in full-content
// coordinates, so eagerly measuring is the dock's entire cost: an ANSI-aware
// width of every row of a long transcript, every frame, when the rows that
// decide anything are a handful near the tail plus the residents' own.
type freeWidths struct {
	rows  []string
	total int
	// Only the visible residents and at most two tail viewports are measured.
	// A transcript-sized slice here still allocated in proportion to the whole
	// session on every frame, even after width measurement became lazy.
	memo map[int]int
}

func newFreeWidths(rows []string, total int) *freeWidths {
	return &freeWidths{rows: rows, total: total, memo: make(map[int]int)}
}

func (f *freeWidths) at(row int) int {
	if width, ok := f.memo[row]; ok {
		return width
	}
	width := max(f.total-lipgloss.Width(strings.TrimRight(f.rows[row], " ")), 0)
	f.memo[row] = width
	return width
}

func (f *freeWidths) len() int { return len(f.rows) }

// firstRows indexes each block to its first content row, -1 for blocks with no
// rows. Built once per Layout: resolving every resident by rescanning owner is
// quadratic in a long session, and it runs on the paint path. A slice rather
// than a map — this walks every row of the transcript per frame, and map stores
// cost several times what a slice store does.
func firstRows(owner []int) []int32 {
	var first []int32
	for i, block := range owner {
		if block < 0 {
			continue
		}
		for len(first) <= block {
			first = append(first, -1)
		}
		if first[block] < 0 {
			first[block] = int32(i)
		}
	}
	return first
}

// contentRow resolves an instance in full-content coordinates.
func (a *instance) contentRow(first []int32) (int, bool) {
	if a.Block < 0 {
		return a.Offset, true
	}
	if a.Block >= len(first) || first[a.Block] < 0 {
		return 0, false
	}
	return int(first[a.Block]) + a.Offset, true
}

func anchorAt(owner []int, row int) (int, int) {
	if owner == nil || row < 0 || row >= len(owner) || owner[row] < 0 {
		return -1, row
	}
	block := owner[row]
	first := row
	for first > 0 && owner[first-1] == block {
		first--
	}
	return block, row - first
}

// ResidentKinds returns the kinds that need a live render or empty stub.
func (d *Dock) ResidentKinds() []WidgetKind {
	out := make([]WidgetKind, 0, len(d.residents))
	seen := make(map[WidgetKind]bool, len(d.residents))
	for _, r := range d.residents {
		if !seen[r.Kind] {
			seen[r.Kind] = true
			out = append(out, r.Kind)
		}
	}
	return out
}

// Layout runs in content coordinates and converts visible placements to screen
// rows at the end.
func (d *Dock) Layout(render map[WidgetKind]Widget, candidates []Widget,
	content []string, owner []int, kindOf func(int) BlockKind,
	streamingBlock, totalWidth, scrollTop, viewH int, firstIndex ...[]int32) []Placement {
	if totalWidth <= WidgetMinWidth+WidgetGap {
		return nil
	}

	free := newFreeWidths(content, totalWidth)
	settledEnd := len(content)
	if owner != nil {
		if streamingBlock >= 0 {
			settledEnd = len(content)
			for i, block := range owner {
				if block == streamingBlock {
					settledEnd = i - SettleMargin
					break
				}
			}
		} else {
			settledEnd = len(content) - SettleMargin
		}
		settledEnd = max(settledEnd, 0)
	}

	dockable := func(row, height int) bool {
		if owner == nil {
			return true
		}
		if row < 0 || height < 0 || row+height > settledEnd || row+height > len(owner) {
			return false
		}
		for i := row; i < row+height; i++ {
			o := owner[i]
			if o != -1 && (kindOf == nil || kindOf(o) == BlockAssistant || kindOf(o) == BlockReasoning) {
				return false
			}
		}
		return true
	}

	place := func(w Widget, index, row int) (Placement, bool) {
		screen := row - scrollTop
		if screen+w.Height() <= 0 || screen >= viewH {
			return Placement{}, false
		}
		return Placement{
			Kind: w.Kind, Index: index, Row: screen, Col: totalWidth - w.Width(),
			Width: w.Width(), Height: w.Height(),
		}, true
	}

	var first []int32
	if len(firstIndex) > 0 && firstIndex[0] != nil {
		first = firstIndex[0]
	} else {
		first = firstRows(owner)
	}
	// Only a handful of widgets can fit in one viewport. Reserving capacity for
	// every historical resident made this allocation grow for the entire
	// session even though almost all residents are offscreen.
	placementCap := min(len(d.residents)+1, max(viewH/WidgetMinHeight+2, 1))
	placements := make([]Placement, 0, placementCap)
	for i := 0; i < len(d.residents); {
		resident := d.residents[i]
		row, ok := resident.contentRow(first)
		if !ok {
			if d.lastSpawn == resident {
				d.lastSpawn = nil
			}
			d.residents = append(d.residents[:i], d.residents[i+1:]...)
			continue
		}
		w, ok := render[resident.Kind]
		if ok {
			// Resolve visibility before measuring ANSI widths. Old residents remain
			// anchored so they reappear when the reader scrolls back, but offscreen
			// history must not cost width scans on every live frame.
			if p, visible := place(w, i, row); visible && fits(free, row, w.Height(), w.Width()) {
				placements = append(placements, p)
			}
		}
		i++
	}

	lastRow, hasFloor := 0, false
	if d.lastSpawn != nil {
		if row, ok := d.lastSpawn.contentRow(first); ok {
			lastRow, hasFloor = row, true
		} else {
			d.lastSpawn = nil
		}
	}
	minRow := max(settledEnd-SpawnSearchViewports*viewH, 0)
	for _, w := range candidates {
		floor := func(row int) bool { return !hasFloor || row-lastRow >= viewH }
		row, ok := findSlot(free, dockable, w.Height(), w.Width(), minRow, floor)
		if !ok {
			continue
		}
		block, offset := anchorAt(owner, row)
		resident := &instance{Kind: w.Kind, Block: block, Offset: offset}
		d.residents = append(d.residents, resident)
		d.lastSpawn = resident
		if p, visible := place(w, len(d.residents)-1, row); visible {
			placements = append(placements, p)
		}
		break
	}
	return placements
}

func fits(free *freeWidths, row, height, width int) bool {
	if row < 0 || row+height > free.len() {
		return false
	}
	for i := row; i < row+height; i++ {
		if free.at(i) < width+WidgetGap {
			return false
		}
	}
	return true
}

// findSlot tests the spacing floor and provenance before measuring width: the
// cheap rejections must come first, or the lazy measurement buys nothing.
func findSlot(free *freeWidths, dockable func(row, height int) bool,
	height, width, low int, extra func(row int) bool) (int, bool) {
	usable := func(row int) bool {
		return (extra == nil || extra(row)) && dockable(row, 1) && fits(free, row, 1, width)
	}
	for end := free.len(); end > low; end-- {
		if !usable(end - 1) {
			continue
		}
		start := end - 1
		for start > low && usable(start-1) {
			start--
		}
		row := max(end-height-SpawnLift, start)
		if row+height <= end && (extra == nil || extra(row)) && dockable(row, height) && fits(free, row, height, width) {
			return row, true
		}
		end = start + 1
	}
	return 0, false
}

// Dismiss kills one instance. lastSpawn deliberately survives.
func (d *Dock) Dismiss(index int) {
	if index >= 0 && index < len(d.residents) {
		d.residents = append(d.residents[:index], d.residents[index+1:]...)
	}
}

func (d *Dock) Reset() { d.residents, d.lastSpawn = nil, nil }

func (d *Dock) Hit(placements []Placement, col, row int) (Placement, bool) {
	for _, p := range placements {
		if row >= p.Row && row < p.Row+p.Height && col >= p.Col && col < p.Col+p.Width {
			return p, true
		}
	}
	return Placement{}, false
}
