package daemon

import (
	"context"
	"sync"
	"testing"
)

// contextFor builds a cancellable context the test cleans up.
func contextFor(t *testing.T) (context.Context, context.CancelFunc) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	return ctx, cancel
}

// H2.19: two attached clients interrupting each other cancel their own turns.
//
// The property that matters is that an interrupt cannot reach across into
// another session's turn — cancel is per-session state, and it used to be
// written from four paths with no lock.
func TestInterruptingOneSessionLeavesTheOtherRunning(t *testing.T) {
	srv, _ := testServer(t)
	defer srv.Close()

	one, err := srv.Open("")
	if err != nil {
		t.Fatal(err)
	}
	two, err := srv.Open("")
	if err != nil {
		t.Fatal(err)
	}

	// Reserve a turn on each, as Input does.
	ctxOne, cancelOne := contextFor(t)
	ctxTwo, cancelTwo := contextFor(t)
	if _, ok := one.beginTurn(cancelOne); !ok {
		t.Fatal("session one refused a turn")
	}
	if _, ok := two.beginTurn(cancelTwo); !ok {
		t.Fatal("session two refused a turn")
	}

	one.Interrupt("", false)

	if ctxOne.Err() == nil {
		t.Error("interrupting a session did not cancel its own turn")
	}
	if ctxTwo.Err() != nil {
		t.Error("interrupting one session cancelled another session's turn")
	}
}

// H2.19: a swarm at the worker cap stays at the cap under concurrent /summon.
func TestSwarmStaysAtItsCapUnderConcurrentSummon(t *testing.T) {
	srv, _ := testServer(t)
	defer srv.Close()

	spawner, err := srv.Open("")
	if err != nil {
		t.Fatal(err)
	}
	held := holdWorkers(t, srv)
	defer held()

	var wg sync.WaitGroup
	for range MaxLiveWorkers * 5 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, _ = srv.SpawnFor(spawner.Name, "busywork", nil, nil)
		}()
	}
	wg.Wait()

	srv.swarm.mu.Lock()
	live := srv.swarm.live
	srv.swarm.mu.Unlock()
	if live > MaxLiveWorkers {
		t.Errorf("%d workers admitted against a cap of %d", live, MaxLiveWorkers)
	}
	if n := srv.liveWorkers(); n > MaxLiveWorkers {
		t.Errorf("%d workers are actually live, past the %d cap", n, MaxLiveWorkers)
	}
}
