package agent

import (
	"context"
	"errors"
	"strings"
	"testing"

	"evilcode/internal/provider"
)

func summarizer(reply string, err error) Summarizer {
	return func(context.Context, string, string) (string, error) { return reply, err }
}

func TestCompactReplacesTheConversation(t *testing.T) {
	conv := NewConversation("sys")
	conv.Append(
		provider.Message{Role: provider.RoleUser, Content: "wire the auth flow"},
		provider.Message{Role: provider.RoleAssistant, Content: "done"},
	)
	c := &Compactor{Summarize: summarizer("we wired auth", nil)}

	if _, err := c.Compact(context.Background(), conv); err != nil {
		t.Fatal(err)
	}
	msgs := conv.Messages()
	if got := msgs[len(msgs)-1].Content; !strings.Contains(got, "we wired auth") {
		t.Errorf("summary message = %q", got)
	}
	for _, m := range msgs {
		if strings.Contains(m.Content, "wire the auth flow") {
			t.Error("the pre-compaction history is still in the conversation")
		}
	}
	if c.Count() != 1 {
		t.Errorf("count = %d, want 1", c.Count())
	}
}

func TestCompactKeepsTheConversationWhenPersistFails(t *testing.T) {
	// The order is the point: dropping the history in memory while nothing
	// reached storage would lose the session outright.
	conv := NewConversation("sys")
	conv.Append(provider.Message{Role: provider.RoleUser, Content: "important work"})
	c := &Compactor{
		Summarize: summarizer("a summary", nil),
		Persist: func(string) ([]provider.Message, error) {
			return nil, errors.New("disk full")
		},
	}

	if _, err := c.Compact(context.Background(), conv); err == nil {
		t.Fatal("a failed persist should be reported")
	}
	msgs := conv.Messages()
	if msgs[len(msgs)-1].Content != "important work" {
		t.Errorf("history was replaced despite the failure: %v", msgs)
	}
}

func TestCompactUsesWhatPersistReturned(t *testing.T) {
	// Storage decides what a resume will replay, so memory follows it rather
	// than guessing — otherwise the two drift the moment the format changes.
	conv := NewConversation("sys")
	conv.Append(provider.Message{Role: provider.RoleUser, Content: "x"})
	stored := []provider.Message{{Role: provider.RoleUser, Content: "canonical replay"}}
	c := &Compactor{
		Summarize: summarizer("s", nil),
		Persist:   func(string) ([]provider.Message, error) { return stored, nil },
	}

	if _, err := c.Compact(context.Background(), conv); err != nil {
		t.Fatal(err)
	}
	msgs := conv.Messages()
	if got := msgs[len(msgs)-1].Content; got != "canonical replay" {
		t.Errorf("memory = %q, want what storage returned", got)
	}
}

func TestAutoCompactHasABreaker(t *testing.T) {
	// Invariant 6. A summary that is itself over the threshold would otherwise
	// compact forever without ever sending a request — which presents as a hang
	// rather than as a loop, and is the worst shape of runaway.
	c := &Compactor{Summarize: summarizer("still enormous", nil)}
	conv := NewConversation("sys")

	for i := 0; i < MaxAutoCompactions+3; i++ {
		conv.Append(provider.Message{Role: provider.RoleUser, Content: "x"})
		if !c.ShouldCompact(99, 100) {
			break
		}
		if _, err := c.Compact(context.Background(), conv); err != nil {
			t.Fatal(err)
		}
	}
	if c.Count() > MaxAutoCompactions {
		t.Errorf("compacted %d times, past the cap of %d", c.Count(), MaxAutoCompactions)
	}
	if c.ShouldCompact(99, 100) {
		t.Error("still willing to compact after hitting the cap")
	}
}

func TestShouldCompactOnlyNearTheLimit(t *testing.T) {
	c := &Compactor{Summarize: summarizer("s", nil)}
	if c.ShouldCompact(10, 100) {
		t.Error("compacted at 10% of the window")
	}
	if !c.ShouldCompact(90, 100) {
		t.Error("did not compact at 90% of the window")
	}
	// An unknown window must never trigger it: dividing by zero would.
	if c.ShouldCompact(90, 0) {
		t.Error("compacted with an unknown context window")
	}
}

func TestNilCompactorIsInert(t *testing.T) {
	var c *Compactor
	if c.Enabled() || c.ShouldCompact(99, 100) || c.Count() != 0 {
		t.Error("a nil compactor is not inert")
	}
}

func TestTranscriptSkipsSystemAndCapsAMessage(t *testing.T) {
	msgs := []provider.Message{
		{Role: provider.RoleSystem, Content: "the system prompt"},
		{Role: provider.RoleUser, Content: strings.Repeat("x", CompactMessageCap*2)},
	}
	got := Transcript(msgs)
	if strings.Contains(got, "the system prompt") {
		t.Error("the system prompt reached the summarizer; it is the same every turn")
	}
	if len(got) > CompactMessageCap*2 {
		t.Errorf("transcript is %d bytes; one pasted file should not crowd out the rest", len(got))
	}
}
