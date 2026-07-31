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
	got := d.Layout([]Widget{widget(WidgetTodos, 3)}, rows, 100, 0, false)
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
	d := NewDock()
	rows := rowsOfWidth(98, 98, 98, 98, 98, 98, 98, 98, 98, 98,
		98, 98, 98, 98, 98, 98, 98, 98, 98, 98)
	if got := d.Layout([]Widget{widget(WidgetTodos, 3)}, rows, 100, 0, false); len(got) != 0 {
		t.Errorf("placements = %+v, want none when there is no margin", got)
	}
}

func TestDockHoldsItsAnchorAcrossFrames(t *testing.T) {
	// A widget that re-picks its slot every frame skitters as text streams
	// under it. It must stay put (plan.md invariant 4).
	d := NewDock()
	rows := rowsOfWidth(10, 10, 10, 10, 10, 10, 10, 10, 10, 10,
		10, 10, 10, 10, 10, 10, 10, 10, 10, 10)
	w := []Widget{widget(WidgetTodos, 3)}

	first := d.Layout(w, rows, 100, 0, false)
	if len(first) != 1 {
		t.Fatal("expected a placement")
	}
	for i := 0; i < 10; i++ {
		got := d.Layout(w, rows, 100, 0, false)
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
	first := d.Layout(w, rows, 100, 10, false)
	if len(first) != 1 {
		t.Fatal("expected a placement")
	}
	wantAnchor := first[0].Row + 10

	// Scroll up by three: the same absolute line is now three rows lower.
	second := d.Layout(w, rows, 100, 7, false)
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

func TestDockHidesInPlaceRatherThanJumping(t *testing.T) {
	// When a wide line slides under a widget it hides rather than re-homing,
	// and only moves after the slot has been unusable for a long time.
	d := NewDock()
	open := rowsOfWidth(10, 10, 10, 10, 10, 10, 10, 10, 10, 10,
		10, 10, 10, 10, 10, 10, 10, 10, 10, 10)
	w := []Widget{widget(WidgetTodos, 3)}

	first := d.Layout(w, open, 100, 0, false)
	if len(first) != 1 {
		t.Fatal("expected a placement")
	}

	// A wide line covers the slot.
	blocked := append([]string(nil), open...)
	for i := first[0].Row; i < first[0].Row+3; i++ {
		blocked[i] = strings.Repeat("x", 98)
	}

	got := d.Layout(w, blocked, 100, 0, false)
	if len(got) != 0 {
		t.Errorf("widget should hide in place, not move to %+v", got)
	}

	// Once the line passes, it comes back to the same slot.
	back := d.Layout(w, open, 100, 0, false)
	if len(back) != 1 || back[0].Row != first[0].Row {
		t.Errorf("widget did not return to its slot: %+v", back)
	}
}

func TestDockRehomesOnlyAfterSustainedBlocking(t *testing.T) {
	d := NewDock()
	open := make([]string, 40)
	for i := range open {
		open[i] = strings.Repeat("x", 10)
	}
	w := []Widget{widget(WidgetTodos, 3)}

	first := d.Layout(w, open, 100, 0, false)
	if len(first) != 1 {
		t.Fatal("expected a placement")
	}

	blocked := append([]string(nil), open...)
	for i := first[0].Row; i < first[0].Row+3; i++ {
		blocked[i] = strings.Repeat("x", 98)
	}

	// Just under the threshold: still hidden, not moved.
	for i := 0; i < RehomeFrames-1; i++ {
		if got := d.Layout(w, blocked, 100, 0, false); len(got) != 0 {
			t.Fatalf("frame %d re-homed early to %+v", i, got)
		}
	}
	// Past it: allowed to find a new home.
	got := d.Layout(w, blocked, 100, 0, false)
	if len(got) != 1 {
		t.Fatal("expected a re-home once the slot was unusable for long enough")
	}
	if got[0].Row == first[0].Row {
		t.Error("re-homed to the same blocked row")
	}
}

func TestDockNeverOverlapsWidgets(t *testing.T) {
	d := NewDock()
	rows := make([]string, 60)
	for i := range rows {
		rows[i] = strings.Repeat("x", 10)
	}
	got := d.Layout([]Widget{
		widget(WidgetTodos, 3),
		widget(WidgetContextUsage, 3),
		widget(WidgetModelInfo, 3),
	}, rows, 100, 0, false)

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

func TestLeftWidgetsNeedCenteredMode(t *testing.T) {
	// Only centered mode has a left margin to dock into.
	d := NewDock()
	rows := make([]string, 40)
	for i := range rows {
		rows[i] = strings.Repeat("x", 10)
	}
	left := widget(WidgetBackgroundTasks, 3)
	if left.Kind.PreferredSide() != SideLeft {
		t.Fatal("BackgroundTasks should prefer the left margin")
	}
	if got := d.Layout([]Widget{left}, rows, 100, 0, false); len(got) != 0 {
		t.Errorf("a left widget docked in left-aligned mode: %+v", got)
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
