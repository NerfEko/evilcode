package daemon

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"evilcode/internal/agent"
	"evilcode/internal/config"
	"evilcode/internal/provider"
)

func TestRingReplaysWhatItHolds(t *testing.T) {
	r := NewRing()
	for i := 1; i <= 5; i++ {
		if got := r.Add(agent.Event{Text: fmt.Sprint(i)}); got != i {
			t.Fatalf("Add returned seq %d, want %d", got, i)
		}
	}

	got, seq := r.Since(2)
	if seq != 5 {
		t.Errorf("seq = %d, want 5", seq)
	}
	if len(got) != 3 {
		t.Fatalf("replayed %d events, want 3", len(got))
	}
	if got[0].Text != "3" || got[2].Text != "5" {
		t.Errorf("replay = %v", texts(got))
	}
}

func TestRingSinceZeroReplaysEverything(t *testing.T) {
	r := NewRing()
	r.Add(agent.Event{Text: "a"})
	r.Add(agent.Event{Text: "b"})
	got, _ := r.Since(0)
	if len(got) != 2 {
		t.Errorf("replayed %v, want both", texts(got))
	}
}

func TestRingSinceFutureReplaysNothing(t *testing.T) {
	// A client that is already current must not be re-sent the whole session.
	r := NewRing()
	r.Add(agent.Event{Text: "a"})
	if got, _ := r.Since(99); len(got) != 0 {
		t.Errorf("replayed %v for a sequence ahead of the ring", texts(got))
	}
}

func TestRingWrapsAndDropsOldest(t *testing.T) {
	r := NewRing()
	for i := 0; i < RingSize+10; i++ {
		r.Add(agent.Event{Text: fmt.Sprint(i)})
	}
	if r.Len() != RingSize {
		t.Errorf("ring holds %d, want it capped at %d", r.Len(), RingSize)
	}
	// Asking for a sequence the ring has overwritten returns what survives
	// rather than erroring: the events are gone either way, and refusing would
	// leave the client with nothing at all.
	got, seq := r.Since(1)
	if seq != RingSize+10 {
		t.Errorf("seq = %d", seq)
	}
	if len(got) != RingSize {
		t.Errorf("replayed %d, want the %d still held", len(got), RingSize)
	}
	if got[0].Text != "10" {
		t.Errorf("oldest survivor = %q, want the 11th event", got[0].Text)
	}
}

func TestRingIsEmptyBeforeAnyAdd(t *testing.T) {
	r := NewRing()
	got, seq := r.Since(0)
	if len(got) != 0 || seq != 0 {
		t.Errorf("fresh ring replayed %v at seq %d", texts(got), seq)
	}
}

func texts(events []agent.Event) []string {
	out := make([]string, len(events))
	for i, e := range events {
		out[i] = e.Text
	}
	return out
}

func TestSocketPathPrefersRuntimeDir(t *testing.T) {
	t.Setenv("XDG_RUNTIME_DIR", "/run/user/1000")
	if got := SocketPath(); got != "/run/user/1000/"+SocketName {
		t.Errorf("SocketPath() = %q", got)
	}
}

func TestSocketPathFallsBackToAUserOwnedDir(t *testing.T) {
	// The fallback must not be a shared path: anything that can connect to this
	// socket can run shell commands as this user.
	t.Setenv("XDG_RUNTIME_DIR", "")
	got := SocketPath()
	if !strings.Contains(got, fmt.Sprintf("evilcode-%d", os.Getuid())) {
		t.Errorf("SocketPath() = %q, want a per-uid directory", got)
	}
}

func TestWorkerPromptCarriesTaskHintsAndSchema(t *testing.T) {
	got := WorkerPrompt("fix the clamp", []string{"internal/tui/scroll.go"},
		json.RawMessage(`{"type":"object"}`))

	for _, want := range []string{
		"fix the clamp",
		"internal/tui/scroll.go",
		"hint, not a boundary",
		"preserve unrelated work",
		"verify the result",
		`{"type":"object"}`,
	} {
		if !strings.Contains(got, want) {
			t.Errorf("prompt is missing %q:\n%s", want, got)
		}
	}
}

func TestWorkerPromptOmitsAbsentSections(t *testing.T) {
	got := WorkerPrompt("do a thing", nil, nil)
	if strings.Contains(got, "schema") || strings.Contains(got, "hint") {
		t.Errorf("prompt invented sections it had no data for:\n%s", got)
	}
}

// testServer starts a daemon on a temp socket against the mock provider.
func testServer(t *testing.T) (*Server, string) {
	t.Helper()

	// A private data directory, so a test never writes to the real session
	// store or memory bank.
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

	// Short path: unix socket paths cap at 108 bytes, and a temp dir plus a
	// long test name overruns it.
	dir, err := os.MkdirTemp("", "evild")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	path := filepath.Join(dir, "s.sock")

	srv := NewServer(cfg, t.TempDir(), "")
	srv.Path = path
	if err := srv.Listen(); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	go srv.Serve(ctx)
	t.Cleanup(srv.Close)
	return srv, path
}

func TestAttachInputAndStreamRoundTrip(t *testing.T) {
	_, path := testServer(t)

	client, err := DialPath(path)
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()

	snap, err := client.Attach("", 0)
	if err != nil {
		t.Fatal(err)
	}
	if snap.Session == "" {
		t.Fatal("attach returned no session name")
	}

	if err := client.Send(ClientMsg{Kind: MsgInput, Text: "hello"}); err != nil {
		t.Fatal(err)
	}

	var text strings.Builder
	deadline := time.After(10 * time.Second)
	for {
		type result struct {
			msg ServerMsg
			err error
		}
		got := make(chan result, 1)
		go func() {
			msg, err := client.Recv()
			got <- result{msg, err}
		}()

		select {
		case <-deadline:
			t.Fatalf("timed out; text so far: %q", text.String())
		case r := <-got:
			if r.err != nil {
				t.Fatal(r.err)
			}
			if r.msg.Kind != MsgEvent || r.msg.Event == nil {
				continue
			}
			if r.msg.Event.Kind == agent.EventTextDelta {
				text.WriteString(r.msg.Event.Text)
			}
			if r.msg.Event.Kind == agent.EventTurnEnd {
				if !strings.Contains(text.String(), "config") {
					t.Errorf("streamed text = %q, want the mock's chat reply", text.String())
				}
				return
			}
		}
	}
}

func TestListReportsOpenSessions(t *testing.T) {
	srv, path := testServer(t)

	client, err := DialPath(path)
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()

	if got, err := client.List(); err != nil || len(got) != 0 {
		t.Fatalf("a fresh daemon listed %v (err %v)", got, err)
	}

	snap, err := client.Attach("", 0)
	if err != nil {
		t.Fatal(err)
	}
	got, err := client.List()
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].Name != snap.Session {
		t.Fatalf("list = %+v, want the attached session", got)
	}
	if got[0].Clients != 1 {
		t.Errorf("clients = %d, want 1", got[0].Clients)
	}
	if len(srv.Sessions()) != 1 {
		t.Errorf("server holds %d sessions", len(srv.Sessions()))
	}
}

func TestSecondClientAttachesToTheSameSession(t *testing.T) {
	// The reason the daemon exists: two terminals, one conversation. Opening
	// the same name twice must not build two agents over one JSONL file.
	srv, path := testServer(t)

	first, err := DialPath(path)
	if err != nil {
		t.Fatal(err)
	}
	defer first.Close()
	snap, err := first.Attach("", 0)
	if err != nil {
		t.Fatal(err)
	}

	second, err := DialPath(path)
	if err != nil {
		t.Fatal(err)
	}
	defer second.Close()
	again, err := second.Attach(snap.Session, 0)
	if err != nil {
		t.Fatal(err)
	}
	if again.Session != snap.Session {
		t.Errorf("second client landed on %q, want %q", again.Session, snap.Session)
	}
	if len(srv.Sessions()) != 1 {
		t.Errorf("server built %d sessions for one name", len(srv.Sessions()))
	}
}

func TestInputBeforeAttachIsRefused(t *testing.T) {
	_, path := testServer(t)
	client, err := DialPath(path)
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()

	if err := client.Send(ClientMsg{Kind: MsgInput, Text: "hi"}); err != nil {
		t.Fatal(err)
	}
	msg, err := client.Recv()
	if err != nil {
		t.Fatal(err)
	}
	if msg.Kind != MsgError {
		t.Fatalf("got %+v, want an error frame", msg)
	}
}

func TestUnknownKindIsNamedBack(t *testing.T) {
	// A silent no-op on an unknown frame looks exactly like a hang.
	_, path := testServer(t)
	client, err := DialPath(path)
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()

	client.Send(ClientMsg{Kind: "teleport"})
	msg, err := client.Recv()
	if err != nil {
		t.Fatal(err)
	}
	if msg.Kind != MsgError || !strings.Contains(msg.Err, "teleport") {
		t.Errorf("got %+v, want an error naming the kind", msg)
	}
}

func TestListenRefusesASecondDaemon(t *testing.T) {
	// Two daemons on one path would leave clients reaching whichever bound
	// last, with no sign anything was wrong.
	_, path := testServer(t)

	second := NewServer(&config.Config{}, t.TempDir(), "")
	second.Path = path
	err := second.Listen()
	if err == nil {
		second.Close()
		t.Fatal("a second daemon bound the same socket")
	}
	if !strings.Contains(err.Error(), "already listening") {
		t.Errorf("err = %v", err)
	}
}

func TestListenClearsAStaleSocket(t *testing.T) {
	dir, err := os.MkdirTemp("", "evild")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)
	path := filepath.Join(dir, "s.sock")

	// A crashed daemon leaves the file behind with nothing listening.
	if err := os.WriteFile(path, nil, 0o600); err != nil {
		t.Fatal(err)
	}

	srv := NewServer(&config.Config{}, t.TempDir(), "")
	srv.Path = path
	if err := srv.Listen(); err != nil {
		t.Fatalf("a stale socket blocked startup: %v", err)
	}
	srv.Close()
}

func TestSocketIsOwnerOnly(t *testing.T) {
	// Anything that can connect can run shell commands as this user, so the
	// mode is the access control, not a nicety.
	_, path := testServer(t)
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Errorf("socket mode = %o, want 600", perm)
	}
}

func TestCloseRemovesTheSocket(t *testing.T) {
	srv, path := testServer(t)
	srv.Close()
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Errorf("the socket survived Close: %v", err)
	}
}

func TestSinceLastTurnIsEmptyWhenIdle(t *testing.T) {
	// The failure this guards: a fresh attach replaying completed turns draws
	// the whole conversation twice, because the snapshot already carries it.
	r := NewRing()
	r.Add(agent.Event{Kind: agent.EventTurnStart})
	r.Add(agent.Event{Kind: agent.EventTextDelta, Text: "hello"})
	r.Add(agent.Event{Kind: agent.EventTurnEnd})

	if got := r.SinceLastTurn(); len(got) != 0 {
		t.Errorf("replayed %v for an idle session", texts(got))
	}
}

func TestSinceLastTurnReplaysAPartialTurn(t *testing.T) {
	// A client reconnecting mid-answer needs the deltas: the assistant message
	// is not in the conversation until the turn ends, so the snapshot lacks it.
	r := NewRing()
	r.Add(agent.Event{Kind: agent.EventTurnStart})
	r.Add(agent.Event{Kind: agent.EventTextDelta, Text: "done"})
	r.Add(agent.Event{Kind: agent.EventTurnEnd})
	r.Add(agent.Event{Kind: agent.EventTurnStart})
	r.Add(agent.Event{Kind: agent.EventTextDelta, Text: "in "})
	r.Add(agent.Event{Kind: agent.EventTextDelta, Text: "flight"})

	got := r.SinceLastTurn()
	if len(got) != 3 {
		t.Fatalf("replayed %v, want the live turn only", texts(got))
	}
	if got[0].Kind != agent.EventTurnStart {
		t.Errorf("replay starts at %q, want the turn start", got[0].Kind)
	}
	if got[1].Text+got[2].Text != "in flight" {
		t.Errorf("replay text = %q", got[1].Text+got[2].Text)
	}
}

func TestSinceLastTurnHandlesAnEmptyRing(t *testing.T) {
	if got := NewRing().SinceLastTurn(); got != nil {
		t.Errorf("a fresh ring replayed %v", texts(got))
	}
}

func TestCheckSocketPathNamesTheLimit(t *testing.T) {
	// The kernel's error for an over-long path is a bare "invalid argument",
	// which says nothing about the actual problem.
	err := CheckSocketPath(strings.Repeat("x", MaxSocketPath+1))
	if err == nil {
		t.Fatal("an over-long path was accepted")
	}
	if !strings.Contains(err.Error(), "limit") {
		t.Errorf("err = %v", err)
	}
	if err := CheckSocketPath("/run/user/1000/evilcode.sock"); err != nil {
		t.Errorf("a normal path was refused: %v", err)
	}
}

// Repairs ride through the attach snapshot, so an attached client's tool rows
// show the same repair suffix as the live session.
func TestSnapshotCarriesRepairs(t *testing.T) {
	srv, _ := testServer(t)
	defer srv.Close()
	sess, err := srv.Open("")
	if err != nil {
		t.Fatal(err)
	}
	// The agent's conversation is what snapshot renders; append a tool message
	// carrying repairs the way appendToolResult does.
	sess.built.Agent.Conv.Append(provider.Message{
		Role: provider.RoleTool, ToolName: "read", Content: "ok",
		Repairs: []string{"file_path→path"},
	})
	snap := sess.snapshot("")
	var found bool
	for _, m := range snap.Messages {
		if len(m.Repairs) > 0 {
			found = true
		}
	}
	if !found {
		t.Error("snapshot lost the repair metadata; attached clients would see no repair suffix")
	}
}
