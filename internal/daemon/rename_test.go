package daemon

import (
	"path/filepath"
	"testing"
	"time"
)

// D3: renaming a session must migrate every identity-indexed piece of
// coordination state — swarm spawner mapping, spawn budget, and file registry
// ownership — so the renamed agent keeps its place in the swarm instead of
// becoming a stranger.
func TestRenameMigratesSwarmAndRegistryIdentity(t *testing.T) {
	srv, _ := testServer(t)
	defer srv.Close()

	sess, err := srv.Open("")
	if err != nil {
		t.Fatal(err)
	}
	old := sess.Name

	// Give the session swarm state and file-registry history under the old name.
	srv.swarm.mu.Lock()
	srv.swarm.spawnedBy["worker-1"] = old
	srv.swarm.spawnCount[old] = 3
	srv.swarm.mu.Unlock()
	srv.Files.Read(old, filepath.Join(srv.Cwd, "a.go"), 1)
	srv.Files.Write(old, filepath.Join(srv.Cwd, "a.go"), 2)

	if err := srv.renameSession(sess, "renamed"); err != nil {
		t.Fatal(err)
	}

	srv.swarm.mu.Lock()
	spawner := srv.swarm.spawnedBy["worker-1"]
	count := srv.swarm.spawnCount["renamed"]
	oldCount := srv.swarm.spawnCount[old]
	srv.swarm.mu.Unlock()
	if spawner != "renamed" {
		t.Errorf("spawnedBy routes to %q, want the new name", spawner)
	}
	if count != 3 || oldCount != 0 {
		t.Errorf("spawn budget = new:%d old:%d, want 3/0", count, oldCount)
	}
	if files := srv.Files.Files("renamed"); len(files) != 1 {
		t.Errorf("renamed session file history = %v, want its own file", files)
	}
	if files := srv.Files.Files(old); len(files) != 0 {
		t.Errorf("old name still owns files: %v", files)
	}
}

// Rename republishes a snapshot with the new identity, so an attached client
// can follow the session instead of addressing the old name.
func TestRenamePublishesNewIdentitySnapshot(t *testing.T) {
	srv, path := testServer(t)
	defer srv.Close()

	client, err := DialPath(path)
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	snap, err := client.Attach("", 0)
	if err != nil {
		t.Fatal(err)
	}

	srv.mu.Lock()
	sess := srv.sessions[snap.Session]
	srv.mu.Unlock()
	if sess == nil {
		t.Fatal("attached session not registered")
	}
	if err := srv.renameSession(sess, "moved"); err != nil {
		t.Fatal(err)
	}

	// The client must observe a snapshot naming the new session.
	deadline := time.After(5 * time.Second)
	for {
		msg, err := client.Recv()
		if err != nil {
			t.Fatal(err)
		}
		if msg.Kind == MsgSnapshot && msg.Snapshot != nil {
			if msg.Snapshot.Session != "moved" {
				t.Fatalf("snapshot session = %q, want the renamed identity", msg.Snapshot.Session)
			}
			return
		}
		select {
		case <-deadline:
			t.Fatal("no rename snapshot arrived")
		default:
		}
	}
}
