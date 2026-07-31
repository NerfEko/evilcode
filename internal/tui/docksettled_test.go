package tui

import (
	"strings"
	"testing"
)

// kindOfFixed returns a kindOf lookup for a fixed slice of kinds, for dock
// tests that exercise the settled region without building real blocks.
func kindOfFixed(kinds ...BlockKind) func(int) BlockKind {
	return func(idx int) BlockKind {
		if idx < 0 || idx >= len(kinds) {
			return BlockAssistant
		}
		return kinds[idx]
	}
}

// TestSettledRegionStopsTailPlacement is the F2.2 fail-then-pass reproduction.
//
// The bug: with full-width model prose filling the top of the viewport, the
// only rows FreeWidth reports as free are the streaming tail and the blank
// slack below the content. findSlot scans top-down and returns the first free
// run — which is exactly where the next line is about to arrive — so the widget
// is placed there and covered next frame. That is the flashing.
//
// The fix: the settled region (§2.3) makes rows at or below the streaming tail
// off-limits, and BlockAssistant rows are never dockable, so there is no slot
// and no widget — zero is a legitimate outcome.
func TestSettledRegionStopsTailPlacement(t *testing.T) {
	const totalWidth = 100
	// 20-row window. Rows 0-11: finished model prose (full width, undockable).
	// Rows 12-19: blank slack below the content (-1, maximally free).
	rows := rowsOfWidth(
		98, 98, 98, 98, 98, 98, 98, 98, 98, 98, 98, 98, // 0-11 prose
		0, 0, 0, 0, 0, 0, 0, 0, // 12-19 slack
	)
	// Owner: 0-11 owned by block 0 (a finished assistant answer), 12-19 chrome.
	owner := make([]int, len(rows))
	for i := range owner {
		if i <= 11 {
			owner[i] = 0
		} else {
			owner[i] = -1
		}
	}
	kindOf := kindOfFixed(BlockAssistant) // block 0 is model prose
	widgets := []Widget{widget(WidgetTodos, 3)}
	const wheight = 5 // widget.Height() == 3 lines + 2 border

	// Old behavior (no provenance): the widget is placed in the slack at the
	// tail — the bug. This is the fail side.
	d := NewDock()
	got := d.Layout(widgets, rows, nil, nil, -1, totalWidth, 0, 20, false)
	if len(got) != 1 {
		t.Fatalf("legacy layout: expected the bug (1 placement in the tail), got %d", len(got))
	}
	if got[0].Row < 12 {
		t.Errorf("legacy layout placed widget at row %d, expected >= 12 (the tail/slack bug)", got[0].Row)
	}

	// New behavior (provenance, nothing streaming): no streaming block, so
	// settledEnd = contentRows - SettleMargin = 12 - 4 = 8. Rows 0-11 are
	// BlockAssistant (undockable) and rows >= 8 are below settledEnd, so there
	// is no slot and no widget. This is the pass side — the flash is gone.
	d2 := NewDock()
	got2 := d2.Layout(widgets, rows, owner, kindOf, -1, totalWidth, 0, 12, false)
	if len(got2) != 0 {
		t.Errorf("settled layout placed %d widget(s); expected none (no dockable settled region):\n%+v",
			len(got2), got2)
	}
}

// TestSettledRegionHoldsSlotAcrossStreaming is the F2.2 stability check: when a
// settled, dockable region (tool rows) is visible alongside a streaming tail,
// the widget holds its slot on the settled rows and its Placement is identical
// on every frame as the tail churns below it.
func TestSettledRegionHoldsSlotAcrossStreaming(t *testing.T) {
	const totalWidth = 100
	// 24-row window. Rows 0-15: short tool rows (block 0, dockable). Rows 16-23:
	// the streaming assistant tail (block 1, full width).
	toolRows := make([]string, 16)
	for i := range toolRows {
		toolRows[i] = strings.Repeat("x", 10)
	}
	streamRows := make([]string, 8)
	for i := range streamRows {
		streamRows[i] = strings.Repeat("x", 98)
	}
	rows := append(append([]string{}, toolRows...), streamRows...)

	owner := make([]int, len(rows))
	for i := range owner {
		if i <= 15 {
			owner[i] = 0 // tool
		} else {
			owner[i] = 1 // streaming assistant
		}
	}
	kindOf := kindOfFixed(BlockTool, BlockAssistant)
	widgets := []Widget{widget(WidgetTodos, 3)} // height 5

	// Frame 1: streaming tail at rows 16-23. settledEnd = 16 - 4 = 12.
	d := NewDock()
	first := d.Layout(widgets, rows, owner, kindOf, 1, totalWidth, 0, 24, false)
	if len(first) != 1 {
		t.Fatalf("frame 1: expected 1 placement, got %d", len(first))
	}
	if first[0].Row+5 > 12 {
		t.Errorf("frame 1: widget at row %d extends below settledEnd (12)", first[0].Row)
	}

	// Frame 2: the tail churns — its lines change width — but the settled tool
	// region is unchanged, so the widget must hold the exact same placement.
	for i := range streamRows {
		streamRows[i] = strings.Repeat("x", 96) // churned, still full width
	}
	rows2 := append(append([]string{}, toolRows...), streamRows...)
	owner2 := make([]int, len(rows2))
	copy(owner2, owner)
	second := d.Layout(widgets, rows2, owner2, kindOf, 1, totalWidth, 0, 24, false)
	if len(second) != 1 {
		t.Fatalf("frame 2: expected 1 placement, got %d", len(second))
	}
	if second[0] != first[0] {
		t.Errorf("placement drifted across streaming frames:\n  frame 1: %+v\n  frame 2: %+v",
			first[0], second[0])
	}
}

// TestSettledRegionExcludesAssistantProse: even in a fully-settled (no
// streaming) transcript, a widget must never sit beside model prose. A run of
// tool rows sandwiched between assistant prose should host the widget; the
// prose rows are not candidates.
func TestSettledRegionExcludesAssistantProse(t *testing.T) {
	const totalWidth = 100
	// 0-4 assistant prose (full width), 5-12 tool (short, dockable), 13-19
	// assistant prose (full width). Nothing streaming.
	rows := []string{}
	for i := 0; i < 5; i++ {
		rows = append(rows, strings.Repeat("x", 98))
	}
	for i := 0; i < 8; i++ {
		rows = append(rows, strings.Repeat("x", 10))
	}
	for i := 0; i < 7; i++ {
		rows = append(rows, strings.Repeat("x", 98))
	}
	owner := []int{}
	for i := 0; i < 5; i++ {
		owner = append(owner, 0) // assistant
	}
	for i := 0; i < 8; i++ {
		owner = append(owner, 1) // tool
	}
	for i := 0; i < 7; i++ {
		owner = append(owner, 2) // assistant
	}
	kindOf := kindOfFixed(BlockAssistant, BlockTool, BlockAssistant)
	widgets := []Widget{widget(WidgetTodos, 3)} // height 5

	d := NewDock()
	got := d.Layout(widgets, rows, owner, kindOf, -1, totalWidth, 0, 20, false)
	if len(got) != 1 {
		t.Fatalf("expected 1 placement in the tool region, got %d", len(got))
	}
	p := got[0]
	// The widget must sit within the tool rows (5-12), never on assistant prose.
	if p.Row < 5 || p.Row+5 > 13 {
		t.Errorf("widget placed at rows %d-%d, must be within the tool region [5,13): %+v",
			p.Row, p.Row+5, p)
	}
}

func TestDockAnchorFollowsBlockAfterRowsAboveCollapse(t *testing.T) {
	// A line-number anchor points at the wrong content when a reasoning block
	// above it collapses. The block-relative anchor must follow the tool block.
	rows := make([]string, 18)
	owner := make([]int, len(rows))
	for i := range rows {
		rows[i] = strings.Repeat("x", 10)
		if i < 8 {
			owner[i] = 0 // assistant/reasoning above the widget
		} else {
			owner[i] = 1 // settled tool block the widget rides
		}
	}
	widgets := []Widget{widget(WidgetTodos, 3)}
	d := NewDock()
	kindOf := kindOfFixed(BlockAssistant, BlockTool)
	first := d.Layout(widgets, rows, owner, kindOf, -1, 100, 0, len(rows), false)
	if len(first) != 1 || first[0].Row != 8 {
		t.Fatalf("initial placement = %+v, want row 8 in the tool block", first)
	}

	// The assistant block shrinks from eight rows to one. The tool block is now
	// at row 1, and the widget must move with it instead of holding stale row 8.
	rows = make([]string, 11)
	owner = make([]int, len(rows))
	for i := range rows {
		rows[i] = strings.Repeat("x", 10)
		if i == 0 {
			owner[i] = 0
		} else {
			owner[i] = 1
		}
	}
	second := d.Layout(widgets, rows, owner, kindOf, -1, 100, 0, len(rows), false)
	if len(second) != 1 || second[0].Row != 1 {
		t.Fatalf("after collapse placement = %+v, want row 1 in the same tool block", second)
	}
}

// (test helpers kept minimal; strings.Repeat is used directly.)
