package agent

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"
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

func TestShouldCompactOnTopicShiftBeforeTheFixedThreshold(t *testing.T) {
	c := &Compactor{Summarize: summarizer("s", nil)}
	for range 2 {
		c.AddEmbeddingSnapshot([]float32{1, 0})
	}
	for range 2 {
		c.AddEmbeddingSnapshot([]float32{0, 1})
	}

	if !c.ShouldCompact(40, 100) {
		t.Fatal("a low-similarity topic shift should compact above the proactive floor")
	}
}

func TestShouldCompactForConversationRequiresAnOlderPrefix(t *testing.T) {
	c := &Compactor{Summarize: summarizer("s", nil)}
	for range 2 {
		c.AddEmbeddingSnapshot([]float32{1, 0})
	}
	for range 2 {
		c.AddEmbeddingSnapshot([]float32{0, 1})
	}

	short := NewConversation("sys")
	for i := 0; i < 4; i++ {
		short.Append(
			provider.Message{Role: provider.RoleUser, Content: fmt.Sprintf("prompt %d", i)},
			provider.Message{Role: provider.RoleAssistant, Content: "answer"},
		)
	}
	if c.ShouldCompactForConversation(40, 100, short) {
		t.Fatal("a topic shift should not fire when there is no prefix to summarize")
	}
	if !c.ShouldCompactForConversation(40, 100, compactableConversation()) {
		t.Fatal("a topic shift should fire once an older prefix can be compacted")
	}
}

func TestShouldCompactUsesGrowthWhenTopicsStaySimilar(t *testing.T) {
	c := &Compactor{Summarize: summarizer("s", nil)}
	for range 4 {
		c.AddEmbeddingSnapshot([]float32{1, 0})
	}

	if c.ShouldCompact(50, 100) {
		t.Fatal("similar topics should not trigger semantic compaction by themselves")
	}
	if c.ShouldCompact(52, 100) {
		t.Fatal("the predictive fallback should still respect its growth evidence")
	}
	if !c.ShouldCompact(55, 100) {
		t.Fatal("the predictive fallback did not trigger when its projection crossed the threshold")
	}
}

func TestCompactionEmbeddingRequestDoesNotBlockShouldCompact(t *testing.T) {
	embedder := &blockingCompactionEmbedder{
		started:  make(chan struct{}),
		release:  make(chan struct{}),
		finished: make(chan struct{}),
	}
	c := &Compactor{
		Summarize: summarizer("s", nil),
		Embedding: embedder,
	}
	c.RecordEmbeddingSnapshot(context.Background(), "a completed assistant turn")
	select {
	case <-embedder.started:
	case <-time.After(time.Second):
		t.Fatal("the asynchronous embedding request did not start")
	}

	decision := make(chan bool, 1)
	go func() { decision <- c.ShouldCompact(40, 100) }()
	select {
	case <-decision:
	case <-time.After(100 * time.Millisecond):
		t.Fatal("ShouldCompact waited for a slow embedding provider")
	}

	close(embedder.release)
	select {
	case <-embedder.finished:
	case <-time.After(time.Second):
		t.Fatal("the embedding request did not finish after release")
	}
}

func TestCompactionEmbeddingSurvivesTurnCancellation(t *testing.T) {
	embedder := &cancellationAwareCompactionEmbedder{
		started:  make(chan struct{}),
		release:  make(chan struct{}),
		finished: make(chan struct{}),
		canceled: make(chan struct{}),
	}
	c := &Compactor{Summarize: summarizer("s", nil), Embedding: embedder}
	ctx, cancel := context.WithCancel(context.Background())
	c.RecordEmbeddingSnapshot(ctx, "a completed assistant turn")
	select {
	case <-embedder.started:
	case <-time.After(time.Second):
		t.Fatal("the asynchronous embedding request did not start")
	}
	cancel()
	close(embedder.release)
	select {
	case <-embedder.finished:
	case <-time.After(time.Second):
		t.Fatal("the detached embedding request did not finish")
	}
	select {
	case <-embedder.canceled:
		t.Fatal("the turn cancellation canceled the detached embedding")
	default:
	}

	c.mu.Lock()
	history := len(c.embeddingHistory)
	c.mu.Unlock()
	if history != 1 {
		t.Fatalf("embedding history length = %d, want 1", history)
	}
}

func TestResetSemanticHistoryDiscardsOldTopics(t *testing.T) {
	c := &Compactor{Summarize: summarizer("s", nil)}
	for range 2 {
		c.AddEmbeddingSnapshot([]float32{1, 0})
	}
	for range 2 {
		c.AddEmbeddingSnapshot([]float32{0, 1})
	}
	c.ResetSemanticHistory()
	if c.ShouldCompact(40, 100) {
		t.Fatal("reset semantic history left a stale topic shift")
	}
}

func TestShouldCompactIgnoresZeroNormEmbeddings(t *testing.T) {
	c := &Compactor{Summarize: summarizer("s", nil)}
	for range 4 {
		c.AddEmbeddingSnapshot([]float32{0, 0})
	}
	if c.ShouldCompact(40, 100) {
		t.Fatal("zero-norm embeddings should fall back instead of signaling a topic shift")
	}
}

type blockingCompactionEmbedder struct {
	started  chan struct{}
	release  chan struct{}
	finished chan struct{}
}

type cancellationAwareCompactionEmbedder struct {
	started  chan struct{}
	release  chan struct{}
	finished chan struct{}
	canceled chan struct{}
}

func (b *cancellationAwareCompactionEmbedder) Embed(ctx context.Context, _ []string) ([][]float32, error) {
	close(b.started)
	defer close(b.finished)
	select {
	case <-b.release:
		return [][]float32{{1, 0}}, nil
	case <-ctx.Done():
		close(b.canceled)
		return nil, ctx.Err()
	}
}

func (b *blockingCompactionEmbedder) Embed(ctx context.Context, _ []string) ([][]float32, error) {
	close(b.started)
	defer close(b.finished)
	select {
	case <-b.release:
		return [][]float32{{1, 0}}, nil
	case <-ctx.Done():
		return nil, ctx.Err()
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
