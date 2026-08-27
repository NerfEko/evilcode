package daemon

import (
	"encoding/json"
	"sync"

	"evilcode/internal/agent"
	"evilcode/internal/provider"
)

// RingSize is how many events a session keeps for reconnect replay.
//
// The JSONL session file is the source of truth (plan.md §20); this only has to
// cover the gap between a client dropping and reconnecting. A few thousand
// events is a long turn's worth of streaming deltas, which is the realistic
// worst case for "my terminal died mid-answer".
const RingSize = 4096

// RingMaxBytes bounds the ring's total payload. Events carry tool output,
// diffs, display payloads, and image bytes, so a count cap alone can retain
// gigabytes when a session runs many large tool calls (D2). The budget keeps
// the newest events — what a reconnecting client actually needs — and drops
// the oldest until the total fits.
const RingMaxBytes = 16 << 20

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
	bytes int // approximate payload bytes currently retained
}

// NewRing builds an empty ring.
func NewRing() *Ring {
	return &Ring{buf: make([]agent.Event, RingSize), seqs: make([]int, RingSize)}
}

// Add appends an event and returns its sequence number. When the byte budget
// is exceeded the oldest retained events are dropped (the newest always
// survives), so a session streaming many large tool results cannot grow the
// ring's memory without limit.
func (r *Ring) Add(e agent.Event) int {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.seq++
	e.Seq = r.seq
	// Once the buffer has wrapped, the slot about to be replaced still holds
	// an event whose bytes are part of r.bytes. Forgetting to subtract it
	// made the retained total grow by the size of every event that ever sat
	// in the slot, so the ring evicted far more history than the budget
	// requires (R2-02).
	replaced := agent.Event{}
	if r.count == len(r.buf) {
		replaced = r.buf[r.next]
	}
	r.buf[r.next] = e
	r.seqs[r.next] = r.seq
	r.bytes += eventBytes(e) - eventBytes(replaced)
	r.next = (r.next + 1) % len(r.buf)
	if r.count < len(r.buf) {
		r.count++
	}
	for r.count > 1 && r.bytes > RingMaxBytes {
		oldest := (r.next - r.count + len(r.buf)) % len(r.buf)
		r.bytes -= eventBytes(r.buf[oldest])
		r.buf[oldest] = agent.Event{}
		r.seqs[oldest] = 0
		r.count--
	}
	return r.seq
}

// Bytes is the ring's own estimate of the payload it currently retains.
func (r *Ring) Bytes() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.bytes
}

// eventBytes approximates how much memory an event's payload retains: its
// text fields, image bytes, the JSON encoding of its display payload, and the
// bulk fields a reconnecting client resyncs from — the turn-end history copy
// (the largest field a turn-end carries), tool-call arguments, ask payloads,
// and background state. Undercounting any of them let the ring hold several
// times its budget without ever evicting (R2-02). Seq/Session/Call pointers
// are shared and counted once at the struct level.
func eventBytes(e agent.Event) int {
	n := len(e.Text) + len(e.Output) + len(e.Diff) + len(e.Intent) + len(e.Session) + len(e.RequestID)
	for _, img := range e.Images {
		n += len(img)
	}
	for _, m := range e.SnapshotMessages {
		n += messageBytes(m)
	}
	if e.Call != nil {
		n += len(e.Call.ID) + len(e.Call.Name) + len(e.Call.Args)
	}
	if e.Display != nil {
		if b, err := json.Marshal(e.Display); err == nil {
			n += len(b)
		}
	}
	if e.Ask != nil {
		if b, err := json.Marshal(*e.Ask); err == nil {
			n += len(b)
		}
	}
	if e.Background != nil {
		if b, err := json.Marshal(*e.Background); err == nil {
			n += len(b)
		}
	}
	return n
}

// messageBytes is the conservative deep size of one conversation message: the
// fields the daemon actually retains on it.
func messageBytes(m provider.Message) int {
	n := len(m.Content) + len(m.Reasoning) + len(m.ToolCallID) + len(m.ToolName)
	for _, img := range m.Images {
		n += len(img)
	}
	for _, c := range m.ToolCalls {
		n += len(c.ID) + len(c.Name) + len(c.Args)
	}
	for _, item := range m.ProviderItems {
		n += len(item)
	}
	for _, r := range m.Repairs {
		n += len(r)
	}
	return n
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
