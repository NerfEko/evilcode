package agent

import (
	"context"
	"encoding/json"
	"testing"

	"evilcode/internal/provider"
	"evilcode/internal/tools"
)

// twoToolProvider answers once with a two-call round and never again.
type twoToolProvider struct{ served bool }

func (p *twoToolProvider) Name() string { return "twotool" }
func (p *twoToolProvider) Embed(ctx context.Context, t []string) ([][]float32, error) {
	return nil, nil
}
func (p *twoToolProvider) Models(ctx context.Context) ([]provider.ModelInfo, error) {
	return nil, nil
}
func (p *twoToolProvider) ChatStream(ctx context.Context, req provider.Req) (<-chan provider.Chunk, error) {
	ch := make(chan provider.Chunk, 2)
	if !p.served {
		p.served = true
		ch <- provider.Chunk{ToolCalls: []provider.ToolCall{
			{ID: "call_1", Name: "blocker", Args: json.RawMessage(`{}`)},
			{ID: "call_2", Name: "blocker", Args: json.RawMessage(`{}`)},
		}}
	} else {
		ch <- provider.Chunk{Text: "done"}
	}
	close(ch)
	return ch, nil
}

// H1.2: a round cancelled mid-batch used to return before appending any result,
// leaving the assistant's tool_calls unanswered in both the conversation and the
// JSONL. A strict OpenAI-compatible endpoint rejects that transcript with a 400
// on the very next request.
func TestCancelledToolRoundStillAnswersEveryCall(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	entered := make(chan struct{}, 2)
	blocker := tools.Tool{
		Name:   "blocker",
		Desc:   "waits for cancellation",
		Schema: json.RawMessage(`{"type":"object"}`),
		Run: func(ctx context.Context, args json.RawMessage) (tools.Result, error) {
			entered <- struct{}{}
			<-ctx.Done()
			return tools.Result{}, ctx.Err()
		},
	}

	a := newTestAgent(t, &twoToolProvider{}, tools.Set{blocker})
	_, err := collect(t, a, func() error {
		go func() {
			<-entered
			<-entered
			cancel()
		}()
		return a.Run(ctx, "go")
	})
	if err != nil {
		t.Fatalf("an interrupt is not an error: %v", err)
	}

	assertToolCallsAnswered(t, a.Conv.Messages())
}

// assertToolCallsAnswered checks the invariant every OpenAI-compatible endpoint
// enforces: each tool_call carries an adjacent result with its ID.
func assertToolCallsAnswered(t *testing.T, msgs []provider.Message) {
	t.Helper()
	answered := make(map[string]bool)
	for _, m := range msgs {
		if m.Role == provider.RoleTool {
			answered[m.ToolCallID] = true
		}
	}
	for _, m := range msgs {
		if m.Role != provider.RoleAssistant {
			continue
		}
		for _, c := range m.ToolCalls {
			if !answered[c.ID] {
				t.Errorf("tool call %q (%s) has no result message", c.ID, c.Name)
			}
		}
	}
}
