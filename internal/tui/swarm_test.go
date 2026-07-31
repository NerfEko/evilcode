package tui

import (
	"strings"
	"testing"
	"time"
)

func TestSwarmWidgetShowsEachAgent(t *testing.T) {
	r := testRenderer(80)
	s := &SwarmState{}
	s.Publish([]SwarmAgent{
		{Name: "bat", Task: "wiring auth", Worker: true, Running: true, Since: 42 * time.Second},
		{Name: "crypt", Task: "palette work", Running: false, Since: 3 * time.Minute},
	})

	joined := strings.Join(plainLines(r.SwarmStatusWidget(s, 0).Lines), "\n")
	for _, want := range []string{"bat", "wiring auth", "42s", "crypt", "3m"} {
		if !strings.Contains(joined, want) {
			t.Errorf("widget is missing %q:\n%s", want, joined)
		}
	}
	// A finished agent gets a tick, not a spinner: §10's rule is that content
	// being read never animates.
	if !strings.Contains(joined, "✓") {
		t.Errorf("an idle agent should not be spinning:\n%s", joined)
	}
}

func TestSwarmWidgetAbsentWhenAlone(t *testing.T) {
	r := testRenderer(80)
	if w := r.SwarmStatusWidget(&SwarmState{}, 0); len(w.Lines) != 0 {
		t.Errorf("an empty swarm drew %d lines", len(w.Lines))
	}
	if w := r.SwarmStatusWidget(nil, 0); len(w.Lines) != 0 {
		t.Error("a nil swarm drew something")
	}
}

func TestSwarmStripNamesLiveAgentsOnly(t *testing.T) {
	r := testRenderer(80)
	s := &SwarmState{}
	s.Publish([]SwarmAgent{
		{Name: "bat", Running: true},
		{Name: "crypt", Running: false},
	})
	strip := plain(r.RenderSwarmStrip(s, 0))
	if !strings.Contains(strip, "1 agent") || !strings.Contains(strip, "bat") {
		t.Errorf("strip = %q", strip)
	}
	if strings.Contains(strip, "crypt") {
		t.Errorf("a finished agent is in the live strip: %q", strip)
	}
}

func TestSwarmStripSilentWhenNothingRuns(t *testing.T) {
	r := testRenderer(80)
	s := &SwarmState{}
	s.Publish([]SwarmAgent{{Name: "bat", Running: false}})
	if got := r.RenderSwarmStrip(s, 0); got != "" {
		t.Errorf("strip = %q, want nothing", got)
	}
}

func TestStripStandsDownOnlyAfterTheWidgetHolds(t *testing.T) {
	// The hysteresis is the whole point: the strip must not vanish the instant
	// the widget appears, or the two flicker against each other every time a
	// wide line slides under the dock.
	s := &SwarmState{}
	now := time.Now()

	if !s.ObserveDock(false, now) {
		t.Fatal("the strip should start visible")
	}
	// Widget appears, but not for long enough yet.
	if !s.ObserveDock(true, now) {
		t.Error("the strip stood down immediately")
	}
	if !s.ObserveDock(true, now.Add(StandDownDelay/2)) {
		t.Error("the strip stood down before the delay elapsed")
	}
	if s.ObserveDock(true, now.Add(StandDownDelay)) {
		t.Error("the strip should have stood down once the widget held")
	}
}

func TestStripReturnsOnlyAfterTheWidgetStaysGone(t *testing.T) {
	s := &SwarmState{}
	now := time.Now()
	s.ObserveDock(true, now)
	s.ObserveDock(true, now.Add(StandDownDelay))
	if s.StripVisible() {
		t.Fatal("setup: the strip should be stood down")
	}

	// The widget briefly loses its slot. The strip must not snap back.
	if s.ObserveDock(false, now.Add(StandDownDelay)) {
		t.Error("the strip returned the instant the widget was covered")
	}
	if !s.ObserveDock(false, now.Add(2*StandDownDelay)) {
		t.Error("the strip should return once the widget has stayed gone")
	}
}

func TestStripHoldsThroughAFlicker(t *testing.T) {
	// A widget that appears and disappears faster than the delay must leave
	// the strip exactly where it was.
	s := &SwarmState{}
	now := time.Now()
	for i := 0; i < 10; i++ {
		s.ObserveDock(i%2 == 0, now.Add(time.Duration(i)*StandDownDelay/4))
	}
	if !s.StripVisible() {
		t.Error("a flickering widget stood the strip down")
	}
}

func TestSwarmRosterIsCopied(t *testing.T) {
	// Agents() hands out a copy: the roster poller replaces the slice from
	// another goroutine while the render loop is walking it.
	s := &SwarmState{}
	s.Publish([]SwarmAgent{{Name: "bat"}})
	got := s.Agents()
	got[0].Name = "mutated"
	if s.Agents()[0].Name != "bat" {
		t.Error("the caller mutated the roster through the returned slice")
	}
}

func TestNilSwarmStateIsInert(t *testing.T) {
	// A solo session has no swarm, and every path has to survive that.
	var s *SwarmState
	if s.Agents() != nil {
		t.Error("a nil swarm listed agents")
	}
	if s.StripVisible() {
		t.Error("a nil swarm wants a strip")
	}
}
