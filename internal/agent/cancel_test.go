package agent

import (
	"context"
	"encoding/json"
	"strings"
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

// partialCallProvider emits text and a tool call, then hangs until cancelled,
// so the interrupt lands after the call was received but before it was run.
type partialCallProvider struct{ started chan struct{} }

func (p *partialCallProvider) Name() string { return "partialcall" }
func (p *partialCallProvider) Embed(ctx context.Context, t []string) ([][]float32, error) {
	return nil, nil
}
func (p *partialCallProvider) Models(ctx context.Context) ([]provider.ModelInfo, error) {
	return nil, nil
}
func (p *partialCallProvider) ChatStream(ctx context.Context, req provider.Req) (<-chan provider.Chunk, error) {
	ch := make(chan provider.Chunk)
	go func() {
		defer close(ch)
		select {
		case ch <- provider.Chunk{Text: "let me look"}:
		case <-ctx.Done():
			return
		}
		select {
		case ch <- provider.Chunk{ToolCalls: []provider.ToolCall{
			{ID: "call_9", Name: "blocker", Args: json.RawMessage(`{}`)},
		}}:
		case <-ctx.Done():
			return
		}
		close(p.started)
		<-ctx.Done()
	}()
	return ch, nil
}

// H1.3: commitPartial checked Content and Reasoning for emptiness and then
// appended the whole message — including tool calls that were received but
// never dispatched, and so can never have results.
func TestInterruptedPartialDropsUnrunToolCalls(t *testing.T) {
	pp := &partialCallProvider{started: make(chan struct{})}
	a := newTestAgent(t, pp, tools.Set{})
	ctx, cancel := context.WithCancel(context.Background())

	_, err := collect(t, a, func() error {
		go func() {
			<-pp.started
			cancel()
		}()
		return a.Run(ctx, "go")
	})
	if err != nil {
		t.Fatalf("an interrupt is not an error: %v", err)
	}

	msgs := a.Conv.Messages()
	var kept bool
	for _, m := range msgs {
		if m.Role == provider.RoleAssistant && strings.Contains(m.Content, "let me look") {
			kept = true
		}
	}
	if !kept {
		t.Error("partial text must still be kept")
	}
	assertToolCallsAnswered(t, msgs)
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

// A tool that finished before the interrupt keeps its own result, even when
// that result is empty — "no output" is an answer, not evidence of a cancel.
func TestCancelledRoundKeepsFinishedResults(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	quiet := make(chan struct{})
	set := tools.Set{
		{
			Name: "quiet", Desc: "succeeds with nothing to say",
			Schema: json.RawMessage(`{"type":"object"}`),
			Run: func(ctx context.Context, args json.RawMessage) (tools.Result, error) {
				close(quiet)
				return tools.Result{}, nil
			},
		},
		{
			Name: "blocker", Desc: "waits for cancellation",
			Schema: json.RawMessage(`{"type":"object"}`),
			Run: func(ctx context.Context, args json.RawMessage) (tools.Result, error) {
				<-ctx.Done()
				return tools.Result{}, ctx.Err()
			},
		},
	}

	a := newTestAgent(t, &mixedProvider{}, set)
	_, err := collect(t, a, func() error {
		go func() {
			<-quiet
			cancel()
		}()
		return a.Run(ctx, "go")
	})
	if err != nil {
		t.Fatalf("an interrupt is not an error: %v", err)
	}

	msgs := a.Conv.Messages()
	assertToolCallsAnswered(t, msgs)
	for _, m := range msgs {
		if m.ToolCallID != "call_quiet" {
			continue
		}
		if strings.Contains(m.Content, "Skipped") {
			t.Errorf("a tool that finished was labelled interrupted: %q", m.Content)
		}
	}
}

// mixedProvider asks for one tool that finishes and one that hangs.
type mixedProvider struct{ served bool }

func (p *mixedProvider) Name() string { return "mixed" }
func (p *mixedProvider) Embed(ctx context.Context, t []string) ([][]float32, error) {
	return nil, nil
}
func (p *mixedProvider) Models(ctx context.Context) ([]provider.ModelInfo, error) {
	return nil, nil
}
func (p *mixedProvider) ChatStream(ctx context.Context, req provider.Req) (<-chan provider.Chunk, error) {
	ch := make(chan provider.Chunk, 1)
	if !p.served {
		p.served = true
		ch <- provider.Chunk{ToolCalls: []provider.ToolCall{
			{ID: "call_quiet", Name: "quiet", Args: json.RawMessage(`{}`)},
			{ID: "call_block", Name: "blocker", Args: json.RawMessage(`{}`)},
		}}
	} else {
		ch <- provider.Chunk{Text: "done"}
	}
	close(ch)
	return ch, nil
}
