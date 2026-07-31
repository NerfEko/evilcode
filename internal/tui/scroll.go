package tui

import "time"

// Scroll feel constants (plan.md §4.1–4.3). Every one of these is a number the
// hand notices; they are not arbitrary.
const (
	// LinesPerNotch is the baseline wheel step.
	LinesPerNotch = 3

	// FastNotchGap is the inter-notch interval below which the wheel is
	// treated as accelerating. Terminals do not report velocity, so it has to
	// be inferred from timing.
	FastNotchGap = 30 * time.Millisecond

	// FastMultiplier and MaxLinesPerNotch bound the acceleration.
	FastMultiplier   = 2
	MaxLinesPerNotch = 5

	// MaxMomentum caps the queue, so a frantic scroll cannot bank seconds of
	// drift the user then has to wait out.
	MaxMomentum = 30

	// PageLines is PageUp/PageDown.
	PageLines = 10

	// TailCatchupThreshold is the jump size below which the view simply snaps.
	// Paced streaming lands here and should not be animated.
	TailCatchupThreshold = 4

	// TailCatchupPerFrame limits how fast the view slides to a large append,
	// which is what kills "the big pop".
	TailCatchupPerFrame = 3
)

// Scroll holds the transcript's scroll state. Offset counts lines from the
// bottom, so zero is pinned to the newest content.
type Scroll struct {
	Offset int

	// Paused is set when the reader has scrolled up. Auto-follow stops, and
	// nothing may delete content out from under them (plan.md §4.6).
	Paused bool

	// momentum is the queue of lines still owed to a wheel gesture.
	momentum int

	// lastNotch times the previous wheel event, for velocity inference.
	lastNotch time.Time

	// snapPending marks an explicit user action — submit, Esc, a jump key —
	// which lands exactly at the bottom instead of animating there.
	snapPending bool

	// bookmark holds a saved position for Ctrl+G, and whether one is set.
	bookmark    int
	hasBookmark bool

	// slack holds the height the transcript has lost but not yet given back.
	//
	// A collapsing thinking trace removes eight or nine lines at the instant the
	// answer starts. Following the tail exactly would haul the whole
	// conversation back *down* the screen to close the gap — a jump in the
	// opposite direction from the one the reader was already tracking, right as
	// text starts arriving. Instead the gap is kept as empty space below the
	// text and spent as new content arrives, so the view only ever moves one
	// way (plan.md invariant 4: prefer "stays put").
	slack int

	// lastHeight is the content height last observed, for detecting a shrink.
	lastHeight int
}

// Observe records the transcript's height, converting any shrink into slack.
//
// Only while following the tail: a reader who has scrolled up is anchored to
// content rather than to the bottom, so there is no downward haul to prevent
// and holding a gap would just be a hole in their view.
func (s *Scroll) Observe(contentHeight, viewportHeight int) {
	if s.lastHeight == 0 {
		s.lastHeight = contentHeight
		return
	}
	switch {
	case contentHeight < s.lastHeight && !s.Paused:
		s.slack += s.lastHeight - contentHeight
	case contentHeight > s.lastHeight:
		// New content spends the gap before it starts scrolling again.
		if s.slack > 0 {
			s.slack = max(s.slack-(contentHeight-s.lastHeight), 0)
		}
	}
	// Capped at half the viewport. Shrinks accumulate across turns, and an
	// uncapped gap eventually scrolls the conversation off the top entirely —
	// a blank screen is not "a little breathing room".
	s.slack = min(s.slack, max(viewportHeight/2, 0))
	s.lastHeight = contentHeight
}

// Slack is the unclaimed height held below the text.
func (s *Scroll) Slack() int { return s.slack }

// ClearSlack drops the gap outright, for anything that repaints from scratch —
// a clear, a compaction, a theme change.
func (s *Scroll) ClearSlack() { s.slack, s.lastHeight = 0, 0 }

// Max is the largest valid offset for the given content and viewport heights.
func Max(contentHeight, viewportHeight int) int {
	return max(contentHeight-viewportHeight, 0)
}

// FollowBottom pins to the newest content and clears the pause, requesting an
// exact snap rather than an animated slide.
func (s *Scroll) FollowBottom() {
	s.Offset = 0
	s.Paused = false
	s.momentum = 0
	s.snapPending = true
}

// AtBottom reports whether the view is pinned.
func (s *Scroll) AtBottom() bool { return s.Offset == 0 }

// TakeSnapPending consumes the snap flag.
func (s *Scroll) TakeSnapPending() bool {
	pending := s.snapPending
	s.snapPending = false
	return pending
}

// RequestSnap marks the next update as an explicit user action.
func (s *Scroll) RequestSnap() { s.snapPending = true }

// Up scrolls back through history by n lines and reports whether the position
// actually changed.
func (s *Scroll) Up(n, contentHeight, viewportHeight int) bool {
	limit := Max(contentHeight, viewportHeight)
	before := s.Offset
	s.Offset = min(s.Offset+n, limit)
	if s.Offset > 0 {
		s.Paused = true
	}
	return s.Offset != before
}

// Down scrolls toward the newest content by n lines and reports whether the
// position actually changed.
//
// Clamping at zero is load-bearing: without it, scrolling down at the bottom
// silently accumulates negative offset that has to be undone before scrolling
// up moves anything, which feels like the wheel is broken.
func (s *Scroll) Down(n int) bool {
	before := s.Offset
	s.Offset = max(s.Offset-n, 0)
	if s.Offset == 0 {
		s.Paused = false
	}
	return s.Offset != before
}

// WheelUp handles one wheel notch away from the bottom, returning the lines
// applied immediately. The rest drains over subsequent frames, which is the
// glide.
func (s *Scroll) WheelUp(now time.Time, contentHeight, viewportHeight int) int {
	lines := s.notchSize(now)
	s.momentum = min(s.momentum+lines, MaxMomentum)

	// One notch's worth lands immediately so the wheel feels connected; the
	// remainder eases out.
	immediate := min(LinesPerNotch, s.momentum)
	s.momentum -= immediate
	s.Up(immediate, contentHeight, viewportHeight)
	return immediate
}

// WheelDown handles one wheel notch toward the bottom.
func (s *Scroll) WheelDown(now time.Time) int {
	lines := s.notchSize(now)
	s.momentum = min(s.momentum+lines, MaxMomentum)
	immediate := min(LinesPerNotch, s.momentum)
	s.momentum -= immediate
	s.Down(immediate)
	return immediate
}

// notchSize infers acceleration from the gap since the last notch.
func (s *Scroll) notchSize(now time.Time) int {
	lines := LinesPerNotch
	if !s.lastNotch.IsZero() && now.Sub(s.lastNotch) <= FastNotchGap {
		lines = min(lines*FastMultiplier, MaxLinesPerNotch)
	}
	s.lastNotch = now
	return lines
}

// DrainStep returns how many lines to move this frame while draining momentum.
// The tiering is the ease-out: fast while there is a lot left, slowing as the
// queue empties.
func DrainStep(queued int) int {
	switch {
	case queued >= 6:
		return 3
	case queued >= 3:
		return 2
	case queued > 0:
		return 1
	default:
		return 0
	}
}

// Momentum reports the lines still queued.
func (s *Scroll) Momentum() int { return s.momentum }

// ResetMomentum zeroes the queue. Switching scroll targets — chat to help
// overlay to picker preview — must do this, or the new target inherits a
// gesture aimed at the old one.
func (s *Scroll) ResetMomentum() { s.momentum = 0 }

// Drain advances one frame of queued momentum, in the given direction, and
// reports whether anything moved.
func (s *Scroll) Drain(up bool, contentHeight, viewportHeight int) bool {
	step := DrainStep(s.momentum)
	if step == 0 {
		return false
	}
	s.momentum -= step
	if up {
		return s.Up(step, contentHeight, viewportHeight)
	}
	return s.Down(step)
}

// ToggleBookmark implements Ctrl+G (plan.md §4.3). It returns the notice to
// show, or an empty string when the key is a no-op.
func (s *Scroll) ToggleBookmark() string {
	switch {
	case s.hasBookmark:
		s.Offset = s.bookmark
		s.Paused = s.Offset > 0
		s.hasBookmark = false
		s.snapPending = true
		return "📌 Returned to bookmark"

	case s.Offset > 0:
		s.bookmark = s.Offset
		s.hasBookmark = true
		s.FollowBottom()
		return "📌 Bookmark set - press again to return"

	default:
		// At the bottom with nothing saved there is nothing to do, and
		// inventing a bookmark here would surprise.
		return ""
	}
}

// HasBookmark reports whether a position is saved.
func (s *Scroll) HasBookmark() bool { return s.hasBookmark }

// TailCatchup returns the offset to render this frame while the view is
// catching up to newly appended content (plan.md §4.2).
//
// A small jump snaps; a large one slides at a bounded rate so a committed
// message or tool result does not teleport the view. The lag is capped at one
// viewport so a giant paste cannot leave the tail arbitrarily behind.
func TailCatchup(currentLag, viewportHeight int, snapPending, animate bool) int {
	if currentLag <= 0 {
		return 0
	}
	// An explicit action lands exactly, and with animation off there is
	// nothing to animate toward.
	if snapPending || !animate {
		return 0
	}
	if currentLag < TailCatchupThreshold {
		return 0
	}
	lag := min(currentLag, max(viewportHeight, 1))
	return max(lag-TailCatchupPerFrame, 0)
}

// Overscroll timings (plan.md §4.4).
const (
	// OverscrollDwell is how long the facts line lingers after the last tick.
	OverscrollDwell = 1500 * time.Millisecond

	// OverscrollGesture is the gap that separates one flick from the next. It
	// must exceed the idle redraw cadence, or a single flick gets split into
	// two gestures and the reveal never triggers.
	OverscrollGesture = 500 * time.Millisecond
)

// OverscrollMode is the config setting for the pull-to-reveal facts line.
type OverscrollMode string

const (
	// OverscrollOff never reveals.
	OverscrollOff OverscrollMode = "off"

	// OverscrollAlways pins the facts line whenever the view is at the bottom.
	OverscrollAlways OverscrollMode = "on"

	// OverscrollPull is the default: reveal on a deliberate downward flick.
	OverscrollPull OverscrollMode = "overscroll"
)

// Overscroll tracks the elastic pull-to-reveal gesture.
//
// The rule that makes it feel intentional rather than twitchy: the gesture must
// have *begun* at the bottom. Momentum that merely arrives at the bottom is
// swallowed, so scrolling down through a long transcript does not flash the
// facts line at the end of every scroll (plan.md §4.4).
type Overscroll struct {
	Mode OverscrollMode

	revealUntil time.Time
	lastTick    time.Time
	beganAtEnd  bool
}

// Tick records a downward wheel notch. atBottom is whether the view was already
// pinned when this notch arrived.
func (o *Overscroll) Tick(now time.Time, atBottom bool) {
	if o.Mode == OverscrollOff {
		return
	}
	// A long enough pause starts a new gesture.
	if now.Sub(o.lastTick) > OverscrollGesture {
		o.beganAtEnd = atBottom
	}
	o.lastTick = now
	if o.beganAtEnd && atBottom {
		o.revealUntil = now.Add(OverscrollDwell)
	}
}

// Cancel hides the facts line immediately, which scrolling up does.
func (o *Overscroll) Cancel() {
	o.revealUntil = time.Time{}
	o.beganAtEnd = false
}

// Visible reports whether the facts line should show.
func (o *Overscroll) Visible(now time.Time, atBottom bool) bool {
	switch o.Mode {
	case OverscrollOff:
		return false
	case OverscrollAlways:
		return atBottom
	default:
		return now.Before(o.revealUntil)
	}
}

// Remaining is the countdown shown beside the facts, in seconds.
func (o *Overscroll) Remaining(now time.Time) float64 {
	if !now.Before(o.revealUntil) {
		return 0
	}
	return o.revealUntil.Sub(now).Seconds()
}

// ScrollbarVisible decides whether the transcript needs a scrollbar, with the
// hysteresis of plan.md §3.6.
//
// The decision feeds back into layout — a visible bar narrows the wrap width,
// which changes the content height, which can change the decision — so it is
// resolved against the *previous* frame's answer. If hiding the bar would make
// the content fit, the wide no-bar layout wins. Without this the layout
// oscillates between two states forever.
func ScrollbarVisible(prevVisible bool, heightWithBar, heightWithoutBar, viewport int) bool {
	fitsWithout := heightWithoutBar <= viewport
	if fitsWithout {
		return false
	}
	if !prevVisible {
		// It did not fit without the bar, so show it.
		return heightWithBar > viewport || heightWithoutBar > viewport
	}
	// Already showing: keep it unless the content genuinely fits either way.
	return heightWithBar > viewport
}
