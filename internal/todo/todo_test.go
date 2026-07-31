package todo

import (
	"strings"
	"testing"
)

func u8(v uint8) *uint8    { return &v }
func str(s string) *string { return &s }

func item(id, content string, status Status, opts ...func(*Item)) Item {
	i := Item{ID: id, Content: content, Status: status, Priority: PriorityMedium}
	for _, o := range opts {
		o(&i)
	}
	return i
}

func withGroup(g string) func(*Item) { return func(i *Item) { i.Group = str(g) } }
func withConf(v uint8) func(*Item)   { return func(i *Item) { i.Confidence = u8(v) } }
func withDone(v uint8) func(*Item)   { return func(i *Item) { i.CompletionConfidence = u8(v) } }
func withPri(p Priority) func(*Item) { return func(i *Item) { i.Priority = p } }
func withHist(h ...uint8) func(*Item) {
	return func(i *Item) { i.ConfidenceHistory = h }
}

func newStore(t *testing.T) *Store {
	t.Helper()
	s, err := NewStore(t.TempDir(), "dracula")
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func TestHistoryIsToolOwned(t *testing.T) {
	// The trail is only meaningful if the agent cannot author it. A
	// model-supplied history must be discarded outright.
	s := newStore(t)
	_, err := s.Apply(Write{Items: []Item{
		item("a", "task", StatusPending, withConf(50), withHist(1, 2, 3, 99)),
	}})
	if err != nil {
		t.Fatal(err)
	}
	got := s.Items()[0].ConfidenceHistory
	if len(got) != 1 || got[0] != 50 {
		t.Errorf("history = %v, want only the tool's own observation [50]", got)
	}
}

func TestHistoryAccumulatesOnePerWrite(t *testing.T) {
	// A single write must not be able to manufacture an apparent sequence of
	// intermediate steps (plan.md §12.2).
	s := newStore(t)
	for _, v := range []uint8{75, 85, 95, 100} {
		if _, err := s.Apply(Write{Items: []Item{
			item("a", "task", StatusInProgress, withConf(v)),
		}}); err != nil {
			t.Fatal(err)
		}
	}
	got := s.Items()[0].ConfidenceHistory
	want := []uint8{75, 85, 95, 100}
	if len(got) != len(want) {
		t.Fatalf("history = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("history = %v, want %v", got, want)
		}
	}
}

func TestHistoryCollapsesUnchangedRepeats(t *testing.T) {
	// Recording the same number twice would make a stalled score look like
	// progress.
	s := newStore(t)
	for i := 0; i < 4; i++ {
		s.Apply(Write{Items: []Item{item("a", "task", StatusPending, withConf(80))}})
	}
	if got := s.Items()[0].ConfidenceHistory; len(got) != 1 {
		t.Errorf("history = %v, want a single entry", got)
	}
}

func TestSpikeDetection(t *testing.T) {
	tests := []struct {
		name string
		item Item
		want bool
	}{
		{
			"evidence-driven rise is not a spike",
			item("a", "x", StatusCompleted, withHist(75, 85, 95, 100)),
			false,
		},
		{
			"bulk end-stamp is a spike",
			item("a", "x", StatusCompleted, withHist(75, 100)),
			true,
		},
		{
			"single entry is never a spike",
			item("a", "x", StatusCompleted, withHist(100)),
			false,
		},
		{
			"empty history compares planning to completion",
			item("a", "x", StatusCompleted, withConf(70), withDone(95)),
			true,
		},
		{
			"empty history with a small gap is fine",
			item("a", "x", StatusCompleted, withConf(90), withDone(95)),
			false,
		},
		{
			"empty history with nothing to compare",
			item("a", "x", StatusCompleted),
			false,
		},
		{
			"incomplete items are never spikes",
			item("a", "x", StatusInProgress, withHist(50, 100)),
			false,
		},
		{
			"exactly at the threshold counts",
			item("a", "x", StatusCompleted, withHist(80, 95)),
			true,
		},
		{
			"just under the threshold does not",
			item("a", "x", StatusCompleted, withHist(80, 94)),
			false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := IsSpike(tt.item); got != tt.want {
				t.Errorf("IsSpike = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestAggregateConfidenceIsPriorityWeighted(t *testing.T) {
	items := []Item{
		item("a", "high", StatusCompleted, withPri(PriorityHigh), withDone(100)),
		item("b", "low", StatusCompleted, withPri(PriorityLow), withDone(60)),
	}
	// (100*3 + 60*1) / 4 = 90
	got, ok := AggregateConfidence(items)
	if !ok || got != 90 {
		t.Errorf("aggregate = %d (%v), want 90", got, ok)
	}
}

func TestAggregateExcludesCancelled(t *testing.T) {
	items := []Item{
		item("a", "kept", StatusCompleted, withDone(100)),
		item("b", "dropped", StatusCancelled, withDone(0)),
	}
	got, ok := AggregateConfidence(items)
	if !ok || got != 100 {
		t.Errorf("aggregate = %d, want cancelled items excluded", got)
	}
}

func TestAggregateRoundsHalfUp(t *testing.T) {
	items := []Item{
		item("a", "x", StatusCompleted, withPri(PriorityLow), withDone(95)),
		item("b", "y", StatusCompleted, withPri(PriorityLow), withDone(96)),
	}
	// 191/2 = 95.5 -> 96
	if got, _ := AggregateConfidence(items); got != 96 {
		t.Errorf("aggregate = %d, want 96", got)
	}
}

func TestOwnershipGateBlocksGroupCompletion(t *testing.T) {
	// The one hard-blocking gate (plan.md §12.3).
	s := newStore(t)
	s.Apply(Write{
		Items: []Item{item("a", "task", StatusInProgress, withGroup("auth"))},
		Goals: []Goal{{Group: "auth", EndToEndOwnership: u8(50)}},
	})

	res, err := s.Apply(Write{
		Items: []Item{item("a", "task", StatusCompleted, withGroup("auth"), withDone(100))},
		Goals: []Goal{{Group: "auth", EndToEndOwnership: u8(50)}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !res.Rejected {
		t.Fatal("completing a group with low ownership must be rejected")
	}
	if !strings.Contains(res.Explanation, "end_to_end_ownership") {
		t.Errorf("explanation = %q, want it to name the gate", res.Explanation)
	}
	// The stored list must be unchanged.
	if got := s.Items()[0].Status; got != StatusInProgress {
		t.Errorf("stored status = %q, want the write to have been refused entirely", got)
	}
}

func TestOwnershipGateAllowsWhenOwned(t *testing.T) {
	s := newStore(t)
	s.Apply(Write{
		Items: []Item{item("a", "task", StatusInProgress, withGroup("auth"))},
	})
	res, err := s.Apply(Write{
		Items: []Item{item("a", "task", StatusCompleted, withGroup("auth"), withDone(100))},
		Goals: []Goal{{Group: "auth", EndToEndOwnership: u8(QualityGate)}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.Rejected {
		t.Fatalf("rejected with sufficient ownership: %s", res.Explanation)
	}
}

func TestOwnershipGateIgnoresAlreadyCompleteGroups(t *testing.T) {
	// Re-writing an already-finished group must not re-trigger the gate, or
	// the list becomes unwritable.
	s := newStore(t)
	s.Apply(Write{
		Items: []Item{item("a", "task", StatusCompleted, withGroup("auth"))},
		Goals: []Goal{{Group: "auth", EndToEndOwnership: u8(100)}},
	})
	res, _ := s.Apply(Write{
		Items: []Item{item("a", "task", StatusCompleted, withGroup("auth"))},
	})
	if res.Rejected {
		t.Errorf("re-writing a finished group was rejected: %s", res.Explanation)
	}
}

// H5.16: blank/duplicate ids, invalid or self-referential dependencies, and
// out-of-range confidence must all be rejected rather than silently
// producing ambiguous or ungoverned state.
func TestApplyRejectsBlankID(t *testing.T) {
	s := newStore(t)
	res, err := s.Apply(Write{Items: []Item{item("", "task", StatusPending)}})
	if err != nil {
		t.Fatal(err)
	}
	if !res.Rejected {
		t.Error("a blank id must be rejected")
	}
	if len(s.Items()) != 0 {
		t.Error("a rejected write must not be stored")
	}
}

func TestApplyRejectsDuplicateID(t *testing.T) {
	s := newStore(t)
	res, err := s.Apply(Write{Items: []Item{
		item("a", "first", StatusPending),
		item("a", "second", StatusPending),
	}})
	if err != nil {
		t.Fatal(err)
	}
	if !res.Rejected {
		t.Error("a duplicate id within one write must be rejected")
	}
}

func TestApplyRejectsSelfReferentialDependency(t *testing.T) {
	s := newStore(t)
	dep := item("a", "task", StatusPending)
	dep.BlockedBy = []string{"a"}
	res, err := s.Apply(Write{Items: []Item{dep}})
	if err != nil {
		t.Fatal(err)
	}
	if !res.Rejected {
		t.Error("an item depending on itself must be rejected")
	}
}

func TestApplyRejectsUnknownDependency(t *testing.T) {
	s := newStore(t)
	dep := item("a", "task", StatusPending)
	dep.BlockedBy = []string{"ghost"}
	res, err := s.Apply(Write{Items: []Item{dep}})
	if err != nil {
		t.Fatal(err)
	}
	if !res.Rejected {
		t.Error("a dependency on an unknown id must be rejected")
	}
}

func TestApplyAllowsDependencyOnAnAlreadyStoredItem(t *testing.T) {
	s := newStore(t)
	if _, err := s.Apply(Write{Items: []Item{item("a", "first", StatusPending)}}); err != nil {
		t.Fatal(err)
	}
	dep := item("b", "second", StatusPending)
	dep.BlockedBy = []string{"a"}
	res, err := s.Apply(Write{Items: []Item{item("a", "first", StatusPending), dep}})
	if err != nil {
		t.Fatal(err)
	}
	if res.Rejected {
		t.Errorf("a dependency on an existing stored item must be allowed: %s", res.Explanation)
	}
}

func TestApplyRejectsOutOfRangeConfidence(t *testing.T) {
	s := newStore(t)
	res, err := s.Apply(Write{Items: []Item{item("a", "task", StatusPending, withConf(101))}})
	if err != nil {
		t.Fatal(err)
	}
	if !res.Rejected {
		t.Error("confidence above 100 must be rejected")
	}
}

func TestApplyRejectsOutOfRangeCompletionConfidence(t *testing.T) {
	s := newStore(t)
	res, err := s.Apply(Write{Items: []Item{item("a", "task", StatusPending, withDone(200))}})
	if err != nil {
		t.Fatal(err)
	}
	if !res.Rejected {
		t.Error("completion_confidence above 100 must be rejected")
	}
}

func TestLowScoresDeferRatherThanNag(t *testing.T) {
	// Nagging per write punishes the healthy low-then-rising pattern.
	s := newStore(t)
	res, err := s.Apply(Write{
		Items: []Item{item("a", "task", StatusPending)},
		Goals: []Goal{{Group: "auth", ClosedFeedbackLoop: u8(40)}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.Immediate != "" {
		t.Errorf("a low loop score fired immediately: %q", res.Immediate)
	}
	if len(s.Observations()) != 1 {
		t.Errorf("observations = %d, want the low score deferred to one", len(s.Observations()))
	}
}

func TestSevereIntentFiresImmediatelyOnFirstPlanWrite(t *testing.T) {
	// The one exception: the agent admitting it does not know the task. A
	// whole turn of wrong work cannot be undone at turn end.
	s := newStore(t)
	res, err := s.Apply(Write{
		Items: []Item{item("a", "task", StatusPending)},
		Plan:  &Plan{UnderstandsUserIntent: u8(30)},
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.Immediate == "" {
		t.Fatal("a first plan write below the severe threshold must fire immediately")
	}
	if !strings.Contains(res.Immediate, "Do not reply conversationally") {
		t.Error("every continuation must tell the model not to answer it")
	}
}

func TestSevereIntentOnlyFiresOnce(t *testing.T) {
	// It is the *first* plan write that fires; later low scores defer like
	// everything else.
	s := newStore(t)
	s.Apply(Write{Items: []Item{item("a", "t", StatusPending)}, Plan: &Plan{UnderstandsUserIntent: u8(30)}})
	res, _ := s.Apply(Write{Items: []Item{item("a", "t", StatusPending)}, Plan: &Plan{UnderstandsUserIntent: u8(20)}})
	if res.Immediate != "" {
		t.Errorf("later low scores should defer, got %q", res.Immediate)
	}
}

func TestObservationCap(t *testing.T) {
	s := newStore(t)
	for i := 0; i < MaxObservations+50; i++ {
		s.Apply(Write{
			Items: []Item{item("a", "t", StatusPending)},
			Goals: []Goal{{Group: "g", ClosedFeedbackLoop: u8(uint8(i%90 + 1))}},
		})
	}
	if got := len(s.Observations()); got > MaxObservations {
		t.Errorf("observations = %d, want at most %d", got, MaxObservations)
	}
}

func TestItemCap(t *testing.T) {
	s := newStore(t)
	var items []Item
	for i := 0; i < MaxItems+1; i++ {
		items = append(items, item(string(rune(i)), "t", StatusPending))
	}
	res, _ := s.Apply(Write{Items: items})
	if !res.Rejected {
		t.Error("a list past the cap must be rejected")
	}
}

func TestDigestWordingByTrajectory(t *testing.T) {
	tests := []struct {
		name  string
		obs   []Observation
		plan  Plan
		goals []Goal
		want  string
	}{
		{
			name: "intent never cleared",
			obs:  []Observation{{Kind: KindIntent, Score: 40}},
			plan: Plan{UnderstandsUserIntent: u8(40)},
			want: "never became solid",
		},
		{
			name: "intent cleared late",
			obs:  []Observation{{Kind: KindIntent, Score: 40}},
			plan: Plan{UnderstandsUserIntent: u8(100)},
			want: "only settled it later",
		},
		{
			name:  "loop never closed",
			obs:   []Observation{{Kind: KindLoop, Group: "auth", Score: 30}},
			goals: []Goal{{Group: "auth", ClosedFeedbackLoop: u8(30)}},
			want:  "never closed its feedback loop",
		},
		{
			name:  "loop closed late",
			obs:   []Observation{{Kind: KindLoop, Group: "auth", Score: 30}},
			goals: []Goal{{Group: "auth", ClosedFeedbackLoop: u8(100)}},
			want:  "never ran over that earlier work",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := BuildDigest(tt.obs, tt.plan, tt.goals)
			if !strings.Contains(got, tt.want) {
				t.Errorf("digest missing %q:\n%s", tt.want, got)
			}
			if !strings.HasPrefix(got, DigestPrefix) {
				t.Error("digest must carry the automated prefix")
			}
		})
	}
}

func TestDigestCollapsesRepeats(t *testing.T) {
	obs := []Observation{
		{Kind: KindIntent, Score: 40},
		{Kind: KindIntent, Score: 45},
		{Kind: KindIntent, Score: 50},
	}
	got := BuildDigest(obs, Plan{UnderstandsUserIntent: u8(50)}, nil)
	if strings.Count(got, "never became solid") != 1 {
		t.Errorf("repeats should collapse to one line:\n%s", got)
	}
	if !strings.Contains(got, "observed 3 times") {
		t.Errorf("digest should say how often:\n%s", got)
	}
}

func TestDigestEmptyWithoutObservations(t *testing.T) {
	if got := BuildDigest(nil, Plan{}, nil); got != "" {
		t.Errorf("digest = %q, want empty", got)
	}
}

func TestWeakCompletionSignals(t *testing.T) {
	tests := []struct {
		name  string
		items []Item
		weak  bool
	}{
		{
			"all strong",
			[]Item{item("a", "x", StatusCompleted, withDone(100))},
			false,
		},
		{
			"missing score",
			[]Item{item("a", "x", StatusCompleted)},
			true,
		},
		{
			// One unverified item must not hide behind confident ones.
			"single weak item among strong ones",
			[]Item{
				item("a", "x", StatusCompleted, withDone(100)),
				item("b", "y", StatusCompleted, withDone(40)),
				item("c", "z", StatusCompleted, withDone(100)),
			},
			true,
		},
		{
			"cancelled items ignored",
			[]Item{
				item("a", "x", StatusCompleted, withDone(100)),
				item("b", "y", StatusCancelled),
			},
			false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			weak, why := WeakCompletion(tt.items)
			if weak != tt.weak {
				t.Errorf("WeakCompletion = %v (%q), want %v", weak, why, tt.weak)
			}
			if weak && why == "" {
				t.Error("a weak verdict must explain itself")
			}
		})
	}
}

func TestPokeDecisionTree(t *testing.T) {
	strong := []Item{item("a", "x", StatusCompleted, withDone(100), withHist(90, 100))}

	t.Run("no todos disarms silently", func(t *testing.T) {
		var st State
		got := Decide(Inputs{}, &st)
		if got.Decision != Disarm || got.Queued != "" {
			t.Errorf("got %+v, want a silent disarm", got)
		}
		if !st.Disarmed {
			t.Error("state should be disarmed")
		}
	})

	t.Run("incomplete todos poke", func(t *testing.T) {
		var st State
		got := Decide(Inputs{Items: []Item{item("a", "x", StatusPending)}}, &st)
		if got.Decision != Continue {
			t.Fatalf("got %+v, want a continuation", got)
		}
		if !strings.Contains(got.SystemLine, "incomplete todos") {
			t.Errorf("system line = %q", got.SystemLine)
		}
		if !strings.Contains(got.Queued, "Do not reply conversationally") {
			t.Error("continuations must tell the model not to answer")
		}
	})

	t.Run("open todos reset the gate counter", func(t *testing.T) {
		// Open todos mean the model is iterating; the counter measures stalled
		// validation, not progress.
		st := State{GateAttempts: 2}
		Decide(Inputs{Items: []Item{item("a", "x", StatusPending)}}, &st)
		if st.GateAttempts != 0 {
			t.Errorf("GateAttempts = %d, want it reset", st.GateAttempts)
		}
	})

	t.Run("gate digest fires once per cycle", func(t *testing.T) {
		var st State
		in := Inputs{
			Items:        strong,
			Plan:         Plan{UnderstandsUserIntent: u8(40)},
			Observations: []Observation{{Kind: KindIntent, Score: 40}},
		}
		first := Decide(in, &st)
		if first.Decision != Continue || !strings.Contains(first.SystemLine, "double-check") {
			t.Fatalf("got %+v, want the digest", first)
		}
		second := Decide(in, &st)
		if strings.Contains(second.SystemLine, "double-check") {
			t.Error("the digest must fire once per completion cycle")
		}
	})

	t.Run("weak completion asks for verification", func(t *testing.T) {
		var st State
		got := Decide(Inputs{Items: []Item{item("a", "x", StatusCompleted, withDone(50))}}, &st)
		if got.Decision != Continue || !strings.Contains(got.SystemLine, "validation") {
			t.Errorf("got %+v", got)
		}
	})

	t.Run("gate budget exhausts and stops", func(t *testing.T) {
		st := State{GateAttempts: MaxGateAttempts}
		got := Decide(Inputs{Items: []Item{item("a", "x", StatusCompleted, withDone(50))}}, &st)
		if got.Decision != Disarm {
			t.Fatalf("got %+v, want a disarm", got)
		}
		if !strings.Contains(got.SystemLine, "stopped poking") {
			t.Errorf("system line = %q, want it to tell the user", got.SystemLine)
		}
	})

	t.Run("confidence spike is challenged", func(t *testing.T) {
		var st State
		spiked := []Item{item("a", "x", StatusCompleted, withDone(100), withHist(70, 100))}
		got := Decide(Inputs{Items: spiked}, &st)
		if got.Decision != Continue || !strings.Contains(got.SystemLine, "confidence jumped") {
			t.Errorf("got %+v", got)
		}
	})

	t.Run("all good completes the rites", func(t *testing.T) {
		var st State
		got := Decide(Inputs{Items: strong}, &st)
		if got.Decision != Disarm {
			t.Fatalf("got %+v, want a disarm", got)
		}
		if !strings.Contains(got.SystemLine, "All rites complete") {
			t.Errorf("system line = %q", got.SystemLine)
		}
		if !st.Disarmed {
			t.Error("state should be disarmed")
		}
	})

	t.Run("refusal breaker overrides everything", func(t *testing.T) {
		st := State{ConsecutiveRefusals: MaxConsecutiveRefusals}
		got := Decide(Inputs{Items: []Item{item("a", "x", StatusPending)}}, &st)
		if got.Decision != Disarm {
			t.Fatalf("got %+v, want a disarm despite incomplete todos", got)
		}
		if !strings.Contains(got.SystemLine, "refused") {
			t.Errorf("system line = %q", got.SystemLine)
		}
	})
}

func TestPokeTerminates(t *testing.T) {
	// The property that matters: however bad the state, repeatedly deciding
	// must reach a disarm rather than looping forever.
	items := []Item{item("a", "x", StatusCompleted, withDone(10), withHist(10, 90))}
	var st State
	for i := 0; i < 50; i++ {
		got := Decide(Inputs{
			Items:        items,
			Observations: []Observation{{Kind: KindLoop, Group: "g", Score: 10}},
		}, &st)
		if got.Decision == Disarm {
			return
		}
	}
	t.Fatal("the poke cycle never disarmed; every self-re-prompting path needs a breaker")
}

func TestAutomatedPrefixRecognition(t *testing.T) {
	if !IsAutomated("[automated todo completion gate - not a user message] go on") {
		t.Error("harness continuations must be recognizable on replay")
	}
	if IsAutomated("please fix the automated tests") {
		t.Error("ordinary text must not be mistaken for a harness message")
	}
}

func TestRearmClearsCycleFlags(t *testing.T) {
	st := State{Disarmed: true, DigestDelivered: true, SpikesChallenged: true}
	st.Rearm()
	if st.Disarmed || st.DigestDelivered || st.SpikesChallenged {
		t.Errorf("state = %+v, want the cycle flags cleared", st)
	}
}

func TestDeltaFormSelection(t *testing.T) {
	prev := []Item{
		item("a", "one", StatusPending),
		item("b", "two", StatusPending),
	}
	tests := []struct {
		name  string
		next  []Item
		formB bool
	}{
		{
			"single status flip is form A",
			[]Item{item("a", "one", StatusInProgress), item("b", "two", StatusPending)},
			false,
		},
		{
			"an add forces form B",
			[]Item{prev[0], prev[1], item("c", "three", StatusPending)},
			true,
		},
		{
			"a removal forces form B",
			[]Item{prev[0]},
			true,
		},
		{
			"two status flips force form B",
			[]Item{item("a", "one", StatusCompleted), item("b", "two", StatusInProgress)},
			true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			d := DiffItems(prev, tt.next)
			if got := d.UsesFormB(); got != tt.formB {
				t.Errorf("UsesFormB = %v, want %v (changes: %+v)", got, tt.formB, d.Changes)
			}
		})
	}
}

func TestFirstWriteIsFormB(t *testing.T) {
	d := DiffItems(nil, []Item{item("a", "x", StatusPending)})
	if !d.FirstWrite || !d.UsesFormB() {
		t.Error("the first write should use the expanded form")
	}
}

func TestDeltaSummaryOrder(t *testing.T) {
	prev := []Item{
		item("a", "one", StatusPending),
		item("b", "two", StatusPending),
		item("c", "three", StatusPending),
	}
	next := []Item{
		item("a", "one", StatusCompleted),
		item("b", "two", StatusInProgress),
		item("c", "three", StatusPending),
		item("d", "four", StatusPending),
	}
	got := DiffItems(prev, next).Summary()
	// Fixed order means the summary reads the same way every time.
	if got != "1 done · 1 started · 1 added" {
		t.Errorf("summary = %q", got)
	}
}

func TestDeltaSummaryFallback(t *testing.T) {
	if got := (Delta{}).Summary(); got != "updated" {
		t.Errorf("summary = %q, want the fallback", got)
	}
}

func TestArrowLabelIsTheAntiGamingTell(t *testing.T) {
	// A completed item whose planning and completion confidence differ shows
	// the arrow; identical values show just the number (plan.md §12.5).
	if got := ArrowLabel(u8(75), u8(100)); got != "75→100%" {
		t.Errorf("got %q", got)
	}
	if got := ArrowLabel(u8(100), u8(100)); got != "100%" {
		t.Errorf("got %q, want no arrow when nothing moved", got)
	}
	if got := ArrowLabel(nil, u8(90)); got != "90%" {
		t.Errorf("got %q", got)
	}
	if got := ArrowLabel(u8(90), nil); got != "" {
		t.Errorf("got %q, want empty", got)
	}
}

func TestScoreDeltas(t *testing.T) {
	got := ScoreDeltas(
		Plan{UnderstandsUserIntent: u8(72)},
		Plan{UnderstandsUserIntent: u8(91)},
		[]Goal{{Group: "auth", ClosedFeedbackLoop: u8(80)}},
		[]Goal{{Group: "auth", ClosedFeedbackLoop: u8(96)}},
	)
	if len(got) != 2 {
		t.Fatalf("deltas = %+v, want 2", got)
	}
	if got[0].Old != 72 || got[0].New != 91 {
		t.Errorf("plan delta = %+v", got[0])
	}
	if got[1].Label != "auth" || got[1].New != 96 {
		t.Errorf("goal delta = %+v", got[1])
	}
}

func TestScoreDeltasIgnoresUnchanged(t *testing.T) {
	got := ScoreDeltas(
		Plan{UnderstandsUserIntent: u8(90)},
		Plan{UnderstandsUserIntent: u8(90)},
		nil, nil,
	)
	if len(got) != 0 {
		t.Errorf("deltas = %+v, want none", got)
	}
}

func TestStorePersistsAcrossOpens(t *testing.T) {
	dir := t.TempDir()
	s1, _ := NewStore(dir, "bat")
	s1.Apply(Write{
		Items: []Item{item("a", "task", StatusInProgress, withConf(80), withGroup("auth"))},
		Plan:  &Plan{UserIntention: str("ship the gate"), UnderstandsUserIntent: u8(87)},
		Goals: []Goal{{Group: "auth", FeedbackLoop: str("go test ./internal/auth/...")}},
	})

	s2, err := NewStore(dir, "bat")
	if err != nil {
		t.Fatal(err)
	}
	items := s2.Items()
	if len(items) != 1 || items[0].Content != "task" {
		t.Fatalf("items = %+v", items)
	}
	if len(items[0].ConfidenceHistory) != 1 {
		t.Errorf("history did not survive: %v", items[0].ConfidenceHistory)
	}
	if p := s2.Plan(); p.UserIntention == nil || *p.UserIntention != "ship the gate" {
		t.Errorf("plan = %+v", p)
	}
	g, ok := s2.Goal("auth")
	if !ok || g.FeedbackLoop == nil || *g.FeedbackLoop != "go test ./internal/auth/..." {
		t.Errorf("goal = %+v (%v)", g, ok)
	}
}

func TestGroupsPutUngroupedLast(t *testing.T) {
	items := []Item{
		item("a", "x", StatusPending, withGroup("auth")),
		item("b", "y", StatusPending),
		item("c", "z", StatusPending, withGroup("scroll")),
	}
	got := Groups(items)
	want := []string{"auth", "scroll", ""}
	if len(got) != len(want) {
		t.Fatalf("groups = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("groups = %v, want %v", got, want)
		}
	}
}

func TestSortItemsPutsActiveFirst(t *testing.T) {
	items := []Item{
		item("a", "done", StatusCompleted),
		item("b", "pending", StatusPending),
		item("c", "active", StatusInProgress),
		item("d", "cancelled", StatusCancelled),
	}
	got := SortItems(items)
	want := []Status{StatusInProgress, StatusPending, StatusCompleted, StatusCancelled}
	for i := range want {
		if got[i].Status != want[i] {
			t.Fatalf("order = %v, want %v", statuses(got), want)
		}
	}
}

func statuses(items []Item) []Status {
	out := make([]Status, len(items))
	for i, it := range items {
		out[i] = it.Status
	}
	return out
}

func TestInvalidWritesRejected(t *testing.T) {
	s := newStore(t)
	tests := []struct {
		name string
		w    Write
	}{
		{"empty content", Write{Items: []Item{{ID: "a", Status: StatusPending}}}},
		{"bad status", Write{Items: []Item{{ID: "a", Content: "x", Status: "nope"}}}},
		{"bad priority", Write{Items: []Item{{ID: "a", Content: "x", Status: StatusPending, Priority: "urgent"}}}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			res, _ := s.Apply(tt.w)
			if !res.Rejected {
				t.Error("want a rejection")
			}
		})
	}
}

func TestStalledPokeLoopTerminates(t *testing.T) {
	// The bug this guards was found by watching the TUI poke sixty times: the
	// incomplete-todos branch resets the gate counter, so without a
	// progress check it never stops. Open todos only mean "still iterating" if
	// the list actually moves.
	items := []Item{item("a", "never finished", StatusPending)}
	var st State
	pokes := 0
	for i := 0; i < 50; i++ {
		got := Decide(Inputs{Items: items}, &st)
		if got.Decision == Disarm {
			if pokes == 0 {
				t.Fatal("it should poke at least once before giving up")
			}
			if !strings.Contains(got.SystemLine, "did not change") {
				t.Errorf("system line = %q, want it to name the stall", got.SystemLine)
			}
			return
		}
		pokes++
	}
	t.Fatalf("poked %d times without stopping; every self-re-prompting path needs a breaker", pokes)
}

func TestProgressResetsTheStallCounter(t *testing.T) {
	// A model that is genuinely working must keep being poked.
	var st State
	for i := 0; i < 20; i++ {
		// Each round the list changes, standing in for real progress.
		items := []Item{
			item("a", "task", StatusPending),
			item(string(rune('b'+i)), "new task", StatusPending),
		}
		got := Decide(Inputs{Items: items}, &st)
		if got.Decision != Continue {
			t.Fatalf("round %d disarmed despite progress: %+v", i, got)
		}
	}
}
