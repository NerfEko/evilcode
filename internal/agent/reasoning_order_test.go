package agent

import (
	"context"
	"testing"

	"evilcode/internal/provider"
)

// A provider chunk can carry the thought's final token and the answer's first
// token in the same packet — coalescing APIs do this at the token boundary.
// The events for that one packet must keep stream order (reasoning, then
// text): the thinking precedes the answer it produced. Emitting the text
// delta first made the TUI close the thinking trace on the answer's first
// token and reopen a trace after the reply, leaving the thought's last word
// stranded below the answer.
type mixedChunkProvider struct{ calls int }

func (p *mixedChunkProvider) Name() string { return "mixed-chunk" }
func (p *mixedChunkProvider) Embed(context.Context, []string) ([][]float32, error) {
	return nil, nil
}
func (p *mixedChunkProvider) Models(context.Context) ([]provider.ModelInfo, error) {
	return nil, nil
}
func (p *mixedChunkProvider) ChatStream(_ context.Context, _ provider.Req) (<-chan provider.Chunk, error) {
	p.calls++
	ch := make(chan provider.Chunk, 3)
	if p.calls == 1 {
		ch <- provider.Chunk{Reasoning: "weighing the options, the answer is"}
		ch <- provider.Chunk{Reasoning: " the final word", Text: "The answer is 4."}
	} else {
		ch <- provider.Chunk{Text: "ok"}
	}
	ch <- provider.Chunk{Done: true}
	close(ch)
	return ch, nil
}

func TestReasoningPrecedesTextWithinACoalescedChunk(t *testing.T) {
	p := &mixedChunkProvider{}
	a := newTestAgent(t, p, nil)
	evs, err := collect(t, a, func() error { return a.Run(context.Background(), "hi") })
	if err != nil {
		t.Fatal(err)
	}

	var order []EventKind
	for _, e := range evs {
		if e.Kind == EventTextDelta || e.Kind == EventReasoningDelta {
			order = append(order, e.Kind)
		}
	}
	want := []EventKind{EventReasoningDelta, EventReasoningDelta, EventTextDelta}
	if len(order) != len(want) {
		t.Fatalf("delta order = %v, want %v — a reasoning delta after the answer starts strands the thought's last word after the reply", order, want)
	}
	for i, kind := range want {
		if order[i] != kind {
			t.Fatalf("delta order = %v, want %v — a reasoning delta after the answer starts strands the thought's last word after the reply", order, want)
		}
	}
}
