package todo

import (
	"fmt"
	"strings"
)

// Change classifies what happened to one item between two lists.
type Change string

const (
	ChangeDone      Change = "done"
	ChangeStarted   Change = "started"
	ChangeReopened  Change = "reopened"
	ChangeCancelled Change = "cancelled"
	ChangeAdded     Change = "added"
	ChangeRemoved   Change = "removed"
	ChangeEdited    Change = "edited"
)

// ItemChange is one entry in a delta.
type ItemChange struct {
	Change Change
	Item   Item
}

// Delta is what one todo write altered. It is display-only and costs no tokens:
// the previous list is recovered by scanning the transcript backward for the
// last todo *write*, which makes it reload-safe with no threaded state
// (plan.md §12.5).
type Delta struct {
	Changes []ItemChange

	// Done and Total summarize the resulting list.
	Done, Total int

	// FirstWrite marks a delta with no previous list to compare against.
	FirstWrite bool
}

// DiffItems computes the change set between two lists.
func DiffItems(prev, next []Item) Delta {
	d := Delta{FirstWrite: len(prev) == 0}

	prevByID := make(map[string]Item, len(prev))
	for _, p := range prev {
		prevByID[p.ID] = p
	}
	nextByID := make(map[string]Item, len(next))
	for _, n := range next {
		nextByID[n.ID] = n
	}

	for _, n := range next {
		p, existed := prevByID[n.ID]
		if !existed {
			d.Changes = append(d.Changes, ItemChange{ChangeAdded, n})
			continue
		}
		switch {
		case p.Status != n.Status:
			d.Changes = append(d.Changes, ItemChange{statusChange(p.Status, n.Status), n})
		case p.Content != n.Content:
			d.Changes = append(d.Changes, ItemChange{ChangeEdited, n})
		}
	}
	for _, p := range prev {
		if _, still := nextByID[p.ID]; !still {
			d.Changes = append(d.Changes, ItemChange{ChangeRemoved, p})
		}
	}

	d.Total = len(next)
	for _, n := range next {
		if n.Status == StatusCompleted {
			d.Done++
		}
	}
	return d
}

func statusChange(from, to Status) Change {
	switch to {
	case StatusCompleted:
		return ChangeDone
	case StatusInProgress:
		return ChangeStarted
	case StatusCancelled:
		return ChangeCancelled
	case StatusPending:
		if from == StatusCompleted {
			return ChangeReopened
		}
		return ChangeStarted
	}
	return ChangeEdited
}

// Empty reports whether nothing changed.
func (d Delta) Empty() bool { return len(d.Changes) == 0 }

// StatusFlips counts changes that moved an item between states, as opposed to
// adds, removes, and content edits.
func (d Delta) StatusFlips() int {
	n := 0
	for _, c := range d.Changes {
		switch c.Change {
		case ChangeDone, ChangeStarted, ChangeReopened, ChangeCancelled:
			n++
		}
	}
	return n
}

// UsesFormB reports whether the expanded delta form applies: the first write,
// any add or remove, or more than one status change (plan.md §12.5).
func (d Delta) UsesFormB() bool {
	if d.FirstWrite {
		return true
	}
	for _, c := range d.Changes {
		if c.Change == ChangeAdded || c.Change == ChangeRemoved {
			return true
		}
	}
	return d.StatusFlips() > 1
}

// summaryOrder is the fixed order segments appear in, so a summary reads the
// same way every time regardless of map iteration.
var summaryOrder = []Change{
	ChangeDone, ChangeStarted, ChangeReopened, ChangeCancelled,
	ChangeAdded, ChangeRemoved, ChangeEdited,
}

var summaryLabel = map[Change]string{
	ChangeDone:      "done",
	ChangeStarted:   "started",
	ChangeReopened:  "reopened",
	ChangeCancelled: "cancelled",
	ChangeAdded:     "added",
	ChangeRemoved:   "removed",
	ChangeEdited:    "edited",
}

// Summary renders the counts line, e.g. `2 done · 1 started · 3 added`.
func (d Delta) Summary() string {
	counts := map[Change]int{}
	for _, c := range d.Changes {
		counts[c.Change]++
	}
	var parts []string
	for _, k := range summaryOrder {
		if n := counts[k]; n > 0 {
			parts = append(parts, fmt.Sprintf("%d %s", n, summaryLabel[k]))
		}
	}
	if len(parts) == 0 {
		return "updated"
	}
	return strings.Join(parts, " · ")
}

// Glyph is the marker a change renders with.
func (c Change) Glyph() string {
	switch c {
	case ChangeDone:
		return "✓"
	case ChangeStarted:
		return "▶"
	case ChangeReopened:
		return "○"
	case ChangeCancelled:
		return "✗"
	case ChangeAdded:
		return "+"
	case ChangeRemoved:
		return "-"
	default:
		return "·"
	}
}

// AssessmentDelta describes a write that only moved scores, so the UI can show
// the change rather than redrawing the whole plan (plan.md §12.5 surface 2).
type AssessmentDelta struct {
	// Label names what moved: "Plan" or a group name.
	Label string

	// Metric is the score's display name.
	Metric string

	Old, New uint8
}

// ScoreDeltas compares two snapshots and reports only the scores that moved.
func ScoreDeltas(oldPlan, newPlan Plan, oldGoals, newGoals []Goal) []AssessmentDelta {
	var out []AssessmentDelta

	if a, b := oldPlan.UnderstandsUserIntent, newPlan.UnderstandsUserIntent; b != nil {
		if a == nil || *a != *b {
			prev := uint8(0)
			if a != nil {
				prev = *a
			}
			out = append(out, AssessmentDelta{"Plan", "Understands user intent", prev, *b})
		}
	}

	oldByGroup := map[string]Goal{}
	for _, g := range oldGoals {
		oldByGroup[g.Group] = g
	}
	for _, g := range newGoals {
		prev := oldByGroup[g.Group]
		label := g.Group
		if label == "" {
			label = "Plan"
		}
		if b := g.ClosedFeedbackLoop; b != nil {
			if a := prev.ClosedFeedbackLoop; a == nil || *a != *b {
				old := uint8(0)
				if a != nil {
					old = *a
				}
				out = append(out, AssessmentDelta{label, "Closed feedback loop", old, *b})
			}
		}
		if b := g.EndToEndOwnership; b != nil {
			if a := prev.EndToEndOwnership; a == nil || *a != *b {
				old := uint8(0)
				if a != nil {
					old = *a
				}
				out = append(out, AssessmentDelta{label, "Ownership", old, *b})
			}
		}
	}
	return out
}

// ArrowLabel renders the `75→100%` tell that distinguishes an evidence-driven
// rise from a bulk end-stamp. It returns "" when the two values match, since an
// arrow to the same number says nothing.
func ArrowLabel(planning, completion *uint8) string {
	if planning == nil || completion == nil || *planning == *completion {
		if completion != nil {
			return fmt.Sprintf("%d%%", *completion)
		}
		return ""
	}
	return fmt.Sprintf("%d→%d%%", *planning, *completion)
}
