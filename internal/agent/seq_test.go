package agent

import (
	"sync"
	"testing"

	"evilcode/internal/provider"
)

// H2.8: a.seq++ was unsynchronized. The daemon's conflict delivery calls Notice
// from the pump goroutine while the turn emits from its own, so two events can
// take the same sequence number — and the sequence is what a reattaching client
// uses to work out what it missed.
//
// Run with -race.
func TestEventSequenceIsUniqueUnderConcurrentEmitters(t *testing.T) {
	a := New("dracula", provider.NewMock("mock", "chat"), "mock-large", nil,
		NewConversation("system"))
	t.Cleanup(a.Close)

	const emitters, each = 8, 50
	seen := make(chan int, emitters*each)
	var wg sync.WaitGroup
	go func() {
		for range a.Events() {
		}
	}()
	for range emitters {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for range each {
				seen <- a.newEvent(EventNotice).Seq
			}
		}()
	}
	wg.Wait()
	close(seen)

	taken := map[int]bool{}
	for n := range seen {
		if taken[n] {
			t.Fatalf("sequence %d was handed out twice", n)
		}
		taken[n] = true
	}
	if len(taken) != emitters*each {
		t.Errorf("%d distinct sequence numbers for %d events", len(taken), emitters*each)
	}
}
