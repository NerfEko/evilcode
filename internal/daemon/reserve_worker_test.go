package daemon

import (
	"sync"
	"testing"
)

// H2.4: the global and per-session worker limits were checked and then acted
// on, with nothing reserving in between. Concurrent spawns all read the same
// count, all pass, and all start — so the two breakers that bound how much a
// swarm can spend are advisory under exactly the load they exist for.
func TestConcurrentSpawnsStayUnderTheLiveLimit(t *testing.T) {
	srv, _ := testServer(t)
	defer srv.Close()

	spawner, err := srv.Open("")
	if err != nil {
		t.Fatal(err)
	}

	// Workers that never finish, so every spawn counts against the live cap for
	// the whole test.
	held := holdWorkers(t, srv)
	defer held()

	var wg sync.WaitGroup
	for range MaxLiveWorkers * 4 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, _ = srv.SpawnFor(spawner.Name, "busywork", nil, nil)
		}()
	}
	wg.Wait()

	if n := srv.liveWorkers(); n > MaxLiveWorkers {
		t.Errorf("%d workers are live, past the %d limit", n, MaxLiveWorkers)
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

// holdWorkers keeps every worker spawned during the test unfinished, so the
// live count reflects what was started rather than what happens to still be
// running against an instant mock.
func holdWorkers(t *testing.T, srv *Server) func() {
	t.Helper()
	var mu sync.Mutex
	var held []*Session
	stop := make(chan struct{})
	done := make(chan struct{})
	go func() {
		defer close(done)
		for {
			select {
			case <-stop:
				return
			default:
			}
			srv.mu.Lock()
			for _, sess := range srv.sessions {
				if !sess.Worker {
					continue
				}
				sess.mu.Lock()
				if sess.closedDone || sess.done == nil {
					sess.closedDone, sess.done = false, make(chan struct{})
					held = append(held, sess)
				}
				sess.mu.Unlock()
			}
			srv.mu.Unlock()
		}
	}()
	return func() {
		close(stop)
		<-done
		mu.Lock()
		defer mu.Unlock()
		for _, sess := range held {
			sess.markFinished()
		}
	}
}
