package todo

import (
	"fmt"
	"sort"
	"strings"
)

// Kind classifies a gate observation.
type Kind string

const (
	KindIntent Kind = "intent"
	KindLoop   Kind = "loop"
)

// Observation records one qualifying write's score. Observations accumulate
// through a turn and are collapsed into a digest at turn end.
//
// Low scores deliberately do NOT nag per write: that punishes the healthy
// low-then-rising pattern and burns the model's reasoning re-justifying a plan
// it is still forming (plan.md §12.3).
type Observation struct {
	Kind  Kind   `json:"kind"`
	Group string `json:"group,omitempty"`
	Score uint8  `json:"score"`
}

// observe appends an observation, dropping the oldest past the cap.
func (s *Store) observe(o Observation) {
	s.obs = append(s.obs, o)
	if len(s.obs) > MaxObservations {
		s.obs = s.obs[len(s.obs)-MaxObservations:]
	}
}

// Observations returns the turn's accumulated observations.
func (s *Store) Observations() []Observation {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]Observation(nil), s.obs...)
}

// ClearObservations resets the turn-scoped set, which happens once a digest has
// been delivered.
func (s *Store) ClearObservations() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.obs = nil
	return s.save()
}

// DigestPrefix labels the digest as harness output rather than a user message,
// and tells the model not to answer it conversationally. Without the second
// half models reply to the reminder instead of acting on it.
const DigestPrefix = "[automated todo quality review - not a user message] " +
	"Before you treat this turn as finished, double-check the weak points it surfaced. " +
	"Do not reply conversationally or wait for the user."

// BuildDigest collapses the turn's observations into one message, choosing
// wording by trajectory (plan.md §12.3).
//
// The distinction that matters is *when* a score cleared, not merely whether it
// did: work done before a loop closed was never covered by that loop, and
// saying so is the difference between a useful nudge and a generic one.
func BuildDigest(obs []Observation, plan Plan, goals []Goal) string {
	if len(obs) == 0 {
		return ""
	}

	var lines []string

	if intents := filterKind(obs, KindIntent); len(intents) > 0 {
		cleared := plan.UnderstandsUserIntent != nil && *plan.UnderstandsUserIntent >= QualityGate
		count := len(intents)
		if cleared {
			lines = append(lines, withCount(
				"you started this work without understanding what the user actually wants, and only "+
					"settled it later. Re-check the work you did before it settled against the request "+
					"you now understand.", count))
		} else {
			lines = append(lines, withCount(
				"your understanding of what the user actually wants never became solid. Re-read the "+
					"request, confirm the work you did matches it, and state any interpretation you had "+
					"to guess at.", count))
		}
	}

	// One line per group, in a stable order so the digest reads the same way
	// twice.
	byGroup := map[string]int{}
	var groupOrder []string
	for _, o := range obs {
		if o.Kind != KindLoop {
			continue
		}
		if _, seen := byGroup[o.Group]; !seen {
			groupOrder = append(groupOrder, o.Group)
		}
		byGroup[o.Group]++
	}
	sort.Strings(groupOrder)

	for _, group := range groupOrder {
		cleared := false
		for _, g := range goals {
			if g.Group == group && g.ClosedFeedbackLoop != nil && *g.ClosedFeedbackLoop >= QualityGate {
				cleared = true
			}
		}
		label := ""
		if group != "" {
			label = fmt.Sprintf(" for %q", group)
		}
		if cleared {
			lines = append(lines, withCount(fmt.Sprintf(
				"the goal%s was worked on before its feedback loop was closed, so the loop you ended up "+
					"with never ran over that earlier work. Run it over the whole result now and report "+
					"what it actually reported back.", label), byGroup[group]))
		} else {
			lines = append(lines, withCount(fmt.Sprintf(
				"the goal%s never closed its feedback loop: no observation reported back on whether the "+
					"work satisfied the requirements. Confirm the result is actually better, with concrete "+
					"evidence rather than inspection.", label), byGroup[group]))
		}
	}

	if len(lines) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString(DigestPrefix)
	for _, l := range lines {
		b.WriteString("\n\n- ")
		b.WriteString(l)
	}
	return b.String()
}

// withCount appends a repeat count so a digest says how often something
// happened without repeating the sentence.
func withCount(s string, n int) string {
	if n <= 1 {
		return s
	}
	return fmt.Sprintf("%s (observed %d times)", s, n)
}

func filterKind(obs []Observation, k Kind) []Observation {
	var out []Observation
	for _, o := range obs {
		if o.Kind == k {
			out = append(out, o)
		}
	}
	return out
}

// WeakCompletion reports whether the finished work is under-validated, and why.
//
// Three independent signals, any of which is enough: the weighted average is
// below the gate, some item never recorded a completion confidence at all, or a
// single item is below the gate on its own. An average alone would let one
// unverified item hide behind several confident ones.
func WeakCompletion(items []Item) (bool, string) {
	var scored int
	for _, item := range items {
		if item.Status == StatusCancelled {
			continue
		}
		if item.CompletionConfidence == nil {
			return true, fmt.Sprintf("%q was marked done without recording how confident you are it works",
				item.Content)
		}
		scored++
		if int(*item.CompletionConfidence) < QualityGate {
			return true, fmt.Sprintf("%q is only %d%% confident it works",
				item.Content, *item.CompletionConfidence)
		}
	}
	if scored == 0 {
		return false, ""
	}
	if avg, ok := AggregateConfidence(items); ok && avg < QualityGate {
		return true, fmt.Sprintf("the weighted average completion confidence is %d%%", avg)
	}
	return false, ""
}

// Spikes returns the completed items whose confidence jumped suspiciously.
func Spikes(items []Item) []Item {
	var out []Item
	for _, item := range items {
		if IsSpike(item) {
			out = append(out, item)
		}
	}
	return out
}
