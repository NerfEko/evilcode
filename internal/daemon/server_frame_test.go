package daemon

import (
	"strings"
	"testing"
	"time"

	"evilcode/internal/agent"
	"evilcode/internal/provider"
)

// R2-01: a healthy client must never be disconnected by the server's own
// frames. History image bytes never travel on the wire (nothing re-renders an
// image from history), and the connection writer downgrades any frame that
// would still exceed the client's scanner limit.

// TestHistoryCarriesNoImageBytes pins the source-level strip: a turn whose
// conversation holds image bytes publishes a turn-end snapshot without them,
// and an attaching client's snapshot omits them too.
func TestHistoryCarriesNoImageBytes(t *testing.T) {
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

	// Inject the image directly: the client→server frame limit would refuse a
	// 20 MiB attachment, which is exactly the size that must not be able to
	// leak into a server→client frame either.
	sess, err := srv.Open(snap.Session)
	if err != nil {
		t.Fatal(err)
	}
	big := make([]byte, 20<<20)
	sess.built.Agent.Conv.Append(provider.Message{
		Role: provider.RoleUser, Content: "look", Images: [][]byte{big},
	})

	if err := client.Send(ClientMsg{Kind: MsgInput, Session: snap.Session, Text: "done?"}); err != nil {
		t.Fatal(err)
	}

	deadline := time.After(10 * time.Second)
	for {
		select {
		case <-deadline:
			t.Fatal("timed out waiting for turn end")
		default:
		}
		msg, err := client.Recv()
		if err != nil {
			t.Fatalf("client disconnected by server frame: %v", err)
		}
		if msg.Kind != MsgEvent || msg.Event == nil || msg.Event.Kind != agent.EventTurnEnd {
			continue
		}
		for i, m := range msg.Event.SnapshotMessages {
			if len(m.Images) > 0 {
				t.Fatalf("turn-end history message %d carries %d image bytes", i, len(m.Images[0]))
			}
		}
		if len(msg.Event.SnapshotMessages) == 0 {
			t.Fatal("turn end carried no history at all")
		}
		break
	}

	// The attach snapshot for a fresh client strips image bytes too.
	second, err := DialPath(path)
	if err != nil {
		t.Fatal(err)
	}
	defer second.Close()
	reattach, err := second.Attach(snap.Session, 0)
	if err != nil {
		t.Fatalf("attach failed: %v", err)
	}
	for i, m := range reattach.Messages {
		if len(m.Images) > 0 {
			t.Fatalf("snapshot message %d carries image bytes", i)
		}
	}
	if len(reattach.Messages) == 0 {
		t.Fatal("snapshot carried no messages")
	}
}

// TestAttachSurvivesImageHeavyHistory is the disconnect the review called
// deterministic: a session whose history holds a 20 MiB image must still
// attach, because the image is stripped before the frame is built.
func TestAttachSurvivesImageHeavyHistory(t *testing.T) {
	srv, path := testServer(t)
	defer srv.Close()

	sess, err := srv.Open("")
	if err != nil {
		t.Fatal(err)
	}
	name := sess.Name
	sess.built.Agent.Conv.Append(provider.Message{
		Role: provider.RoleUser, Content: "look", Images: [][]byte{make([]byte, 20<<20)},
	})

	client, err := DialPath(path)
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	snap, err := client.Attach(name, 0)
	if err != nil {
		t.Fatalf("attach to a 20 MiB-image session failed: %v", err)
	}
	if snap.Truncated {
		t.Fatal("snapshot was truncated; image stripping should have kept it under the limit")
	}
	if len(snap.Messages) == 0 {
		t.Fatal("snapshot lost its messages")
	}
}

// TestTurnEndFrameDowngradeKeepsClientConnected drives the writer guard: a
// turn-end event whose history copy alone exceeds MaxServerFrameBytes is
// downgraded to an incomplete snapshot instead of disconnecting the client.
func TestTurnEndFrameDowngradeKeepsClientConnected(t *testing.T) {
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

	// One oversized event, published through the normal path: publishEvent
	// rebuilds a turn-end history from the live conversation, so the oversized
	// text must be in the conversation itself.
	sess, err := srv.Open(snap.Session)
	if err != nil {
		t.Fatal(err)
	}
	sess.built.Agent.Conv.Append(provider.Message{
		Role: provider.RoleUser, Content: strings.Repeat("x", MaxServerFrameBytes+1),
	})
	sess.publishEvent(agent.Event{Kind: agent.EventTurnEnd, Session: snap.Session})

	deadline := time.After(5 * time.Second)
	for {
		select {
		case <-deadline:
			t.Fatal("timed out waiting for the downgraded turn end")
		default:
		}
		msg, err := client.Recv()
		if err != nil {
			t.Fatalf("client disconnected instead of receiving a downgraded frame: %v", err)
		}
		if msg.Kind != MsgEvent || msg.Event == nil || msg.Event.Kind != agent.EventTurnEnd {
			continue
		}
		if !msg.Event.SnapshotIncomplete {
			t.Fatal("downgraded turn end did not mark itself incomplete")
		}
		if len(msg.Event.SnapshotMessages) != 0 {
			t.Fatal("downgraded turn end still carries history bytes")
		}
		break
	}

	// The connection is still good after the downgrade.
	if _, err := client.Attach(snap.Session, 0); err != nil {
		t.Fatalf("connection unusable after downgrade: %v", err)
	}
}

// TestSnapshotTruncationKeepsNewestHalf checks the snapshot downgrade: an
// oversized message list is cut from the oldest side, keeps the newest
// messages that fit, and is flagged so clients know the transcript is partial.
func TestSnapshotTruncationKeepsNewestHalf(t *testing.T) {
	// Each repeated message is just over half the frame budget: the full list
	// cannot fit, but the newest message alone does, so halving must stop with
	// the newest message kept rather than dropping everything.
	half := MaxServerFrameBytes/2 + 2048
	snap := &Snapshot{Session: "s", Messages: []Message{
		{Role: "user", Content: "oldest"},
		{Role: "user", Content: strings.Repeat("x", half)},
		{Role: "user", Content: strings.Repeat("y", half)},
	}}
	out, _, ok := fitServerFrame(ServerMsg{Kind: MsgSnapshot, Snapshot: snap})
	if !ok {
		t.Fatal("snapshot could not be fitted at all")
	}
	if !out.Snapshot.Truncated {
		t.Fatal("truncated snapshot was not flagged")
	}
	if len(out.Snapshot.Messages) != 1 {
		t.Fatalf("kept %d messages, want the newest one", len(out.Snapshot.Messages))
	}
	if strings.Count(out.Snapshot.Messages[0].Content, "y") != half {
		t.Fatalf("truncation dropped the newest message: kept %q", out.Snapshot.Messages[0].Content[:20])
	}
}

// TestSnapshotWithSingleOversizedMessageDegradesToEnvelope pins the fallback:
// when one message alone cannot fit a frame, the client still receives the
// snapshot envelope instead of an unbounded frame or a dropped connection.
func TestSnapshotWithSingleOversizedMessageDegradesToEnvelope(t *testing.T) {
	snap := &Snapshot{Session: "s", Messages: []Message{
		{Role: "user", Content: strings.Repeat("x", MaxServerFrameBytes)},
	}}
	out, _, ok := fitServerFrame(ServerMsg{Kind: MsgSnapshot, Snapshot: snap})
	if !ok {
		t.Fatal("envelope-only downgrade failed")
	}
	if !out.Snapshot.Truncated {
		t.Fatal("envelope downgrade was not flagged as truncated")
	}
	if len(out.Snapshot.Messages) != 0 {
		t.Fatalf("envelope downgrade kept %d messages", len(out.Snapshot.Messages))
	}
}

// TestEveryServerFrameKindIsEncodable guards the "unreachable" branch in
// writeServerFrame: the scalar shell of an event must always marshal under
// the limit.
func TestEveryServerFrameKindIsEncodable(t *testing.T) {
	shells := []ServerMsg{
		{Kind: MsgEvent, Event: &agent.Event{Kind: agent.EventTurnEnd, Session: "s", Text: strings.Repeat("y", 32<<10)}},
		{Kind: MsgSnapshot, Snapshot: &Snapshot{Session: "s"}},
		{Kind: MsgError, Err: strings.Repeat("e", MaxServerFrameBytes+1)},
	}
	for i, msg := range shells {
		fitted, buf, ok := fitServerFrame(msg)
		if !ok {
			t.Fatalf("frame %d (%s) could not be fitted", i, msg.Kind)
		}
		if len(buf) > MaxServerFrameBytes {
			t.Fatalf("frame %d is %d bytes, over the limit", i, len(buf))
		}
		if fitted.Kind != msg.Kind {
			t.Fatalf("frame %d lost its kind", i)
		}
	}
}
