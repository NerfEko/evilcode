package agent

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"evilcode/internal/provider"
	"evilcode/internal/tools"
)

// MaxSteps bounds tool-call rounds in a single turn. A model that keeps calling
// tools without converging must stop somewhere; this is the outermost backstop
// under the §12.6 breakers.
const MaxSteps = 60

// Agent runs the conversation loop. It is a plain struct driving a plain
// function — not an actor system — so the whole flow reads top to bottom.
type Agent struct {
	Session  string
	Provider provider.Provider
	Model    string
	Tools    tools.Set
	Conv     *Conversation
	Hooks    Hooks

	// Recall is the passive-memory seam (plan.md §19). It runs once per user
	// message and returns the tail message to append, plus a display payload
	// for the 🧠 tile. Living on the agent rather than in the TUI is what gives
	// headless `run` and the daemon the same memory without a second copy.
	//
	// It must not block for long and must not fail the turn: a nil hook, a slow
	// embedder, and an empty result are all the same thing to the loop.
	Recall func(ctx context.Context, userInput string) (tail string, display any)

	// Forward diverts a turn to a session living somewhere else — the daemon,
	// for an attached client (plan.md §20). See remote.go.
	Forward Remote

	// OnInterject observes a queued interrupt. An attached client returns true
	// to say it sent the interrupt onward, so it is not also queued locally
	// where nothing would ever drain it.
	OnInterject func(Interrupt) bool

	// NumCtx requests a context window from providers that accept one.
	NumCtx int

	// Retry policy for transient failures.
	MaxRetries int
	BaseDelay  time.Duration

	// sleep is swappable so retry tests do not actually wait.
	sleep func(context.Context, time.Duration) error

	events chan Event

	// done is closed by Close. The events channel itself is never closed: a
	// turn unwinding on its own goroutine can still be emitting, and closing
	// the channel out from under it is a data race no amount of flag-checking
	// fixes. Consumers select on Done instead.
	//
	// Created lazily so an Agent built as a struct literal — which tests do —
	// still works rather than closing a nil channel.
	doneOnce  sync.Once
	done      chan struct{}
	closeOnce sync.Once

	seq int

	mu         sync.Mutex
	interrupts []Interrupt
	running    bool
}

// Interrupt is a message injected into a live turn at a safe point (§6.3).
type Interrupt struct {
	// Source groups interrupts. Different sources are flushed separately, so a
	// system nudge never merges into the user's sentence.
	Source string

	Text string

	// Urgent injects at safe point C — between tool executions — instead of
	// waiting for point D. Every remaining tool gets a stub result first,
	// because the API requires tool_use and tool_result to stay adjacent.
	Urgent bool
}

// Sources for Interrupt.
const (
	SourceUser           = "User"
	SourceSystem         = "System"
	SourceBackgroundTask = "BackgroundTask"
)

// New builds an agent with sensible retry defaults.
func New(session string, p provider.Provider, model string, ts tools.Set, conv *Conversation) *Agent {
	return &Agent{
		Session:    session,
		Provider:   p,
		Model:      model,
		Tools:      ts,
		Conv:       conv,
		MaxRetries: 4,
		BaseDelay:  time.Second,
		sleep:      sleepCtx,
		events:     make(chan Event, 256),
	}
}

func sleepCtx(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-t.C:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// Events is the stream every frontend consumes. It is closed when the agent is
// closed, never at the end of a turn — a session outlives its turns.
func (a *Agent) Events() <-chan Event { return a.events }

// Done is closed when the agent is closed. Every consumer of Events must select
// on it, because Events itself is never closed.
func (a *Agent) Done() <-chan struct{} { return a.doneChan() }

// doneChan returns the close signal, creating it on first use.
func (a *Agent) doneChan() chan struct{} {
	a.doneOnce.Do(func() {
		if a.done == nil {
			a.done = make(chan struct{})
		}
	})
	return a.done
}

// Close releases the event channel. After Close the agent must not be run.
//
// Closing is flagged before the channel is closed so a turn still unwinding
// stops emitting rather than panicking. The daemon makes this ordinary rather
// than exotic: shutting it down closes every session while spawned workers are
// still mid-turn, and "send on closed channel" is not an acceptable way for a
// process to exit.
func (a *Agent) Close() {
	ch := a.doneChan()
	a.closeOnce.Do(func() { close(ch) })
}

func (a *Agent) emit(e Event) {
	if e.Err != nil && e.ErrText == "" {
		e.ErrText = e.Err.Error()
	}
	select {
	case a.events <- e:
	case <-a.doneChan():
		// Closed, with nobody left reading. Dropping the event is correct;
		// blocking here would wedge a shutting-down daemon forever.
	}
}

// Notice emits an out-of-band message for the UI.
func (a *Agent) Notice(level Level, format string, args ...any) {
	e := a.newEvent(EventNotice)
	e.Level = level
	e.Text = fmt.Sprintf(format, args...)
	a.emit(e)
}

// Interject queues a message for injection at the next safe point. It is safe
// to call while a turn is running — that is the entire point.
func (a *Agent) Interject(in Interrupt) {
	if a.OnInterject != nil && a.OnInterject(in) {
		return
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	a.interrupts = append(a.interrupts, in)
}

// Running reports whether a turn is in flight, which is what the composer's
// send model (§6.3) branches on.
func (a *Agent) Running() bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.running
}

// PendingInterrupts reports how many messages are waiting to be injected.
func (a *Agent) PendingInterrupts() int {
	a.mu.Lock()
	defer a.mu.Unlock()
	return len(a.interrupts)
}

// DrainInterrupts removes and returns queued interrupts, grouped by source and
// joined within each group. Different sources stay separate messages.
func (a *Agent) DrainInterrupts(urgentOnly bool) []provider.Message {
	a.mu.Lock()
	var taken, left []Interrupt
	for _, in := range a.interrupts {
		if urgentOnly && !in.Urgent {
			left = append(left, in)
			continue
		}
		taken = append(taken, in)
	}
	a.interrupts = left
	a.mu.Unlock()

	if len(taken) == 0 {
		return nil
	}

	// Preserve first-seen source order so the model reads them in the order
	// they were sent.
	var order []string
	grouped := map[string][]string{}
	for _, in := range taken {
		if _, ok := grouped[in.Source]; !ok {
			order = append(order, in.Source)
		}
		grouped[in.Source] = append(grouped[in.Source], in.Text)
	}

	out := make([]provider.Message, 0, len(order))
	for _, src := range order {
		out = append(out, provider.Message{
			Role:    provider.RoleUser,
			Content: strings.Join(grouped[src], "\n\n"),
		})
	}
	return out
}

// Run executes one turn: append the user's message, then loop until the model
// stops asking for tools and no hook appends anything further.
func (a *Agent) Run(ctx context.Context, userInput string) error {
	// An attached client forwards the turn instead of running it. The check is
	// first because everything below — the conversation append, recall, the
	// loop — belongs to whichever process actually owns the session (§20).
	if a.Forward != nil {
		return a.Forward(ctx, userInput)
	}
	if strings.TrimSpace(userInput) != "" {
		a.Conv.Append(provider.Message{Role: provider.RoleUser, Content: userInput})
		a.recall(ctx, userInput)
	}
	return a.Loop(ctx)
}

// recall injects remembered context after the user message.
//
// The memories go in as a user-role message rather than a system one: they are
// notes, not instructions, and a model that reads them as instructions will
// follow a stale memory over what the user just said.
func (a *Agent) recall(ctx context.Context, userInput string) {
	if a.Recall == nil {
		return
	}
	tail, display := a.Recall(ctx, userInput)
	if tail == "" {
		return
	}
	a.Conv.Append(provider.Message{Role: provider.RoleUser, Content: tail})
	ev := a.newEvent(EventMemoryRecall)
	ev.Display = display
	a.emit(ev)
}

// Loop is the agent loop proper (plan.md §15).
func (a *Agent) Loop(ctx context.Context) error {
	a.mu.Lock()
	a.running = true
	a.mu.Unlock()
	defer func() {
		a.mu.Lock()
		a.running = false
		a.mu.Unlock()
	}()

	a.emit(a.newEvent(EventTurnStart))

	for step := 0; ; step++ {
		if step >= MaxSteps {
			a.Notice(LevelWarning, "Stopped after %d tool rounds without finishing.", MaxSteps)
			a.endTurn(EndMaxSteps)
			return nil
		}

		msg, err := a.stream(ctx)
		if err != nil {
			if errors.Is(err, context.Canceled) {
				// An interrupt keeps whatever the model had produced so far,
				// so the reader does not lose a half-written answer.
				a.commitPartial(msg)
				a.endTurn(EndInterrupted)
				return nil
			}
			ev := a.newEvent(EventError)
			ev.Err = err
			a.emit(ev)
			a.endTurn(EndError)
			return err
		}

		a.Conv.Append(msg)

		if len(msg.ToolCalls) > 0 {
			if err := a.runTools(ctx, msg.ToolCalls); err != nil {
				if errors.Is(err, context.Canceled) {
					a.endTurn(EndInterrupted)
					return nil
				}
				return err
			}
			// Safe point D: after all tool results, before the next request.
			// This is the default injection point.
			a.injectInterrupts(false)
			continue
		}

		// Safe point B: the stream ended with no tool calls — always safe.
		injected := a.injectInterrupts(false)

		if !injected {
			if a.Hooks != nil {
				appended, err := a.Hooks.PostTurn(ctx, a)
				if err != nil {
					a.Notice(LevelWarning, "post-turn hook: %v", err)
				}
				if appended {
					continue
				}
			}
			a.endTurn(EndComplete)
			return nil
		}
	}
}

func (a *Agent) endTurn(reason EndReason) {
	e := a.newEvent(EventTurnEnd)
	e.Reason = reason
	a.emit(e)
}

// commitPartial keeps interrupted output as a real assistant message rather
// than discarding it.
func (a *Agent) commitPartial(msg provider.Message) {
	if strings.TrimSpace(msg.Content) == "" && strings.TrimSpace(msg.Reasoning) == "" {
		return
	}
	a.Conv.Append(msg)
}

// injectInterrupts appends any queued interrupts and reports whether it did.
func (a *Agent) injectInterrupts(urgentOnly bool) bool {
	msgs := a.DrainInterrupts(urgentOnly)
	if len(msgs) == 0 {
		return false
	}
	a.Conv.Append(msgs...)
	return true
}

// stream runs one request, retrying transient failures, and accumulates the
// response into a single assistant message.
func (a *Agent) stream(ctx context.Context) (provider.Message, error) {
	var lastErr error
	for attempt := 0; attempt <= a.MaxRetries; attempt++ {
		if attempt > 0 {
			delay := a.BaseDelay << (attempt - 1)
			a.Notice(LevelWarning, "Retrying in %s (attempt %d/%d): %v",
				delay.Round(time.Millisecond), attempt, a.MaxRetries, lastErr)
			if err := a.sleep(ctx, delay); err != nil {
				return provider.Message{}, err
			}
		}

		msg, err := a.streamOnce(ctx)
		if err == nil {
			return msg, nil
		}
		if ctx.Err() != nil {
			// Cancellation is not a failure to retry; it is the user saying stop.
			return msg, context.Canceled
		}
		if !retryable(err) {
			return msg, err
		}
		lastErr = err
	}
	return provider.Message{}, fmt.Errorf("giving up after %d retries: %w", a.MaxRetries, lastErr)
}

// retryable decides whether resending the identical request could work. Auth
// and bad-model errors are deterministic — retrying them just spins (§12.6).
func retryable(err error) bool {
	var he *provider.HTTPError
	if errors.As(err, &he) {
		return he.Retryable()
	}
	// A transport error (connection refused, DNS, reset) is explicitly
	// retryable: treating a transient network fault as permanent is the bug
	// plan.md §12.6 calls out.
	return true
}

func (a *Agent) streamOnce(ctx context.Context) (provider.Message, error) {
	req := provider.Req{
		Model:    a.Model,
		Messages: a.Conv.Messages(),
		Tools:    toolDefs(a.Tools),
		NumCtx:   a.NumCtx,
	}

	ch, err := a.Provider.ChatStream(ctx, req)
	if err != nil {
		return provider.Message{}, err
	}

	msg := provider.Message{Role: provider.RoleAssistant}
	var text, reasoning strings.Builder

	for chunk := range ch {
		if chunk.Err != nil {
			msg.Content, msg.Reasoning = text.String(), reasoning.String()
			return msg, chunk.Err
		}
		if chunk.Text != "" {
			text.WriteString(chunk.Text)
			e := a.newEvent(EventTextDelta)
			e.Text = chunk.Text
			a.emit(e)
		}
		if chunk.Reasoning != "" {
			reasoning.WriteString(chunk.Reasoning)
			e := a.newEvent(EventReasoningDelta)
			e.Text = chunk.Reasoning
			a.emit(e)
		}
		// Tool calls may arrive at any point, including with no text before
		// them — never assume deltas come first (plan.md Part V).
		msg.ToolCalls = append(msg.ToolCalls, chunk.ToolCalls...)

		if chunk.Usage != nil {
			e := a.newEvent(EventTokenUsage)
			e.Usage = &Usage{
				In:      chunk.Usage.PromptTokens,
				Out:     chunk.Usage.CompletionTokens,
				CtxUsed: chunk.Usage.PromptTokens + chunk.Usage.CompletionTokens,
				CtxMax:  chunk.Usage.ContextMax,
			}
			a.emit(e)
		}
	}

	msg.Content, msg.Reasoning = text.String(), reasoning.String()
	if ctx.Err() != nil {
		return msg, context.Canceled
	}
	return msg, nil
}

func toolDefs(ts tools.Set) []provider.ToolDef {
	out := make([]provider.ToolDef, 0, len(ts))
	for _, t := range ts {
		out = append(out, provider.ToolDef{Name: t.Name, Desc: t.Desc, Schema: t.Schema})
	}
	return out
}

// stubSkipped is the tool result written for calls abandoned at safe point C.
// The API requires every tool_use to have an adjacent tool_result, so skipping
// a call still has to answer it.
const stubSkipped = "[Skipped: user interrupted]"

// runTools executes a round of tool calls and appends their results.
func (a *Agent) runTools(ctx context.Context, calls []provider.ToolCall) error {
	batch := make([]tools.Call, len(calls))
	for i, c := range calls {
		batch[i] = tools.Call{ID: c.ID, Name: c.Name, Args: c.Args}
		e := a.newEvent(EventToolStart)
		call := c
		e.Call = &call
		a.emit(e)
	}

	// Safe point C: an urgent interrupt cuts the round short. Every call still
	// gets a result, because the wire format requires it.
	if a.hasUrgent() {
		for i, c := range calls {
			_ = i
			a.appendToolResult(c, stubSkipped, fmt.Errorf("interrupted"), tools.Result{})
		}
		a.injectInterrupts(true)
		a.Notice(LevelInfo, "⚡ %d tool(s) skipped", len(calls))
		return nil
	}

	outcomes := a.Tools.RunBatch(ctx, batch)
	for i, out := range outcomes {
		if ctx.Err() != nil {
			return context.Canceled
		}
		a.appendToolResult(calls[i], out.Result.Output, out.Err, out.Result)
	}
	return nil
}

func (a *Agent) hasUrgent() bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	for _, in := range a.interrupts {
		if in.Urgent {
			return true
		}
	}
	return false
}

// appendToolResult records a result on the conversation and emits its event.
func (a *Agent) appendToolResult(call provider.ToolCall, output string, err error, res tools.Result) {
	content := output
	if err != nil {
		// The model needs the error text to recover; an empty result teaches
		// it nothing.
		if content == "" {
			content = "Error: " + err.Error()
		} else {
			content = "Error: " + err.Error() + "\n\n" + content
		}
	}
	if content == "" {
		content = "(no output)"
	}

	a.Conv.Append(provider.Message{
		Role:       provider.RoleTool,
		Content:    content,
		ToolCallID: call.ID,
		ToolName:   call.Name,
		IsError:    err != nil,
	})

	e := a.newEvent(EventToolResult)
	c := call
	e.Call = &c
	e.Output = output
	e.Err = err
	e.Diff = res.Diff
	e.DiffStat = res.DiffStat
	e.Intent = res.Intent
	e.Display = res.Display
	a.emit(e)
}
