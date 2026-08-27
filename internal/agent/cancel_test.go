package agent

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
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
	ch := make(chan provider.Chunk, 3)
	if !p.served {
		p.served = true
		ch <- provider.Chunk{ToolCalls: []provider.ToolCall{
			{ID: "call_1", Name: "blocker", Args: json.RawMessage(`{}`)},
			{ID: "call_2", Name: "blocker", Args: json.RawMessage(`{}`)},
		}}
	} else {
		ch <- provider.Chunk{Text: "done"}
	}
	ch <- provider.Chunk{Done: true}
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
		Name: "blocker",
		Desc: "waits for cancellation",
		// Read-only: the round must have two calls in flight when cancel lands,
		// which only read-only calls can be since R2-07's effect scheduling.
		Effect: tools.EffectReadOnly,
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

// A deadline owned by one tool is an ordinary tool error, not evidence that
// the user cancelled the whole turn. bg wait uses a child timeout to bound how
// long it blocks while the underlying task keeps running.
func TestToolLocalDeadlineDoesNotInterruptTurn(t *testing.T) {
	wait := tools.Tool{
		Name:   "wait",
		Desc:   "waits with its own deadline",
		Schema: json.RawMessage(`{"type":"object"}`),
		Run: func(context.Context, json.RawMessage) (tools.Result, error) {
			return tools.Result{Output: "background task 1 is still running"}, context.DeadlineExceeded
		},
	}

	a := newTestAgent(t, &deadlineProvider{}, tools.Set{wait})
	events, err := collect(t, a, func() error {
		return a.Run(context.Background(), "go")
	})
	if err != nil {
		t.Fatalf("a tool-local deadline must not fail the turn: %v", err)
	}
	if last := events[len(events)-1]; last.Kind != EventTurnEnd || last.Reason != EndComplete {
		t.Fatalf("turn ended with %s, want complete", last.Reason)
	}

	var result string
	for _, msg := range a.Conv.Messages() {
		if msg.ToolCallID == "call_wait" {
			result = msg.Content
		}
	}
	if !strings.Contains(result, "background task 1 is still running") {
		t.Fatalf("tool timeout result was lost: %q", result)
	}
	if strings.Contains(result, stubSkipped) {
		t.Fatalf("tool-local deadline was mislabeled as an interrupt: %q", result)
	}
}

type deadlineProvider struct{ served bool }

func (p *deadlineProvider) Name() string { return "deadline" }
func (p *deadlineProvider) Embed(context.Context, []string) ([][]float32, error) {
	return nil, nil
}
func (p *deadlineProvider) Models(context.Context) ([]provider.ModelInfo, error) {
	return nil, nil
}
func (p *deadlineProvider) ChatStream(context.Context, provider.Req) (<-chan provider.Chunk, error) {
	ch := make(chan provider.Chunk, 2)
	if !p.served {
		p.served = true
		ch <- provider.Chunk{ToolCalls: []provider.ToolCall{{
			ID: "call_wait", Name: "wait", Args: json.RawMessage(`{}`),
		}}}
	} else {
		ch <- provider.Chunk{Text: "done"}
	}
	ch <- provider.Chunk{Done: true}
	close(ch)
	return ch, nil
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
	ch := make(chan provider.Chunk, 2)
	if !p.served {
		p.served = true
		ch <- provider.Chunk{ToolCalls: []provider.ToolCall{
			{ID: "call_quiet", Name: "quiet", Args: json.RawMessage(`{}`)},
			{ID: "call_block", Name: "blocker", Args: json.RawMessage(`{}`)},
		}}
	} else {
		ch <- provider.Chunk{Text: "done"}
	}
	ch <- provider.Chunk{Done: true}
	close(ch)
	return ch, nil
}

// H1.14: the invariant that actually matters is the one on disk. The
// conversation is rebuilt from the persisted messages on every resume, so a
// transcript with an unanswered call is a 400 waiting for whoever resumes it,
// long after the interrupt that caused it is forgotten.
func TestPersistedTranscriptOfACancelledTurnIsWellFormed(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	entered := make(chan struct{}, 2)
	blocker := tools.Tool{
		Name: "blocker", Desc: "waits for cancellation",
		// Read-only: two calls must be in flight when cancel lands, which only
		// read-only calls can be since R2-07's effect scheduling.
		Effect: tools.EffectReadOnly,
		Schema: json.RawMessage(`{"type":"object"}`),
		Run: func(ctx context.Context, args json.RawMessage) (tools.Result, error) {
			entered <- struct{}{}
			<-ctx.Done()
			return tools.Result{}, ctx.Err()
		},
	}

	a := newTestAgent(t, &twoToolProvider{}, tools.Set{blocker})
	var persisted []provider.Message
	var mu sync.Mutex
	a.Conv.Persist(func(m provider.Message) error {
		mu.Lock()
		defer mu.Unlock()
		persisted = append(persisted, m)
		return nil
	})

	if _, err := collect(t, a, func() error {
		go func() {
			<-entered
			<-entered
			cancel()
		}()
		return a.Run(ctx, "go")
	}); err != nil {
		t.Fatalf("an interrupt is not an error: %v", err)
	}

	mu.Lock()
	defer mu.Unlock()
	if len(persisted) == 0 {
		t.Fatal("nothing was persisted, so the test proves nothing")
	}
	assertToolCallsAnswered(t, persisted)
}

// H2.3: nothing stopped two turns running on one agent. They share the
// conversation, the tool set and the event sequence, so the result is one
// transcript interleaved from two turns — which is exactly what the duplicated
// overnight step produced before H1.12 was fixed.
func TestOneTurnAtATimePerAgent(t *testing.T) {
	release := make(chan struct{})
	entered := make(chan struct{}, 1)
	blocker := tools.Tool{
		Name: "blocker", Desc: "holds the turn open",
		Schema: json.RawMessage(`{"type":"object"}`),
		Run: func(ctx context.Context, args json.RawMessage) (tools.Result, error) {
			select {
			case entered <- struct{}{}:
			default:
			}
			<-release
			return tools.Result{Output: "done"}, nil
		},
	}

	a := newTestAgent(t, &twoToolProvider{}, tools.Set{blocker})
	go func() { _ = a.Run(context.Background(), "first") }()
	<-entered

	if err := a.Run(context.Background(), "second"); !errors.Is(err, ErrBusy) {
		t.Errorf("a second turn on a busy agent returned %v, want ErrBusy", err)
	}
	close(release)
}

// The refusal must come before anything is committed. Refusing inside Loop left
// the user message, the recall tail and the images already appended, so a caller
// told "busy" that re-sent its text duplicated it.
func TestABusyAgentCommitsNothing(t *testing.T) {
	release := make(chan struct{})
	entered := make(chan struct{}, 1)
	blocker := tools.Tool{
		Name: "blocker", Desc: "holds the turn open",
		Schema: json.RawMessage(`{"type":"object"}`),
		Run: func(ctx context.Context, args json.RawMessage) (tools.Result, error) {
			select {
			case entered <- struct{}{}:
			default:
			}
			<-release
			return tools.Result{Output: "done"}, nil
		},
	}

	a := newTestAgent(t, &twoToolProvider{}, tools.Set{blocker})
	go func() { _ = a.Run(context.Background(), "first") }()
	<-entered

	before := a.Conv.Len()
	if err := a.Run(context.Background(), "second"); !errors.Is(err, ErrBusy) {
		t.Fatalf("second turn returned %v, want ErrBusy", err)
	}
	if got := a.Conv.Len(); got != before {
		t.Errorf("the refused turn appended %d message(s) before being refused", got-before)
	}
	for _, m := range a.Conv.Messages() {
		if m.Content == "second" {
			t.Error("the refused prompt is in the conversation")
		}
	}
	close(release)
}

// A turn is released twice: by endTurn, so a listener acting on TurnEnd finds a
// ready agent, and by the deferred call covering the paths endTurn misses.
// Between them the next turn can start — and an unconditional release would
// clear its reservation, letting a third turn run beside it.
func TestAFinishedTurnDoesNotReleaseTheNextOnes(t *testing.T) {
	a := newTestAgent(t, provider.NewMock("mock", "chat"), nil)

	// One full turn, so its deferred release has definitely run.
	if _, err := collect(t, a, func() error { return a.Run(context.Background(), "first") }); err != nil {
		t.Fatal(err)
	}

	gen, ok := a.beginRun()
	if !ok {
		t.Fatal("the agent is still reserved after its turn finished")
	}
	// A stale release from any earlier turn must not free this one.
	a.endRun(gen - 1)
	if _, stillFree := a.beginRun(); stillFree {
		t.Error("a stale release freed the live turn's reservation")
	}
	a.endRun(gen)
	if _, free := a.beginRun(); !free {
		t.Error("the turn's own release did not free the reservation")
	}
}
