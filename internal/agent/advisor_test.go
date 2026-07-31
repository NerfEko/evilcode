package agent

import (
	"context"
	"errors"
	"strings"
	"testing"

	"evilcode/internal/provider"
)

// advisorSaying builds an advisor with a canned reply and a call counter.
func advisorSaying(reply string, err error) (*Advisor, *int, *string) {
	calls := 0
	var sawUser string
	a := NewAdvisor(func(_ context.Context, _, user string) (string, error) {
		calls++
		sawUser = user
		return reply, err
	}, true)
	return a, &calls, &sawUser
}

func TestAdvisorStaysQuietOnNone(t *testing.T) {
	// The default has to be silence. An advisor that comments every turn is the
	// second driver §21 says this must never become.
	a, _, _ := advisorSaying(AdvisorNone, nil)
	ag := newTestAgent(t, provider.NewMock("mock", "chat"), nil)
	ag.Conv.Append(provider.Message{Role: provider.RoleUser, Content: "do the thing"})

	appended, err := a.PostTurn(context.Background(), ag)
	if err != nil || appended {
		t.Fatalf("PostTurn = %v, %v", appended, err)
	}
	if ag.PendingInterrupts() != 0 {
		t.Error("a NONE answer still injected something")
	}
}

func TestAdvisorInjectsAConcernAsASystemInterrupt(t *testing.T) {
	a, _, _ := advisorSaying("It is deleting files the user never mentioned.", nil)
	ag := newTestAgent(t, provider.NewMock("mock", "chat"), nil)
	ag.Conv.Append(provider.Message{Role: provider.RoleUser, Content: "tidy up"})

	if appended, _ := a.PostTurn(context.Background(), ag); appended {
		t.Error("the advisor appended to the conversation; it must interject instead")
	}
	msgs := ag.DrainInterrupts(false)
	if len(msgs) != 1 {
		t.Fatalf("interrupts = %d, want 1", len(msgs))
	}
	if !strings.Contains(msgs[0].Content, "advisor:") {
		t.Errorf("concern = %q, want it marked as the advisor's", msgs[0].Content)
	}
	if !strings.Contains(msgs[0].Content, "deleting files") {
		t.Errorf("concern = %q", msgs[0].Content)
	}
}

func TestAdvisorDoesNotRepeatItself(t *testing.T) {
	// The same worry twice is noise, and noise gets ignored — including the
	// time it happens to be right.
	a, _, _ := advisorSaying("It has said it is done but the list is not.", nil)
	ag := newTestAgent(t, provider.NewMock("mock", "chat"), nil)
	ag.Conv.Append(provider.Message{Role: provider.RoleUser, Content: "finish up"})

	a.PostTurn(context.Background(), ag)
	ag.DrainInterrupts(false)
	a.PostTurn(context.Background(), ag)

	if n := ag.PendingInterrupts(); n != 0 {
		t.Errorf("the same concern was raised twice (%d pending)", n)
	}
}

func TestAdvisorYieldsToAnotherVoice(t *testing.T) {
	// §21: never while something else is mid-argument with the model.
	a, calls, _ := advisorSaying("something is wrong", nil)
	ag := newTestAgent(t, provider.NewMock("mock", "chat"), nil)
	ag.Conv.Append(provider.Message{Role: provider.RoleUser, Content: "go on"})
	ag.Interject(Interrupt{Source: SourceSystem, Text: "you still have incomplete todos"})

	a.PostTurn(context.Background(), ag)
	if *calls != 0 {
		t.Error("the advisor spoke while auto-poke already had the floor")
	}
}

func TestAdvisorDisabledDoesNothing(t *testing.T) {
	a, calls, _ := advisorSaying("concern", nil)
	a.SetEnabled(false)
	ag := newTestAgent(t, provider.NewMock("mock", "chat"), nil)
	ag.Conv.Append(provider.Message{Role: provider.RoleUser, Content: "hi"})

	a.PostTurn(context.Background(), ag)
	if *calls != 0 {
		t.Errorf("a disabled advisor made %d calls", *calls)
	}
}

func TestAdvisorSurvivesAFailedSideCall(t *testing.T) {
	// A conscience that breaks the turn is worse than no conscience.
	a, _, _ := advisorSaying("", errors.New("model unreachable"))
	ag := newTestAgent(t, provider.NewMock("mock", "chat"), nil)
	ag.Conv.Append(provider.Message{Role: provider.RoleUser, Content: "hi"})

	appended, err := a.PostTurn(context.Background(), ag)
	if err != nil || appended {
		t.Errorf("PostTurn = %v, %v; a failed advisor must be invisible", appended, err)
	}
}

func TestAdvisorViewCarriesTodoStateAndToolCalls(t *testing.T) {
	// Both are things the advisor cannot otherwise see, and both are what its
	// two most useful checks depend on: "says done but isn't", and "has tried
	// the same thing three times".
	a, _, sawUser := advisorSaying(AdvisorNone, nil)
	a.TodoState = func() string { return "1/4 done" }

	ag := newTestAgent(t, provider.NewMock("mock", "chat"), nil)
	ag.Conv.Append(
		provider.Message{Role: provider.RoleUser, Content: "fix the parser"},
		provider.Message{Role: provider.RoleAssistant, ToolCalls: []provider.ToolCall{
			{Name: "edit"}, {Name: "bash"},
		}},
		provider.Message{Role: provider.RoleAssistant, Content: "All done."},
	)
	a.PostTurn(context.Background(), ag)

	for _, want := range []string{"1/4 done", "fix the parser", "edit", "bash", "All done."} {
		if !strings.Contains(*sawUser, want) {
			t.Errorf("the advisor's view is missing %q:\n%s", want, *sawUser)
		}
	}
}

func TestAdvisorViewOmitsTheSystemPrompt(t *testing.T) {
	// It is the same every turn and it is large; paying for it on every side
	// call is exactly the cost §21 says this must not have.
	a, _, sawUser := advisorSaying(AdvisorNone, nil)
	ag := newTestAgent(t, provider.NewMock("mock", "chat"), nil)
	ag.Conv.Append(provider.Message{Role: provider.RoleUser, Content: "hello"})

	a.PostTurn(context.Background(), ag)
	if strings.Contains(*sawUser, "system") {
		t.Errorf("the system prompt reached the advisor:\n%s", *sawUser)
	}
}

func TestAdvisorViewTruncatesAHugeMessage(t *testing.T) {
	a, _, sawUser := advisorSaying(AdvisorNone, nil)
	ag := newTestAgent(t, provider.NewMock("mock", "chat"), nil)
	ag.Conv.Append(provider.Message{
		Role: provider.RoleUser, Content: strings.Repeat("x", AdvisorMessageCap*3),
	})

	a.PostTurn(context.Background(), ag)
	if len(*sawUser) > AdvisorMessageCap*2 {
		t.Errorf("view is %d bytes; one pasted file should not crowd out the rest", len(*sawUser))
	}
	if !strings.Contains(*sawUser, "[…]") {
		t.Error("truncation should be visible, not silent")
	}
}

func TestAdvisorStatusCountsWhatItRaised(t *testing.T) {
	a, _, _ := advisorSaying("a real concern", nil)
	ag := newTestAgent(t, provider.NewMock("mock", "chat"), nil)
	ag.Conv.Append(provider.Message{Role: provider.RoleUser, Content: "go"})

	if got := a.Status(); !strings.Contains(got, "0 concerns") {
		t.Errorf("status = %q", got)
	}
	a.PostTurn(context.Background(), ag)
	if got := a.Status(); !strings.Contains(got, "1 concern") {
		t.Errorf("status = %q, want the count", got)
	}
}

func TestNilAdvisorIsInert(t *testing.T) {
	var a *Advisor
	if a.Enabled() {
		t.Error("a nil advisor reports enabled")
	}
	a.SetEnabled(true)
	if !strings.Contains(a.Status(), "not configured") {
		t.Errorf("status = %q", a.Status())
	}
}
