package daemon

import (
	"strings"
	"testing"
	"time"

	"evilcode/internal/agent"
)

// TestSessionsReportsMessageCount is the feedback loop for the start-page "all
// options say 0 messages" fix: after a real turn, the daemon's Sessions() list
// must report the conversation's message count, not zero.
func TestSessionsReportsMessageCount(t *testing.T) {
	srv, path := testServer(t)

	client, err := DialPath(path)
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()

	snap, err := client.Attach("", 0)
	if err != nil {
		t.Fatal(err)
	}

	// Before any prompt, the session has no user messages.
	if got, err := client.List(); err != nil {
		t.Fatal(err)
	} else if got[0].Messages != 0 {
		t.Errorf("pre-prompt Messages = %d, want 0", got[0].Messages)
	}

	if err := client.Send(ClientMsg{Kind: MsgInput, Session: snap.Session, Text: "hello"}); err != nil {
		t.Fatal(err)
	}

	// Wait for the turn to finish so the assistant reply is in the conversation.
	var text strings.Builder
	deadline := time.After(10 * time.Second)
	for {
		got := make(chan struct {
			msg ServerMsg
			err error
		}, 1)
		go func() {
			msg, err := client.Recv()
			got <- struct {
				msg ServerMsg
				err error
			}{msg, err}
		}()
		select {
		case <-deadline:
			t.Fatalf("timed out waiting for turn end; text so far: %q", text.String())
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
				goto done
			}
		}
	}
done:
	list, err := client.List()
	if err != nil {
		t.Fatal(err)
	}
	var row *SessionInfo
	for i := range list {
		if list[i].Name == snap.Session {
			row = &list[i]
		}
	}
	if row == nil {
		t.Fatalf("Sessions() did not list %q: %+v", snap.Session, list)
	}
	if row.Messages < 2 {
		t.Errorf("Sessions().Messages = %d, want >= 2 after a turn (user + assistant)", row.Messages)
	}
	if len(srv.Sessions()) == 0 || srv.Sessions()[0].Messages < 2 {
		t.Errorf("server Sessions() did not report the message count: %+v", srv.Sessions())
	}
}
