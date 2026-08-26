package agent

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"evilcode/internal/provider"
)

// noDoneProvider closes its stream without a terminal Done chunk — a contract
// violation a third-party provider could commit. The turn must fail instead of
// reading as a complete answer (B5).
type noDoneProvider struct{}

func (p *noDoneProvider) Name() string { return "nodone" }
func (p *noDoneProvider) Embed(context.Context, []string) ([][]float32, error) {
	return nil, nil
}
func (p *noDoneProvider) Models(context.Context) ([]provider.ModelInfo, error) {
	return nil, nil
}
func (p *noDoneProvider) ChatStream(context.Context, provider.Req) (<-chan provider.Chunk, error) {
	ch := make(chan provider.Chunk, 1)
	ch <- provider.Chunk{Text: "half an answer"}
	close(ch)
	return ch, nil
}

func TestStreamWithoutTerminalDoneIsAnError(t *testing.T) {
	a := newTestAgent(t, &noDoneProvider{}, nil)
	a.MaxRetries = 0
	evs, err := collect(t, a, func() error { return a.Run(context.Background(), "hi") })
	if err == nil {
		t.Fatal("a stream that closes without a terminal chunk must not succeed")
	}
	if !strings.Contains(err.Error(), "terminal done chunk") {
		t.Errorf("err = %v, want the missing-terminal diagnostic", err)
	}
	if last := evs[len(evs)-1]; last.Reason != EndError {
		t.Errorf("reason = %v, want error turn end", last.Reason)
	}
}

// doubleDoneProvider emits two terminal chunks in one stream — also a contract
// violation. Exactly one terminal chunk is the contract (B5).
type doubleDoneProvider struct{}

func (p *doubleDoneProvider) Name() string { return "doubledone" }
func (p *doubleDoneProvider) Embed(context.Context, []string) ([][]float32, error) {
	return nil, nil
}
func (p *doubleDoneProvider) Models(context.Context) ([]provider.ModelInfo, error) {
	return nil, nil
}
func (p *doubleDoneProvider) ChatStream(context.Context, provider.Req) (<-chan provider.Chunk, error) {
	ch := make(chan provider.Chunk, 3)
	ch <- provider.Chunk{Text: "one"}
	ch <- provider.Chunk{Done: true}
	ch <- provider.Chunk{Done: true}
	close(ch)
	return ch, nil
}

func TestMultipleTerminalChunksAreAnError(t *testing.T) {
	a := newTestAgent(t, &doubleDoneProvider{}, nil)
	a.MaxRetries = 0
	_, err := collect(t, a, func() error { return a.Run(context.Background(), "hi") })
	if err == nil {
		t.Fatal("a stream with two terminal chunks must be rejected")
	}
	if !strings.Contains(err.Error(), "multiple terminal chunks") {
		t.Errorf("err = %v, want the multiple-terminal diagnostic", err)
	}
}

// truncatedProvider closes its stream before any visible output on the first
// attempt. The truncation is a transport-level interruption, so a retry is
// allowed when nothing was shown (B2/B6); the second attempt completes.
type truncatedProvider struct{ attempts *int }

func (p *truncatedProvider) Name() string { return "truncated" }
func (p *truncatedProvider) Embed(context.Context, []string) ([][]float32, error) {
	return nil, nil
}
func (p *truncatedProvider) Models(context.Context) ([]provider.ModelInfo, error) {
	return nil, nil
}
func (p *truncatedProvider) ChatStream(context.Context, provider.Req) (<-chan provider.Chunk, error) {
	*p.attempts++
	ch := make(chan provider.Chunk, 2)
	if *p.attempts == 1 {
		ch <- provider.Chunk{Err: fmt.Errorf("openai: %w", provider.ErrStreamTruncated)}
		close(ch)
		return ch, nil
	}
	ch <- provider.Chunk{Text: "recovered"}
	ch <- provider.Chunk{Done: true}
	close(ch)
	return ch, nil
}

func TestTruncatedStreamRetriesWhenNothingShown(t *testing.T) {
	var attempts int
	a := newTestAgent(t, &truncatedProvider{attempts: &attempts}, nil)
	a.MaxRetries = 2
	evs, err := collect(t, a, func() error { return a.Run(context.Background(), "hi") })
	if err != nil {
		t.Fatalf("a truncation before any output should be retried: %v", err)
	}
	if attempts != 2 {
		t.Errorf("attempts = %d, want 2 (truncation then recovery)", attempts)
	}
	if text := textOf(evs); !strings.Contains(text, "recovered") {
		t.Errorf("text = %q, want the recovered answer", text)
	}
	if last := evs[len(evs)-1]; last.Reason != EndComplete {
		t.Errorf("reason = %v, want complete", last.Reason)
	}
}
