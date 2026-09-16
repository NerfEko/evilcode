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
	if !sess.built.Agent.ToolBlocked("todo") {
		t.Fatal("todo tool remained available in orchestrator mode")
	}
	if sess.poke != nil && sess.poke.Enabled() {
		t.Fatal("auto-poke remained enabled in orchestrator mode")
	}
	if err := sess.Command("poke", "on", ""); err == nil {
		t.Fatal("/poke on was accepted while orchestrator mode was active")
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
	if sess.built.Agent.ToolBlocked("todo") {
		t.Fatal("todo tool stayed blocked after orchestrator mode ended")
	}
	if sess.poke != nil && !sess.poke.Enabled() {
		t.Fatal("auto-poke was not restored after orchestrator mode ended")
	}
}
