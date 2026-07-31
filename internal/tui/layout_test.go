package tui

import (
	"testing"
	"time"
)

func stack(available, content int, fixed map[Slot]int) Stack {
	s := Stack{Available: available, ContentHeight: content}
	for slot, h := range fixed {
		s.Heights[slot] = h
	}
	return s
}

func TestPackedLayoutHugsTheComposer(t *testing.T) {
	// The signature trick: while it fits, the transcript is exactly as tall as
	// its content, so a short conversation sits above the composer rather than
	// leaving a dead gutter (plan.md §3.2).
	s := stack(40, 5, map[Slot]int{SlotStatus: 1, SlotComposer: 2})
	got := s.Resolve()
	if got.Scrolling {
		t.Error("content fits; the layout must be packed")
	}
	if got.Transcript != 5 {
		t.Errorf("transcript = %d, want exactly the content height 5", got.Transcript)
	}
	if got.Total() != 8 {
		t.Errorf("total = %d, want 8 — a packed stack must not claim the whole screen", got.Total())
	}
}

func TestOverflowBecomesScrollingViewport(t *testing.T) {
	s := stack(20, 100, map[Slot]int{SlotStatus: 1, SlotComposer: 3})
	got := s.Resolve()
	if !got.Scrolling {
		t.Error("content overflows; the transcript must scroll")
	}
	if got.Transcript != 16 {
		t.Errorf("transcript = %d, want the remaining 16", got.Transcript)
	}
	if got.Total() != 20 {
		t.Errorf("total = %d, want the full 20 once scrolling", got.Total())
	}
}

func TestExactFitStaysPacked(t *testing.T) {
	// Content exactly filling the room is still packed; it only scrolls once
	// it genuinely overflows.
	s := stack(10, 6, map[Slot]int{SlotStatus: 1, SlotComposer: 3})
	got := s.Resolve()
	if got.Scrolling {
		t.Error("an exact fit must not flip to scrolling")
	}
	if got.Transcript != 6 {
		t.Errorf("transcript = %d, want 6", got.Transcript)
	}
}

func TestTranscriptFloorWhenSqueezed(t *testing.T) {
	s := stack(10, 100, map[Slot]int{SlotComposer: 8, SlotStatus: 1})
	got := s.Resolve()
	if got.Transcript != MinScrollingTranscript {
		t.Errorf("transcript = %d, want the floor of %d", got.Transcript, MinScrollingTranscript)
	}
}

func TestTranscriptCollapsesRatherThanEvictingTheComposer(t *testing.T) {
	// Losing history is recoverable; losing the input box is not.
	s := stack(4, 100, map[Slot]int{SlotComposer: 4, SlotStatus: 1})
	got := s.Resolve()
	if got.Transcript != 0 {
		t.Errorf("transcript = %d, want 0 when the fixed rows alone overflow", got.Transcript)
	}
	if got.Heights[SlotComposer] != 4 {
		t.Error("the composer must keep its height")
	}
}

func TestFixedExcludesTranscript(t *testing.T) {
	s := stack(40, 5, map[Slot]int{SlotStatus: 1, SlotComposer: 2, SlotNotice: 1})
	s.Heights[SlotTranscript] = 999 // must be ignored
	if got := s.Fixed(); got != 4 {
		t.Errorf("Fixed() = %d, want 4", got)
	}
}

func TestHorizontalSplit(t *testing.T) {
	tests := []struct {
		name               string
		h                  Horizontal
		wantChat, wantSide int
	}{
		{"closed", Horizontal{Width: 120, SidePaneOpen: false}, 120, 0},
		{"half", Horizontal{Width: 120, SidePaneOpen: true, SidePaneRatio: 50}, 59, 60},
		{"ratio clamps up", Horizontal{Width: 120, SidePaneOpen: true, SidePaneRatio: 5}, 89, 30},
		// Too narrow to give the diff its minimum and keep a usable chat
		// column: drop the pane rather than show half a diff.
		{"too narrow", Horizontal{Width: 45, SidePaneOpen: true, SidePaneRatio: 50}, 45, 0},
		{"zero width", Horizontal{Width: 0, SidePaneOpen: true}, 0, 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			chat, side := tt.h.Split()
			if chat != tt.wantChat || side != tt.wantSide {
				t.Errorf("Split() = %d chat / %d side, want %d / %d",
					chat, side, tt.wantChat, tt.wantSide)
			}
			if side > 0 && chat < MinChatWidth {
				t.Errorf("chat column %d fell below the minimum %d", chat, MinChatWidth)
			}
		})
	}
}

func TestInsetAndContentWidth(t *testing.T) {
	if got := Inset(false); got != LeftInset {
		t.Errorf("left-aligned inset = %d, want %d", got, LeftInset)
	}
	if got := Inset(true); got != 0 {
		t.Errorf("centered inset = %d, want 0 — centering provides its own margins", got)
	}

	w, pad := ContentWidth(120, false)
	if w != 119 || pad != 1 {
		t.Errorf("left-aligned = %d wide, %d pad", w, pad)
	}

	w, pad = ContentWidth(120, true)
	if w != CenteredCap {
		t.Errorf("centered width = %d, want the %d cap", w, CenteredCap)
	}
	if pad != (120-CenteredCap)/2 {
		t.Errorf("centered pad = %d", pad)
	}

	// Narrower than the cap: use it all, no padding.
	w, pad = ContentWidth(40, true)
	if w != 40 || pad != 0 {
		t.Errorf("narrow centered = %d wide, %d pad", w, pad)
	}
}

func TestScrollBasics(t *testing.T) {
	var s Scroll
	if !s.AtBottom() {
		t.Error("a fresh scroll starts pinned")
	}
	if !s.Up(5, 100, 20) {
		t.Error("Up should have moved")
	}
	if s.Offset != 5 || !s.Paused {
		t.Errorf("offset = %d, paused = %v", s.Offset, s.Paused)
	}
	if !s.Down(5) {
		t.Error("Down should have moved")
	}
	if !s.AtBottom() || s.Paused {
		t.Error("returning to the bottom must unpause")
	}
}

func TestScrollDownAtBottomDoesNotAccumulate(t *testing.T) {
	// Without clamping, scrolling down at the bottom banks negative offset
	// that must be undone before scrolling up moves anything — the wheel
	// feels broken for a second.
	var s Scroll
	for i := 0; i < 10; i++ {
		if s.Down(3) {
			t.Fatal("Down at the bottom must report no movement")
		}
	}
	if s.Offset != 0 {
		t.Fatalf("offset = %d, want 0", s.Offset)
	}
	if !s.Up(1, 100, 20) {
		t.Error("one notch up must move immediately, with no phantom offset to undo")
	}
	if s.Offset != 1 {
		t.Errorf("offset = %d, want exactly 1", s.Offset)
	}
}

func TestScrollUpClampsToContent(t *testing.T) {
	var s Scroll
	s.Up(1000, 50, 20)
	if want := Max(50, 20); s.Offset != want {
		t.Errorf("offset = %d, want the limit %d", s.Offset, want)
	}
	if s.Up(10, 50, 20) {
		t.Error("scrolling past the top must report no movement")
	}
}

func TestWheelAcceleration(t *testing.T) {
	var s Scroll
	now := time.Now()

	// A slow first notch is the baseline.
	s.WheelUp(now, 1000, 20)
	if s.Momentum() != 0 {
		t.Errorf("momentum = %d, want the first notch fully applied", s.Momentum())
	}

	// A fast follow-up accelerates and banks the excess.
	s.WheelUp(now.Add(10*time.Millisecond), 1000, 20)
	if s.Momentum() == 0 {
		t.Error("a fast notch should bank momentum for the glide")
	}

	// A slow follow-up does not accelerate.
	var s2 Scroll
	s2.WheelUp(now, 1000, 20)
	s2.WheelUp(now.Add(time.Second), 1000, 20)
	if s2.Momentum() != 0 {
		t.Errorf("momentum = %d; a slow notch must not accelerate", s2.Momentum())
	}
}

func TestMomentumIsCapped(t *testing.T) {
	var s Scroll
	now := time.Now()
	for i := 0; i < 100; i++ {
		s.WheelUp(now.Add(time.Duration(i)*time.Millisecond), 100000, 20)
	}
	if s.Momentum() > MaxMomentum {
		t.Errorf("momentum = %d, want at most %d — a frantic scroll must not bank seconds of drift",
			s.Momentum(), MaxMomentum)
	}
}

func TestDrainStepEasesOut(t *testing.T) {
	tests := []struct{ queued, want int }{
		{0, 0}, {1, 1}, {2, 1}, {3, 2}, {5, 2}, {6, 3}, {30, 3},
	}
	for _, tt := range tests {
		if got := DrainStep(tt.queued); got != tt.want {
			t.Errorf("DrainStep(%d) = %d, want %d", tt.queued, got, tt.want)
		}
	}
	// The steps must be non-increasing as the queue empties — that is the
	// ease-out.
	prev := DrainStep(30)
	for q := 30; q >= 0; q-- {
		got := DrainStep(q)
		if got > prev {
			t.Errorf("DrainStep grew from %d to %d as the queue emptied", prev, got)
		}
		prev = got
	}
}

func TestResetMomentumOnTargetSwitch(t *testing.T) {
	var s Scroll
	now := time.Now()
	s.WheelUp(now, 1000, 20)
	s.WheelUp(now.Add(5*time.Millisecond), 1000, 20)
	if s.Momentum() == 0 {
		t.Fatal("expected banked momentum")
	}
	s.ResetMomentum()
	if s.Momentum() != 0 {
		t.Error("switching scroll targets must zero the queue")
	}
}

func TestFollowBottom(t *testing.T) {
	var s Scroll
	s.Up(10, 100, 20)
	s.FollowBottom()
	if !s.AtBottom() || s.Paused || s.Momentum() != 0 {
		t.Errorf("FollowBottom left offset=%d paused=%v momentum=%d",
			s.Offset, s.Paused, s.Momentum())
	}
	if !s.TakeSnapPending() {
		t.Error("FollowBottom must request an exact snap")
	}
	if s.TakeSnapPending() {
		t.Error("the snap flag must be consumed")
	}
}

func TestBookmark(t *testing.T) {
	var s Scroll

	// At the bottom with nothing saved, the key is a no-op rather than
	// inventing a bookmark.
	if got := s.ToggleBookmark(); got != "" {
		t.Errorf("notice = %q, want none", got)
	}

	s.Up(25, 200, 20)
	notice := s.ToggleBookmark()
	if notice != "📌 Bookmark set - press again to return" {
		t.Errorf("notice = %q", notice)
	}
	if !s.AtBottom() {
		t.Error("setting a bookmark must jump to the bottom")
	}
	if !s.HasBookmark() {
		t.Error("bookmark should be set")
	}

	notice = s.ToggleBookmark()
	if notice != "📌 Returned to bookmark" {
		t.Errorf("notice = %q", notice)
	}
	if s.Offset != 25 {
		t.Errorf("offset = %d, want the saved 25", s.Offset)
	}
	if s.HasBookmark() {
		t.Error("returning must clear the bookmark")
	}
}

func TestTailCatchup(t *testing.T) {
	tests := []struct {
		name                 string
		lag, viewport        int
		snapPending, animate bool
		want                 int
	}{
		{"no lag", 0, 20, false, true, 0},
		// Paced streaming produces small jumps that should simply snap.
		{"small jump snaps", 3, 20, false, true, 0},
		// A committed message is a big jump and must slide.
		{"big jump slides", 12, 20, false, true, 9},
		// An explicit action lands exactly.
		{"snap pending", 12, 20, true, true, 0},
		// With animation off there is nothing to animate toward.
		{"animation off", 12, 20, false, false, 0},
		// A giant paste cannot leave the tail arbitrarily behind.
		{"lag capped to a viewport", 500, 20, false, true, 17},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := TailCatchup(tt.lag, tt.viewport, tt.snapPending, tt.animate)
			if got != tt.want {
				t.Errorf("TailCatchup(%d, %d, %v, %v) = %d, want %d",
					tt.lag, tt.viewport, tt.snapPending, tt.animate, got, tt.want)
			}
		})
	}
}

func TestTailCatchupConverges(t *testing.T) {
	// However far behind it starts, the slide must reach zero.
	lag := 200
	for i := 0; i < 1000 && lag > 0; i++ {
		next := TailCatchup(lag, 20, false, true)
		if next >= lag && lag > 0 {
			t.Fatalf("lag did not shrink: %d -> %d", lag, next)
		}
		lag = next
	}
	if lag != 0 {
		t.Errorf("lag settled at %d, want 0", lag)
	}
}

func TestScrollbarHysteresis(t *testing.T) {
	// The decision feeds back into layout: a visible bar narrows the wrap
	// width, changing the content height, changing the decision. Without
	// hysteresis this oscillates forever (plan.md §3.6).
	tests := []struct {
		name                          string
		prev                          bool
		withBar, withoutBar, viewport int
		want                          bool
	}{
		{"fits either way", false, 10, 10, 20, false},
		{"overflows either way", false, 40, 38, 20, true},
		{"stays hidden when hiding makes it fit", false, 21, 20, 20, false},
		// The oscillating case: showing the bar pushes it over, hiding it
		// brings it back. The wide layout wins.
		{"showing, but hiding would fit", true, 21, 20, 20, false},
		{"showing and still overflows", true, 30, 25, 20, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ScrollbarVisible(tt.prev, tt.withBar, tt.withoutBar, tt.viewport)
			if got != tt.want {
				t.Errorf("ScrollbarVisible(%v, %d, %d, %d) = %v, want %v",
					tt.prev, tt.withBar, tt.withoutBar, tt.viewport, got, tt.want)
			}
		})
	}
}

func TestScrollbarDecisionIsStable(t *testing.T) {
	// Feed the decision back into itself the way the frame loop does; it must
	// reach a fixed point rather than flipping every frame.
	visible := false
	const withBar, withoutBar, viewport = 21, 20, 20
	seen := []bool{}
	for i := 0; i < 10; i++ {
		visible = ScrollbarVisible(visible, withBar, withoutBar, viewport)
		seen = append(seen, visible)
	}
	for i := 1; i < len(seen); i++ {
		if seen[i] != seen[i-1] {
			t.Fatalf("scrollbar visibility oscillated: %v", seen)
		}
	}
}
