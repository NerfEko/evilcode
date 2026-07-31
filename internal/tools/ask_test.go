package tools

import (
	"testing"
	"time"
)

// H2.15: PendingAsk was a single slot with no queue. A second question
// overwrote the first, whose Reply channel then had nobody to answer it — so
// its tool call blocked until the user interrupted the whole turn. The comment
// claiming the tool batch bounded this was wrong: a batch runs its calls
// concurrently, so two asks in one round is the ordinary case, not an exotic
// one.
func TestTwoQuestionsInOneRoundAreBothAnswered(t *testing.T) {
	var p PendingAsk
	first := &AskRequest{Question: "one?", Reply: make(chan []string, 1)}
	second := &AskRequest{Question: "two?", Reply: make(chan []string, 1)}

	p.Set(first)
	p.Set(second)

	if p.Get() != first {
		t.Fatal("the second question displaced the first on screen")
	}
	p.Answer([]string{"a"})
	if got := answerOf(t, first); got[0] != "a" {
		t.Errorf("the first question was answered %v", got)
	}

	if p.Get() != second {
		t.Fatal("answering the first question did not bring up the one behind it")
	}
	p.Answer([]string{"b"})
	if got := answerOf(t, second); got[0] != "b" {
		t.Errorf("the second question was answered %v", got)
	}
	if p.Get() != nil {
		t.Error("a question is still showing after both were answered")
	}
}

// A cancelled call resolves its own question, not whichever one is on screen.
func TestRemovingAQueuedQuestionLeavesTheRest(t *testing.T) {
	var p PendingAsk
	first := &AskRequest{Question: "one?", Reply: make(chan []string, 1)}
	second := &AskRequest{Question: "two?", Reply: make(chan []string, 1)}
	p.Set(first)
	p.Set(second)

	p.Remove(second)
	if len(answerOf(t, second)) != 0 {
		t.Error("the removed question came back with an answer")
	}
	if p.Get() != first {
		t.Error("removing a queued question disturbed the one on screen")
	}

	p.Answer([]string{"a"})
	if p.Get() != nil {
		t.Error("the removed question was shown after the first was answered")
	}
}

// Cancelling the turn releases every waiting call, not just the visible one.
func TestCancelReleasesEveryWaitingCall(t *testing.T) {
	var p PendingAsk
	reqs := make([]*AskRequest, 3)
	for i := range reqs {
		reqs[i] = &AskRequest{Question: "q?", Reply: make(chan []string, 1)}
		p.Set(reqs[i])
	}

	p.Cancel()
	for i, req := range reqs {
		if len(answerOf(t, req)) != 0 {
			t.Errorf("question %d came back with an answer", i)
		}
	}
	if p.Get() != nil {
		t.Error("a question is still showing after cancelling")
	}
}

func answerOf(t *testing.T, req *AskRequest) []string {
	t.Helper()
	select {
	case answer := <-req.Reply:
		return answer
	case <-time.After(2 * time.Second):
		t.Fatalf("question %q was never resolved; its tool call blocks until the "+
			"turn is interrupted", req.Question)
		return nil
	}
}
