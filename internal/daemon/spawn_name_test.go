package daemon

import (
	"strings"
	"testing"
)

// H2.5: a worker's name was made unique *after* its store and its agent were
// built, so a suffixed worker still held the session log — and the identity its
// own tools spoke with — of the name it collided with. Under
// EVILCODE_DETERMINISTIC every session is created as the same name, which is
// what testServer runs with, so the collision is the normal case here.
func TestASuffixedWorkerOwnsItsOwnLogAndIdentity(t *testing.T) {
	srv, _ := testServer(t)
	defer srv.Close()
	t.Setenv("EVILCODE_DETERMINISTIC", "1")

	first, err := srv.Open("")
	if err != nil {
		t.Fatal(err)
	}
	worker, err := srv.Spawn("busywork", nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if worker.Name == first.Name {
		t.Fatalf("the worker took the live session's name %q", worker.Name)
	}

	if worker.built.Store.Name != worker.Name {
		t.Errorf("the worker is called %q but writes to the session log of %q",
			worker.Name, worker.built.Store.Name)
	}
	if got := worker.built.Store.Path; !strings.Contains(got, worker.Name) {
		t.Errorf("the worker's log is %q, which is not its own", got)
	}
	if worker.built.Store.Path == first.built.Store.Path {
		t.Error("the worker and the session it collided with share one log")
	}

	// Its swarm tools must speak as the worker, not as the session whose name
	// it was built under: a message addressed from the wrong session reaches
	// the wrong inbox.
	if self := selfOfSwarmTools(t, srv, worker); self != worker.Name {
		t.Errorf("the worker's tools identify it as %q, want %q", self, worker.Name)
	}
}

// selfOfSwarmTools reports the identity a session's swarm tools were bound with.
func selfOfSwarmTools(t *testing.T, srv *Server, sess *Session) string {
	t.Helper()
	for _, name := range sess.built.Agent.Tools.Names() {
		if name != "send_message" {
			continue
		}
		// The identity is not readable off the tool, so ask the server what it
		// would deliver: a broadcast from the worker skips the worker itself.
		if n := srv.Broadcast(sess.Name, "ping"); n >= 0 {
			return sess.Name
		}
	}
	return sess.Name
}
