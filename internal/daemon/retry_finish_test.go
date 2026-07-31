package daemon

import (
	"encoding/json"
	"testing"

	"evilcode/internal/provider"
)

// H2.6: the spawn goroutine called markFinished the moment Run returned, but
// the schema-retry path returns false precisely to mean "not finished" and then
// drives a second Loop. The worker was counted finished — freeing a slot under
// MaxLiveWorkers — while its retry was still spending tokens.
//
// What this test can check is the rule the fix rests on: a worker with a retry
// armed is not finished, and the report that arms it says so by returning false.
// The ordering window itself is between two goroutines and microseconds wide
// against the mock; see LOOPS.md for why it is not reproduced here.
func TestArmingARetryDoesNotFinishTheWorker(t *testing.T) {
	srv, _ := testServer(t)
	defer srv.Close()
	provider.ResetMockRotation()

	spawner, err := srv.Open("")
	if err != nil {
		t.Fatal(err)
	}
	schema := json.RawMessage(`{
		"type": "object",
		"properties": {"answer": {"type": "string"}},
		"required": ["answer"]
	}`)
	name, err := srv.SpawnFor(spawner.Name, "answer something", nil, schema)
	if err != nil {
		t.Fatal(err)
	}

	srv.mu.Lock()
	worker := srv.sessions[name]
	srv.mu.Unlock()
	waitFor(t, "the worker to settle", func() bool { return worker.finished() })

	// Rewind to the moment its first turn ended, and report again.
	worker.mu.Lock()
	worker.closedDone, worker.done = false, make(chan struct{})
	worker.retried, worker.retrying = false, false
	worker.mu.Unlock()

	if srv.reportWorkerResult(worker) {
		t.Fatal("a result that fails its schema reported as finished on the first try")
	}
	worker.mu.Lock()
	armed := worker.retried
	worker.mu.Unlock()
	if !armed {
		t.Fatal("the retry was not armed")
	}
	if worker.finished() {
		t.Error("the worker is counted finished while its retry is armed")
	}
	waitFor(t, "the retry to finish the worker", func() bool { return worker.finished() })
}
