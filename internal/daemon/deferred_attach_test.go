package daemon

import (
	"strings"
	"testing"
	"time"

	"evilcode/internal/agent"
	"evilcode/internal/config"
	"evilcode/internal/session"
)

// recvFrame reads one frame with a deadline, so a daemon that never answers
// fails the test instead of hanging it.
func recvFrame(t *testing.T, c *Client) ServerMsg {
	t.Helper()
	if err := c.SetDeadline(10 * time.Second); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = c.SetDeadline(0) }()
	msg, err := c.Recv()
	if err != nil {
		t.Fatalf("recv: %v", err)
	}
	return msg
}

// drainText consumes frames until a turn ends, returning the streamed text and
// the snapshot that named the session.
func drainUntilTurnEnd(t *testing.T, c *Client) (string, *Snapshot) {
	t.Helper()
	var text strings.Builder
	var snap *Snapshot
	deadline := time.After(10 * time.Second)
	for {
		got := make(chan ServerMsg, 1)
		go func() {
			m, err := c.Recv()
			if err != nil {
				t.Error(err)
				return
			}
			got <- m
		}()
		select {
		case <-deadline:
			t.Fatalf("timed out; text so far: %q", text.String())
		case m := <-got:
			switch m.Kind {
			case MsgSnapshot:
				snap = m.Snapshot
			case MsgEvent:
				if m.Event == nil {
					continue
				}
				if m.Event.Kind == agent.EventTextDelta {
					text.WriteString(m.Event.Text)
				}
				if m.Event.Kind == agent.EventTurnEnd {
					return text.String(), snap
				}
			}
		}
	}
}

// TestDeferredAttachCreatesNothingUntilInput is the feedback loop for the
// deferred attach: the reply is a preview with no session name and nothing on
// disk, and the session materializes only when a prompt crosses the socket.
func TestDeferredAttachCreatesNothingUntilInput(t *testing.T) {
	srv, path := testServer(t)

	client, err := DialPath(path)
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()

	snap, err := client.AttachDeferred("deferred-ws", "")
	if err != nil {
		t.Fatal(err)
	}
	if snap.Session != "" {
		t.Fatalf("deferred attach invented a session %q", snap.Session)
	}
	if snap.Model != "mock-large" || snap.Provider != "mock" {
		t.Errorf("preview model = %s@%s, want the resolved mock default", snap.Model, snap.Provider)
	}
	if snap.Cwd != "deferred-ws" {
		t.Errorf("preview cwd = %q, want the attach cwd", snap.Cwd)
	}

	if rows, err := client.List(); err != nil || len(rows) != 0 {
		t.Fatalf("a deferred attach listed %v (err %v)", rows, err)
	}
	if len(srv.Sessions()) != 0 {
		t.Fatalf("a deferred attach hydrated %d session(s)", len(srv.Sessions()))
	}
	if stored, err := session.List(config.DataDir()); err != nil || len(stored) != 0 {
		t.Fatalf("the start page wrote %v session(s) to disk (err %v)", stored, err)
	}

	if err := client.Send(ClientMsg{Kind: MsgInput, RequestID: "r1", Text: "hello"}); err != nil {
		t.Fatal(err)
	}
	text, materialized := drainUntilTurnEnd(t, client)
	if !strings.Contains(text, "config") {
		t.Errorf("streamed text = %q, want the mock's chat reply", text)
	}
	if materialized == nil || materialized.Session == "" {
		t.Fatal("the first prompt produced no session snapshot")
	}

	rows := srv.Sessions()
	if len(rows) != 1 || rows[0].Name != materialized.Session {
		t.Fatalf("after the prompt the server holds %+v, want %q", rows, materialized.Session)
	}
	if stored, err := session.List(config.DataDir()); err != nil || len(stored) != 1 {
		t.Fatalf("after the prompt the disk holds %v session(s) (err %v)", stored, err)
	}
}

// TestDeferredAttachModelPickBecomesTheSessionModel: a model picked on the
// start page is what the session is created with, and the pick still persists
// through the same EventModel a session-owned switch publishes.
func TestDeferredAttachModelPickBecomesTheSessionModel(t *testing.T) {
	srv, path := testServer(t)

	client, err := DialPath(path)
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()

	if _, err := client.AttachDeferred("deferred-ws", ""); err != nil {
		t.Fatal(err)
	}
	if err := client.Send(ClientMsg{Kind: MsgModel, Model: "mock-small@mock"}); err != nil {
		t.Fatal(err)
	}
	msg := recvFrame(t, client)
	if msg.Kind != MsgEvent || msg.Event == nil || msg.Event.Kind != agent.EventModel {
		t.Fatalf("model pick reply = %v %v, want an EventModel", msg.Kind, msg.Event)
	}
	if msg.Event.Model != "mock-small" || msg.Event.Provider != "mock" {
		t.Errorf("EventModel = %s@%s, want mock-small@mock", msg.Event.Model, msg.Event.Provider)
	}
	if rows := srv.Sessions(); len(rows) != 0 {
		t.Fatalf("picking a model hydrated %d session(s)", len(rows))
	}

	if err := client.Send(ClientMsg{Kind: MsgInput, RequestID: "r1", Text: "hello"}); err != nil {
		t.Fatal(err)
	}
	_, snap := drainUntilTurnEnd(t, client)
	if snap == nil || snap.Session == "" {
		t.Fatal("the first prompt produced no session snapshot")
	}
	if snap.Model != "mock-small" {
		t.Errorf("session model = %q, want the pre-session pick mock-small", snap.Model)
	}
}

// TestDeferredAttachEffortPickIsApplied: an effort chosen before the session
// exists survives into the session, so the first turn runs with it.
func TestDeferredAttachEffortPickIsApplied(t *testing.T) {
	_, path := testServer(t)

	client, err := DialPath(path)
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()

	if _, err := client.AttachDeferred("deferred-ws", ""); err != nil {
		t.Fatal(err)
	}
	if err := client.Send(ClientMsg{Kind: MsgReasoningEffort, ReasoningEffort: "low"}); err != nil {
		t.Fatal(err)
	}
	msg := recvFrame(t, client)
	if msg.Kind != MsgEvent || msg.Event == nil || msg.Event.Kind != agent.EventModel {
		t.Fatalf("effort pick reply = %v, want an EventModel", msg.Kind)
	}
	if msg.Event.ReasoningEffort != "low" {
		t.Errorf("EventModel effort = %q, want low", msg.Event.ReasoningEffort)
	}

	if err := client.Send(ClientMsg{Kind: MsgInput, RequestID: "r1", Text: "hello"}); err != nil {
		t.Fatal(err)
	}
	_, snap := drainUntilTurnEnd(t, client)
	if snap == nil {
		t.Fatal("the first prompt produced no session snapshot")
	}
	if snap.ReasoningEffort != "low" {
		t.Errorf("session effort = %q, want the pre-session pick low", snap.ReasoningEffort)
	}
}

// TestDeferredAttachInterruptBeforeAnyPromptIsANoOp: Esc on the start page
// must not produce a daemon error for a session that does not exist.
func TestDeferredAttachInterruptBeforeAnyPromptIsANoOp(t *testing.T) {
	_, path := testServer(t)

	client, err := DialPath(path)
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()

	if _, err := client.AttachDeferred("deferred-ws", ""); err != nil {
		t.Fatal(err)
	}
	if err := client.Send(ClientMsg{Kind: MsgInterrupt, Text: "stop"}); err != nil {
		t.Fatal(err)
	}
	// A follow-up list answers on the same connection; an error would have
	// been queued ahead of it.
	if err := client.Send(ClientMsg{Kind: MsgList}); err != nil {
		t.Fatal(err)
	}
	msg := recvFrame(t, client)
	if msg.Kind != MsgSessions {
		t.Fatalf("after an interrupt the next frame = %v, want the list reply", msg.Kind)
	}
	if rows := srvSessions(path); len(rows) != 0 {
		t.Fatalf("the interrupt hydrated %v", rows)
	}
}

// TestDeferredAttachCommandNamesTheMissingSession: a slash command before any
// prompt explains itself instead of the old attach-order error.
func TestDeferredAttachCommandNamesTheMissingSession(t *testing.T) {
	_, path := testServer(t)

	client, err := DialPath(path)
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()

	if _, err := client.AttachDeferred("deferred-ws", ""); err != nil {
		t.Fatal(err)
	}
	if err := client.Send(ClientMsg{Kind: MsgCommand, Text: "clear"}); err != nil {
		t.Fatal(err)
	}
	msg := recvFrame(t, client)
	if msg.Kind != MsgError {
		t.Fatalf("command reply = %v, want an error", msg.Kind)
	}
	if want := "no session yet"; !strings.Contains(msg.Err, want) {
		t.Errorf("error = %q, want it to contain %q", msg.Err, want)
	}
}

// srvSessions is a helper for the no-op test, which has no server handle.
func srvSessions(path string) []SessionInfo {
	c, err := DialPath(path)
	if err != nil {
		return nil
	}
	defer c.Close()
	rows, err := c.List()
	if err != nil {
		return nil
	}
	return rows
}
