package daemon

import (
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// H2.4: the global and per-session worker limits were checked and then acted
// on, with nothing reserving in between. Concurrent spawns all read the same
// count, all pass, and all start — so the two breakers that bound how much a
// swarm can spend are advisory under exactly the load they exist for.
//
// The worker turns are paced with the mock's stream delay so a spawn's turn is
// genuinely in flight while the burst races the admission gate. The old helper
// re-armed finished sessions' done channels instead, which resurrected
// terminal workers into live-looking state and double-released reservations —
// the count it was holding steady was the count it corrupted (R2-06).
func TestConcurrentSpawnsStayUnderTheLiveLimit(t *testing.T) {
	srv, _ := testServer(t)
	defer srv.Close()

	t.Setenv("EVILCODE_MOCK_STREAM_DELAY", "40ms")

	spawner, err := srv.Open("")
	if err != nil {
		t.Fatal(err)
	}

	stop, maxLive := sampleLive(t, srv)
	defer close(stop)

	var wg sync.WaitGroup
	for range MaxLiveWorkers * 4 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, _ = srv.SpawnFor(spawner.Name, "busywork", nil, nil)
		}()
	}
	wg.Wait()

	if got := atomic.LoadInt64(maxLive); got > MaxLiveWorkers {
		t.Errorf("%d workers were live at once, past the %d limit", got, MaxLiveWorkers)
	}
	waitReservationsDrained(t, srv)
}

// sampleLive polls the swarm's live-reservation counter until the returned
// stop channel closes and reports the highest value it saw. The admission gate
// is the thing the cap protects, so the test watches the counter itself rather
// than reconstructing liveness from sessions after the fact.
func sampleLive(t *testing.T, srv *Server) (chan struct{}, *int64) {
	t.Helper()
	var maxLive int64
	stop := make(chan struct{})
	go func() {
		for {
			select {
			case <-stop:
				return
			default:
			}
			srv.swarm.mu.Lock()
			n := srv.swarm.live
			srv.swarm.mu.Unlock()
			for {
				old := atomic.LoadInt64(&maxLive)
				if int64(n) <= old || atomic.CompareAndSwapInt64(&maxLive, old, int64(n)) {
					break
				}
			}
			time.Sleep(2 * time.Millisecond)
		}
	}()
	return stop, &maxLive
}

// waitReservationsDrained pins the terminal lifecycle: every worker eventually
// marks itself finished and returns its reservation.
func waitReservationsDrained(t *testing.T, srv *Server) {
	t.Helper()
	deadline := time.Now().Add(15 * time.Second)
	for {
		srv.swarm.mu.Lock()
		live := srv.swarm.live
		srv.swarm.mu.Unlock()
		if live == 0 && srv.liveWorkers() == 0 {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("reservations never drained: %d live, %d unfinished sessions", live, srv.liveWorkers())
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// H2.4, the per-session half: the same race against MaxWorkersPerSession.
func TestConcurrentSpawnsStayUnderThePerSessionLimit(t *testing.T) {
	srv, _ := testServer(t)
	defer srv.Close()

	spawner, err := srv.Open("")
	if err != nil {
		t.Fatal(err)
	}

	var wg sync.WaitGroup
	for range MaxWorkersPerSession * 3 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, _ = srv.SpawnFor(spawner.Name, "busywork", nil, nil)
		}()
	}
	wg.Wait()

	srv.swarm.mu.Lock()
	used := srv.swarm.spawnCount[spawner.Name]
	srv.swarm.mu.Unlock()
	if used > MaxWorkersPerSession {
		t.Errorf("the session spawned %d workers, past the %d limit", used, MaxWorkersPerSession)
	}
}

// The old holdWorkers helper was deleted with R2-06: it re-armed finished
// sessions' done channels (closedDone=true → false, fresh channel), which made
// terminal workers look live and let cleanup's markFinished release the same
// reservation twice. Both tests now pace worker turns with the mock stream
// delay and sample the swarm's live counter directly.
