package attachcmd

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"evilcode/internal/config"
	"evilcode/internal/daemon"
)

// TestMayStopDaemon is the feedback loop for the quit-side daemon stop: the
// daemon goes down only when this window was its sole occupant — no other
// live session, and this window's own session idle, unwatched, and waiting on
// nothing.
func TestMayStopDaemon(t *testing.T) {
	cases := []struct {
		name string
		rows []daemon.SessionInfo
		self string
		want bool
	}{
		{"a fresh daemon holds nothing", nil, "", true},
		{"stored rows are files, not daemon state",
			[]daemon.SessionInfo{{Name: "old", Stored: true}}, "", true},
		{"our own idle session", []daemon.SessionInfo{{Name: "ours", Live: true}},
			"ours", true},
		{"another live session blocks",
			[]daemon.SessionInfo{{Name: "ours", Live: true}, {Name: "other", Live: true}},
			"ours", false},
		{"a stranger is enough when we have no session",
			[]daemon.SessionInfo{{Name: "other", Live: true}}, "", false},
		{"a mid-turn session blocks", []daemon.SessionInfo{{Name: "ours", Live: true, Running: true}},
			"ours", false},
		{"a second window on our session blocks",
			[]daemon.SessionInfo{{Name: "ours", Live: true, Clients: 2}}, "ours", false},
		{"an unanswered ask blocks", []daemon.SessionInfo{{Name: "ours", Live: true, Pending: 1}},
			"ours", false},
		{"a stale worker is still live state",
			[]daemon.SessionInfo{{Name: "w", Live: true, Stale: true}}, "", false},
	}

	for _, tc := range cases {
		if got := mayStopDaemon(tc.rows, tc.self); got != tc.want {
			t.Errorf("%s: mayStopDaemon = %v, want %v", tc.name, got, tc.want)
		}
	}
}

// idleTestDaemon starts a daemon on a short temp socket with the mock
// provider, the same way the daemon package's own tests do.
func idleTestDaemon(t *testing.T) (string, *daemon.Server) {
	t.Helper()

	home := t.TempDir()
	t.Setenv("XDG_DATA_HOME", filepath.Join(home, "data"))
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, "config"))
	t.Setenv("HOME", home)
	t.Setenv("EVILCODE_PROVIDER", "mock")
	t.Setenv("EVILCODE_SCENARIO", "chat")
	t.Setenv("EVILCODE_CONFIG", filepath.Join(home, "nonexistent.toml"))

	cfg, err := config.Load()
	if err != nil {
		t.Fatal(err)
	}
	dir, err := os.MkdirTemp("", "ecattach")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })

	srv := daemon.NewServer(cfg, t.TempDir(), "")
	srv.Path = filepath.Join(dir, "s.sock")
	if err := srv.Listen(); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	go srv.Serve(ctx)
	t.Cleanup(srv.Close)
	return srv.Path, srv
}

// TestStopDaemonIfIdleStopsAnEmptyDaemon: quitting a window that never
// prompted is the same as `ec serve -stop` on an idle daemon.
func TestStopDaemonIfIdleStopsAnEmptyDaemon(t *testing.T) {
	path, _ := idleTestDaemon(t)

	stopDaemonIfIdle(path, "", false)

	if _, err := daemon.DialPath(path); err == nil {
		t.Fatal("the daemon is still listening after the quit")
	}
}

// TestStopDaemonIfIdleSparesAWorkingDaemon: a live session — even this
// window's own — keeps the daemon up, exactly as before.
func TestStopDaemonIfIdleSparesAWorkingDaemon(t *testing.T) {
	path, srv := idleTestDaemon(t)

	if _, err := srv.Open(""); err != nil {
		t.Fatal(err)
	}
	stopDaemonIfIdle(path, "", false)

	if _, err := daemon.DialPath(path); err != nil {
		t.Fatalf("the daemon was stopped with a live session: %v", err)
	}
}

// TestStopDaemonIfIdleSparesTheOvernightLoop: an unattended run keeps the
// daemon alive across a detach.
func TestStopDaemonIfIdleSparesTheOvernightLoop(t *testing.T) {
	path, _ := idleTestDaemon(t)

	stopDaemonIfIdle(path, "", true)

	if _, err := daemon.DialPath(path); err != nil {
		t.Fatalf("the daemon was stopped while overnight was active: %v", err)
	}
}
