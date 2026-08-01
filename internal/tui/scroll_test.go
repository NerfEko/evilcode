package tui

import "testing"

// observe at a viewport tall enough that the cap does not interfere with the
// behaviour under test.
func (s *Scroll) Observe2(h int) { s.Observe(h, 200) }

func TestSlackHoldsTheGapWhenContentShrinks(t *testing.T) {
	// A thinking trace collapsing from nine lines to one removes those lines at
	// the instant the answer starts. Following the tail exactly would haul the
	// whole conversation back down the screen to close the gap — a jump in the
	// opposite direction from the one the reader was already tracking, right as
	// text starts arriving.
	var s Scroll
	s.Observe2(100)
	s.Observe2(92)

	if got := s.Slack(); got != 8 {
		t.Errorf("slack = %d, want the 8 lines the collapse removed", got)
	}
}

func TestSlackIsSpentByNewContent(t *testing.T) {
	// The gap is filled before scrolling resumes, so the view only ever moves
	// one way.
	var s Scroll
	s.Observe2(100)
	s.Observe2(92)

	s.Observe2(95) // three lines of answer arrive
	if got := s.Slack(); got != 5 {
		t.Errorf("slack = %d, want 5 after three lines filled it", got)
	}
	s.Observe2(120) // the rest of the answer overruns the gap
	if got := s.Slack(); got != 0 {
		t.Errorf("slack = %d, want it fully spent", got)
	}
}

func TestSlackIsNotHeldWhileScrolledUp(t *testing.T) {
	// A reader who has scrolled up is anchored to content rather than to the
	// bottom, so there is no downward haul to prevent and a held gap would just
	// be a hole in their view.
	var s Scroll
	s.Paused = true
	s.Observe2(100)
	s.Observe2(92)

	if got := s.Slack(); got != 0 {
		t.Errorf("slack = %d, want none while paused", got)
	}
}

func TestSlackNeverGoesNegative(t *testing.T) {
	var s Scroll
	s.Observe2(100)
	s.Observe2(99)
	s.Observe2(500)
	if got := s.Slack(); got != 0 {
		t.Errorf("slack = %d, want it floored at zero", got)
	}
}

func TestClearSlackResetsForARepaint(t *testing.T) {
	// A clear, a compaction or a theme change repaints from scratch; holding a
	// gap from the old content would be meaningless.
	var s Scroll
	s.Observe2(100)
	s.Observe2(80)
	s.ClearSlack()
	if got := s.Slack(); got != 0 {
		t.Errorf("slack = %d after a reset", got)
	}
	// And it re-baselines rather than treating the next height as a shrink.
	s.Observe2(50)
	if got := s.Slack(); got != 0 {
		t.Errorf("slack = %d, want the next height taken as the new baseline", got)
	}
}

func TestSlackHoldsOneCollapseNotTheirSum(t *testing.T) {
	// A turn can collapse several traces — think, call a tool, think again.
	// Summing them grew the gap past anything a reply could fill, which is the
	// "giant blank space after it finishes" report. One trace's worth is what
	// the answer is expected to fill, and does.
	var s Scroll
	s.Observe(1000, 60)
	s.Observe(994, 60) // a six-line trace collapses
	s.Observe(988, 60) // and another
	s.Observe(982, 60) // and another

	if got := s.Slack(); got != 6 {
		t.Errorf("slack = %d, want the largest single collapse, not the sum", got)
	}
}

func TestSlackIsCappedWellBelowTheViewport(t *testing.T) {
	// Backstop: the gap is scaffolding, so anything approaching half the screen
	// is a hole rather than breathing room.
	var s Scroll
	s.Observe(1000, 21)
	s.Observe(900, 21)
	if got := s.Slack(); got > 7 {
		t.Errorf("slack = %d, want it capped for a 21-row viewport", got)
	}
}

// TestSlackIsDroppedOnceTheReaderScrollsUp: slack is scaffolding for a reply
// that is still arriving. A reader who has scrolled up is anchored to content,
// so holding the gap left a hole below the text — the "big empty space after a
// turn" — and shifted the window forward past the oldest lines.
func TestSlackIsDroppedOnceTheReaderScrollsUp(t *testing.T) {
	var s Scroll
	s.Observe(100, 40)
	s.Observe(85, 40) // a thinking trace collapsed
	if s.Slack() == 0 {
		t.Fatal("no slack after a shrink; the case under test never set up")
	}
	s.Up(5, 85, 40)
	s.Observe(85, 40)
	if s.Slack() != 0 {
		t.Errorf("slack = %d while scrolled up, want 0", s.Slack())
	}
}
