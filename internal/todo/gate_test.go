package todo

import (
	"strings"
	"testing"
)

// I1: one write can finish several groups at once, and every newly completed
// group must pass the ownership gate — a low-score sibling must not slip
// through beside a validated one just because map iteration found the good
// group first.
func TestOwnershipGateValidatesEveryNewlyCompletedGroup(t *testing.T) {
	s := newStore(t)
	s.Apply(Write{
		Items: []Item{
			item("a", "task", StatusInProgress, withGroup("auth")),
			item("b", "task", StatusInProgress, withGroup("db")),
		},
	})

	// Both groups complete in one write; only "auth" carries ownership.
	res, err := s.Apply(Write{
		Items: []Item{
			item("a", "task", StatusCompleted, withGroup("auth"), withDone(100)),
			item("b", "task", StatusCompleted, withGroup("db"), withDone(100)),
		},
		Goals: []Goal{{Group: "auth", EndToEndOwnership: u8(QualityGate)}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !res.Rejected {
		t.Fatal("completing two groups with only one owned must be rejected")
	}
	if !strings.Contains(res.Explanation, `"db"`) {
		t.Errorf("explanation = %q, want it to name the unowned group", res.Explanation)
	}
	if got := s.Items()[1].Status; got != StatusInProgress {
		t.Errorf("stored status = %q, want the write refused entirely", got)
	}

	// Owning both groups lets the same write through.
	res, err = s.Apply(Write{
		Items: []Item{
			item("a", "task", StatusCompleted, withGroup("auth"), withDone(100)),
			item("b", "task", StatusCompleted, withGroup("db"), withDone(100)),
		},
		Goals: []Goal{
			{Group: "auth", EndToEndOwnership: u8(QualityGate)},
			{Group: "db", EndToEndOwnership: u8(QualityGate)},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.Rejected {
		t.Fatalf("rejected with both groups owned: %s", res.Explanation)
	}
}

// I2: ungrouped items are a bucket too. Marking a flat plan entirely complete
// with no end-to-end ownership must hit the same hard gate.
func TestOwnershipGateAppliesToUngroupedCompletion(t *testing.T) {
	s := newStore(t)
	s.Apply(Write{
		Items: []Item{
			item("a", "task one", StatusInProgress),
			item("b", "task two", StatusInProgress),
		},
	})

	res, err := s.Apply(Write{
		Items: []Item{
			item("a", "task one", StatusCompleted, withDone(100)),
			item("b", "task two", StatusCompleted, withDone(100)),
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !res.Rejected {
		t.Fatal("completing the ungrouped bucket without ownership must be rejected")
	}
	if !strings.Contains(res.Explanation, "ungrouped") {
		t.Errorf("explanation = %q, want it to name the ungrouped bucket", res.Explanation)
	}

	// With an explicit ownership goal for the ungrouped bucket, it passes.
	res, err = s.Apply(Write{
		Items: []Item{
			item("a", "task one", StatusCompleted, withDone(100)),
			item("b", "task two", StatusCompleted, withDone(100)),
		},
		Goals: []Goal{{Group: "", EndToEndOwnership: u8(QualityGate)}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.Rejected {
		t.Fatalf("rejected with ungrouped ownership: %s", res.Explanation)
	}
}

// Completing a named group must not trip the gate on unrelated in-progress
// ungrouped items: only buckets the write itself touches are evaluated.
func TestOwnershipGateIgnoresUnrelatedUngroupedItems(t *testing.T) {
	s := newStore(t)
	s.Apply(Write{
		Items: []Item{
			item("a", "task", StatusInProgress, withGroup("auth")),
			item("u", "unrelated flat task", StatusInProgress),
		},
	})
	res, err := s.Apply(Write{
		Items: []Item{
			item("a", "task", StatusCompleted, withGroup("auth"), withDone(100)),
			item("u", "unrelated flat task", StatusInProgress),
		},
		Goals: []Goal{{Group: "auth", EndToEndOwnership: u8(QualityGate)}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.Rejected {
		t.Fatalf("a named-group completion was blocked by an untouched ungrouped item: %s", res.Explanation)
	}
}
