package daemon

import (
	"testing"
	"time"

	"evilcode/internal/config"
	"evilcode/internal/provider"
	"evilcode/internal/session"
)

func TestIdleSessionExpiresAndResumesFromDisk(t *testing.T) {
	srv, _ := testServer(t)
	defer srv.Close()

	sess, err := srv.Open("")
	if err != nil {
		t.Fatal(err)
	}
	if err := sess.built.Store.WriteMessage(provider.Message{
		Role: provider.RoleUser, Content: "persist this idle session",
	}); err != nil {
		t.Fatal(err)
	}
	name := sess.Name
	path := sess.built.Store.Path

	// Drive the sweep without waiting ten minutes. The session has no
	// subscribers and no turn reservation, so it is eligible for teardown.
	now := time.Now()
	sess.mu.Lock()
	sess.idleSince = now.Add(-SessionIdleTimeout - time.Second)
	sess.mu.Unlock()
	srv.expireIdleSessions(now)

	srv.mu.Lock()
	_, live := srv.sessions[name]
	srv.mu.Unlock()
	if live {
		t.Fatalf("idle session %q is still hydrated", name)
	}

	info, err := session.Describe(config.DataDir(), name)
	if err != nil {
		t.Fatal(err)
	}
	if info.Crashed {
		t.Fatal("idle teardown did not write a clean-exit marker")
	}

	var stored *SessionInfo
	for _, row := range srv.Sessions() {
		if row.Name == name {
			copy := row
			stored = &copy
			break
		}
	}
	if stored == nil {
		t.Fatalf("expired session %q disappeared from durable session list", name)
	}
	if stored.Live || stored.Running {
		t.Fatalf("expired session row = live:%v running:%v, want both false", stored.Live, stored.Running)
	}

	resumed, err := srv.Open(name)
	if err != nil {
		t.Fatal(err)
	}
	if resumed == sess {
		t.Fatal("resume returned the unloaded session object")
	}
	if resumed.built.Store.Path != path {
		t.Fatalf("resume opened %q, want original log %q", resumed.built.Store.Path, path)
	}
	found := false
	for _, msg := range resumed.built.Agent.Conv.Messages() {
		if msg.Content == "persist this idle session" {
			found = true
			break
		}
	}
	if !found {
		t.Fatal("resume did not restore the persisted conversation")
	}
}

func TestAttachedOrRunningSessionDoesNotExpire(t *testing.T) {
	srv, _ := testServer(t)
	defer srv.Close()

	sess, err := srv.Open("")
	if err != nil {
		t.Fatal(err)
	}
	sub := sess.subscribe()
	now := time.Now()
	sess.mu.Lock()
	sess.idleSince = now.Add(-SessionIdleTimeout - time.Second)
	sess.mu.Unlock()
	srv.expireIdleSessions(now)

	srv.mu.Lock()
	_, live := srv.sessions[sess.Name]
	srv.mu.Unlock()
	if !live {
		t.Fatal("session with an attached window was expired")
	}
	sess.unsubscribe(sub)

	// A turn reservation also blocks expiry even when no window is attached.
	sess.mu.Lock()
	sess.running = true
	sess.idleSince = now.Add(-SessionIdleTimeout - time.Second)
	sess.mu.Unlock()
	srv.expireIdleSessions(now)
	srv.mu.Lock()
	_, live = srv.sessions[sess.Name]
	srv.mu.Unlock()
	if !live {
		t.Fatal("running session was expired")
	}
	sess.mu.Lock()
	sess.running = false
	sess.mu.Unlock()
}

func TestShutdownMarksIdleSessionsDoneButActiveTurnsCrashed(t *testing.T) {
	srv, _ := testServer(t)

	idle, err := srv.Open("")
	if err != nil {
		t.Fatal(err)
	}
	active, err := srv.Open("")
	if err != nil {
		t.Fatal(err)
	}

	// A turn reservation is enough to model the shutdown boundary without
	// involving a provider: the daemon must classify this session as active,
	// even if the provider's own running bit is between event rounds.
	active.mu.Lock()
	active.running = true
	active.mu.Unlock()

	idleName, activeName := idle.Name, active.Name
	idlePath, activePath := idle.built.Store.Path, active.built.Store.Path
	srv.Close()

	idleInfo, err := session.Describe(config.DataDir(), idleName)
	if err != nil {
		t.Fatal(err)
	}
	if idleInfo.Crashed {
		t.Fatal("an idle session was reported as crashed after daemon shutdown")
	}
	activeInfo, err := session.Describe(config.DataDir(), activeName)
	if err != nil {
		t.Fatal(err)
	}
	if !activeInfo.Crashed {
		t.Fatal("a session with an active turn was reported as clean after daemon shutdown")
	}
	if idlePath == activePath {
		t.Fatal("the two shutdown lifecycle checks used the same session log")
	}
}
