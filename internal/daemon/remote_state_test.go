package daemon

import (
	"context"
	"strings"
	"testing"
	"time"

	"evilcode/internal/agent"
	"evilcode/internal/provider"
	"evilcode/internal/tools"
)

func TestAskAnswerBroadcastsResolution(t *testing.T) {
	b := newAskBroker()
	events := make(chan agent.Event, 2)
	b.SetPublisher(func(e agent.Event) { events <- e })
	req := &tools.AskRequest{
		Question: "pick one",
		Options:  []tools.AskOption{{Label: "yes"}, {Label: "no"}},
		Reply:    make(chan []string, 1),
	}
	result := make(chan []string, 1)
	go func() {
		answer, err := b.Ask(context.Background(), req)
		if err != nil {
			t.Errorf("Ask returned an error: %v", err)
			return
		}
		result <- answer
	}()

	first := <-events
	if first.Kind != agent.EventAsk || first.Ask == nil {
		t.Fatalf("first event = %+v, want ask", first)
	}
	if err := b.Answer(req.ID, []string{"yes"}); err != nil {
		t.Fatal(err)
	}
	select {
	case got := <-result:
		if len(got) != 1 || got[0] != "yes" {
			t.Fatalf("answer = %v, want [yes]", got)
		}
	case <-time.After(time.Second):
		t.Fatal("ask did not receive its answer")
	}
	resolved := <-events
	if resolved.Kind != agent.EventAskResolved || resolved.RequestID != req.ID {
		t.Fatalf("resolution event = %+v, want request %q", resolved, req.ID)
	}
}

func TestNoToolsSessionDoesNotReceiveDaemonCoordinationTools(t *testing.T) {
	srv, _ := testServer(t)
	defer srv.Close()

	sess, err := srv.OpenWithOptions("", OpenOptions{NoTools: true})
	if err != nil {
		t.Fatal(err)
	}
	if !sess.NoTools || len(sess.built.Agent.Tools) != 0 {
		t.Fatalf("no-tools session = noTools:%v tools:%d", sess.NoTools, len(sess.built.Agent.Tools))
	}

	regular, err := srv.Open("")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := srv.OpenWithOptions(regular.Name, OpenOptions{NoTools: true}); err == nil ||
		!strings.Contains(err.Error(), "already has tools") {
		t.Fatalf("existing tools session accepted --no-tools: %v", err)
	}
}

func TestExistingSessionHonorsExplicitModel(t *testing.T) {
	srv, _ := testServer(t)
	defer srv.Close()

	sess, err := srv.OpenWithOptions("", OpenOptions{Model: "mock-small@mock"})
	if err != nil {
		t.Fatal(err)
	}
	if sess.Model != "mock-small" {
		t.Fatalf("initial model = %q", sess.Model)
	}
	again, err := srv.OpenWithOptions(sess.Name, OpenOptions{Model: "mock-large@mock"})
	if err != nil {
		t.Fatal(err)
	}
	if again != sess || again.Model != "mock-large" {
		t.Fatalf("resumed session = %p model %q, want same session with mock-large", again, again.Model)
	}
}

func TestRejectedModelEffortLeavesSessionUnchanged(t *testing.T) {
	srv, _ := testServer(t)
	defer srv.Close()

	sess, err := srv.OpenWithOptions("", OpenOptions{Model: "mock-large@mock"})
	if err != nil {
		t.Fatal(err)
	}
	if err := sess.SetModelWithEffort("mock-small@mock", provider.ReasoningEffortHigh); err == nil {
		t.Fatal("unsupported reasoning effort was accepted")
	}
	if sess.Model != "mock-large" || sess.built.Agent.Model != "mock-large" {
		t.Fatalf("rejected switch changed model to %q/%q", sess.Model, sess.built.Agent.Model)
	}
}

func TestCredentialUpdateReachesEveryLiveSessionConfig(t *testing.T) {
	srv, _ := testServer(t)
	defer srv.Close()

	first, err := srv.Open("")
	if err != nil {
		t.Fatal(err)
	}
	second, err := srv.Open("")
	if err != nil {
		t.Fatal(err)
	}
	if err := first.setCredential("mock", "session-secret"); err != nil {
		t.Fatal(err)
	}
	for _, sess := range []*Session{first, second} {
		found := false
		for _, pc := range sess.built.Config.Providers {
			if pc.Name == "mock" {
				found = pc.APIKey == "session-secret"
			}
		}
		if !found {
			t.Fatalf("session %q did not receive the credential", sess.Name)
		}
	}
	srv.mu.Lock()
	defer srv.mu.Unlock()
	for _, pc := range srv.Cfg.Providers {
		if pc.Name == "mock" && pc.APIKey != "session-secret" {
			t.Fatalf("daemon config key = %q", pc.APIKey)
		}
	}
}

func TestHiddenInputIsHiddenInEventsAndSnapshots(t *testing.T) {
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
	if err := client.Send(ClientMsg{
		Kind: MsgInput, Session: snap.Session, RequestID: "hidden-test",
		Text: "do not draw this", Hidden: true,
	}); err != nil {
		t.Fatal(err)
	}

	deadline := time.After(10 * time.Second)
	var turnEnd *agent.Event
	for {
		select {
		case <-deadline:
			t.Fatal("timed out waiting for hidden turn")
		default:
		}
		msg, recvErr := client.Recv()
		if recvErr != nil {
			t.Fatal(recvErr)
		}
		if msg.Kind != MsgEvent || msg.Event == nil {
			continue
		}
		if msg.Event.Kind == agent.EventTurnStart {
			if !msg.Event.Hidden || msg.Event.Text != "" {
				t.Fatalf("hidden turn start = %+v", *msg.Event)
			}
		}
		if msg.Event.Kind == agent.EventTurnEnd {
			turnEnd = msg.Event
			break
		}
	}
	if turnEnd == nil || len(turnEnd.SnapshotMessages) == 0 || !turnEnd.SnapshotMessages[0].Hidden {
		t.Fatalf("turn-end history did not retain hidden marker: %+v", turnEnd)
	}

	second, err := DialPath(path)
	if err != nil {
		t.Fatal(err)
	}
	defer second.Close()
	reconnected, err := second.Attach(snap.Session, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(reconnected.Messages) == 0 || !reconnected.Messages[0].Hidden {
		t.Fatalf("reconnected snapshot lost hidden marker: %+v", reconnected.Messages)
	}
}
