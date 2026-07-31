package tui

import (
	"strings"
	"testing"
	"time"
)

// rowsOfWidth builds a frame whose rows are the given text widths.
func rowsOfWidth(widths ...int) []string {
	out := make([]string, len(widths))
	for i, w := range widths {
		out[i] = strings.Repeat("x", w)
	}
	return out
}

// layoutDock is the legacy Layout call used by the synthetic-row unit tests:
// no provenance, so the settled-region constraint is dropped and every row is a
// candidate (the pre-F2.2 behavior). Tests that exercise the settled region
// call d.Layout directly with a real owner array.
func layoutDock(d *Dock, w []Widget, rows []string, totalWidth, scrollTop, contentHeight int, centered bool) []Placement {
	return d.Layout(w, rows, nil, nil, -1, totalWidth, scrollTop, contentHeight, centered)
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

func TestDockHoldsItsSlotAsTextStreamsUnder(t *testing.T) {
	// The anchor is the point: a widget keeps the same row as content changes
	// beneath it, rather than being re-placed every frame.
	d := NewDock()
	open := rowsOfWidth(10, 10, 10, 10, 10, 10, 10, 10, 10, 10,
		10, 10, 10, 10, 10, 10, 10, 10, 10, 10)
	w := []Widget{widget(WidgetTodos, 3)}

	first := layoutDock(d, w, open, 100, 0, 999, false)
	if len(first) != 1 {
		t.Fatal("expected a placement")
	}

	// A wide line slides under it: it hides in place rather than jumping, and
	// comes back to the same slot once the line passes.
	blocked := append([]string(nil), open...)
	for i := first[0].Row; i < first[0].Row+3; i++ {
		blocked[i] = strings.Repeat("x", 98)
	}

	if got := layoutDock(d, w, blocked, 100, 0, 999, false); len(got) != 0 {
		t.Errorf("widget moved to %+v, want it hidden in place", got)
	}

	// Once the line passes, it comes back to the same slot.
	back := layoutDock(d, w, open, 100, 0, 999, false)
	if len(back) != 1 || back[0].Row != first[0].Row {
		t.Errorf("widget did not return to its slot: %+v", back)
	}
}

func TestDockRehomesOnlyAfterSustainedBlocking(t *testing.T) {
	// Hysteresis now guards the one case left that can actually displace a
	// widget: another widget taking its rows. Width no longer blocks anything,
	// because boxes overlay text.
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

	// A higher-priority widget now claims the same rows first.
	both := []Widget{widget(WidgetTodos, 3), widget(WidgetTips, 3)}
	for i := 0; i < RehomeFrames-1; i++ {
		got := layoutDock(d, both, rows, 100, 0, 999, false)
		for _, p := range got {
			if p.Kind == WidgetTips && p.Row != first[0].Row {
				t.Fatalf("frame %d: tips jumped to row %d rather than holding %d",
					i, p.Row, first[0].Row)
			}
		}
	}

	// Past the threshold it is allowed to find a new home.
	var moved bool
	for i := 0; i < 3 && !moved; i++ {
		for _, p := range layoutDock(d, both, rows, 100, 0, 999, false) {
			if p.Kind == WidgetTips && p.Row != first[0].Row {
				moved = true
			}
		}
	}
	if !moved {
		t.Error("tips never re-homed after the slot stayed taken")
	}
}

func TestDockNeverOverlapsWidgets(t *testing.T) {
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

	if len(got) < 2 {
		t.Fatalf("placements = %d, want several", len(got))
	}
	used := map[int]WidgetKind{}
	for _, p := range got {
		for r := p.Row; r < p.Row+p.Height; r++ {
			if prev, taken := used[r]; taken {
				t.Fatalf("row %d claimed by both %v and %v", r, prev, p.Kind)
			}
			used[r] = p.Kind
		}
	}
}

func TestLeftWidgetsFallBackToTheRightMargin(t *testing.T) {
	// Honouring the left preference meant six widget kinds that could never
	// render at all: the centered left margin is 22 cells at 140 columns, under
	// WidgetMinWidth of 24. Falling back to the right is the difference between
	// showing and not existing (see DEVIATIONS).
	d := NewDock()
	rows := make([]string, 40)
	for i := range rows {
		rows[i] = strings.Repeat("x", 10)
	}
	left := widget(WidgetBackgroundTasks, 3)
	if left.Kind.PreferredSide() != SideLeft {
		t.Fatal("BackgroundTasks should prefer the left margin")
	}
	got := layoutDock(d, []Widget{left}, rows, 100, 0, 999, false)
	if len(got) != 1 {
		t.Fatalf("a left-preferring widget did not dock at all: %+v", got)
	}
	if got[0].Col+got[0].Width != 100 {
		t.Errorf("right edge at %d, want the frame edge", got[0].Col+got[0].Width)
	}
}

func TestEmptyWidgetDrawsNothing(t *testing.T) {
	// An empty box claims space to say nothing, which is worse than absence.
	r := testRenderer(80)
	if got := r.RenderWidget(Widget{Kind: WidgetTodos}); got != nil {
		t.Errorf("an empty widget rendered %d rows", len(got))
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
	if got := r.RenderSidePanel(PanelContent{Diff: "x"}, DiffPinned, 10, 20, false); got != nil {
		t.Errorf("a %d-wide pane rendered %d rows", 10, len(got))
	}
}

func TestSidePanelEmptySaysSo(t *testing.T) {
	r := testRenderer(120)
	rows := plainLines(r.RenderSidePanel(PanelContent{}, DiffPinned, 40, 6, false))
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

func TestWidgetRehomesImmediatelyWhenItScrollsOffTheTop(t *testing.T) {
	// Scrolling with the content and staying visible used to be in conflict:
	// once the anchor scrolled above the viewport the row went negative, `fits`
	// failed, and hide-in-place held it invisible instead of re-placing it.
	d := NewDock()
	rows := make([]string, 40)
	for i := range rows {
		rows[i] = strings.Repeat("x", 10)
	}
	w := []Widget{widget(WidgetTodos, 3)}

	if got := layoutDock(d, w, rows, 100, 0, 999, false); len(got) != 1 {
		t.Fatal("expected a placement")
	}
	// The content it was riding has scrolled well above the viewport.
	got := layoutDock(d, w, rows, 100, 500, 999, false)
	if len(got) != 1 {
		t.Error("the widget disappeared instead of re-homing after scrolling off")
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

func TestDockRehomesWhenContentShrinks(t *testing.T) {
	// A thinking trace collapsing from nine lines to one the instant the answer
	// starts removes lines from *above* the widgets. An anchor is an absolute
	// content line, so every one of them silently starts naming different
	// content and the box lurches to wherever its old number now points.
	d := NewDock()
	rows := make([]string, 40)
	for i := range rows {
		rows[i] = strings.Repeat("x", 10)
	}
	w := []Widget{widget(WidgetTodos, 3)}

	first := layoutDock(d, w, rows, 100, 20, 200, false)
	if len(first) != 1 {
		t.Fatal("expected a placement")
	}

	// Eight lines vanish from above, as a collapsing trace does.
	got := layoutDock(d, w, rows, 100, 12, 192, false)
	if len(got) != 1 {
		t.Fatal("the widget disappeared when the transcript shortened")
	}
	// It re-homed cleanly rather than holding a line that now means something
	// else. What matters is that it is placed and on screen.
	if got[0].Row < 0 || got[0].Row+got[0].Height > len(rows) {
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
	before := d.anchors[WidgetTodos].ContentTop

	// Several frames of content arriving.
	for i := 1; i <= 5; i++ {
		layoutDock(d, w, rows, 100, 0, 100+i, false)
	}
	if got := d.anchors[WidgetTodos].ContentTop; got != before {
		t.Errorf("anchor moved from %d to %d while content only grew", before, got)
	}
}
