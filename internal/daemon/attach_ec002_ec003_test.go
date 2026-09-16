package daemon

import (
	"strings"
	"testing"
	"time"

	"evilcode/internal/agent"
)

// TestAttachBoundaryBeforeDuringAfter proves EC-002 exactly-once delivery for
// the three boundary positions relative to subscribe-then-snapshot.
//
//   - before: published before subscribe, covered by replay, never queued live.
//   - during: published after subscribe but at/below highWater, present in both
//     ring and live queue; the live copy must be discarded.
//   - after: published after highWater, absent from bounded replay, delivered live.
//
// The test drives the helper contract directly so the outcome is deterministic
// rather than racing a real attach window.
func TestAttachBoundaryBeforeDuringAfter(t *testing.T) {
	srv, _ := testServer(t)
	defer srv.Close()
	sess, err := srv.Open("")
	if err != nil {
		t.Fatal(err)
	}

	seqA := publish(t, sess, "before-A")
	seqB := publish(t, sess, "before-B")

	// Subscribe, then land one event before the high-water capture so it sits
	// in both the ring and the live queue (the EC-002 duplicate).
	sub := sess.subscribe()
	seqDuring := publish(t, sess, "during")
	highWater := sess.ring.Seq()
	if seqDuring > highWater {
		t.Fatalf("during seq %d above highWater %d", seqDuring, highWater)
	}
	// Land one event after highWater: live only, never in bounded replay.
	seqAfter := publish(t, sess, "after")

	// Bounded replay as attachSubscribe computes it for since=seqA.
	replay, _ := sess.ring.Since(seqA)
	var bounded []agent.Event
	for _, e := range replay {
		if e.Seq <= highWater {
			bounded = append(bounded, e)
		}
	}
	gotReplay := map[int]string{}
	for _, e := range bounded {
		gotReplay[e.Seq] = e.Text
	}
	if _, ok := gotReplay[seqA]; ok {
		t.Errorf("replay includes since=%d itself, want strictly after", seqA)
	}
	if gotReplay[seqB] != "before-B" {
		t.Errorf("replay missing before-B (seq %d): %v", seqB, gotReplay)
	}
	if gotReplay[seqDuring] != "during" {
		t.Errorf("replay missing during (seq %d): %v", seqDuring, gotReplay)
	}
	if _, ok := gotReplay[seqAfter]; ok {
		t.Errorf("replay includes after (seq %d) above highWater %d", seqAfter, highWater)
	}

	// Drain the live queue without blocking.
	var live []ServerMsg
drain:
	for {
		select {
		case m := <-sub.ch:
			live = append(live, m)
		default:
			break drain
		}
	}
	// The live queue holds only post-subscribe broadcasts: during + after.
	// before-* were broadcast before subscribe and must never appear live.
	for _, m := range live {
		if m.Kind == MsgEvent && m.Event != nil {
			if m.Event.Seq == seqA || m.Event.Seq == seqB {
				t.Errorf("live queue holds pre-subscribe seq %d, want only post-subscribe", m.Event.Seq)
			}
		}
	}
	// Apply the relay filter.
	var kept []ServerMsg
	for _, m := range live {
		if !isLiveDuplicate(m, highWater) {
			kept = append(kept, m)
		}
	}
	keptSeqs := map[int]bool{}
	for _, m := range kept {
		if m.Kind == MsgEvent && m.Event != nil {
			if keptSeqs[m.Event.Seq] {
				t.Errorf("filtered live holds duplicate seq %d", m.Event.Seq)
			}
			keptSeqs[m.Event.Seq] = true
		}
	}
	if keptSeqs[seqDuring] {
		t.Errorf("live duplicate seq %d (during) was not discarded", seqDuring)
	}
	if !keptSeqs[seqAfter] {
		t.Errorf("live seq %d (after) was discarded, want it delivered", seqAfter)
	}

	// Union of bounded replay and filtered live holds each post-since seq once.
	for _, seq := range []int{seqB, seqDuring, seqAfter} {
		inReplay := false
		for _, e := range bounded {
			if e.Seq == seq {
				inReplay = true
			}
		}
		inLive := keptSeqs[seq]
		if seq == seqAfter {
			if inReplay || !inLive {
				t.Errorf("seq %d: replay=%v live=%v, want live only", seq, inReplay, inLive)
			}
			continue
		}
		if !inReplay || inLive {
			t.Errorf("seq %d: replay=%v live=%v, want replay only", seq, inReplay, inLive)
		}
	}
	sess.unsubscribe(sub)
}

// TestAttachLiveDuplicatePreservesNonEvents proves the EC-002 filter never
// discards snapshots, errors, unsequenced events, or nil payloads: only
// ring-sequenced MsgEvents at/below highWater qualify.
func TestAttachLiveDuplicatePreservesNonEvents(t *testing.T) {
	ev := &agent.Event{Kind: agent.EventTextDelta, Text: "x"}
	ev.Seq = 5
	dup := ServerMsg{Kind: MsgEvent, Event: ev}
	if !isLiveDuplicate(dup, 5) {
		t.Error("Seq==highWater must count as duplicate")
	}
	if !isLiveDuplicate(dup, 6) {
		t.Error("Seq<highWater must count as duplicate")
	}
	if isLiveDuplicate(dup, 4) {
		t.Error("Seq>highWater must not count as duplicate")
	}
	// Snapshots always pass, even with an old-looking snapshot seq.
	snap := ServerMsg{Kind: MsgSnapshot, Snapshot: &Snapshot{Session: "s", Seq: 1}}
	if isLiveDuplicate(snap, 100) {
		t.Error("snapshot must never be discarded as a duplicate")
	}
	// Non-event kinds always pass.
	other := ServerMsg{Kind: MsgSessions}
	if isLiveDuplicate(other, 100) {
		t.Error("non-event frame must never be discarded")
	}
	// Unsequenced and nil events always pass.
	zero := ServerMsg{Kind: MsgEvent, Event: &agent.Event{Kind: agent.EventNotice, Text: "preview"}}
	if isLiveDuplicate(zero, 100) {
		t.Error("unsequenced event (Seq 0) must never be discarded")
	}
	nilEv := ServerMsg{Kind: MsgEvent}
	if isLiveDuplicate(nilEv, 100) {
		t.Error("nil event must never be discarded")
	}
	if isLiveDuplicate(dup, 0) {
		t.Error("empty ring (highWater 0) must discard nothing")
	}
}

// TestAttachConcurrentPublishExactlyOnce hammers the real attachSubscribe
// helper while a publisher floods the ring, then verifies the EC-002
// invariant deterministically: replay never exceeds highWater, and the union
// of replay plus filtered live holds every sequence in (since, latest]
// exactly once with no gap and no duplicate.
func TestAttachConcurrentPublishExactlyOnce(t *testing.T) {
	srv, _ := testServer(t)
	defer srv.Close()
	sess, err := srv.Open("")
	if err != nil {
		t.Fatal(err)
	}

	base := publish(t, sess, "base")
	const n = 200
	publishing := make(chan struct{})
	go func() {
		defer close(publishing)
		for i := 0; i < n; i++ {
			sess.publishEvent(agent.Event{Kind: agent.EventTextDelta, Text: strings.Repeat("y", 16)})
		}
	}()

	sub, replay, highWater := sess.attachSubscribe(base)
	defer sess.unsubscribe(sub)
	<-publishing
	latest := sess.ring.Seq()

	for _, e := range replay {
		if e.Seq <= base {
			t.Fatalf("replay holds seq %d at/below since=%d", e.Seq, base)
		}
		if e.Seq > highWater {
			t.Fatalf("replay holds seq %d above highWater %d", e.Seq, highWater)
		}
	}
	// Drain what the live queue still holds, then filter duplicates.
	var live []ServerMsg
drain:
	for {
		select {
		case m := <-sub.ch:
			live = append(live, m)
		default:
			break drain
		}
	}
	seen := map[int]int{}
	for _, e := range replay {
		seen[e.Seq]++
	}
	for _, m := range live {
		if isLiveDuplicate(m, highWater) {
			continue
		}
		if m.Kind == MsgEvent && m.Event != nil {
			seen[m.Event.Seq]++
		}
	}
	for seq := base + 1; seq <= latest; seq++ {
		if seen[seq] != 1 {
			t.Fatalf("seq %d delivered %d times in (since=%d, latest=%d], want exactly once", seq, seen[seq], base, latest)
		}
	}
}

// TestSlowClientOverflowEvictsOnce proves EC-003 at the broadcast layer: the
// first full send evicts the subscription and closes done without blocking
// the publisher, Clients decrements once, a second unsubscribe is a no-op
// that leaves the idle countdown untouched, and publishing stays nonblocking
// afterwards. The buffer is not enlarged and publishEvent never blocks.
func TestSlowClientOverflowEvictsOnce(t *testing.T) {
	srv, _ := testServer(t)
	defer srv.Close()
	sess, err := srv.Open("")
	if err != nil {
		t.Fatal(err)
	}

	sub := sess.subscribe()
	// Fill without ever reading: 256 slots, so 512 publishes guarantee overflow
	// even if the runtime is fast.
	for i := 0; i < 512; i++ {
		sess.publishEvent(agent.Event{Kind: agent.EventTextDelta, Text: "slow"})
	}
	select {
	case <-sub.done:
	default:
		t.Fatal("overflow did not close the subscription done signal")
	}

	sess.mu.Lock()
	n := len(sess.subs)
	idleBefore := sess.idleSince
	sess.mu.Unlock()
	if n != 0 {
		t.Fatalf("subs = %d after overflow, want 0 (evicted)", n)
	}
	if idleBefore.IsZero() {
		t.Fatal("idle countdown did not start when the last subscriber overflowed")
	}
	if got := len(srv.Sessions()); got != 1 {
		t.Fatalf("sessions = %d, want 1", got)
	}
	for _, info := range srv.Sessions() {
		if info.Name == sess.Name && info.Clients != 0 {
			t.Errorf("Clients = %d after overflow, want 0", info.Clients)
		}
	}

	// A second unsubscribe must be a no-op: Clients stays 0 and idleSince keeps
	// its first value so the watchdog interval is not extended.
	sess.unsubscribe(sub)
	sess.mu.Lock()
	n2 := len(sess.subs)
	idleAfter := sess.idleSince
	sess.mu.Unlock()
	if n2 != 0 {
		t.Errorf("subs = %d after second unsubscribe, want 0", n2)
	}
	if !idleAfter.Equal(idleBefore) {
		t.Errorf("second unsubscribe moved idleSince from %v to %v, want unchanged", idleBefore, idleAfter)
	}

	// The pump must never block on an evicted (or absent) subscriber.
	done := make(chan struct{})
	go func() {
		defer close(done)
		for i := 0; i < 200; i++ {
			sess.publishEvent(agent.Event{Kind: agent.EventTextDelta, Text: "after"})
		}
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("publish stalled after overflow")
	}
}
