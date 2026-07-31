package todo

import (
	"fmt"
	"strings"
)

// Poke limits (plan.md §12.4, §12.6). Every auto-continuation path needs a
// breaker; each of these exists because the unguarded version caused a real
// infinite loop somewhere.
const (
	// MaxGateAttempts is how many times the harness will nudge without the
	// validation improving before it gives up and tells the user.
	MaxGateAttempts = 3

	// MaxConsecutiveRefusals stops re-poking a model that keeps refusing. A
	// refusal is deterministic for the same request, so an unguarded retry
	// loops forever.
	MaxConsecutiveRefusals = 2

	// MaxStalledPokes bounds "you still have incomplete todos" nudges that
	// change nothing. The spec resets the gate counter on open todos, on the
	// grounds that open todos mean the model is still iterating — but that is
	// only true if the list actually moves. A model that answers the nudge
	// without touching the list otherwise loops until the step cap catches it,
	// which is the exact failure §12.6 exists to prevent.
	MaxStalledPokes = 3
)

// Decision is what the post-turn hook should do.
type Decision int

const (
	// Disarm ends the poke cycle; nothing more is queued.
	Disarm Decision = iota

	// Continue queues a message and runs the loop again.
	Continue
)

// Poke is the outcome of the turn-end decision tree.
type Poke struct {
	Decision Decision

	// SystemLine renders in the transcript as a system message, telling the
	// user what the harness just did on their behalf.
	SystemLine string

	// Queued is appended to the conversation as a user-role message. It is
	// recognized on replay by its `[automated ...]` prefix and re-rendered as
	// a system line, so the model sees a normal continuation while the user
	// sees the harness talking (plan.md §12.4).
	Queued string

	// Reason records why, for the log.
	Reason string
}

// State carries the counters the breakers need across turns.
type State struct {
	// GateAttempts counts nudges made without the validation improving.
	GateAttempts int

	// ConsecutiveRefusals counts provider refusals in a row.
	ConsecutiveRefusals int

	// DigestDelivered marks the gate digest as already sent this completion
	// cycle, so it fires once rather than every turn.
	DigestDelivered bool

	// SpikesChallenged marks confidence spikes as already questioned.
	SpikesChallenged bool

	// Disarmed stops the cycle until the next todo write re-arms it.
	Disarmed bool

	// lastFingerprint and StalledPokes detect a nudge that changed nothing.
	lastFingerprint string
	StalledPokes    int
}

// Inputs is everything the decision tree reads.
type Inputs struct {
	Items        []Item
	Plan         Plan
	Goals        []Goal
	Observations []Observation
}

// AllRitesComplete is the one piece of flavor the poke system is allowed
// (plan.md §2.1).
const AllRitesComplete = "🦇 All rites complete. Completion confidence: %d%%."

// Decide runs the turn-end decision tree of plan.md §12.4. The order is
// normative: each branch exists because the one before it was not enough.
func Decide(in Inputs, st *State) Poke {
	// Breaker first: a refusing model must never be re-poked, whatever else
	// the state says.
	if st.ConsecutiveRefusals >= MaxConsecutiveRefusals {
		st.Disarmed = true
		return Poke{
			Decision: Disarm,
			SystemLine: "🛑 The model refused several times in a row. We stopped poking — " +
				"re-poking an identical request just repeats the refusal.",
			Reason: "consecutive refusals",
		}
	}

	if st.Disarmed {
		return Poke{Decision: Disarm, Reason: "already disarmed"}
	}

	if len(in.Items) == 0 {
		// Nothing was ever planned, so there is nothing to hold the model to.
		st.Disarmed = true
		return Poke{Decision: Disarm, Reason: "no todos"}
	}

	if n := Incomplete(in.Items); n > 0 {
		// Open todos mean the model is still iterating, so the gate-attempt
		// counter resets: it is measuring stalled validation, not progress.
		st.GateAttempts = 0

		// "Still iterating" only holds if the list actually moves. Poking a
		// model that answers without touching its todos is an infinite loop.
		fp := fingerprint(in.Items)
		if fp == st.lastFingerprint {
			st.StalledPokes++
		} else {
			st.StalledPokes = 0
			st.lastFingerprint = fp
		}
		if st.StalledPokes >= MaxStalledPokes {
			st.Disarmed = true
			return Poke{
				Decision: Disarm,
				SystemLine: fmt.Sprintf(
					"⚠ We poked about %d incomplete todos %d times and the list did not change. "+
						"We stopped; pick it up yourself or update the todos.", n, st.StalledPokes),
				Reason: "stalled poke loop",
			}
		}

		return Poke{
			Decision:   Continue,
			SystemLine: fmt.Sprintf("👉 %d incomplete todos. We poked it for you. /poke off to stop.", n),
			Queued: fmt.Sprintf(
				"[automated todo completion gate - not a user message] You have %d incomplete todos. "+
					"Continue working, or update the todo tool if the list is stale. "+
					"Do not reply conversationally or wait for the user.", n),
			Reason: "incomplete todos",
		}
	}

	// Everything is marked done from here on. The remaining branches ask
	// whether "done" is believable.

	if !st.DigestDelivered {
		if digest := BuildDigest(in.Observations, in.Plan, in.Goals); digest != "" {
			st.DigestDelivered = true
			return Poke{
				Decision:   Continue,
				SystemLine: "🔎 We asked the agent to double-check this turn's weak points.",
				Queued:     digest,
				Reason:     "unresolved gate observations",
			}
		}
	}

	if weak, why := WeakCompletion(in.Items); weak {
		st.GateAttempts++
		if st.GateAttempts > MaxGateAttempts {
			st.Disarmed = true
			return Poke{
				Decision: Disarm,
				SystemLine: "⚠ We nudged the agent several times but its validation still isn't holding up. " +
					"We stopped poking; review the remaining todos yourself.",
				Reason: "gate budget exhausted",
			}
		}
		return Poke{
			Decision: Continue,
			SystemLine: "🛑 The agent marked its work done without strong enough validation. " +
				"We asked it to double-check.",
			Queued: fmt.Sprintf(
				"[automated todo completion gate - not a user message] You marked the work complete, but "+
					"%s. Verify it actually works — run the feedback loop you recorded and report what it "+
					"returned, rather than asserting success. Update the todo tool with what you find. "+
					"Do not reply conversationally or wait for the user.", why),
			Reason: "weak completion confidence",
		}
	}

	if !st.SpikesChallenged {
		if spikes := Spikes(in.Items); len(spikes) > 0 {
			st.SpikesChallenged = true
			st.GateAttempts++
			return Poke{
				Decision: Continue,
				SystemLine: "🛑 The agent's confidence jumped suddenly. We asked it to verify that " +
					"independently.",
				Queued: fmt.Sprintf(
					"[automated todo completion gate - not a user message] Your confidence in %q rose by "+
						"at least %d points in a single step, which usually means it was asserted rather "+
						"than observed. Verify that item independently and report the concrete evidence. "+
						"Do not reply conversationally or wait for the user.",
					spikes[0].Content, SpikeDelta),
				Reason: "unchallenged confidence spike",
			}
		}
	}

	// Everything checks out.
	st.Disarmed = true
	st.GateAttempts = 0
	confidence := 100
	if avg, ok := AggregateConfidence(in.Items); ok {
		confidence = avg
	}
	return Poke{
		Decision:   Disarm,
		SystemLine: fmt.Sprintf(AllRitesComplete, confidence),
		Reason:     "all complete",
	}
}

// fingerprint summarizes a list so an unchanged one is detectable. Content and
// status are what "progress" means here; scores moving without the list moving
// is what the completion-confidence branch is for.
func fingerprint(items []Item) string {
	var b strings.Builder
	for _, i := range items {
		b.WriteString(i.ID)
		b.WriteByte(':')
		b.WriteString(string(i.Status))
		b.WriteByte(':')
		b.WriteString(i.Content)
		b.WriteByte('\n')
	}
	return b.String()
}

// AutomatedPrefix marks a harness-authored continuation. Messages carrying it
// persist as user-role — the model must see a normal continuation — but render
// as system lines on load, replay, and attach (plan.md §12.4).
const AutomatedPrefix = "[automated "

// IsAutomated reports whether a stored user message was actually written by the
// harness.
func IsAutomated(content string) bool {
	return len(content) >= len(AutomatedPrefix) && content[:len(AutomatedPrefix)] == AutomatedPrefix
}

// Rearm clears the disarm flags, which a fresh todo write does.
func (s *State) Rearm() {
	s.Disarmed = false
	s.DigestDelivered = false
	s.SpikesChallenged = false
	s.StalledPokes = 0
}

// RecordRefusal counts a provider refusal toward the breaker.
func (s *State) RecordRefusal() { s.ConsecutiveRefusals++ }

// RecordSuccess resets the refusal counter.
func (s *State) RecordSuccess() { s.ConsecutiveRefusals = 0 }
