package daemon

import (
	"testing"

	"evilcode/internal/todo"
)

// Todo state belongs to a durable conversation. Two sessions in one daemon
// may share the file-conflict registry, but their plans, overnight loops, and
// todo-triggered hooks must not leak into one another.
func TestSessionsHavePrivateTodoStores(t *testing.T) {
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
	if one.built.Todos == two.built.Todos {
		t.Fatal("different sessions share the same todo store")
	}

	if _, err := one.built.Todos.Apply(todo.Write{
		Items: []todo.Item{{ID: "1", Content: "wire the auth flow", Status: todo.StatusPending}},
	}); err != nil {
		t.Fatal(err)
	}
	if seen := two.built.Todos.Items(); len(seen) != 0 {
		t.Fatalf("the second session sees %d items, want an empty private plan", len(seen))
	}
	if _, err := two.built.Todos.Apply(todo.Write{
		Items: []todo.Item{{ID: "2", Content: "add the retry gate", Status: todo.StatusPending}},
	}); err != nil {
		t.Fatal(err)
	}

	if got := one.built.Todos.Items(); len(got) != 1 || got[0].Content != "wire the auth flow" {
		t.Errorf("first session sees %v, want only its own item", got)
	}
	if got := two.built.Todos.Items(); len(got) != 1 || got[0].Content != "add the retry gate" {
		t.Errorf("second session sees %v, want only its own item", got)
	}
}

func TestSessionRenameKeepsItsPrivateTodoState(t *testing.T) {
	srv, _ := testServer(t)
	defer srv.Close()

	sess, err := srv.Open("")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := sess.built.Todos.Apply(todo.Write{
		Items: []todo.Item{{ID: "1", Content: "stay with this session", Status: todo.StatusPending}},
	}); err != nil {
		t.Fatal(err)
	}
	old := sess.Name
	if err := srv.renameSession(sess, "raven"); err != nil {
		t.Fatal(err)
	}
	if sess.Name != "raven" || sess.built.Todos.Session != "raven" {
		t.Fatalf("renamed identities = session:%q todo:%q", sess.Name, sess.built.Todos.Session)
	}
	if got := sess.built.Todos.Items(); len(got) != 1 || got[0].Content != "stay with this session" {
		t.Fatalf("renamed todo state = %v", got)
	}
	srv.mu.Lock()
	_, oldLive := srv.sessions[old]
	_, newLive := srv.sessions["raven"]
	srv.mu.Unlock()
	if oldLive {
		t.Fatalf("old live session name %q still exists", old)
	}
	if !newLive {
		t.Fatal("new live session name is missing")
	}
}
