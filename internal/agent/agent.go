package agent

import (
	"context"
	"errors"
	"fmt"
	"net"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"evilcode/internal/provider"
	"evilcode/internal/tools"
)

// DefaultMaxSteps is the tool-round limit when none is configured.
//
// Zero: unlimited. The fixed cap of 60 was a guess standing in for a judgement
// nobody had made, and it fired on exactly the turns least able to afford it —
// a long refactor across many files converges slowly and legitimately, and
// being cut off at round 60 with no way to raise it loses the work rather than
// bounding it. The breakers that matter are elsewhere: the token budget, the
// wall clock, the interrupt key, and the user watching.
//
// Set `max_steps` in `[features]` to reinstate a limit.
const DefaultMaxSteps = 0

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

	// ForwardImages is the image-aware remote seam. It preserves the exact
	// attachment semantics of a local turn while keeping image bytes inside the
	// server-owned session rather than silently dropping them at the TUI edge.
	ForwardImages RemoteImages

	// ForwardHidden is the same remote seam with a transport-safe marker for
	// harness-authored prompts such as /plan and /overnight.
	ForwardHidden RemoteHidden

	// OnInterject observes a queued interrupt. An attached client returns true
	// to say it sent the interrupt onward, so it is not also queued locally
	// where nothing would ever drain it.
	OnInterject func(Interrupt) bool

	// NumCtx requests a context window from providers that accept one.
	NumCtx int

	// reasoningEffort is guarded by mu because the TUI can change it while a
	// tool round is still running. The next provider request sees the new
	// value without racing the current stream.
	reasoningEffort provider.ReasoningEffort

	// MaxSteps bounds tool-call rounds in one turn. Zero — the default — does
	// not bound them at all; see DefaultMaxSteps.
	MaxSteps int

	// LenientToolParse enables the JSON-in-text tool-call fallback for models
	// that emit tool calls as prose instead of structured tool_call records
	// (config [[model]] lenient_tool_parse). Off by default: it can misfire on
	// ordinary text, so it is opt-in per model.
	LenientToolParse bool

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

	seq atomic.Int64

	// runGen distinguishes one turn's reservation from the next, so a finished
	// turn's release cannot free the reservation a later one is holding.
	runGen uint64

	mu         sync.Mutex
	interrupts []Interrupt
	running    bool
	// toolPolicy is installed by the skill tool after a skill declares
	// allowed-tools. It is read once per tool round, so a skill loaded in one
	// round governs the turns that follow it rather than racing the current
	// batch.
	toolPolicy *tools.ToolPolicy

	// Compactor collapses the conversation when it approaches the window. Nil
	// disables it, which is what a session with no summariser gets.
	Compactor *Compactor

	// lastCtx is the newest request's context size, for the threshold.
	lastCtx int

	// pendingImages ride along with the next user message (§6.6).
	pendingImages [][]byte

	// prompt is what started the current turn, held so TurnStart can report it
	// after recall has appended to the conversation behind it.
	prompt string
}

// Attach stages images for the next Run. They travel with exactly one message.
func (a *Agent) Attach(images [][]byte) {
	a.mu.Lock()
	a.pendingImages = images
	a.mu.Unlock()
}

// takePrompt returns and clears the turn's originating prompt.
func (a *Agent) takePrompt() string {
	a.mu.Lock()
	defer a.mu.Unlock()
	p := a.prompt
	a.prompt = ""
	return p
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

// ReasoningEffort returns the active effort, using the provider's current
// default when the user has not explicitly selected one.
func (a *Agent) ReasoningEffort() provider.ReasoningEffort {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.reasoningEffort.Valid() {
		return a.reasoningEffort
	}
	return provider.DefaultReasoningEffort
}

// configuredReasoningEffort returns the explicit setting, if any. Keeping the
// unset state lets ordinary OpenAI-compatible models retain their own default;
// Codex applies medium at its provider edge.
func (a *Agent) configuredReasoningEffort() provider.ReasoningEffort {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.reasoningEffort
}

// SetReasoningEffort changes the setting used by the next provider request.
// It emits a small state event so attached UIs update together with the
// daemon-owned agent.
func (a *Agent) SetReasoningEffort(effort provider.ReasoningEffort) error {
	parsed, ok := provider.ParseReasoningEffort(string(effort))
	if !ok {
		return fmt.Errorf("unsupported reasoning effort %q", effort)
	}
	a.mu.Lock()
	changed := a.reasoningEffort != parsed
	a.reasoningEffort = parsed
	a.mu.Unlock()
	if changed && a.events != nil {
		e := a.newEvent(EventReasoningEffort)
		e.ReasoningEffort = parsed
		e.ReasoningEffortKnown = true
		a.emit(e)
	}
	return nil
}

// SetReasoningEffortQuiet updates the live setting without emitting a UI
// event. Session-level model switches use it to normalize a preference before
// publishing one canonical EventModel, keeping all attached clients in order.
func (a *Agent) SetReasoningEffortQuiet(effort provider.ReasoningEffort) error {
	parsed, ok := provider.ParseReasoningEffort(string(effort))
	if !ok {
		return fmt.Errorf("unsupported reasoning effort %q", effort)
	}
	a.mu.Lock()
	a.reasoningEffort = parsed
	a.mu.Unlock()
	return nil
}

// PendingInterrupts reports how many messages are waiting to be injected.
func (a *Agent) PendingInterrupts() int {
	a.mu.Lock()
	defer a.mu.Unlock()
	return len(a.interrupts)
}

// SetToolPolicy installs the restriction declared by a loaded skill. A nil
// policy restores the ordinary session tool set when a skill has no
// allowed-tools declaration.
func (a *Agent) SetToolPolicy(policy *tools.ToolPolicy) {
	a.mu.Lock()
	a.toolPolicy = policy
	a.mu.Unlock()
}

func (a *Agent) currentToolPolicy() *tools.ToolPolicy {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.toolPolicy
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
	return a.run(ctx, userInput, false)
}

// RunHidden executes a harness-authored turn. Local frontends use their own
// hiddenPrompt marker, while remote clients send the marker to the daemon so
// every attached window applies the same rendering rule.
func (a *Agent) RunHidden(ctx context.Context, userInput string) error {
	return a.run(ctx, userInput, true)
}

func (a *Agent) run(ctx context.Context, userInput string, hidden bool) error {
	// An attached client forwards the turn instead of running it. The check is
	// first because everything below — the conversation append, recall, the
	// loop — belongs to whichever process actually owns the session (§20).
	if a.ForwardHidden != nil {
		a.mu.Lock()
		images := a.pendingImages
		a.pendingImages = nil
		a.mu.Unlock()
		return a.ForwardHidden(ctx, userInput, images, hidden)
	}
	if a.ForwardImages != nil {
		a.mu.Lock()
		images := a.pendingImages
		a.pendingImages = nil
		a.mu.Unlock()
		return a.ForwardImages(ctx, userInput, images)
	}
	if a.Forward != nil {
		return a.Forward(ctx, userInput)
	}
	// Reserved before anything is mutated. Refusing inside Loop meant the user
	// message, the recall tail and the attached images had already been
	// committed by the time the caller was told the turn never started — and a
	// caller that then re-sent the text duplicated it.
	gen, ok := a.beginRun()
	if !ok {
		return ErrBusy
	}
	defer a.endRun(gen)

	if strings.TrimSpace(userInput) != "" {
		msg := provider.Message{Role: provider.RoleUser, Content: userInput, Hidden: hidden}
		a.mu.Lock()
		a.prompt = userInput
		// Attach writes pendingImages under the lock; the swap has to take it
		// too. Safe until now only because the TUI happened to attach before
		// starting the run goroutine.
		msg.Images, a.pendingImages = a.pendingImages, nil
		a.mu.Unlock()
		a.Conv.Append(msg)
		a.recall(ctx, userInput)
	}
	return a.loop(ctx)
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

// autoCompact summarises the conversation when it is close to filling the
// window, so the next request has room to answer in.
//
// Bounded by MaxAutoCompactions (invariant 6): a summary that is itself over the
// threshold would otherwise compact forever without ever sending a request,
// which presents as a hang rather than as a loop.
func (a *Agent) autoCompact(ctx context.Context) {
	if a.Compactor == nil {
		return
	}
	if !a.Compactor.ShouldCompactForConversation(a.pendingContextSize(), a.NumCtx, a.Conv) {
		return
	}
	// Queue the exact snapshot that will be compacted, including the prompt
	// that Run appended. Give an already-started lookup a small grace period;
	// Compact itself still never waits on the provider and falls back to the
	// ordinary recency boundary when this snapshot is unavailable.
	msgs := a.Conv.Messages()
	a.Compactor.PrepareRelevance(ctx, msgs)
	a.Compactor.WaitForRelevance(ctx, msgs, CompactRelevanceWait)
	if _, err := a.Compactor.Compact(ctx, a.Conv); err != nil {
		a.Notice(LevelWarning, "Could not compact: %v", err)
		return
	}
	a.Compactor.noteAutoCompaction()
	a.Notice(LevelInfo, "✓ Context compacted. Retrying...")
}

// ctxUsed is the size of the last request, which is what the threshold is
// measured against.
func (a *Agent) ctxUsed() int {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.lastCtx
}

// pendingContextSize estimates what the next provider request will carry.
// After a tool round the conversation already includes the results, while
// lastCtx still describes the request that preceded them — the largest gap
// between "checked" and "sent" a turn can have (R2-14). Characters-to-tokens
// at four to one, the same rule the status line's live estimate uses.
func (a *Agent) pendingContextSize() int {
	used := a.ctxUsed()
	n := 0
	for _, m := range a.Conv.Messages() {
		n += len(m.Content) + len(m.Reasoning) + len(m.ToolCallID) + len(m.ToolName)
		for _, c := range m.ToolCalls {
			n += len(c.Args)
		}
	}
	if est := n / 4; est > used {
		used = est
	}
	return used
}

// noteContext records the newest request's size for the compaction threshold.
func (a *Agent) noteContext(n int) {
	a.mu.Lock()
	a.lastCtx = n
	a.mu.Unlock()
}

// ErrBusy is returned by Loop when a turn is already running on this agent.
var ErrBusy = errors.New("a turn is already running on this session")

// beginRun takes the single-flight reservation, reporting whether it got it.
//
// One turn at a time. Two loops share one conversation, one tool set and one
// event sequence, and the result is a transcript interleaved from two turns —
// which is what the duplicated overnight step (H1.12) produced before it was
// fixed at the call site. Callers are expected to check before starting; this
// is the backstop for the ones that check and race.
func (a *Agent) beginRun() (uint64, bool) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.running {
		return 0, false
	}
	a.running = true
	a.runGen++
	return a.runGen, true
}

// endRun releases the reservation, if this run still holds it.
//
// The generation is what makes that "if" real. A turn is released twice — once
// by endTurn, so a listener acting on TurnEnd finds a ready agent, and once by
// the deferred call that covers the paths endTurn does not reach. Between those
// two the next turn can start, and an unconditional release would clear *its*
// reservation and let a third turn in beside it.
func (a *Agent) endRun(gen uint64) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.runGen == gen {
		a.running = false
	}
}

// releaseRun ends the current reservation and invalidates its generation, so
// the run's own deferred release becomes a no-op.
func (a *Agent) releaseRun() {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.running = false
	a.runGen++
}

// Loop is the agent loop proper (plan.md §15). It is the entry point for a turn
// with no new user message — a worker's schema retry, most notably.
func (a *Agent) Loop(ctx context.Context) error {
	gen, ok := a.beginRun()
	if !ok {
		return ErrBusy
	}
	defer a.endRun(gen)
	return a.loop(ctx)
}

// loop is the body, with the reservation already held.
func (a *Agent) loop(ctx context.Context) error {

	// TurnStart carries the prompt that started the turn. A local frontend
	// already drew it — it typed it — but a client that attached mid-session
	// has no other way to learn what was asked: the conversation only reaches
	// it in a snapshot, and the snapshot is taken once (plan.md §20).
	//
	// It is the prompt Run was given, not the last message on the conversation:
	// recall appends a `<memories>` tail after the user's turn, and taking the
	// last message drew that tail as the user's prompt — numbered band and all.
	// Compact before dispatching rather than after failing: the provider's
	// context-length error arrives only once tokens are already spent, and
	// classifying it needs per-provider parsing that the threshold makes
	// unnecessary in almost every case (plan.md §9.9).
	a.autoCompact(ctx)

	start := a.newEvent(EventTurnStart)
	start.Text = a.takePrompt()
	a.emit(start)

	for step := 0; ; {
		// Checked every round, not once per turn: tool results land in the
		// conversation between rounds, and a large one can push the context
		// past the window before the next request even though the turn-start
		// check saw plenty of headroom (R2-14). The check is cheap — no
		// provider call — so paying it per round costs nothing.
		a.autoCompact(ctx)

		msg, err := a.stream(ctx)
		if err != nil {
			if errors.Is(err, context.Canceled) {
				// An interrupt keeps whatever the model had produced so far,
				// so the reader does not lose a half-written answer.
				a.commitPartial(msg)
				a.endTurn(EndInterrupted)
				return nil
			}
			// A mid-stream error that already showed deltas keeps the partial too:
			// the reader watched the answer form, and discarding it here would
			// leave the transcript shorter than what the UI showed — a resume then
			// loses the text. The same rationale as the interrupt path.
			a.commitPartial(msg)
			ev := a.newEvent(EventError)
			ev.Err = err
			a.emit(ev)
			a.endTurn(EndError)
			return err
		}

		// Opt-in JSON-in-text tool calls: a small model that cannot emit
		// structured tool calls may still write one as prose. Parsed only when
		// the response carried no structured calls, and only with strict
		// name/schema validation so ordinary prose cannot misfire.
		if a.LenientToolParse && len(msg.ToolCalls) == 0 {
			if calls, stripped, ok := parseLenientToolCalls(msg.Content, a.Tools); ok {
				msg.ToolCalls = calls
				msg.Content = stripped
			}
		}

		a.Conv.Append(msg)
		// A tool-call assistant message is an intermediate step, not the
		// completed turn. Snapshot only the final assistant response so the
		// semantic window describes what the user actually received.
		if len(msg.ToolCalls) == 0 && a.Compactor != nil {
			a.Compactor.RecordEmbeddingSnapshot(ctx, msg.Content)
		}

		if len(msg.ToolCalls) > 0 {
			// The cap counts executed tool rounds, not model requests: with
			// max_steps=1, one tool call runs and the concluding answer is
			// still allowed. A response that would need another round beyond
			// the cap is refused — its calls are answered with stub results
			// (the wire format requires every tool_use to have an adjacent
			// tool_result) and the turn ends with an explicit EndMaxSteps.
			if a.MaxSteps > 0 && step >= a.MaxSteps {
				for _, c := range msg.ToolCalls {
					a.appendToolResult(c, stubMaxSteps, fmt.Errorf("stopped by features.max_steps"), tools.Result{})
				}
				a.Notice(LevelWarning,
					"Stopped after %d tool rounds without finishing (features.max_steps).",
					a.MaxSteps)
				a.endTurn(EndMaxSteps)
				return nil
			}
			if err := a.runTools(ctx, msg.ToolCalls); err != nil {
				if errors.Is(err, context.Canceled) {
					a.endTurn(EndInterrupted)
					return nil
				}
				// Unreachable today — runTools returns nil or context.Canceled —
				// but a turn that returns without ending leaves every listener
				// waiting for a TurnEnd that never comes.
				a.endTurn(EndError)
				return err
			}
			step++
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
			if a.Compactor != nil {
				a.Compactor.PrepareRelevanceIfNeeded(ctx, a.ctxUsed(), a.NumCtx, a.Conv)
			}
			a.endTurn(EndComplete)
			return nil
		}
	}
}

func (a *Agent) endTurn(reason EndReason) {
	// Released before the event: a listener that starts the next turn the
	// instant it sees TurnEnd — the worker schema retry does exactly that —
	// would otherwise be refused by a turn that has already finished.
	a.releaseRun()

	// A transcript that is behind the conversation is invisible until someone
	// resumes and finds the session short. Every turn ends here, so this is the
	// one place that cannot be forgotten.
	if err := a.Conv.PersistErr(); err != nil {
		a.Notice(LevelError, "This turn was not fully written to the session file: %v", err)
	}
	e := a.newEvent(EventTurnEnd)
	e.Reason = reason
	a.emit(e)
}

// commitPartial keeps interrupted output as a real assistant message rather
// than discarding it.
//
// Tool calls do not survive the interrupt. A call the stream had finished
// delivering but the loop never dispatched can never acquire a result, and an
// unanswered tool_call is a transcript strict endpoints reject — the same
// invariant runTools defends from the other side. The text is what the reader
// wanted kept; the calls were an intention, not an event.
func (a *Agent) commitPartial(msg provider.Message) {
	if strings.TrimSpace(msg.Content) == "" && strings.TrimSpace(msg.Reasoning) == "" {
		return
	}
	msg.ToolCalls = nil
	// ProviderItems describe a completed response. An interrupted response may
	// have finished individual output items without completing the response as
	// a whole; replaying that partial array can resurrect an undispatched call.
	msg.ProviderItems = nil
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

		msg, emitted, err := a.streamOnce(ctx)
		if err == nil {
			return msg, nil
		}
		if ctx.Err() != nil {
			// Cancellation is not a failure to retry; it is the user saying stop.
			return msg, context.Canceled
		}
		// A retry re-streams from the start of the response. If the failed
		// attempt already emitted visible deltas, retrying replays them — the
		// TUI appends the second attempt to the same live block and the user
		// watches the answer restart. Retry only before any content was
		// shown; a mid-stream failure keeps the partial and surfaces the error.
		if emitted || !retryable(err) {
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
	// A truncated stream is a transport-level interruption: the response never
	// reached its terminal marker, so the connection may recover on a retry.
	// The agent retries it only before any content was shown.
	if errors.Is(err, provider.ErrStreamTruncated) {
		return true
	}
	// Recognized transient transport failures (DNS, refused, reset, timeout).
	var netErr net.Error
	if errors.As(err, &netErr) {
		return true
	}
	// Everything else — malformed SSE, invalid JSON, unsupported message
	// shapes, local serialization errors — is deterministic. Retrying it only
	// costs latency and API calls and hides the real problem.
	return false
}

func (a *Agent) streamOnce(ctx context.Context) (provider.Message, bool, error) {
	req := provider.Req{
		Model:           a.Model,
		Messages:        a.Conv.Messages(),
		Tools:           toolDefs(a.Tools),
		NumCtx:          a.NumCtx,
		ReasoningEffort: a.configuredReasoningEffort(),
	}

	started := time.Now()
	ch, err := a.Provider.ChatStream(ctx, req)
	if err != nil {
		return provider.Message{}, false, err
	}

	msg := provider.Message{Role: provider.RoleAssistant}
	var text, reasoning strings.Builder
	// emitted tracks whether this attempt has shown any visible content. A
	// retry after content was shown replays it, so the retry decision in
	// stream depends on this.
	var emitted bool
	// done tracks the protocol terminal chunk. Every provider must emit exactly
	// one; a channel that closes without it is a contract violation and must
	// not be treated as a complete response (B5).
	sawDone := false

	for chunk := range ch {
		if chunk.Err != nil {
			msg.Content, msg.Reasoning = text.String(), reasoning.String()
			return msg, emitted, chunk.Err
		}
		if chunk.Done {
			if sawDone {
				msg.Content, msg.Reasoning = text.String(), reasoning.String()
				return msg, emitted, fmt.Errorf("%s: multiple terminal chunks in one stream", a.Provider.Name())
			}
			sawDone = true
		}
		if chunk.Text != "" {
			text.WriteString(chunk.Text)
			emitted = true
			e := a.newEvent(EventTextDelta)
			e.Text = chunk.Text
			a.emit(e)
		}
		if chunk.Reasoning != "" {
			reasoning.WriteString(chunk.Reasoning)
			emitted = true
			e := a.newEvent(EventReasoningDelta)
			e.Text = chunk.Reasoning
			a.emit(e)
		}
		// Tool calls may arrive at any point, including with no text before
		// them — never assume deltas come first (plan.md Part V).
		msg.ToolCalls = append(msg.ToolCalls, chunk.ToolCalls...)
		msg.ProviderItems = append(msg.ProviderItems, chunk.ProviderItems...)

		if chunk.Usage != nil {
			e := a.newEvent(EventTokenUsage)
			e.Usage = &Usage{
				In:         chunk.Usage.PromptTokens,
				Out:        chunk.Usage.CompletionTokens,
				CtxUsed:    chunk.Usage.PromptTokens + chunk.Usage.CompletionTokens,
				CtxMax:     chunk.Usage.ContextMax,
				CacheRead:  chunk.Usage.CacheReadTokens,
				CacheWrite: chunk.Usage.CacheWriteTokens,
				CacheHit:   chunk.Usage.CacheReadTokens > 0,
				GenMS:      int(time.Since(started).Milliseconds()),
			}
			a.noteContext(e.Usage.CtxUsed)
			a.emit(e)
		}
	}

	msg.Content, msg.Reasoning = text.String(), reasoning.String()
	if ctx.Err() != nil {
		return msg, emitted, context.Canceled
	}
	if !sawDone {
		// Defense in depth: the built-in adapters already surface truncation as
		// an error chunk, but a third-party provider that closes its channel
		// without a terminal chunk must not read as a completed turn.
		return msg, emitted, fmt.Errorf("%s: stream closed without a terminal done chunk", a.Provider.Name())
	}
	return msg, emitted, nil
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

// stubMaxSteps is the tool result written for calls refused because the
// features.max_steps cap was reached. Same adjacency requirement.
const stubMaxSteps = "[Skipped: features.max_steps reached]"

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
		for _, c := range calls {
			a.appendToolResult(c, stubSkipped, fmt.Errorf("interrupted"), tools.Result{})
		}
		a.injectInterrupts(true)
		a.Notice(LevelInfo, "⚡ %d tool(s) skipped", len(calls))
		return nil
	}

	outcomes := a.Tools.RunBatchWithPolicy(ctx, batch, a.currentToolPolicy())

	// Answer every call even when the round was cancelled. Returning early left
	// the assistant's tool_calls unanswered in the conversation and in the
	// JSONL, and a strict OpenAI-compatible endpoint rejects that transcript on
	// the next request — including the next request of a *resumed* session,
	// long after the interrupt is forgotten.
	//
	// Which calls the cancel actually reached is read off each outcome rather
	// than off the round: a tool that finished before the interrupt keeps its
	// real result, including one that legitimately returns nothing, and a tool
	// that failed for its own reasons keeps its own error rather than being
	// relabelled as interrupted.
	// A tool can impose its own deadline without the turn being cancelled. The
	// bg wait tool does exactly that: its timeout means "still running", not
	// "the user interrupted the agent". Only the parent turn context can tell us
	// that this round was actually interrupted.
	interrupted := ctx.Err() != nil
	for i, out := range outcomes {
		if interrupted && (errors.Is(out.Err, context.Canceled) || errors.Is(out.Err, context.DeadlineExceeded)) {
			a.appendToolResult(calls[i], stubSkipped, fmt.Errorf("interrupted"), tools.Result{})
			continue
		}
		a.appendToolResult(calls[i], out.Result.Output, out.Err, out.Result)
	}
	if interrupted {
		return context.Canceled
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
	if len(res.Repairs) > 0 {
		// The tool row's "repaired:" note is display-only — the model never
		// sees it, so it would keep emitting the same alias on every call and
		// every row would carry the note. One terse line in the result teaches
		// the canonical argument names; once the model adopts them, the repairs
		// stop.
		content += "\n\nNote: tool arguments were repaired: " + strings.Join(res.Repairs, ", ")
	}

	a.Conv.Append(provider.Message{
		Role:       provider.RoleTool,
		Content:    content,
		ToolCallID: call.ID,
		ToolName:   call.Name,
		IsError:    err != nil,
		Held:       res.Held,
		Images:     res.Images,
		Repairs:    res.Repairs,
		Diff:       res.Diff,
	})

	e := a.newEvent(EventToolResult)
	c := call
	if len(res.EffectiveArgs) > 0 {
		// Keep the assistant's original tool call in the conversation, but let
		// event consumers inspect the repaired object the tool actually ran.
		// Daemon conflict tracking and the TUI quick view both use this event
		// payload, so leaving the misspelled path here would make a successful
		// write invisible to them.
		c.Args = res.EffectiveArgs
	}
	e.Call = &c
	e.Output = output
	e.Err = err
	e.Diff = res.Diff
	e.DiffStat = res.DiffStat
	e.Intent = res.Intent
	e.Held = res.Held
	e.Display = res.Display
	e.Images = res.Images
	e.NoWrite = res.NoWrite
	e.Repairs = res.Repairs
	a.emit(e)
}
