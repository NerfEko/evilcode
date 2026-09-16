package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"evilcode/internal/agent/compact"
	"evilcode/internal/provider"
)

const compactionFixtureTurns = 12

func summarizer(reply string, err error) Summarizer {
	return func(context.Context, string, string) (string, error) {
		if err != nil {
			return "", err
		}
		return ompOK(reply), nil
	}
}

func compactableConversation() *Conversation {
	conv := NewConversation("sys")
	for i := 0; i < compactionFixtureTurns; i++ {
		// ~2.5k tokens per turn so the 20000-token keep-recent budget leaves
		// a real summarized prefix in a 12-turn fixture (omp cut semantics).
		conv.Append(
			provider.Message{Role: provider.RoleUser, Content: fmt.Sprintf("turn %02d prompt %s", i, strings.Repeat("p", 6000))},
			provider.Message{Role: provider.RoleAssistant, Content: fmt.Sprintf("turn %02d answer %s", i, strings.Repeat("a", 2000))},
		)
	}
	return conv
}

func largeCompactionConversation() *Conversation {
	conv := NewConversation("sys")
	for i := 0; i < 4; i++ {
		conv.Append(
			provider.Message{
				Role:    provider.RoleUser,
				Content: fmt.Sprintf("large turn %02d %s", i, strings.Repeat("context ", 625)),
			},
			provider.Message{Role: provider.RoleAssistant, Content: "acknowledged"},
		)
	}
	return conv
}

// ompOK is the minimal omp-skeleton summary the gate accepts for the
// compactionFixtureTurns fixture; the fixture goal "turn NN prompt" is not
// real-world text, so FirstUserMessage is empty in these tests and the
// goal-echo gate is inert.
func ompOK(summary string) string {
	return "## Goal\n" + summary + "\n\n## Progress\n\n### Done\n- [x] x\n\n## Next Steps\n1. next"
}

func TestCompactReplacesTheConversation(t *testing.T) {
	conv := compactableConversation()
	var summarized, shortInput string
	c := &Compactor{Summarize: func(_ context.Context, _, user string) (string, error) {
		if summarized == "" {
			summarized = user
		} else if shortInput == "" {
			shortInput = user
		}
		return ompOK("we wired auth"), nil
	}}

	if _, err := c.Compact(context.Background(), conv); err != nil {
		t.Fatal(err)
	}
	msgs := conv.Messages()
	if !strings.Contains(msgs[1].Content, "we wired auth") {
		t.Errorf("summary message = %q", msgs[1].Content)
	}
	if !strings.HasPrefix(msgs[1].Content, "[conversation compacted]") {
		t.Errorf("summary message lost the marker: %q", msgs[1].Content[:40])
	}
	// omp cut semantics on this fixture: the summarizer sees turns 00-04;
	// turns 05..11 stay verbatim in the recent tail (FirstKept=11).
	for _, kept := range []string{"turn 05", "turn 11"} {
		if strings.Contains(summarized, kept) {
			t.Errorf("summarizer saw kept-tail turn %q", kept)
		}
	}
	if !strings.Contains(summarized, "turn 00") {
		t.Errorf("summarizer missed the oldest turn: %q", summarized)
	}
	if !strings.Contains(summarized, "<conversation>") {
		t.Errorf("transcript lost the omp conversation wrapper")
	}
	if strings.Contains(strings.Join(messageContents(msgs), "\n"), "turn 00") {
		t.Error("the old turn survived instead of being summarized")
	}
	for _, kept := range []string{"turn 05 prompt", "turn 11 answer"} {
		if !strings.Contains(strings.Join(messageContents(msgs), "\n"), kept) {
			t.Errorf("kept turn %q did not survive verbatim", kept)
		}
	}
	if c.Count() != 1 {
		t.Errorf("count = %d, want 1", c.Count())
	}
}

func TestCompactCarriesForwardPriorSummary(t *testing.T) {
	conv := compactableConversation()
	const prior = "FACT_EXACT_731 means preserve the rollback checklist"
	summaries := []string{prior, "newer work summary"}
	var inputs []string
	c := &Compactor{
		FirstUserMessage: "",
		Summarize: func(_ context.Context, _, user string) (string, error) {
			inputs = append(inputs, user)
			// Two side-calls per compaction: history summary then short
			// summary. Map each to its slot: history calls are inputs 0 and 2.
			idx := (len(inputs)+1)/2 - 1
			if idx >= len(summaries) {
				idx = len(summaries) - 1
			}
			return ompOK(summaries[idx]), nil
		},
	}

	if _, err := c.Compact(context.Background(), conv); err != nil {
		t.Fatal(err)
	}
	for j := 0; j < 3; j++ {
		conv.Append(
			provider.Message{Role: provider.RoleUser, Content: fmt.Sprintf("new work %d %s", j, strings.Repeat("n", 6000))},
			provider.Message{Role: provider.RoleAssistant, Content: fmt.Sprintf("done %d %s", j, strings.Repeat("d", 6000))},
		)
	}
	if _, err := c.Compact(context.Background(), conv); err != nil {
		t.Fatal(err)
	}
	// Two side-calls per compaction: [hist1, short1, hist2, short2]. The
	// second history summary rides the <previous-summary> block.
	if len(inputs) != 4 || !strings.Contains(inputs[2], "<previous-summary>") || !strings.Contains(inputs[2], prior) {
		t.Fatalf("second summary did not receive the prior summary: %#v", inputs)
	}
	joined := strings.Join(messageContents(conv.Messages()), "\n")
	if !strings.Contains(joined, prior) || !strings.Contains(joined, summaries[1]) {
		t.Fatalf("checkpoint lost prior or new summary: %q", joined)
	}
}

func TestCompactDropsProviderStateFromTheCheckpointTail(t *testing.T) {
	conv := compactableConversation()
	conv.Append(
		provider.Message{Role: provider.RoleUser, Content: "current request"},
		provider.Message{
			Role:      provider.RoleAssistant,
			Content:   "visible answer",
			Reasoning: "private model thinking that must not cross the boundary",
			ProviderItems: []json.RawMessage{
				json.RawMessage(`{"type":"reasoning","encrypted_content":"secret continuation"}`),
			},
			Images:  [][]byte{[]byte("raw image bytes")},
			Repairs: []string{"display-only repair"},
			Diff:    "display-only diff",
		},
	)

	var persisted []provider.Message
	c := &Compactor{
		Summarize: summarizer("summary", nil),
		PersistWithTail: func(_ string, tail []provider.Message) ([]provider.Message, error) {
			persisted = append(persisted, tail...)
			return tail, nil
		},
	}
	if _, err := c.Compact(context.Background(), conv); err != nil {
		t.Fatal(err)
	}

	for _, name := range []string{"durable", "model"} {
		msgs := conv.Messages()
		if name == "model" {
			msgs = conv.MessagesForModel()
		}
		for _, msg := range msgs {
			if msg.Reasoning != "" || len(msg.ProviderItems) > 0 ||
				len(msg.ToolCalls) > 0 || msg.Role == provider.RoleTool {
				t.Fatalf("%s checkpoint retained provider-native state: %#v", name, msg)
			}
		}
		joined := strings.Join(messageContents(msgs), "\n")
		if strings.Contains(joined, "private model thinking") ||
			strings.Contains(joined, "secret continuation") ||
			strings.Contains(joined, "raw image bytes") {
			t.Fatalf("%s checkpoint exposed discarded provider state: %q", name, joined)
		}
		if !strings.Contains(joined, "visible answer") {
			t.Fatalf("%s checkpoint lost visible recent context: %q", name, joined)
		}
	}
	if len(persisted) != 1 || !persisted[0].Hidden ||
		!strings.HasPrefix(persisted[0].Content, CompactedRecentPrefix) {
		t.Fatalf("persisted tail = %#v, want one hidden serialized checkpoint", persisted)
	}
}

func TestCompactDoesNotLoseAnAppendDuringSummarization(t *testing.T) {
	conv := compactableConversation()
	started := make(chan struct{})
	release := make(chan struct{})
	c := &Compactor{Summarize: func(context.Context, string, string) (string, error) {
		// Two side-calls per compaction now; only the first must block the
		// racing append. closing an already-closed channel panics.
		select {
		case <-started:
		default:
			close(started)
		}
		<-release
		return ompOK("summary"), nil
	}}

	done := make(chan error, 1)
	go func() {
		_, err := c.Compact(context.Background(), conv)
		done <- err
	}()
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("compaction did not reach the summarizer")
	}

	appendDone := make(chan struct{})
	go func() {
		conv.Append(provider.Message{Role: provider.RoleUser, Content: "arrived during compaction"})
		close(appendDone)
	}()
	select {
	case <-appendDone:
		t.Fatal("append was allowed to race the compaction rewrite")
	case <-time.After(20 * time.Millisecond):
	}

	close(release)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	select {
	case <-appendDone:
	case <-time.After(time.Second):
		t.Fatal("append stayed blocked after compaction completed")
	}
	last, ok := conv.Last()
	if !ok || last.Content != "arrived during compaction" {
		t.Fatalf("last message = %#v, want the post-compaction append", last)
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

func TestCompactUsesWhatPersistWithTailReturned(t *testing.T) {
	conv := compactableConversation()
	stored := []provider.Message{{Role: provider.RoleUser, Content: "canonical durable replay"}}
	c := &Compactor{
		Summarize: summarizer("s", nil),
		PersistWithTail: func(string, []provider.Message) ([]provider.Message, error) {
			return stored, nil
		},
	}

	if _, err := c.Compact(context.Background(), conv); err != nil {
		t.Fatal(err)
	}
	msgs := conv.Messages()
	if got := msgs[len(msgs)-1].Content; got != "canonical durable replay" {
		t.Errorf("memory = %q, want what durable storage returned", got)
	}
}

type keywordCompactionEmbedder struct{}

func (keywordCompactionEmbedder) Embed(_ context.Context, texts []string) ([][]float32, error) {
	vectors := make([][]float32, len(texts))
	for i, text := range texts {
		if strings.Contains(strings.ToLower(text), "oauth") {
			vectors[i] = []float32{1, 0}
		} else {
			vectors[i] = []float32{0, 1}
		}
	}
	return vectors, nil
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
		conv.Append(provider.Message{Role: provider.RoleUser, Content: fmt.Sprintf("x%d %s", i, strings.Repeat("y", 6000))})
		if !c.ShouldCompact(99, 100) {
			break
		}
		// omp rule: a compaction with nothing new since the last summary is a
		// no-op; only the count of SUCCESSFUL compactions feeds the breaker.
		if _, err := c.Compact(context.Background(), conv); err != nil {
			break // history exhausted: every surviving turn is in the tail
		}
		// The automatic path records its compaction against its own budget
		// (R2-14); manual /compact calls do not.
		c.noteAutoCompaction()
	}
	if c.Count() > MaxAutoCompactions {
		t.Errorf("compacted %d times, past the cap of %d", c.Count(), MaxAutoCompactions)
	}
	c2 := &Compactor{Summarize: summarizer("s", nil), Settings: compact.DefaultSettings()}
	for range MaxAutoCompactions {
		c2.noteAutoCompaction()
	}
	if c2.ShouldCompact(90000, 100000) {
		t.Error("still willing to compact after hitting the cap")
	}
}

func TestCompactionDoesNotSplitToolCallResult(t *testing.T) {
	msgs := []provider.Message{
		{Role: provider.RoleAssistant, ToolCalls: []provider.ToolCall{{ID: "call-1", Name: "read"}}},
		{Role: provider.RoleTool, ToolCallID: "call-1", ToolName: "read", Content: "ok"},
		{Role: provider.RoleUser, Content: "continue"},
	}
	if got := compact.SafeToolBoundary(msgs, 1); got != 0 {
		t.Fatalf("cutoff = %d, want compaction refused for a split tool pair", got)
	}

	conv := NewConversation("sys")
	for i := 0; i < compactionFixtureTurns+1; i++ {
		conv.Append(provider.Message{Role: provider.RoleUser, Content: fmt.Sprintf("prompt %d", i)})
	}
	conv.Append(provider.Message{Role: provider.RoleAssistant, ToolCalls: []provider.ToolCall{{ID: "unfinished", Name: "read"}}})
	c := &Compactor{Summarize: summarizer("summary", nil)}
	if _, err := c.Compact(context.Background(), conv); err == nil {
		t.Fatal("compaction should refuse an unanswered tool call in the kept tail")
	}
}

func TestCompactRequiresAnOlderTurn(t *testing.T) {
	// omp: prepareCompaction returns undefined when nothing would be
	// summarized — a conversation with only the newest turn kept has an
	// empty old prefix. One small turn: everything stays in the tail.
	conv := NewConversation("sys")
	for range 1 {
		conv.Append(provider.Message{Role: provider.RoleUser, Content: fmt.Sprintf("prompt %d", 0)})
	}
	c := &Compactor{Summarize: summarizer("summary", nil)}
	if _, err := c.Compact(context.Background(), conv); err == nil {
		t.Fatal("compaction should not summarize an empty old prefix")
	}
}

func TestCompactUsesATokenTailBeforeTenTurns(t *testing.T) {
	conv := NewConversation("sys")
	// 10 turns × ~1.3k tokens: the 2500-token keep-recent budget keeps only
	// the newest turns; the older prefix is summarized before any turn-count
	// gate would fire (omp cut semantics vs the old fixed ten-turn rule).
	for i := 0; i < 10; i++ {
		conv.Append(
			provider.Message{
				Role:    provider.RoleUser,
				Content: fmt.Sprintf("turn %02d %s", i, strings.Repeat("context ", 625)),
			},
			provider.Message{Role: provider.RoleAssistant, Content: "answer"},
		)
	}

	var summarized string
	c := &Compactor{
		ContextWindow: 10_000,
		// omp fixed keep-recent budget scaled to the small fixture window:
		// settings are the knob; the 20000 default presumes real windows.
		Settings: func() compact.Settings {
			s := compact.DefaultSettings()
			s.KeepRecentTokens = 2500
			return s
		}(),
		Summarize: func(_ context.Context, _, user string) (string, error) {
			if summarized == "" {
				summarized = user
			}
			return ompOK("summary"), nil
		},
	}
	if _, err := c.Compact(context.Background(), conv); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(summarized, "turn 00") {
		t.Fatalf("the old prefix was not summarized: %q", summarized)
	}
	if strings.Contains(strings.Join(messageContents(conv.Messages()), "\n"), "turn 00") {
		t.Fatal("the oldest turn survived instead of being compacted")
	}
	if !strings.Contains(strings.Join(messageContents(conv.Messages()), "\n"), "turn 09") {
		t.Fatal("the newest turn was not preserved")
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
	c := &Compactor{Summarize: summarizer("s", nil), Settings: compact.DefaultSettings()}
	// omp: threshold = window - max(15% window, 16384) → 100k window
	// compacts past 83616.
	if c.ShouldCompact(10000, 100000) {
		t.Error("compacted at 10% of the window")
	}
	if !c.ShouldCompact(90000, 100000) {
		t.Error("did not compact at 90% of the window")
	}
	// An unknown window must never trigger it.
	if c.ShouldCompact(90000, 0) {
		t.Error("compacted with an unknown context window")
	}
}

func TestShouldCompactForConversationRequiresAnOlderPrefix(t *testing.T) {
	c := &Compactor{Summarize: summarizer("s", nil), Settings: compact.DefaultSettings()}

	// omp trigger semantics: the threshold needs a window past the reserve
	// floor; 100 tokens sits inside the 16384 default reserve.
	short := NewConversation("sys")
	for i := 0; i < 4; i++ {
		short.Append(
			provider.Message{Role: provider.RoleUser, Content: fmt.Sprintf("prompt %d", i)},
			provider.Message{Role: provider.RoleAssistant, Content: "answer"},
		)
	}
	if c.ShouldCompactForConversation(10000, 100000, short) {
		t.Fatal("compacted under the threshold")
	}
	if !c.ShouldCompactForConversation(90000, 100000, largeCompactionConversation()) {
		t.Fatal("did not compact past the threshold with a summarizable prefix")
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

func TestTranscriptKeepsMultibyteTextIntact(t *testing.T) {
	// The omp serializer truncates tool results at a char budget; multibyte
	// text must survive intact either way.
	content := "ok é " + strings.Repeat("b", 10)
	msgs := []provider.Message{{Role: provider.RoleUser, Content: content}}
	got := Transcript(msgs)
	if !utf8.ValidString(got) {
		t.Errorf("transcript is not valid UTF-8: %q", got)
	}
	if !strings.Contains(got, "ok é") {
		t.Errorf("multibyte content mangled: %q", got)
	}
}

func TestTranscriptSkipsSystem(t *testing.T) {
	msgs := []provider.Message{
		{Role: provider.RoleSystem, Content: "the system prompt"},
		{Role: provider.RoleUser, Content: "visible"},
	}
	got := Transcript(msgs)
	if strings.Contains(got, "the system prompt") {
		t.Error("the system prompt reached the summarizer; it is the same every turn")
	}
	if !strings.Contains(got, "visible") {
		t.Errorf("user content lost: %q", got)
	}
}

func TestTranscriptKeepsEveryTurn(t *testing.T) {
	// omp sends the full history to the summarizer: no byte cap, no middle
	// elision. Long sessions rely on the model window, not a byte clamp.
	msgs := []provider.Message{{Role: provider.RoleUser, Content: "first goal"}}
	for i := 0; i < 100; i++ {
		msgs = append(msgs, provider.Message{
			Role:    provider.RoleAssistant,
			Content: fmt.Sprintf("middle %03d %s", i, strings.Repeat("x", 500)),
		})
	}
	msgs = append(msgs, provider.Message{Role: provider.RoleUser, Content: "last active state"})

	got := Transcript(msgs)
	if !strings.Contains(got, "first goal") || !strings.Contains(got, "last active state") ||
		!strings.Contains(got, "middle 050") {
		t.Fatalf("transcript lost its turns: %q", got)
	}
}

func TestTranscriptDescribesToolCallsAndResults(t *testing.T) {
	msgs := []provider.Message{
		{Role: provider.RoleAssistant, ToolCalls: []provider.ToolCall{{ID: "call-1", Name: "read", Args: []byte(`{"path":"plan4.md"}`)}}},
		{Role: provider.RoleTool, ToolCallID: "call-1", ToolName: "read", Content: "the complete plan"},
	}
	got := Transcript(msgs)
	// omp serializer markers: name(args) for calls, labeled results.
	if !strings.Contains(got, `[Tool Call]: read({"path":"plan4.md"})`) {
		t.Fatalf("tool invocation was absent from compaction transcript: %q", got)
	}
	if !strings.Contains(got, "[Tool Result (read)]: the complete plan") {
		t.Fatalf("tool result identity was absent from compaction transcript: %q", got)
	}
}
