package daemon

import (
	"sync"

	"evilcode/internal/agent"
)

// RingSize is how many events a session keeps for reconnect replay.
//
// The JSONL session file is the source of truth (plan.md §20); this only has to
// cover the gap between a client dropping and reconnecting. A few thousand
// events is a long turn's worth of streaming deltas, which is the realistic
// worst case for "my terminal died mid-answer".
const RingSize = 4096

// Ring is a fixed-size event buffer with sequence numbers.
//
// Events carry their own Seq from the agent, but the ring assigns its own on
// top: agent sequences restart per agent, and a client reconnecting needs a
// number that is monotonic for the session as a whole.
type Ring struct {
	mu    sync.Mutex
	buf   []agent.Event
	seqs  []int
	next  int // write position
	count int
	seq   int
}

// NewRing builds an empty ring.
func NewRing() *Ring {
	return &Ring{buf: make([]agent.Event, RingSize), seqs: make([]int, RingSize)}
}

// Add appends an event and returns its sequence number.
func (r *Ring) Add(e agent.Event) int {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.seq++
	r.buf[r.next] = e
	r.seqs[r.next] = r.seq
	r.next = (r.next + 1) % len(r.buf)
	if r.count < len(r.buf) {
		r.count++
	}
	return r.seq
}

// Seq is the newest sequence number.
func (r *Ring) Seq() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.seq
}

// Since returns every retained event after seq, oldest first, with the sequence
// number of the last one.
//
// A client that asks for a sequence the ring has already overwritten gets
// everything still held rather than an error: the events are gone either way,
// and refusing to replay leaves the client with nothing at all.
func (r *Ring) Since(seq int) ([]agent.Event, int) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.count == 0 {
		return nil, r.seq
	}

	start := (r.next - r.count + len(r.buf)) % len(r.buf)
	var out []agent.Event
	for i := 0; i < r.count; i++ {
		idx := (start + i) % len(r.buf)
		if r.seqs[idx] > seq {
			out = append(out, r.buf[idx])
		}
	}
	return out, r.seq
}

// SinceLastTurn returns the events of the turn currently in flight, or nothing
// when the session is idle.
//
// This is what a fresh attach replays. The snapshot already carries every
// completed message, so replaying the deltas that produced them would render
// the whole conversation twice — which is exactly what it did before this
// existed. Only a partially streamed turn is missing from the snapshot, because
// its message is not committed to the conversation until it ends.
func (r *Ring) SinceLastTurn() []agent.Event {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.count == 0 {
		return nil
	}

	start := (r.next - r.count + len(r.buf)) % len(r.buf)
	// Walk back to the newest TurnStart that has no TurnEnd after it.
	from := -1
	for i := r.count - 1; i >= 0; i-- {
		idx := (start + i) % len(r.buf)
		if r.buf[idx].Kind == agent.EventTurnEnd {
			return nil
		}
		if r.buf[idx].Kind == agent.EventTurnStart {
			from = i
			break
		}
	}
	if from < 0 {
		return nil
	}

	var out []agent.Event
	for i := from; i < r.count; i++ {
		out = append(out, r.buf[(start+i)%len(r.buf)])
	}
	return out
}

// Len is how many events the ring currently holds.
func (r *Ring) Len() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.count
}
