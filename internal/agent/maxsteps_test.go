package agent

import (
	"context"
	"encoding/json"
	"testing"

	"evilcode/internal/provider"
	"evilcode/internal/tools"
)

// maxStepsProvider requests one tool round, then another, then a final
// no-tool answer — the shape that distinguishes "count model requests" from
// "count executed tool rounds" (C2).
type maxStepsProvider struct {
	round int
}

func (p *maxStepsProvider) Name() string { return "maxsteps" }
func (p *maxStepsProvider) Embed(context.Context, []string) ([][]float32, error) {
	return nil, nil
}
func (p *maxStepsProvider) Models(context.Context) ([]provider.ModelInfo, error) {
	return nil, nil
}
func (p *maxStepsProvider) ChatStream(context.Context, provider.Req) (<-chan provider.Chunk, error) {
	ch := make(chan provider.Chunk, 2)
	switch p.round {
	case 0:
		ch <- provider.Chunk{ToolCalls: []provider.ToolCall{{ID: "call_1", Name: "spin", Args: json.RawMessage(`{}`)}}}
	case 1:
		ch <- provider.Chunk{ToolCalls: []provider.ToolCall{{ID: "call_2", Name: "spin", Args: json.RawMessage(`{}`)}}}
	default:
		ch <- provider.Chunk{Text: "final answer"}
	}
	p.round++
	ch <- provider.Chunk{Done: true}
	close(ch)
	return ch, nil
}

// C2: max_steps counts executed tool rounds. With max_steps=1 the first tool
// round runs, the second request is refused with a stub result (the wire
// format requires adjacency), and the turn ends EndMaxSteps — the old code
// stopped after one *model request* and never executed anything.
func TestMaxStepsCountsExecutedToolRounds(t *testing.T) {
	var executed int
	spin := tools.Tool{
		Name:   "spin",
		Desc:   "spin",
		Schema: json.RawMessage(`{"type":"object"}`),
		Run: func(context.Context, json.RawMessage) (tools.Result, error) {
			executed++
			return tools.Result{Output: "spun"}, nil
		},
	}
	a := newTestAgent(t, &maxStepsProvider{}, tools.Set{spin})
	a.MaxSteps = 1

	evs, err := collect(t, a, func() error { return a.Run(context.Background(), "go") })
	if err != nil {
		t.Fatal(err)
	}
	if executed != 1 {
		t.Errorf("executed tool rounds = %d, want exactly 1", executed)
	}
	if last := evs[len(evs)-1]; last.Reason != EndMaxSteps {
		t.Errorf("reason = %v, want max_steps", last.Reason)
	}
	// Every call must be answered, including the refused one.
	assertToolCallsAnswered(t, a.Conv.Messages())
}

// oneRoundThenAnswerProvider requests a single tool round and then answers.
type oneRoundThenAnswerProvider struct {
	round int
}

func (p *oneRoundThenAnswerProvider) Name() string { return "oneround" }
func (p *oneRoundThenAnswerProvider) Embed(context.Context, []string) ([][]float32, error) {
	return nil, nil
}
func (p *oneRoundThenAnswerProvider) Models(context.Context) ([]provider.ModelInfo, error) {
	return nil, nil
}
func (p *oneRoundThenAnswerProvider) ChatStream(context.Context, provider.Req) (<-chan provider.Chunk, error) {
	ch := make(chan provider.Chunk, 2)
	if p.round == 0 {
		ch <- provider.Chunk{ToolCalls: []provider.ToolCall{{ID: "call_1", Name: "spin", Args: json.RawMessage(`{}`)}}}
	} else {
		ch <- provider.Chunk{Text: "final answer"}
	}
	p.round++
	ch <- provider.Chunk{Done: true}
	close(ch)
	return ch, nil
}

// With max_steps=1 and a provider that asks for one round then answers, the
// concluding no-tool response is allowed: the cap must not strand a valid
// tool exchange without its final answer.
func TestMaxStepsAllowsTheConcludingAnswer(t *testing.T) {
	var executed int
	spin := tools.Tool{
		Name:   "spin",
		Desc:   "spin",
		Schema: json.RawMessage(`{"type":"object"}`),
		Run: func(context.Context, json.RawMessage) (tools.Result, error) {
			executed++
			return tools.Result{Output: "spun"}, nil
		},
	}
	a := newTestAgent(t, &oneRoundThenAnswerProvider{}, tools.Set{spin})
	a.MaxSteps = 1

	evs, err := collect(t, a, func() error { return a.Run(context.Background(), "go") })
	if err != nil {
		t.Fatal(err)
	}
	if executed != 1 {
		t.Errorf("executed tool rounds = %d, want exactly 1", executed)
	}
	if last := evs[len(evs)-1]; last.Reason != EndComplete {
		t.Errorf("reason = %v, want complete (the concluding answer must be allowed)", last.Reason)
	}
}
