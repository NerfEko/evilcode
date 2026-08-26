package todo

import (
	"strings"
	"testing"
)

// TestPhase2Verification drives the §12 behaviors the plan's Phase 2 exit
// criteria name, end to end against a real store rather than in pieces.
func TestPhase2Verification(t *testing.T) {
	t.Run("3-item task pokes on an early stop", func(t *testing.T) {
		s := newStore(t)
		s.Apply(Write{Items: []Item{
			item("a", "read the handler", StatusCompleted, withDone(100)),
			item("b", "wire the refresh path", StatusPending),
			item("c", "add the retry gate", StatusPending),
		}})

		var st State
		got := Decide(Inputs{Items: s.Items()}, &st)
		if got.Decision != Continue {
			t.Fatalf("stopping with 2 open todos should poke, got %+v", got)
		}
		if !strings.Contains(got.SystemLine, "2 incomplete todos") {
			t.Errorf("system line = %q", got.SystemLine)
		}
	})

	t.Run("low loop score produces a digest at turn end", func(t *testing.T) {
		s := newStore(t)
		s.Apply(Write{
			Items: []Item{item("a", "task", StatusCompleted, withGroup("auth"), withDone(100))},
			Goals: []Goal{{Group: "auth", ClosedFeedbackLoop: u8(40),
				EndToEndOwnership: u8(QualityGate)}},
		})

		var st State
		got := Decide(Inputs{
			Items: s.Items(), Plan: s.Plan(), Goals: s.Goals(),
			Observations: s.Observations(),
		}, &st)
		if got.Decision != Continue {
			t.Fatalf("a low loop score should produce a digest, got %+v", got)
		}
		if !strings.Contains(got.Queued, "feedback loop") {
			t.Errorf("digest = %q", got.Queued)
		}
	})

	t.Run("the arrow shows a bulk end-stamp", func(t *testing.T) {
		s := newStore(t)
		s.Apply(Write{Items: []Item{item("a", "task", StatusInProgress, withGroup("auth"), withConf(75))}})
		s.Apply(Write{
			Items: []Item{
				item("a", "task", StatusCompleted, withGroup("auth"), withConf(100), withDone(100)),
			},
			Goals: []Goal{{Group: "auth", EndToEndOwnership: u8(QualityGate)}},
		})

		got := s.Items()[0]
		if label := ArrowLabel(got.Confidence, got.CompletionConfidence); label == "" {
			t.Error("a completed item should carry a confidence label")
		}
		if !IsSpike(got) {
			t.Error("75 → 100 in one step is the spike the arrow exists to expose")
		}
	})

	t.Run("the ownership gate rejects a premature group close", func(t *testing.T) {
		s := newStore(t)
		s.Apply(Write{
			Items: []Item{item("a", "task", StatusInProgress, withGroup("auth"))},
		})
		res, _ := s.Apply(Write{
			Items: []Item{item("a", "task", StatusCompleted, withGroup("auth"), withDone(100))},
			Goals: []Goal{{Group: "auth", EndToEndOwnership: u8(50)}},
		})
		if !res.Rejected {
			t.Fatal("completing a group without end-to-end ownership must be rejected")
		}
		if s.Items()[0].Status != StatusInProgress {
			t.Error("a rejected write must leave the stored list untouched")
		}
	})
}
