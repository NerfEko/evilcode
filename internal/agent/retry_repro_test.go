package agent

import (
	"context"
	"strings"
	"testing"

	"evilcode/internal/provider"
)

// midStreamFlaky emits visible text and then fails mid-stream with a
// retryable error on the first attempt, then succeeds on the second.
type midStreamFlaky struct{ attempts *int }

func (m *midStreamFlaky) Name() string { return "midstreamflaky" }
func (m *midStreamFlaky) Embed(ctx context.Context, t []string) ([][]float32, error) {
	return nil, nil
}
func (m *midStreamFlaky) Models(ctx context.Context) ([]provider.ModelInfo, error) {
	return nil, nil
}
func (m *midStreamFlaky) ChatStream(ctx context.Context, req provider.Req) (<-chan provider.Chunk, error) {
	*m.attempts++
	ch := make(chan provider.Chunk, 4)
	if *m.attempts == 1 {
		ch <- provider.Chunk{Text: "Hello"}
		ch <- provider.Chunk{Err: &provider.HTTPError{Status: 503, Message: "upstream busy"}}
		close(ch)
		return ch, nil
	}
	ch <- provider.Chunk{Text: "Hello, world"}
	ch <- provider.Chunk{Done: true, Usage: &provider.Usage{PromptTokens: 1, CompletionTokens: 1}}
	close(ch)
	return ch, nil
}

func TestStreamDoesNotReplayVisibleDeltasOnRetry(t *testing.T) {
	// A stream that fails mid-stream after deltas were already emitted must
	// not be retried: the provider re-streams from scratch, and the TUI
	// appends the second attempt's deltas to the same live block — replaying
	// the prefix visibly. Retry only before the first emitted delta.
	var attempts int
	p := &midStreamFlaky{attempts: &attempts}
	a := newTestAgent(t, p, nil)
	evs, _ := collect(t, a, func() error { return a.Run(context.Background(), "hi") })
	if text := textOf(evs); strings.Contains(text, "HelloHello") {
		t.Errorf("visible content replayed on retry: %q", text)
	}
}
