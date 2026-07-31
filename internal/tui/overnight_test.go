package tui

import (
	"fmt"
	"strings"
	"testing"
	"time"
)

func armed(t *testing.T) (*Overnight, time.Time) {
	t.Helper()
	now := time.Date(2026, 7, 31, 22, 0, 0, 0, time.UTC)
	var o Overnight
	o.Start(now)
	return &o, now
}

func TestOvernightContinuesWhileTheListMoves(t *testing.T) {
	o, now := armed(t)
	for i := 1; i <= 4; i++ {
		ok, why := o.ShouldContinue(now.Add(time.Minute),
			fmt.Sprintf("%d/9 done", i), 1000)
		if !ok {
			t.Fatalf("stopped at %d done: %s", i, why)
		}
	}
}

func TestOvernightStopsWhenTheListIsFinished(t *testing.T) {
	o, now := armed(t)
	ok, why := o.ShouldContinue(now, "9/9 done", 100)
	if ok {
		t.Fatal("kept going with nothing left to do")
	}
	if !strings.Contains(why, "finished") {
		t.Errorf("why = %q", why)
	}
}

func TestOvernightStopsOnTheTokenBudget(t *testing.T) {
	// The failure mode is spending money while nobody is watching.
	o, now := armed(t)
	ok, why := o.ShouldContinue(now, "1/9 done", OvernightBudget+1)
	if ok {
		t.Fatal("kept going past the token budget")
	}
	if !strings.Contains(why, "budget") {
		t.Errorf("why = %q", why)
	}
}

func TestOvernightStopsOnTheTurnCap(t *testing.T) {
	// Tokens alone are not a bound: a run that produces nothing burns few and
	// would otherwise never stop.
	o, now := armed(t)
	var why string
	for i := 0; i < OvernightMaxTurns+5; i++ {
		var ok bool
		ok, why = o.ShouldContinue(now, fmt.Sprintf("%d/999 done", i), 10)
		if !ok {
			break
		}
	}
	if o.Active {
		t.Fatal("ran past the turn cap")
	}
	if !strings.Contains(why, "turn cap") {
		t.Errorf("why = %q", why)
	}
}

func TestOvernightStopsAtItsDeadline(t *testing.T) {
	// "Overnight" is a wall-clock promise to whoever started it.
	o, now := armed(t)
	ok, why := o.ShouldContinue(now.Add(OvernightHours*time.Hour+time.Minute), "1/9 done", 10)
	if ok {
		t.Fatal("ran past its deadline")
	}
	if !strings.Contains(why, "time") {
		t.Errorf("why = %q", why)
	}
}

func TestOvernightStopsWhenTheListStopsMoving(t *testing.T) {
	// The breaker that matters: the others bound the damage, this one notices
	// the model is answering rather than working. It happened for real with
	// auto-poke.
	o, now := armed(t)
	var why string
	for i := 0; i <= OvernightMaxStalls+1; i++ {
		var ok bool
		ok, why = o.ShouldContinue(now, "2/9 done", 10)
		if !ok {
			break
		}
	}
	if o.Active && !strings.Contains(why, "not moved") {
		t.Fatalf("kept poking a list that never moved: %q", why)
	}
	if !strings.Contains(why, "not moved") {
		t.Errorf("why = %q", why)
	}
}

func TestOvernightStallCounterResetsOnProgress(t *testing.T) {
	// A turn that closes something clears the count, or a slow run that is
	// nonetheless working would be cut off.
	o, now := armed(t)
	o.ShouldContinue(now, "2/9 done", 10)
	o.ShouldContinue(now, "2/9 done", 10)
	o.ShouldContinue(now, "3/9 done", 10)
	if o.stalled != 0 {
		t.Errorf("stalled = %d after progress", o.stalled)
	}
}

func TestOvernightInertWhenNotArmed(t *testing.T) {
	var o Overnight
	if ok, _ := o.ShouldContinue(time.Now(), "1/9 done", 0); ok {
		t.Error("an unarmed loop asked for another turn")
	}
}

func TestOvernightStopsWithNoList(t *testing.T) {
	o, now := armed(t)
	if ok, why := o.ShouldContinue(now, "", 10); ok || !strings.Contains(why, "no todo") {
		t.Errorf("ok=%v why=%q, want a stop explaining there is nothing to do", ok, why)
	}
}

func TestIsCompleteReadsTheSummary(t *testing.T) {
	cases := map[string]bool{
		"9/9 done":  true,
		"3/9 done":  false,
		"0/0 done":  false,
		"nonsense":  false,
		"10/9 done": true,
	}
	for in, want := range cases {
		if got := isComplete(in); got != want {
			t.Errorf("isComplete(%q) = %v, want %v", in, got, want)
		}
	}
}

func TestOvernightStatusSaysWhyItStopped(t *testing.T) {
	// Waking up to a stopped loop and not knowing why is useless.
	o, now := armed(t)
	o.ShouldContinue(now, "9/9 done", 5000)
	o.Stop("the todo list is finished")
	got := o.Status()
	if !strings.Contains(got, "finished") {
		t.Errorf("status = %q", got)
	}
}

func TestOvernightPromptForbidsQuestions(t *testing.T) {
	// Nobody is at the keyboard, so a question is a run that stalls until
	// morning having done nothing.
	low := strings.ToLower(OvernightPrompt)
	if !strings.Contains(low, "question") {
		t.Errorf("the prompt does not address questions:\n%s", OvernightPrompt)
	}
}
