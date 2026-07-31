package daemon

import (
	"runtime"
	"testing"
	"time"
)

// H3.8: every attach on a connection started a new event relay without stopping
// the previous one. The old goroutine is blocked reading a channel nobody
// publishes to any more — it was unsubscribed — and stays there until the whole
// connection closes. A client switching between sessions leaks one per switch.
func TestReattachingDoesNotLeakARelay(t *testing.T) {
	srv, path := testServer(t)
	defer srv.Close()

	// Two sessions to switch between.
	one, err := srv.Open("")
	if err != nil {
		t.Fatal(err)
	}
	two, err := srv.Open("")
	if err != nil {
		t.Fatal(err)
	}

	client, err := DialPath(path)
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()

	if _, err := client.Attach(one.Name, 0); err != nil {
		t.Fatal(err)
	}
	settle()
	base := runtime.NumGoroutine()

	for range 20 {
		if _, err := client.Attach(two.Name, 0); err != nil {
			t.Fatal(err)
		}
		if _, err := client.Attach(one.Name, 0); err != nil {
			t.Fatal(err)
		}
	}
	settle()

	if grew := runtime.NumGoroutine() - base; grew > 4 {
		t.Errorf("40 re-attaches on one connection left %d extra goroutines; "+
			"each attach starts a relay and none of them stop", grew)
	}
}

// And closing the connection takes everything with it.
func TestClosingAConnectionStopsItsGoroutines(t *testing.T) {
	srv, path := testServer(t)
	defer srv.Close()

	sess, err := srv.Open("")
	if err != nil {
		t.Fatal(err)
	}

	settle()
	base := runtime.NumGoroutine()

	for range 10 {
		client, err := DialPath(path)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := client.Attach(sess.Name, 0); err != nil {
			t.Fatal(err)
		}
		client.Close()
	}
	settle()

	if grew := runtime.NumGoroutine() - base; grew > 4 {
		t.Errorf("10 closed connections left %d extra goroutines behind", grew)
	}
}

// settle gives goroutines a moment to finish before they are counted.
func settle() {
	for range 20 {
		runtime.Gosched()
		time.Sleep(10 * time.Millisecond)
	}
}
