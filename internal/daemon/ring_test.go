package daemon

import (
	"strings"
	"testing"

	"evilcode/internal/agent"
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
