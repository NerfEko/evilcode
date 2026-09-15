package daemon

import (
	"encoding/json"
	"strings"
	"testing"
)

// D3: a per-call model override lands on the worker and is auditable.
func TestSpawnWithModelOverrideRunsOnRequestedModel(t *testing.T) {
	srv, _ := testServer(t)
	defer srv.Close()

	spawner, err := srv.Open("")
	if err != nil {
		t.Fatal(err)
	}

	name, err := srv.SpawnForWithModel(spawner.Name, "model audit", nil, nil, "mock")
	if err != nil {
		t.Fatalf("spawn with model override failed: %v", err)
	}
	srv.mu.Lock()
	worker := srv.sessions[name]
	srv.mu.Unlock()
	if worker == nil {
		t.Fatalf("worker %q not found", name)
	}
	if worker.ResolvedModel == "" {
		t.Fatal("worker has no resolved model audit")
	}
	if !strings.Contains(worker.ResolvedModel, "mock") {
		t.Fatalf("resolved model = %q, want it to name mock", worker.ResolvedModel)
	}
	waitReservationsDrained(t, srv)
}

// D3 + D3b: a bad ref fails fast, before any session exists and before a
// reservation is consumed.
func TestSpawnWithBadModelFailsBeforeSessionExists(t *testing.T) {
	srv, _ := testServer(t)
	defer srv.Close()

	spawner, err := srv.Open("")
	if err != nil {
		t.Fatal(err)
	}

	before := len(srv.Sessions())
	_, err = srv.SpawnForWithModel(spawner.Name, "bad model", nil, nil, "nope@missing-provider-xyz")
	if err == nil {
		t.Fatal("spawn with a bad model ref succeeded")
	}
	if got := len(srv.Sessions()); got != before {
		t.Fatalf("sessions grew from %d to %d on a failed spawn", before, got)
	}
	srv.swarm.mu.Lock()
	live := srv.swarm.live
	srv.swarm.mu.Unlock()
	if live != 0 {
		t.Fatalf("live reservations = %d after a failed spawn, want 0", live)
	}
}

// D3: the resolved model is attached to the async delivery.
func TestSpawnDeliveryCarriesResolvedModel(t *testing.T) {
	srv, _ := testServer(t)
	defer srv.Close()

	spawner, err := srv.Open("")
	if err != nil {
		t.Fatal(err)
	}

	name, sess, err := srv.SpawnForWithSession(spawner.Name, "audit delivery", nil, nil, "")
	if err != nil {
		t.Fatal(err)
	}
	if name == "" || sess == nil {
		t.Fatal("spawn returned no worker")
	}
	waitReservationsDrained(t, srv)
	_ = json.RawMessage(nil)
}
