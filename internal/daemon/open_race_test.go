package daemon

import (
	"encoding/json"
	"sync"
	"testing"

	"evilcode/internal/session"
)

// H2.7: Open built the session outside the map lock and only then checked
// whether another goroutine had won. The loser was closed — and closing a
// session writes its lifecycle markers into the log the winner is still using,
// so a shared session log ends up with a clean-exit marker in the middle of a
// live conversation.
func TestConcurrentOpensOfOneSessionBuildOne(t *testing.T) {
	srv, _ := testServer(t)
	defer srv.Close()

	// A session that exists on disk but is not open in the daemon: every racer
	// has to build it, which is the case with no winner already in the map.
	seed, err := srv.Open("")
	if err != nil {
		t.Fatal(err)
	}
	name := seed.Name
	srv.mu.Lock()
	delete(srv.sessions, name)
	srv.mu.Unlock()
	first := (*Session)(nil)

	const racers = 8
	got := make(chan *Session, racers)
	var wg sync.WaitGroup
	start := make(chan struct{})
	for range racers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			sess, err := srv.Open(name)
			if err != nil {
				return
			}
			got <- sess
		}()
	}
	close(start)
	wg.Wait()
	close(got)

	for sess := range got {
		if first == nil {
			first = sess
			continue
		}
		if sess != first {
			t.Fatalf("a second session object was built for %q; the loser closes "+
				"and writes lifecycle markers into the winner's log", name)
		}
	}

	// The damage is not the duplicate object — that was already discarded — but
	// what discarding it wrote. Every loser opened the same log and closing a
	// store appends a clean-exit marker, so a live session's log ends up
	// claiming it exited, as many times as it was raced.
	entries, err := session.Read(first.built.Store.Path)
	if err != nil {
		t.Fatal(err)
	}
	exits := 0
	for _, e := range entries {
		if e.Type != session.TypeMeta {
			continue
		}
		var m session.Meta
		if json.Unmarshal(e.Data, &m) == nil && m.Kind == session.MetaCleanExit {
			exits++
		}
	}
	if exits > 0 {
		t.Errorf("the live session's log holds %d clean-exit marker(s) written by "+
			"discarded duplicates; crash detection now reads it as cleanly closed", exits)
	}
}
