package daemon

import (
	"context"
	"encoding/json"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// Workers are grunts: grunt-N off the on-disk high-water mark, never a
// creature name, so no worker ever reads as a normal session.
func TestSpawnedWorkerIsNamedGrunt(t *testing.T) {
	srv, _ := testServer(t)
	defer srv.Close()

	spawner, err := srv.Open("")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"grunt-1", "grunt-2"} {
		name, err := srv.SpawnFor(spawner.Name, "grunt work", nil, nil)
		if err != nil {
			t.Fatal(err)
		}
		if name != want {
			t.Fatalf("worker name = %q, want %q", name, want)
		}
	}
	waitReservationsDrained(t, srv)
}

// D8: [features] max_live_workers is honored under concurrent load.
func TestMaxLiveWorkersConfigIsHonored(t *testing.T) {
	srv, _ := testServer(t)
	defer srv.Close()

	srv.mu.Lock()
	srv.Cfg.Features.MaxLiveWorkers = 1
	srv.mu.Unlock()

	t.Setenv("EVILCODE_MOCK_STREAM_DELAY", "40ms")

	spawner, err := srv.Open("")
	if err != nil {
		t.Fatal(err)
	}

	stop, maxLive := sampleLive(t, srv)
	defer close(stop)

	done := make(chan struct{})
	go func() {
		defer close(done)
		for i := 0; i < 4; i++ {
			_, _ = srv.SpawnFor(spawner.Name, "busywork", nil, nil)
		}
	}()

	select {
	case <-done:
	case <-time.After(30 * time.Second):
		t.Fatal("spawns did not return")
	}
	if got := atomic.LoadInt64(maxLive); got > 1 {
		t.Fatalf("%d workers were live at once with max_live_workers=1", got)
	}
	waitReservationsDrained(t, srv)
}

// D8: workers get no spawn_worker by default, and get it back behind
// worker_spawning.
func TestWorkerToolSetDepthGate(t *testing.T) {
	srv, _ := testServer(t)
	defer srv.Close()

	spawner, err := srv.Open("")
	if err != nil {
		t.Fatal(err)
	}
	name, err := srv.SpawnFor(spawner.Name, "count the TODOs", nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	srv.mu.Lock()
	worker := srv.sessions[name]
	srv.mu.Unlock()
	if _, ok := worker.built.Agent.Tools.Find("spawn_worker"); ok {
		t.Error("worker has spawn_worker by default; depth 1 means it should not")
	}
	if _, ok := worker.built.Agent.Tools.Find("send_message"); !ok {
		t.Error("depth gate dropped messaging tools too")
	}
	waitReservationsDrained(t, srv)

	srv.mu.Lock()
	srv.Cfg.Features.WorkerSpawning = true
	srv.mu.Unlock()
	deep, err := srv.SpawnFor(spawner.Name, "delegate again", nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	srv.mu.Lock()
	deepWorker := srv.sessions[deep]
	srv.mu.Unlock()
	if _, ok := deepWorker.built.Agent.Tools.Find("spawn_worker"); !ok {
		t.Error("worker_spawning=true did not restore spawn_worker")
	}
	waitReservationsDrained(t, srv)
}

// D8: worker_max_steps bounds a worker's tool rounds via the max_steps
// machinery.
func TestWorkerMaxStepsBoundsWorkerRounds(t *testing.T) {
	srv, _ := testServer(t)
	defer srv.Close()

	srv.mu.Lock()
	srv.Cfg.Features.WorkerMaxSteps = 3
	srv.mu.Unlock()

	spawner, err := srv.Open("")
	if err != nil {
		t.Fatal(err)
	}
	name, err := srv.SpawnFor(spawner.Name, "bounded work", nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	srv.mu.Lock()
	worker := srv.sessions[name]
	srv.mu.Unlock()
	if worker.built.Agent.MaxSteps != 3 {
		t.Fatalf("worker MaxSteps = %d, want 3", worker.built.Agent.MaxSteps)
	}
	waitReservationsDrained(t, srv)
}

// D6: cancel aborts the worker, releases the live slot, and salvages.
func TestCancelWorkerAbortsAndSalvages(t *testing.T) {
	srv, _ := testServer(t)
	defer srv.Close()

	t.Setenv("EVILCODE_MOCK_STREAM_DELAY", "100ms")

	spawner, err := srv.Open("")
	if err != nil {
		t.Fatal(err)
	}
	name, err := srv.SpawnFor(spawner.Name, "a long survey of everything", nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	summary, err := srv.CancelWorker(spawner.Name, name)
	if err != nil {
		t.Fatalf("cancel of a live worker failed: %v", err)
	}
	if !strings.Contains(summary, name) {
		t.Fatalf("cancel summary = %q, want the worker name", summary)
	}

	srv.mu.Lock()
	worker := srv.sessions[name]
	srv.mu.Unlock()
	waitFor(t, "the cancelled worker to finish", worker.finished)

	srv.swarm.mu.Lock()
	live := srv.swarm.live
	srv.swarm.mu.Unlock()
	if live != 0 {
		t.Fatalf("live reservations = %d after cancel, want 0", live)
	}
	worker.mu.Lock()
	partial, failure := worker.partial, worker.workerFailure
	worker.mu.Unlock()
	if partial == "" && failure == nil && lastAssistantText(worker) == "" {
		t.Fatal("cancel left nothing salvageable: no partial text and no failure")
	}

	if _, err := srv.CancelWorker(spawner.Name, name); err == nil {
		t.Fatal("second cancel of a finished worker did not fail")
	}
	if _, err := srv.CancelWorker(spawner.Name, "no-such-worker"); err == nil {
		t.Fatal("cancel of an unknown worker did not fail")
	}
	if _, err := srv.CancelWorker(spawner.Name, spawner.Name); err == nil {
		t.Fatal("cancel of a plain session did not fail")
	}
}

// D4: schema failure after 3 total prompts reports a failed result with the
// last text, and the retry counter shows two retries.
func TestSchemaExhaustionReportsFailedResultWithContent(t *testing.T) {
	srv, _ := testServer(t)
	defer srv.Close()

	spawner, err := srv.Open("")
	if err != nil {
		t.Fatal(err)
	}
	schema := json.RawMessage(`{
		"type": "object",
		"properties": {"answer": {"type": "string"}},
		"required": ["answer"]
	}`)
	name, output, status, err := srv.SpawnForForegroundResult(
		context.Background(), spawner.Name, "answer something", nil, schema, "")
	if err != nil {
		t.Fatalf("schema-exhausted worker returned an error instead of a failed result: %v", err)
	}
	if status != "failed" {
		t.Fatalf("status = %q, want failed", status)
	}
	if strings.TrimSpace(output) == "" {
		t.Fatal("failed result carries no text")
	}
	srv.mu.Lock()
	worker := srv.sessions[name]
	srv.mu.Unlock()
	worker.mu.Lock()
	retries := worker.retries
	worker.mu.Unlock()
	if retries != 2 {
		t.Fatalf("retries = %d, want 2 (3 total prompts)", retries)
	}
}

// D7: per-worker tokens accumulate and show on the roster row.
func TestWorkerTokensAccumulateOnRoster(t *testing.T) {
	srv, _ := testServer(t)
	defer srv.Close()

	spawner, err := srv.Open("")
	if err != nil {
		t.Fatal(err)
	}
	name, err := srv.SpawnFor(spawner.Name, "count the TODOs", nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	srv.mu.Lock()
	worker := srv.sessions[name]
	srv.mu.Unlock()
	waitFor(t, "the worker to finish", worker.finished)

	var row *SessionInfo
	for _, info := range srv.Sessions() {
		if info.Name == name {
			row = &info
		}
	}
	if row == nil {
		t.Fatalf("worker %q missing from the roster", name)
	}
	if row.Tokens <= 0 {
		t.Fatalf("worker tokens = %d, want the mock usage accumulated", row.Tokens)
	}
}
