package daemon

import (
	"sync"
	"testing"
)

// H2.1: sess.cancel was assigned, read and cleared from the input, interrupt,
// close and worker paths with no consistent locking. Two attached clients, or
// an interrupt racing a turn start, could cancel the wrong turn — and the
// unsynchronized access is a data race in its own right.
//
// Run with -race: the detector is the point of this test.
func TestCancelIsNotRacedBetweenPaths(t *testing.T) {
	srv, _ := testServer(t)
	defer srv.Close()

	sess, err := srv.Open("")
	if err != nil {
		t.Fatal(err)
	}

	var wg sync.WaitGroup
	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			sess.Input("go")
		}()
		wg.Add(1)
		go func() {
			defer wg.Done()
			sess.Interrupt("", false)
		}()
	}

	// A worker assigns its own cancel while the above is still running, which
	// is the path that was never under the lock at all.
	for range 4 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, _ = srv.Spawn("busywork", nil, nil)
		}()
	}
	wg.Wait()
}
