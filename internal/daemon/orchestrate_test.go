package daemon

import (
	"testing"
)

func TestOrchestrateCommandRoundTrips(t *testing.T) {
	srv, _ := testServer(t)
	defer srv.Close()

	sess, err := srv.Open("")
	if err != nil {
		t.Fatal(err)
	}
	if sess.orchestrate == nil {
		t.Fatal("session has no orchestrate hook")
	}
	if err := sess.Command("orchestrate", "status", ""); err != nil {
		t.Fatal(err)
	}
	if sess.orchestrate.Active() {
		t.Fatal("orchestrator mode armed itself")
	}
	if err := sess.Command("orchestrate", "on", ""); err != nil {
		t.Fatal(err)
	}
	if !sess.orchestrate.Active() {
		t.Fatal("/orchestrate on did not arm")
	}
	if err := sess.Command("orchestrate", "bogus", ""); err == nil {
		t.Fatal("/orchestrate bogus did not fail")
	}
	if err := sess.Command("orchestrate", "off", ""); err != nil {
		t.Fatal(err)
	}
	if sess.orchestrate.Active() {
		t.Fatal("/orchestrate off did not disarm")
	}
}
