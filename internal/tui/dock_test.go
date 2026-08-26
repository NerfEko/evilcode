package tui

import (
	"strings"
	"testing"
	"time"

	"evilcode/internal/todo"
)

// rowsOfWidth builds a frame whose rows are the given text widths.
func rowsOfWidth(widths ...int) []string {
	out := make([]string, len(widths))
	for i, w := range widths {
		out[i] = strings.Repeat("x", w)
	}
	return out
}

func renderWidgets(widgets []Widget) map[WidgetKind]Widget {
	render := make(map[WidgetKind]Widget, len(widgets))
	for _, w := range widgets {
		render[w.Kind] = w
	}
	return render
}

func layoutDock(d *Dock, w []Widget, rows []string, totalWidth, scrollTop, contentHeight int, centered bool) []Placement {
	_ = centered
	return d.Layout(renderWidgets(w), w, rows, nil, nil, -1, totalWidth, scrollTop, contentHeight)
}

func widget(kind WidgetKind, lines int) Widget {
	w := Widget{Kind: kind}
	for i := 0; i < lines; i++ {
		w.Lines = append(w.Lines, strings.Repeat("c", 20))
	}
	return w
}

func TestFreeWidthMeasuresTrailingSpace(t *testing.T) {
	got := FreeWidth(rowsOfWidth(0, 40, 100), 100)
	want := []int{100, 60, 0}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("free[%d] = %d, want %d", i, got[i], want[i])
		}
	}
}

func TestDockPlacesIntoBlankMargin(t *testing.T) {
	d := NewDock()
	rows := rowsOfWidth(10, 10, 10, 10, 10, 10, 10, 10, 10, 10,
		10, 10, 10, 10, 10, 10, 10, 10, 10, 10)
	got := layoutDock(d, []Widget{widget(WidgetTodos, 3)}, rows, 100, 0, 999, false)
	if len(got) != 1 {
		t.Fatalf("placements = %d, want 1", len(got))
	}
	p := got[0]
	if p.Col+p.Width != 100 {
		t.Errorf("widget right edge at %d, want the frame edge 100", p.Col+p.Width)
	}
	if p.Width < WidgetMinWidth || p.Width > WidgetMaxWidth {
		t.Errorf("width = %d, want between %d and %d", p.Width, WidgetMinWidth, WidgetMaxWidth)
	}
}

func TestDockRefusesWhenTextFillsTheRow(t *testing.T) {
	// Widgets live in the margin, not over the text. Overlaying was tried and
	// reverted: there is always somewhere to put a box if you are willing to
	// cover prose, so widgets appeared constantly and were harder to read past
	// than a missing box is to live without.
	d := NewDock()
	rows := rowsOfWidth(98, 98, 98, 98, 98, 98, 98, 98, 98, 98,
		98, 98, 98, 98, 98, 98, 98, 98, 98, 98)
	if got := layoutDock(d, []Widget{widget(WidgetTodos, 3)}, rows, 100, 0, 999, false); len(got) != 0 {
		t.Errorf("placements = %+v, want none when there is no margin", got)
	}
}

func TestDockPrefersAClearSlotToCoveringText(t *testing.T) {
	// Overlay is the fallback, not the preference: given a choice, a widget
	// still takes the margin.
	d := NewDock()
	rows := rowsOfWidth(98, 98, 98, 98, 98, 98, 98,
		10, 10, 10, 10, 10, 10, 10, 10, 10, 10, 10, 10, 10)

	got := layoutDock(d, []Widget{widget(WidgetTodos, 3)}, rows, 100, 0, 999, false)
	if len(got) != 1 {
		t.Fatal("expected a placement")
	}
	if got[0].Row < 7 {
		t.Errorf("placed at row %d, over the wide text, when clear rows were available",
			got[0].Row)
	}
}

func TestDockHoldsItsAnchorAcrossFrames(t *testing.T) {
	// A widget that re-picks its slot every frame skitters as text streams
	// under it. It must stay put (plan.md invariant 4).
	d := NewDock()
	rows := rowsOfWidth(10, 10, 10, 10, 10, 10, 10, 10, 10, 10,
		10, 10, 10, 10, 10, 10, 10, 10, 10, 10)
	w := []Widget{widget(WidgetTodos, 3)}

	first := layoutDock(d, w, rows, 100, 0, 999, false)
	if len(first) != 1 {
		t.Fatal("expected a placement")
	}
	for i := 0; i < 10; i++ {
		got := layoutDock(d, w, rows, 100, 0, 999, false)
		if len(got) != 1 || got[0].Row != first[0].Row {
			t.Fatalf("frame %d moved the widget from row %d to %+v", i, first[0].Row, got)
		}
	}
}

func TestDockScrollsWithTheText(t *testing.T) {
	// The anchor is an absolute transcript line, not a viewport row, so the
	// widget stays beside the content it was docked next to as the view
	// scrolls rather than hovering at a fixed screen position.
	d := NewDock()
	rows := make([]string, 40)
	for i := range rows {
		rows[i] = strings.Repeat("x", 10)
	}
	w := []Widget{widget(WidgetTodos, 3)}

	// Dock while the view is scrolled down, so the widget has an absolute
	// anchor well inside the transcript.
	first := layoutDock(d, w, rows, 100, 10, 999, false)
	if len(first) != 1 {
		t.Fatal("expected a placement")
	}
	wantAnchor := first[0].Row + 10

	// Scroll up by three: the same absolute line is now three rows lower.
	second := layoutDock(d, w, rows, 100, 7, 999, false)
	if len(second) != 1 {
		t.Fatal("expected a placement after scrolling")
	}
	if got := second[0].Row + 7; got != wantAnchor {
		t.Errorf("anchor moved from line %d to %d; it must stay with its content",
			wantAnchor, got)
	}
	if second[0].Row != first[0].Row+3 {
		t.Errorf("row = %d, want %d", second[0].Row, first[0].Row+3)
	}
}

func TestBlockedResidentHidesInPlaceRatherThanMoving(t *testing.T) {
	// A wide line sliding under the resident must not send it somewhere else.
	// It hides for as long as the line covers it and returns to the identical
	// row after — a widget that relocates has visibly teleported, which is worse
	// than one that is briefly absent.
	d := NewDock()
	open := rowsOfWidth(10, 10, 10, 10, 10, 10, 10, 10, 10, 10,
		10, 10, 10, 10, 10, 10, 10, 10, 10, 10)
	w := []Widget{widget(WidgetTodos, 3)}

	first := layoutDock(d, w, open, 100, 0, 999, false)
	if len(first) != 1 {
		t.Fatal("expected a placement")
	}

	blocked := append([]string(nil), open...)
	for i := first[0].Row; i < first[0].Row+first[0].Height; i++ {
		blocked[i] = strings.Repeat("x", 98)
	}
	if got := layoutDock(d, w, blocked, 100, 0, 999, false); len(got) != 0 {
		t.Errorf("widget moved to %+v instead of hiding in place", got)
	}

	back := layoutDock(d, w, open, 100, 0, 999, false)
	if len(back) != 1 || back[0].Row != first[0].Row {
		t.Errorf("came back at %+v, want its original row %d", back, first[0].Row)
	}
}

func TestDockPlacesAtMostOneWidget(t *testing.T) {
	// F2.3: at most one widget is on screen, and zero is fine. Offer several
	// candidates that all fit and assert the dock places exactly one — never
	// two, never the wall of boxes the multi-widget layout used to produce.
	d := NewDock()
	rows := make([]string, 60)
	for i := range rows {
		rows[i] = strings.Repeat("x", 10)
	}
	got := layoutDock(d, []Widget{
		widget(WidgetTodos, 3),
		widget(WidgetContextUsage, 3),
		widget(WidgetModelInfo, 3),
	}, rows, 100, 0, 999, false)

	if len(got) != 1 {
		t.Fatalf("placements = %d, want exactly 1 (one slot, zero is fine, never several)", len(got))
	}
}

func TestDockSecondCandidateGetsNoSlotWhileFirstHolds(t *testing.T) {
	// With one slot, a higher-priority widget that holds its rows leaves no room
	// for a second candidate: it is not placed at all (not re-homed elsewhere).
	// The cross-widget rehome hysteresis tested here previously is gone by design
	// — F2.5's salience score is what rotates the slot between candidates.
	d := NewDock()
	rows := make([]string, 40)
	for i := range rows {
		rows[i] = strings.Repeat("x", 10)
	}

	low := []Widget{widget(WidgetTips, 3)}
	first := layoutDock(d, low, rows, 100, 0, 999, false)
	if len(first) != 1 {
		t.Fatal("expected a placement")
	}

	// A higher-ranked candidate turns up. It does not get the slot: ranking
	// decides who moves in when the dock is empty, and never evicts a sitting
	// resident. Priority outranking a resident is what produced the swap-on-a-
	// timer behaviour.
	both := []Widget{widget(WidgetTodos, 3), widget(WidgetTips, 3)}
	for i := 0; i < 2; i++ {
		got := layoutDock(d, both, rows, 100, 0, 999, false)
		if len(got) != 1 {
			t.Fatalf("frame %d: placed %d widgets, want 1 (one slot)", i, len(got))
		}
		if got[0].Kind != WidgetTips || got[0].Row != first[0].Row {
			t.Fatalf("frame %d: slot went to %+v, want the sitting Tips at row %d",
				i, got[0], first[0].Row)
		}
	}
}

func TestEmptyWidgetDrawsNothing(t *testing.T) {
	// An empty box claims space to say nothing, which is worse than absence.
	r := testRenderer(80)
	if got := r.RenderWidget(Widget{Kind: WidgetTodos}); got != nil {
		t.Errorf("an empty widget rendered %d rows", len(got))
	}
}

func TestContextSaliencePreemptsStaticModelInfo(t *testing.T) {
	m := NewModel(nil, HeaderState{Model: "mock"})
	m.ctxUsed = 190_000
	widgets := m.activeWidgets()
	if len(widgets) == 0 || widgets[0].Kind != WidgetContextUsage {
		t.Fatalf("top widget = %+v, want near-full context meter", widgets)
	}
}

// TestResidentIsNeverSwappedForAnotherWidget is the reproduction for the
// reported "widgets disappear and get replaced with another widget like on a
// clock".
//
// A previous version of this file asserted the opposite — that the slot changes
// hands — which is where the clock came from: airtime accrued against a sitting
// widget until it outscored it, roughly every two seconds at the 80ms tick. A
// widget is a resident, not a timeslot. Nothing may take the screen from one
// that is still riding its anchor.
func TestResidentIsNeverSwappedForAnotherWidget(t *testing.T) {
	t.Setenv("EVILCODE_DETERMINISTIC", "")
	m := NewModel(nil, HeaderState{Model: "mock", SessionName: "s", Provider: "mock"})
	m.width, m.height = 140, 40
	m.blocks = []Block{{Kind: BlockTool, ToolName: "read", ToolTarget: "x"}}
	if first := m.activeWidgets(); len(first) < 2 {
		t.Fatalf("need at least two widget candidates, got %+v", first)
	}

	// Short rows owned by a settled tool block: dockable everywhere, so nothing
	// but policy decides what is on screen.
	const rowCount = 30
	owner := make([]int, rowCount)
	seen := map[WidgetKind]bool{}
	for i := 0; i < 400; i++ {
		rows := make([]string, rowCount)
		m.dockWidgets(rows, rows, rowCount, 0, owner)
		for _, p := range m.placements {
			seen[p.Kind] = true
		}
	}
	if len(seen) != 1 {
		t.Errorf("%d widgets held the dock over 400 unscrolled frames (%v); "+
			"a resident is never exchanged", len(seen), seen)
	}
	// Spacing is measured from the last spawn, so a screen's worth of unchanged
	// content spawns once and never again. Getting that test backwards is what
	// stacked widgets on top of each other and dragged the frame rate down.
	if len(m.dock.residents) != 1 {
		t.Errorf("%d residents after 400 frames over one screen of content, want 1",
			len(m.dock.residents))
	}
}

func TestScrolledOffWidgetReappearsOnScrollUp(t *testing.T) {
	const rowCount, viewH = 40, 10
	rows := make([]string, rowCount)
	d := NewDock()
	w := []Widget{widget(WidgetTodos, 3)}

	// Spawns land near the tail of the settled content, so the user following
	// the tail is the one who sees them.
	const tail = rowCount - viewH
	first := d.Layout(renderWidgets(w), w, rows, nil, nil, -1, 100, tail, viewH)
	if len(first) != 1 {
		t.Fatalf("no initial placement: %+v", first)
	}
	anchor := first[0].Row

	if got := d.Layout(renderWidgets(w), w, rows, nil, nil, -1, 100, 0, viewH); len(got) != 0 {
		t.Fatalf("offscreen resident produced a placement: %+v", got)
	}
	if len(d.residents) != 1 {
		t.Fatalf("offscreen resident count = %d, want 1", len(d.residents))
	}

	back := d.Layout(renderWidgets(w), w, rows, nil, nil, -1, 100, tail, viewH)
	if len(back) != 1 || back[0].Row != anchor {
		t.Fatalf("scrolling back produced %+v, want row %d", back, anchor)
	}
}

func TestSpawnSpacingIsOneViewport(t *testing.T) {
	const viewH = 10
	w1, w2 := widget(WidgetTodos, 3), widget(WidgetContextUsage, 3)
	render := renderWidgets([]Widget{w1, w2})
	d := NewDock()
	d.Layout(render, []Widget{w1}, make([]string, 40), nil, nil, -1, 100, 0, viewH)
	if len(d.residents) != 1 {
		t.Fatalf("residents after first spawn = %d", len(d.residents))
	}
	firstAnchor := d.residents[0].Offset

	// A few more lines of content is not a screenful, so no second spawn — no
	// matter how salient the candidate is.
	for _, grown := range []int{41, 44, firstAnchor + viewH - 1} {
		d.Layout(render, []Widget{w2}, make([]string, grown), nil, nil, -1, 100, 0, viewH)
		if len(d.residents) != 1 {
			t.Fatalf("candidate bypassed spacing at %d rows: %d residents", grown, len(d.residents))
		}
	}

	d.Layout(render, []Widget{w2}, make([]string, 60), nil, nil, -1, 100, 0, viewH)
	if len(d.residents) != 2 {
		t.Fatalf("spacing floor never cleared: %d residents", len(d.residents))
	}
	if gap := d.residents[1].Offset - firstAnchor; gap < viewH {
		t.Errorf("second spawn only %d rows below the first, want >= %d", gap, viewH)
	}
}

func TestDismissKillsInstanceOnly(t *testing.T) {
	w := widget(WidgetTodos, 3)
	render := renderWidgets([]Widget{w})
	d := NewDock()
	content := make([]string, 40)
	first := d.Layout(render, []Widget{w}, content, nil, nil, -1, 100, 30, 10)
	if len(first) != 1 {
		t.Fatal("expected first placement")
	}
	d.Dismiss(first[0].Index)
	if len(d.residents) != 0 {
		t.Fatalf("dismiss left %d residents", len(d.residents))
	}

	d.Layout(render, []Widget{w}, content, nil, nil, -1, 100, 30, 10)
	if len(d.residents) != 0 {
		t.Fatal("dismissed instance respawned before its spacing floor cleared")
	}
	content = make([]string, 50)
	placements := d.Layout(render, []Widget{w}, content, nil, nil, -1, 100, 42, 10)
	if len(d.residents) != 1 || len(placements) != 1 {
		t.Fatalf("kind did not respawn after spacing: residents=%d placements=%d", len(d.residents), len(placements))
	}
}

func TestDismissDoesNotResetSpacing(t *testing.T) {
	w := widget(WidgetTodos, 3)
	render := renderWidgets([]Widget{w})
	d := NewDock()
	content := make([]string, 40)
	first := d.Layout(render, []Widget{w}, content, nil, nil, -1, 100, 30, 10)
	if len(first) != 1 {
		t.Fatal("expected first placement")
	}
	d.Dismiss(first[0].Index)
	d.Layout(render, []Widget{w}, make([]string, 41), nil, nil, -1, 100, 30, 10)
	if len(d.residents) != 0 {
		t.Fatalf("dismissal reset spacing and respawned: %d residents", len(d.residents))
	}
}

func TestSpawnLandsOffscreenWhileScrolledUp(t *testing.T) {
	w := widget(WidgetTodos, 3)
	render := renderWidgets([]Widget{w})
	d := NewDock()
	content := make([]string, 60)
	if got := d.Layout(render, []Widget{w}, content, nil, nil, -1, 100, 0, 10); len(got) != 0 {
		t.Fatalf("tail spawn was painted while at the top: %+v", got)
	}
	if len(d.residents) != 1 {
		t.Fatal("offscreen spawn was not retained")
	}
	if got := d.Layout(render, []Widget{w}, content, nil, nil, -1, 100, 52, 10); len(got) != 1 {
		t.Fatalf("scrolling to the tail did not reveal the resident: %+v", got)
	}
}

func TestEmptyDataRendersStub(t *testing.T) {
	m := NewModel(nil, HeaderState{Model: "mock"})
	m.width, m.height = 100, 30
	m.todos = &todo.Store{}
	m.blocks = []Block{{Kind: BlockTool, ToolName: "read"}}
	m.dock.residents = []*instance{{Kind: WidgetTodos, Block: -1, Offset: 5}}
	m.dock.lastSpawn = m.dock.residents[0]

	content := make([]string, 30)
	owner := make([]int, len(content))
	for i := range owner {
		owner[i] = -1
	}
	rows := append([]string(nil), content[:20]...)
	m.dockWidgets(rows, content, 20, 0, owner)
	if len(m.placements) != 1 {
		t.Fatalf("stub placement = %+v", m.placements)
	}
	if !strings.Contains(strings.Join(rows, "\n"), "no todos") {
		t.Fatalf("empty resident did not render its stub:\n%s", strings.Join(rows, "\n"))
	}
}

func TestResizeKeepsResidents(t *testing.T) {
	w := widget(WidgetTodos, 3)
	render := renderWidgets([]Widget{w})
	d := NewDock()
	content := make([]string, 20)
	first := d.Layout(render, []Widget{w}, content, nil, nil, -1, 100, 0, 20)
	if len(first) != 1 {
		t.Fatal("expected first placement")
	}
	second := d.Layout(render, []Widget{w}, content, nil, nil, -1, 80, 0, 20)
	if len(d.residents) != 1 || len(second) != 1 || second[0].Row != first[0].Row {
		t.Fatalf("resize lost resident: first=%+v second=%+v residents=%d", first, second, len(d.residents))
	}
}

func TestSpawnSeatsLowWithClearanceAboveTheTail(t *testing.T) {
	// Two things at once. Seated near the pocket floor the widget has the whole
	// viewport above it as runway before it scrolls out — seated at the top it
	// would retire almost at once and the dock would spend its life respawning.
	// SpawnLift then keeps it clear of the floor, which is where the live
	// thinking bubble sits.
	const rowCount = 40
	rows := make([]string, rowCount)
	owner := make([]int, rowCount)
	// Block 0 is a live reasoning trace occupying the tail from row 30.
	for i := range owner {
		if i >= 30 {
			owner[i] = 1
		}
	}
	d := NewDock()
	w := widget(WidgetTodos, 3)

	got := d.Layout(renderWidgets([]Widget{w}), []Widget{w}, rows, owner,
		kindOfFixed(BlockTool, BlockReasoning), 1, 100, 0, rowCount)
	if len(got) != 1 {
		t.Fatalf("no placement: %+v", got)
	}
	// settledEnd = 30 - SettleMargin. The widget's bottom must clear it by the
	// lift, and it must sit well below the top of the screen.
	settledEnd := 30 - SettleMargin
	if bottom := got[0].Row + got[0].Height; bottom > settledEnd-SpawnLift {
		t.Errorf("widget bottom at row %d, want at or above %d — %d rows clear of the tail",
			bottom, settledEnd-SpawnLift, SpawnLift)
	}
	if got[0].Row < rowCount/3 {
		t.Errorf("widget seated at row %d of %d — too high to have any runway",
			got[0].Row, rowCount)
	}
}

func TestWidgetBoxIsRectangular(t *testing.T) {
	r := testRenderer(80)
	rows := plainLines(r.RenderWidget(Widget{
		Kind:  WidgetTodos,
		Lines: []string{"short", strings.Repeat("much longer line", 3)},
	}))
	width := len([]rune(rows[0]))
	for i, row := range rows {
		if got := len([]rune(row)); got != width {
			t.Errorf("row %d is %d cells, want %d: %q", i, got, width, row)
		}
	}
	if width > WidgetMaxWidth {
		t.Errorf("widget is %d cells wide, want at most %d", width, WidgetMaxWidth)
	}
}

func TestWidgetTitleOnlyWhenSet(t *testing.T) {
	r := testRenderer(80)
	plainBox := plainLines(r.RenderWidget(Widget{Kind: WidgetTodos, Lines: []string{"x"}}))
	if strings.Contains(plainBox[0], " ") && !strings.HasPrefix(plainBox[0], "╭─") {
		t.Errorf("untitled widget has a title area: %q", plainBox[0])
	}
	titled := plainLines(r.RenderWidget(Widget{
		Kind: WidgetWorkspaceMap, Title: "Workspace", Lines: []string{"x"},
	}))
	if !strings.Contains(titled[0], "Workspace") {
		t.Errorf("titled widget = %q", titled[0])
	}
}

func TestMeterColorsFollowRemaining(t *testing.T) {
	// A bar that reddens as it fills is a progress bar; one that reddens as it
	// empties is a warning. The spec wants the warning.
	full := SegmentedBar(0, 100, 10)
	nearlyGone := SegmentedBar(90, 100, 10)
	if full == nearlyGone {
		t.Fatal("bars at 0% and 90% used rendered identically")
	}
	if !strings.Contains(nearlyGone, "255;100;100") {
		t.Errorf("a nearly-exhausted meter should be red:\n%q", nearlyGone)
	}
	if !strings.Contains(full, "100;200;100") {
		t.Errorf("an empty meter should be green:\n%q", full)
	}
}

func TestBarsHandleDegenerateInputs(t *testing.T) {
	if got := SegmentedBar(5, 0, 10); got != "" {
		t.Errorf("zero total = %q, want empty", got)
	}
	if got := SolidBar(5, 100, 0); got != "" {
		t.Errorf("zero cells = %q, want empty", got)
	}
	// Over-full must clamp rather than repeat a negative count.
	if got := SegmentedBar(500, 100, 10); got == "" {
		t.Error("an over-full bar should still render")
	}
}

func TestFactStackIsOneObject(t *testing.T) {
	r := testRenderer(80)
	rows := plainLines(r.RenderFactStack(FactStack{
		Provider: "ollama-cloud", Auth: "api-key",
		Model: "qwen3-coder 480b", Cwd: "~/projects/evilcode", Branch: "main",
		Used: 84000, Total: 200000,
	}))
	if len(rows) != 4 {
		t.Fatalf("rows = %d, want the four fact rows", len(rows))
	}
	joined := strings.Join(rows, "\n")
	for _, want := range []string{"ollama-cloud", "api-key", "qwen3-coder", "main", "84.0k"} {
		if !strings.Contains(joined, want) {
			t.Errorf("fact stack is missing %q:\n%s", want, joined)
		}
	}
}

func TestFactStackOmitsUnknownFields(t *testing.T) {
	r := testRenderer(80)
	if got := r.RenderFactStack(FactStack{}); len(got) != 0 {
		t.Errorf("an empty fact stack rendered %d rows", len(got))
	}
}

func TestOverscrollRequiresGestureToBeginAtBottom(t *testing.T) {
	// The rule that makes it feel intentional: momentum merely *arriving* at
	// the bottom is swallowed, so scrolling down through a long transcript
	// does not flash the facts line every time (plan.md §4.4).
	now := time.Now()

	var arriving Overscroll
	arriving.Mode = OverscrollPull
	arriving.Tick(now, false) // gesture began mid-transcript
	arriving.Tick(now.Add(50*time.Millisecond), true)
	if arriving.Visible(now.Add(60*time.Millisecond), true) {
		t.Error("a gesture that merely arrived at the bottom must not reveal")
	}

	var deliberate Overscroll
	deliberate.Mode = OverscrollPull
	deliberate.Tick(now, true) // already pinned when the flick started
	if !deliberate.Visible(now.Add(10*time.Millisecond), true) {
		t.Error("a flick that began at the bottom should reveal")
	}
}

func TestOverscrollDwellExpires(t *testing.T) {
	now := time.Now()
	var o Overscroll
	o.Mode = OverscrollPull
	o.Tick(now, true)
	if !o.Visible(now.Add(OverscrollDwell-time.Millisecond), true) {
		t.Error("should still be visible within the dwell")
	}
	if o.Visible(now.Add(OverscrollDwell+time.Millisecond), true) {
		t.Error("should have rebounded away after the dwell")
	}
}

func TestOverscrollGestureGapStartsANewGesture(t *testing.T) {
	// The gap must exceed the idle redraw cadence, or one flick gets split
	// into two gestures and the reveal never triggers.
	if OverscrollGesture <= SpinnerInterval {
		t.Errorf("gesture gap %v must exceed the redraw cadence %v",
			OverscrollGesture, SpinnerInterval)
	}
	now := time.Now()
	var o Overscroll
	o.Mode = OverscrollPull
	o.Tick(now, false)
	// A long pause, then a flick that does begin at the bottom.
	o.Tick(now.Add(OverscrollGesture+time.Millisecond), true)
	if !o.Visible(now.Add(OverscrollGesture+2*time.Millisecond), true) {
		t.Error("a new gesture beginning at the bottom should reveal")
	}
}

func TestOverscrollModes(t *testing.T) {
	now := time.Now()

	var off Overscroll
	off.Mode = OverscrollOff
	off.Tick(now, true)
	if off.Visible(now, true) {
		t.Error("off must never reveal")
	}

	var always Overscroll
	always.Mode = OverscrollAlways
	if !always.Visible(now, true) {
		t.Error("on should show whenever pinned, with no gesture")
	}
	if always.Visible(now, false) {
		t.Error("on should hide when scrolled up")
	}
}

func TestOverscrollCancelledByScrollingUp(t *testing.T) {
	now := time.Now()
	var o Overscroll
	o.Mode = OverscrollPull
	o.Tick(now, true)
	o.Cancel()
	if o.Visible(now, true) {
		t.Error("scrolling up must cancel the reveal instantly")
	}
}

func TestCenteredContentWidth(t *testing.T) {
	// Centering is literal left padding, which keeps copy and column math sane.
	w, pad := ContentWidth(160, true)
	if w != CenteredCap {
		t.Errorf("width = %d, want the cap %d", w, CenteredCap)
	}
	if pad*2+w > 160 {
		t.Errorf("pad %d + width %d overflows 160", pad, w)
	}
}

func TestDiffModeCycles(t *testing.T) {
	// Off → Inline → Pinned → File → Off, and only the last two need the pane.
	seen := map[DiffMode]bool{}
	m := DiffOff
	for i := 0; i < int(numDiffModes); i++ {
		if seen[m] {
			t.Fatalf("cycle repeated %v after %d steps", m, i)
		}
		seen[m] = true
		m = m.Next()
	}
	if m != DiffOff {
		t.Errorf("cycle ended at %v, want it back at off", m)
	}
	if DiffOff.UsesPanel() || DiffInline.UsesPanel() {
		t.Error("off and inline should not open the pane")
	}
	if !DiffPinned.UsesPanel() || !DiffFile.UsesPanel() {
		t.Error("pinned and file need the pane")
	}
}

func TestFileDiffBlanksDeletedLineNumbers(t *testing.T) {
	// A deleted line does not exist in the new file, so numbering it invites
	// the reader to go look at a line that says something else (plan.md §9.4).
	r := testRenderer(80)
	diff := "--- a/x.go\n+++ b/x.go\n@@ -3,4 +3,4 @@\n ctx\n-old line\n+new line\n more\n"
	rows := plainLines(r.fileDiffLines("x.go", diff, 70))

	var deleted, added string
	for _, row := range rows {
		if strings.Contains(row, "old line") {
			deleted = row
		}
		if strings.Contains(row, "new line") {
			added = row
		}
	}
	if deleted == "" || added == "" {
		t.Fatalf("rows = %v", rows)
	}
	gutter := strings.SplitN(deleted, "│", 2)[0]
	if strings.TrimSpace(gutter) != "" {
		t.Errorf("deleted line carries number %q, want a blank gutter", gutter)
	}
	if strings.TrimSpace(strings.SplitN(added, "│", 2)[0]) == "" {
		t.Error("the added line should be numbered")
	}
}

func TestParseHunkStart(t *testing.T) {
	tests := []struct {
		in   string
		want int
		ok   bool
	}{
		{"@@ -1,3 +5,4 @@", 5, true},
		{"@@ -1 +12 @@ func main", 12, true},
		{"not a hunk", 0, false},
		{"@@ -1,3 @@", 0, false},
	}
	for _, tt := range tests {
		got, ok := parseHunkStart(tt.in)
		if got != tt.want || ok != tt.ok {
			t.Errorf("parseHunkStart(%q) = %d, %v; want %d, %v", tt.in, got, ok, tt.want, tt.ok)
		}
	}
}

func TestSidePanelRefusesTooNarrow(t *testing.T) {
	// Half a diff is worse than none.
	r := testRenderer(80)
	if got := r.RenderSidePanel(PanelContent{Diff: "x"}, DiffPinned, 10, 20, false, 0, false); got != nil {
		t.Errorf("a %d-wide pane rendered %d rows", 10, len(got))
	}
}

func TestSidePanelEmptySaysSo(t *testing.T) {
	r := testRenderer(120)
	rows := plainLines(r.RenderSidePanel(PanelContent{}, DiffPinned, 40, 6, false, 0, false))
	if !strings.Contains(strings.Join(rows, "\n"), "nothing pinned") {
		t.Errorf("rows = %v", rows)
	}
}

func TestWidgetReturnsToTheSameSlotAfterAGap(t *testing.T) {
	// A widget whose content empties for a frame — no todos, no background
	// tasks — used to keep a stale anchor, fail `fits` on return, and then hide
	// for RehomeFrames (~10s at the 80ms tick) before reappearing somewhere
	// else. That is the "it vanishes and comes back in a different place"
	// report.
	d := NewDock()
	rows := make([]string, 40)
	for i := range rows {
		rows[i] = strings.Repeat("x", 10)
	}
	w := []Widget{widget(WidgetTodos, 3)}

	first := layoutDock(d, w, rows, 100, 0, 999, false)
	if len(first) != 1 {
		t.Fatal("expected a placement")
	}

	// Gone for a frame.
	if got := layoutDock(d, nil, rows, 100, 0, 999, false); len(got) != 0 {
		t.Fatalf("an empty list placed %+v", got)
	}

	back := layoutDock(d, w, rows, 100, 0, 999, false)
	if len(back) != 1 {
		t.Fatal("the widget did not come back")
	}
	if back[0].Row != first[0].Row {
		t.Errorf("came back at row %d, want its original %d", back[0].Row, first[0].Row)
	}
}

func TestWidgetSurvivesTheScrollbarAppearing(t *testing.T) {
	// The dock runs before the scrollbar is painted and reserves its column, so
	// a bar appearing mid-session must not push a widget off the edge.
	d := NewDock()
	rows := make([]string, 40)
	for i := range rows {
		rows[i] = strings.Repeat("x", 10)
	}
	w := []Widget{widget(WidgetTodos, 3)}

	wide := layoutDock(d, w, rows, 100, 0, 999, false)
	narrow := layoutDock(d, w, rows, 100-ScrollbarReserve, 0, 999, false)
	if len(wide) != 1 || len(narrow) != 1 {
		t.Fatal("expected placements at both widths")
	}
	if narrow[0].Col+narrow[0].Width > 100-ScrollbarReserve {
		t.Errorf("widget right edge at %d, past the reserved width",
			narrow[0].Col+narrow[0].Width)
	}
}

func TestShrinkingContentNeverRelocatesTheResident(t *testing.T) {
	// A thinking trace collapsing from nine lines to one the instant the answer
	// starts removes lines from *above* the widget. With provenance the anchor
	// is block-relative and simply follows its block (see
	// TestDockAnchorFollowsBlockAfterRowsAboveCollapse). Without it — the
	// legacy synthetic-row path — the only safe answer is to stay put or be
	// absent. Reappearing at some other row is the lurch being guarded against.
	d := NewDock()
	rows := make([]string, 40)
	for i := range rows {
		rows[i] = strings.Repeat("x", 10)
	}
	w := []Widget{widget(WidgetTodos, 3)}

	first := layoutDock(d, w, rows, 100, 0, 200, false)
	if len(first) != 1 {
		t.Fatal("expected a placement")
	}

	// Eight lines vanish, as a collapsing trace does.
	got := layoutDock(d, w, rows[:len(rows)-8], 100, 0, 200, false)
	if len(got) == 1 && got[0].Row != first[0].Row {
		t.Errorf("widget lurched from row %d to %d when the transcript shortened",
			first[0].Row, got[0].Row)
	}
	if len(got) == 1 && (got[0].Row < 0 || got[0].Row+got[0].Height > len(rows)) {
		t.Errorf("widget landed off screen at row %d", got[0].Row)
	}
}

func TestDockKeepsItsAnchorWhileContentOnlyGrows(t *testing.T) {
	// Growing is the ordinary case — streaming appends — and must not re-home.
	// The anchor is an absolute content line, so holding it is what makes the
	// widget ride along with the text instead of snapping to a fresh slot.
	d := NewDock()
	rows := make([]string, 40)
	for i := range rows {
		rows[i] = strings.Repeat("x", 10)
	}
	w := []Widget{widget(WidgetTodos, 3)}

	if got := layoutDock(d, w, rows, 100, 0, 100, false); len(got) != 1 {
		t.Fatal("expected a placement")
	}
	before := d.residents[0].Offset

	// Several frames of content arriving.
	for i := 1; i <= 5; i++ {
		layoutDock(d, w, rows, 100, 0, 100+i, false)
	}
	if len(d.residents) != 1 {
		t.Fatal("resident retired while content only grew")
	}
	if got := d.residents[0].Offset; got != before {
		t.Errorf("anchor moved from %d to %d while content only grew", before, got)
	}
}
