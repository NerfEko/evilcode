package tui

import (
	"strings"
	"testing"
	"time"

	"evilcode/internal/agent"
	"evilcode/internal/todo"
)

// H1.12: a turn end called stepOvernight twice. Every finished turn counted as
// two against the turn cap — halving it — and both calls could pass
// ShouldContinue and each submit a continuation, putting two agent.Run
// goroutines on one agent.
func TestOneTurnCountsOnce(t *testing.T) {
	m := newTestModel(t).WithTodos(workingList(t), nil)
	m.overnight.Start(time.Now())
	before := m.overnight.Turns

	m.applyEvent(agent.Event{Kind: agent.EventTurnEnd, Reason: agent.EndComplete})

	if got := m.overnight.Turns - before; got != 1 {
		t.Errorf("one turn advanced the counter by %d, want 1", got)
	}
}

// H1.13: the turn's tokens were read after the status line had been reset, so
// the number was always zero, and ShouldContinue assigned it rather than adding
// it. The budget breaker could not fire.
func TestOvernightAccumulatesTokensAcrossTurns(t *testing.T) {
	o, now := armed(t)
	for range 3 {
		if ok, why := o.ShouldContinue(now, moving(o.Turns), 1000); !ok {
			t.Fatalf("stopped early: %s", why)
		}
	}
	if o.Tokens != 3000 {
		t.Errorf("Tokens = %d after three 1000-token turns, want 3000", o.Tokens)
	}
}

func TestOvernightBudgetBreakerActuallyFires(t *testing.T) {
	o, now := armed(t)
	perTurn := OvernightBudget/4 + 1
	var why string
	for i := range OvernightMaxTurns {
		var ok bool
		ok, why = o.ShouldContinue(now, moving(i), perTurn)
		if !ok {
			break
		}
	}
	if !strings.Contains(why, "budget") {
		t.Errorf("stopped for %q after spending %d of a %d budget, want the budget breaker",
			why, o.Tokens, OvernightBudget)
	}
}

// H1.13 again, through the model: the tokens a turn spent must survive the
// status reset that happens in the same event.
func TestTurnEndReportsTheTurnsTokens(t *testing.T) {
	m := newTestModel(t).WithTodos(workingList(t), nil)
	m.overnight.Start(time.Now())
	m.status.TokensIn, m.status.TokensOut = 700, 300

	m.applyEvent(agent.Event{Kind: agent.EventTurnEnd, Reason: agent.EndComplete})

	if m.overnight.Tokens != 1000 {
		t.Errorf("overnight recorded %d tokens for a 1000-token turn", m.overnight.Tokens)
	}
}

// workingList is a todo store with unfinished work in it, which is what keeps
// the overnight loop from halting for reasons the test is not about.
func workingList(t *testing.T) *todo.Store {
	t.Helper()
	store, err := todo.NewStore(t.TempDir(), "session")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.Apply(todo.Write{Items: []todo.Item{
		{Content: "wire the auth flow", Status: todo.StatusPending},
		{Content: "add the retry gate", Status: todo.StatusPending},
	}}); err != nil {
		t.Fatal(err)
	}
	return store
}

// moving returns a todo summary that differs every turn, so the stall detector
// stays out of the way of whatever the test is actually measuring.
func moving(n int) string {
	return strings.Repeat("x", n%7) + "1/99 done"
}
