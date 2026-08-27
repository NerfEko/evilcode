package daemon

import (
	"encoding/json"
	"strings"
	"testing"

	"evilcode/internal/agent"
	"evilcode/internal/provider"
)

// D2: the ring's byte budget must evict the oldest events when a session
// streams many large tool results, so the retained payload cannot grow
// without limit even though every single event fits the count cap.
func TestRingDropsOldestWhenOverByteBudget(t *testing.T) {
	r := NewRing()
	big := strings.Repeat("x", RingMaxBytes/2)

	r.Add(agent.Event{Text: "first"})
	r.Add(agent.Event{Text: big})
	r.Add(agent.Event{Text: big})
	r.Add(agent.Event{Text: "last"})

	// "first" is evicted as soon as the third event overflows the budget; the
	// newest two survive.
	got, _ := r.Since(0)
	if len(got) != 2 {
		t.Fatalf("ring holds %d events, want the newest 2 (oldest evicted)", len(got))
	}
	if got[0].Text != big || got[1].Text != "last" {
		t.Fatalf("retained events = %q..%q, want big..last", textLen(got[0]), textLen(got[1]))
	}
}

// D2: retained payload bytes stay within the budget (allowing one event that
// alone exceeds it — the newest always survives).
func TestRingByteBudgetHoldsAcrossManyEvents(t *testing.T) {
	r := NewRing()
	big := strings.Repeat("y", RingMaxBytes/2)
	for i := 0; i < 20; i++ {
		r.Add(agent.Event{Text: big})
	}
	var sum int
	got, _ := r.Since(0)
	for _, e := range got {
		sum += len(e.Text)
	}
	if sum > RingMaxBytes+len(big) {
		t.Errorf("retained %d bytes, want at most budget + one oversized event (%d)", sum, RingMaxBytes+len(big))
	}
	if len(got) == 0 {
		t.Error("the newest event must always survive the eviction")
	}
	// Sequence numbers stay monotonic regardless of evictions.
	if _, seq := r.Since(0); seq != 20 {
		t.Errorf("seq = %d, want 20", seq)
	}
}

// A single event larger than the whole budget is still kept: dropping the
// only event would leave a reconnecting client with nothing.
func TestRingKeepsSingleOversizedEvent(t *testing.T) {
	r := NewRing()
	huge := strings.Repeat("z", RingMaxBytes*2)
	r.Add(agent.Event{Text: huge})
	got, _ := r.Since(0)
	if len(got) != 1 || got[0].Text != huge {
		t.Fatalf("single oversized event was dropped: %d events retained", len(got))
	}
}

func textLen(e agent.Event) string {
	if len(e.Text) > 8 {
		return e.Text[:8] + "…"
	}
	return e.Text
}

// R2-02: wrapping must subtract the replaced slot's bytes. Before the fix the
// retained total grew by the size of every event that ever sat in a slot, so
// past RingSize the ring evicted history the budget still had room for.
func TestRingWrapAccountsForReplacedEvents(t *testing.T) {
	r := NewRing()
	one := strings.Repeat("a", 1024)
	r.Add(agent.Event{Text: one}) // seeds the account without wrap
	for i := 0; i < RingSize+64; i++ {
		r.Add(agent.Event{Text: one})
	}
	// Every slot has been overwritten at least once; the retained total must
	// be exactly the payload of the RingSize events still held.
	if got, want := r.Bytes(), RingSize*1024; got != want {
		t.Fatalf("ring accounts %d bytes, want %d (%d events)", got, want, r.Len())
	}
	if r.Len() != RingSize {
		t.Fatalf("ring holds %d events, want %d", r.Len(), RingSize)
	}
}

// R2-02: the deep fields — the turn-end history copy above all — count toward
// the budget. A ring fed only turn-end events must evict instead of retaining
// several budgets' worth of uncounted history.
func TestRingCountsSnapshotHistoryTowardBudget(t *testing.T) {
	r := NewRing()
	history := []provider.Message{
		{Role: provider.RoleUser, Content: strings.Repeat("h", RingMaxBytes/2+1)},
	}
	r.Add(agent.Event{Kind: agent.EventTurnEnd, SnapshotMessages: history})
	r.Add(agent.Event{Kind: agent.EventTurnEnd, SnapshotMessages: history})

	if got := r.Bytes(); got > RingMaxBytes+RingMaxBytes/2 {
		t.Fatalf("ring retains %d bytes; the history copy was not counted", got)
	}
	// The newest event survives; the first was evicted under the budget.
	got, _ := r.Since(0)
	if len(got) != 1 || len(got[0].SnapshotMessages) != 1 {
		t.Fatalf("retained %d events, want the newest turn end only", len(got))
	}
}

// R2-02: tool-call arguments, ask payloads, and background state are retained
// memory too, so they count.
func TestRingCountsToolArgsAndAskPayloads(t *testing.T) {
	r := NewRing()
	args := json.RawMessage(strings.Repeat("a", 1<<20))
	r.Add(agent.Event{Call: &provider.ToolCall{ID: "1", Name: "edit", Args: args}})
	if got := r.Bytes(); got < 1<<20 {
		t.Fatalf("tool args not counted: %d bytes", got)
	}
	r.Add(agent.Event{Ask: &agent.AskEvent{Question: strings.Repeat("q", 1<<20)}})
	if got := r.Bytes(); got < 2<<20 {
		t.Fatalf("ask payload not counted: %d bytes", got)
	}
}
