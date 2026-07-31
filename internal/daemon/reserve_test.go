package daemon

import (
	"context"
	"sync"
	"testing"
)

// H2.2: Running() and starting a turn were two operations. A turn's goroutine
// sets Running, so for the first instants after a start the session still looks
// idle — long enough for a second client to check, see idle, and launch a
// second Run against one conversation.
func TestOnlyOneTurnCanBeReservedAtATime(t *testing.T) {
	srv, _ := testServer(t)
	defer srv.Close()

	sess, err := srv.Open("")
	if err != nil {
		t.Fatal(err)
	}

	const racers = 16
	var wg sync.WaitGroup
	var mu sync.Mutex
	var won int
	start := make(chan struct{})
	for range racers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			_, cancel := context.WithCancel(context.Background())
			defer cancel()
			if _, ok := sess.beginTurn(cancel); ok {
				mu.Lock()
				won++
				mu.Unlock()
			}
		}()
	}
	close(start)
	wg.Wait()

	if won != 1 {
		t.Errorf("%d of %d racers each started a turn on one session, want 1", won, racers)
	}

	// And the reservation is released, so the session is usable afterwards.
	sess.endTurn()
	_, cancel := context.WithCancel(context.Background())
	defer cancel()
	if _, ok := sess.beginTurn(cancel); !ok {
		t.Error("the session refused a turn after the previous one ended")
	}
}
