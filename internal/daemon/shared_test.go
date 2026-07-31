package daemon

import (
	"testing"

	"evilcode/internal/todo"
)

// H1.10: every session in a daemon is built with TodoNamespace "swarm", but each
// one opened its *own* todo.Store over those same files. Two in-memory copies,
// no reload before a read, and a whole-file write at the end of each
// transaction: the second session's write erases the first's items, and neither
// ever sees the other's plan. The §20 shared plan did not share.
func TestSessionsShareOneTodoStore(t *testing.T) {
	srv, _ := testServer(t)
	defer srv.Close()

	one, err := srv.Open("")
	if err != nil {
		t.Fatal(err)
	}
	two, err := srv.Open("")
	if err != nil {
		t.Fatal(err)
	}
	if one.built.Todos == nil || two.built.Todos == nil {
		t.Fatal("a daemon session has no todo store")
	}

	if _, err := one.built.Todos.Apply(todo.Write{
		Items: []todo.Item{{ID: "1", Content: "wire the auth flow", Status: todo.StatusPending}},
	}); err != nil {
		t.Fatal(err)
	}
	// The second session works from the list as it stands, which is the whole
	// point of a shared plan: a todo write replaces the list, so a session
	// holding a stale copy silently deletes the other's work.
	seen := two.built.Todos.Items()
	if len(seen) != 1 {
		t.Fatalf("the second session sees %d items, want the first session's one", len(seen))
	}
	if _, err := two.built.Todos.Apply(todo.Write{
		Items: append(seen, todo.Item{ID: "2", Content: "add the retry gate", Status: todo.StatusPending}),
	}); err != nil {
		t.Fatal(err)
	}

	for _, sess := range []*Session{one, two} {
		var got []string
		for _, it := range sess.built.Todos.Items() {
			got = append(got, it.Content)
		}
		if len(got) != 2 {
			t.Errorf("%s sees %v, want both sessions' items — the swarm plan is not shared",
				sess.Name, got)
		}
	}
}
