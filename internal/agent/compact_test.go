package agent

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"unicode/utf8"

	"evilcode/internal/provider"
)

func summarizer(reply string, err error) Summarizer {
	return func(context.Context, string, string) (string, error) { return reply, err }
}

func compactableConversation() *Conversation {
	conv := NewConversation("sys")
	for i := 0; i < RecentTurnsToKeep+2; i++ {
		conv.Append(
			provider.Message{Role: provider.RoleUser, Content: fmt.Sprintf("turn %02d prompt", i)},
			provider.Message{Role: provider.RoleAssistant, Content: fmt.Sprintf("turn %02d answer", i)},
		)
	}
	return conv
}

func TestCompactReplacesTheConversation(t *testing.T) {
	conv := compactableConversation()
	var summarized string
	c := &Compactor{Summarize: func(_ context.Context, _, user string) (string, error) {
		summarized = user
		return "we wired auth", nil
	}}

	if _, err := c.Compact(context.Background(), conv); err != nil {
		t.Fatal(err)
	}
	msgs := conv.Messages()
	if !strings.Contains(msgs[1].Content, "we wired auth") {
		t.Errorf("summary message = %q", msgs[1].Content)
	}
	if strings.Contains(summarized, "turn 11") || !strings.Contains(summarized, "turn 00") {
		t.Errorf("summarizer saw the wrong portion: %q", summarized)
	}
	if strings.Contains(strings.Join(messageContents(msgs), "\n"), "turn 00") {
		t.Error("the old turn survived instead of being summarized")
	}
	if !strings.Contains(strings.Join(messageContents(msgs), "\n"), "turn 11 answer") {
		t.Error("the newest turn was not preserved verbatim")
	}
	if c.Count() != 1 {
		t.Errorf("count = %d, want 1", c.Count())
	}
}

func TestCompactKeepsTheConversationWhenPersistFails(t *testing.T) {
	// The order is the point: dropping the history in memory while nothing
	// reached storage would lose the session outright.
	conv := compactableConversation()
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
	if !strings.Contains(msgs[len(msgs)-1].Content, "turn 11 answer") ||
		!strings.Contains(strings.Join(messageContents(msgs), "\n"), "turn 00 prompt") {
		t.Errorf("history was replaced despite the failure: %v", msgs)
	}
}

func TestCompactUsesWhatPersistReturned(t *testing.T) {
	// Storage decides what a resume will replay, so memory follows it rather
	// than guessing — otherwise the two drift the moment the format changes.
	conv := compactableConversation()
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

func TestCompactCallsOnCompactionAfterReset(t *testing.T) {
	conv := compactableConversation()
	called := 0
	c := &Compactor{
		Summarize:    summarizer("fresh context", nil),
		OnCompaction: func() { called++ },
	}
	if _, err := c.Compact(context.Background(), conv); err != nil {
		t.Fatal(err)
	}
	if called != 1 {
		t.Fatalf("OnCompaction called %d times, want once", called)
	}
}

func TestAutoCompactHasABreaker(t *testing.T) {
	// Invariant 6. A summary that is itself over the threshold would otherwise
	// compact forever without ever sending a request — which presents as a hang
	// rather than as a loop, and is the worst shape of runaway.
	c := &Compactor{Summarize: summarizer("still enormous", nil)}
	conv := compactableConversation()

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

func TestCompactionDoesNotSplitToolCallResult(t *testing.T) {
	msgs := []provider.Message{
		{Role: provider.RoleAssistant, ToolCalls: []provider.ToolCall{{ID: "call-1", Name: "read"}}},
		{Role: provider.RoleTool, ToolCallID: "call-1", ToolName: "read", Content: "ok"},
		{Role: provider.RoleUser, Content: "continue"},
	}
	if got := safeToolBoundary(msgs, 1); got != 0 {
		t.Fatalf("cutoff = %d, want compaction refused for a split tool pair", got)
	}

	conv := NewConversation("sys")
	for i := 0; i < RecentTurnsToKeep+1; i++ {
		conv.Append(provider.Message{Role: provider.RoleUser, Content: fmt.Sprintf("prompt %d", i)})
	}
	conv.Append(provider.Message{Role: provider.RoleAssistant, ToolCalls: []provider.ToolCall{{ID: "unfinished", Name: "read"}}})
	c := &Compactor{Summarize: summarizer("summary", nil)}
	if _, err := c.Compact(context.Background(), conv); err == nil {
		t.Fatal("compaction should refuse an unanswered tool call in the kept tail")
	}
}

func TestCompactRequiresAnOlderTurn(t *testing.T) {
	conv := NewConversation("sys")
	for i := 0; i < RecentTurnsToKeep; i++ {
		conv.Append(provider.Message{Role: provider.RoleUser, Content: fmt.Sprintf("prompt %d", i)})
	}
	c := &Compactor{Summarize: summarizer("summary", nil)}
	if _, err := c.Compact(context.Background(), conv); err == nil {
		t.Fatal("compaction should not summarize an empty old prefix")
	}
}

func messageContents(msgs []provider.Message) []string {
	out := make([]string, 0, len(msgs))
	for _, msg := range msgs {
		out = append(out, msg.Content)
	}
	return out
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

func TestShouldCompactProjectsAheadOfTheThreshold(t *testing.T) {
	c := &Compactor{Summarize: summarizer("s", nil)}
	if c.ShouldCompact(50, 100) {
		t.Fatal("compacted before collecting a growth delta")
	}
	if c.ShouldCompact(52, 100) {
		t.Fatal("compacted when the projection still fits below the threshold")
	}
	if !c.ShouldCompact(55, 100) {
		t.Fatal("did not compact on a projection that crosses the threshold")
	}
}

func TestCompactionResetsTheGrowthProjection(t *testing.T) {
	conv := compactableConversation()
	c := &Compactor{Summarize: summarizer("summary", nil)}
	if c.ShouldCompact(50, 100) || !c.ShouldCompact(55, 100) {
		t.Fatal("expected the rising context to trigger a projected compaction")
	}
	if _, err := c.Compact(context.Background(), conv); err != nil {
		t.Fatal(err)
	}
	if c.ShouldCompact(50, 100) {
		t.Fatal("the pre-compaction growth slope leaked into the new context")
	}
	if c.ShouldCompact(52, 100) {
		t.Fatal("a fresh projection compacted before it had enough headroom evidence")
	}
}

func TestShouldCompactDropsStaleGrowthAfterContextShrinks(t *testing.T) {
	c := &Compactor{Summarize: summarizer("s", nil)}
	if c.ShouldCompact(50, 100) || !c.ShouldCompact(55, 100) {
		t.Fatal("expected the rising context to trigger a projected compaction")
	}
	if c.ShouldCompact(40, 100) {
		t.Fatal("a lower context should discard the stale growth projection")
	}
	if c.ShouldCompact(42, 100) {
		t.Fatal("a fresh low context should not compact immediately")
	}
}

func TestNilCompactorIsInert(t *testing.T) {
	var c *Compactor
	if c.Enabled() || c.ShouldCompact(99, 100) || c.Count() != 0 {
		t.Error("a nil compactor is not inert")
	}
}

func TestTranscriptCapDoesNotSplitARune(t *testing.T) {
	// "é" is two bytes; placed so the cap (a byte index) lands on its second
	// byte, a naive text[:CompactMessageCap] slice would split it in half.
	content := strings.Repeat("a", CompactMessageCap-1) + "é" + strings.Repeat("b", 10)
	msgs := []provider.Message{{Role: provider.RoleUser, Content: content}}
	got := Transcript(msgs)
	if !utf8.ValidString(got) {
		t.Errorf("transcript is not valid UTF-8: %q", got)
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
