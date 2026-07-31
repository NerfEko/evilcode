package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"go/build"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"evilcode/internal/memory"
	"evilcode/internal/provider"
	"evilcode/internal/todo"
	"evilcode/internal/tools"
)

// collect drains events until TurnEnd, returning them in order.
func collect(t *testing.T, a *Agent, run func() error) ([]Event, error) {
	t.Helper()
	done := make(chan []Event, 1)
	go func() {
		var out []Event
		for e := range a.Events() {
			out = append(out, e)
			if e.Kind == EventTurnEnd {
				break
			}
		}
		done <- out
	}()
	err := run()
	select {
	case evs := <-done:
		return evs, err
	case <-time.After(10 * time.Second):
		t.Fatal("timed out waiting for TurnEnd")
		return nil, err
	}
}

func kinds(evs []Event) []EventKind {
	out := make([]EventKind, len(evs))
	for i, e := range evs {
		out[i] = e.Kind
	}
	return out
}

func textOf(evs []Event) string {
	var b strings.Builder
	for _, e := range evs {
		if e.Kind == EventTextDelta {
			b.WriteString(e.Text)
		}
	}
	return b.String()
}

func newTestAgent(t *testing.T, p provider.Provider, ts tools.Set) *Agent {
	t.Helper()
	a := New("dracula", p, "mock-large", ts, NewConversation("system"))
	a.BaseDelay = time.Millisecond
	a.sleep = func(ctx context.Context, d time.Duration) error { return nil }
	t.Cleanup(a.Close)
	return a
}

func TestSimpleTurn(t *testing.T) {
	a := newTestAgent(t, provider.NewMock("mock", "chat"), nil)
	evs, err := collect(t, a, func() error { return a.Run(context.Background(), "hi") })
	if err != nil {
		t.Fatal(err)
	}
	if evs[0].Kind != EventTurnStart {
		t.Errorf("first event = %v, want turn_start", evs[0].Kind)
	}
	last := evs[len(evs)-1]
	if last.Kind != EventTurnEnd || last.Reason != EndComplete {
		t.Errorf("last event = %+v, want a complete turn_end", last)
	}
	if textOf(evs) == "" {
		t.Error("no text deltas were emitted")
	}
	// user + assistant
	if got := a.Conv.Len(); got != 2 {
		t.Errorf("conversation length = %d, want 2", got)
	}
}

func TestEventsCarrySessionAndSequence(t *testing.T) {
	a := newTestAgent(t, provider.NewMock("mock", "chat"), nil)
	evs, _ := collect(t, a, func() error { return a.Run(context.Background(), "hi") })
	for i, e := range evs {
		if e.Session != "dracula" {
			t.Errorf("event %d has session %q, want dracula", i, e.Session)
		}
		if i > 0 && e.Seq <= evs[i-1].Seq {
			t.Errorf("event %d seq %d does not follow %d", i, e.Seq, evs[i-1].Seq)
		}
	}
}

func TestToolTurnRunsToolAndContinues(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "config.go"), []byte("package config\n"), 0o644)
	os.MkdirAll(filepath.Join(dir, "internal", "config"), 0o755)
	os.WriteFile(filepath.Join(dir, "internal", "config", "config.go"), []byte("package config\n"), 0o644)

	a := newTestAgent(t, provider.NewMock("mock", "tools"), tools.NewFS(dir).Tools())
	evs, err := collect(t, a, func() error { return a.Run(context.Background(), "explain") })
	if err != nil {
		t.Fatal(err)
	}

	var started, resulted int
	for _, e := range evs {
		switch e.Kind {
		case EventToolStart:
			started++
		case EventToolResult:
			resulted++
			if e.IsError() {
				t.Errorf("tool failed: %v", e.Err)
			}
		}
	}
	if started != 1 || resulted != 1 {
		t.Errorf("tool events = %d start / %d result, want 1 each", started, resulted)
	}
	// user, assistant(with call), tool result, assistant
	if got := a.Conv.Len(); got != 4 {
		t.Errorf("conversation length = %d, want 4; kinds: %v", got, kinds(evs))
	}
}

func TestToolResultAppendedEvenOnError(t *testing.T) {
	// A tool_use with no adjacent tool_result is a protocol violation, so a
	// failing tool still has to answer.
	failing := tools.Set{{
		Name:   "read",
		Desc:   "read",
		Schema: json.RawMessage(`{"type":"object","properties":{}}`),
		Run: func(ctx context.Context, raw json.RawMessage) (tools.Result, error) {
			return tools.Result{}, fmt.Errorf("disk on fire")
		},
	}}
	a := newTestAgent(t, provider.NewMock("mock", "tools"), failing)
	evs, err := collect(t, a, func() error { return a.Run(context.Background(), "go") })
	if err != nil {
		t.Fatal(err)
	}

	var sawErrResult bool
	for _, e := range evs {
		if e.Kind == EventToolResult && e.IsError() {
			sawErrResult = true
		}
	}
	if !sawErrResult {
		t.Fatal("want an error tool result event")
	}

	msgs := a.Conv.Messages()
	var toolMsg *provider.Message
	for i := range msgs {
		if msgs[i].Role == provider.RoleTool {
			toolMsg = &msgs[i]
		}
	}
	if toolMsg == nil {
		t.Fatal("no tool result message was appended")
	}
	if !toolMsg.IsError {
		t.Error("tool message should be flagged as an error")
	}
	if !strings.Contains(toolMsg.Content, "disk on fire") {
		t.Errorf("tool content = %q, want the error text so the model can recover", toolMsg.Content)
	}
}

func TestBatchToolCallsAllResolve(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "main.go"), []byte("package main\n"), 0o644)
	set := append(tools.NewFS(dir).Tools(), tools.NewExec(dir).Tools()...)

	a := newTestAgent(t, provider.NewMock("mock", "tools-batch"), set)
	evs, err := collect(t, a, func() error { return a.Run(context.Background(), "check") })
	if err != nil {
		t.Fatal(err)
	}
	var results int
	for _, e := range evs {
		if e.Kind == EventToolResult {
			results++
		}
	}
	if results != 3 {
		t.Errorf("tool results = %d, want 3", results)
	}

	// Every call must have exactly one result message, in call order.
	msgs := a.Conv.Messages()
	var ids []string
	for _, m := range msgs {
		if m.Role == provider.RoleTool {
			ids = append(ids, m.ToolCallID)
		}
	}
	if len(ids) != 3 || ids[0] != "call_1" || ids[2] != "call_3" {
		t.Errorf("tool result IDs = %v, want call_1..call_3 in order", ids)
	}
}

func TestBufferedToolCallWithNoPrecedingText(t *testing.T) {
	dir := t.TempDir()
	a := newTestAgent(t, provider.NewMock("mock", "tools-buffered"), tools.NewExec(dir).Tools())
	evs, err := collect(t, a, func() error { return a.Run(context.Background(), "find") })
	if err != nil {
		t.Fatal(err)
	}
	var sawStart bool
	for _, e := range evs {
		if e.Kind == EventToolStart {
			sawStart = true
		}
	}
	if !sawStart {
		t.Error("a tool call arriving with no preceding text must still run")
	}
}

func TestStreamErrorEndsTurn(t *testing.T) {
	a := newTestAgent(t, provider.NewMock("mock", "error"), nil)
	a.MaxRetries = 0
	evs, err := collect(t, a, func() error { return a.Run(context.Background(), "go") })
	if err == nil {
		t.Fatal("want an error")
	}
	last := evs[len(evs)-1]
	if last.Kind != EventTurnEnd || last.Reason != EndError {
		t.Errorf("last event = %+v, want an error turn_end", last)
	}
	var sawError bool
	for _, e := range evs {
		if e.Kind == EventError {
			sawError = true
		}
	}
	if !sawError {
		t.Error("want an error event")
	}
}

// blockingProvider streams slowly so a test can cancel mid-turn.
type blockingProvider struct {
	started chan struct{}
	once    bool
}

func (b *blockingProvider) Name() string { return "blocking" }
func (b *blockingProvider) Embed(ctx context.Context, t []string) ([][]float32, error) {
	return nil, nil
}
func (b *blockingProvider) Models(ctx context.Context) ([]provider.ModelInfo, error) {
	return nil, nil
}
func (b *blockingProvider) ChatStream(ctx context.Context, req provider.Req) (<-chan provider.Chunk, error) {
	ch := make(chan provider.Chunk)
	go func() {
		defer close(ch)
		select {
		case ch <- provider.Chunk{Text: "partial answer"}:
		case <-ctx.Done():
			return
		}
		if !b.once {
			b.once = true
			close(b.started)
		}
		<-ctx.Done()
	}()
	return ch, nil
}

func TestInterruptKeepsPartialText(t *testing.T) {
	bp := &blockingProvider{started: make(chan struct{})}
	a := newTestAgent(t, bp, nil)
	ctx, cancel := context.WithCancel(context.Background())

	evs, err := collect(t, a, func() error {
		go func() {
			<-bp.started
			cancel()
		}()
		return a.Run(ctx, "hi")
	})
	if err != nil {
		t.Fatalf("an interrupt is not an error: %v", err)
	}
	last := evs[len(evs)-1]
	if last.Reason != EndInterrupted {
		t.Errorf("reason = %v, want interrupted", last.Reason)
	}

	msgs := a.Conv.Messages()
	found := false
	for _, m := range msgs {
		if m.Role == provider.RoleAssistant && strings.Contains(m.Content, "partial answer") {
			found = true
		}
	}
	if !found {
		t.Error("partial output must be kept as an assistant message, not discarded")
	}
}

func TestInterruptsGroupBySource(t *testing.T) {
	a := newTestAgent(t, provider.NewMock("mock", "chat"), nil)
	a.Interject(Interrupt{Source: SourceUser, Text: "also check the tests"})
	a.Interject(Interrupt{Source: SourceUser, Text: "and the docs"})
	a.Interject(Interrupt{Source: SourceSystem, Text: "[automated] finish the todos"})

	if got := a.PendingInterrupts(); got != 3 {
		t.Fatalf("pending = %d, want 3", got)
	}

	msgs := a.DrainInterrupts(false)
	if len(msgs) != 2 {
		t.Fatalf("messages = %d, want one per source", len(msgs))
	}
	if !strings.Contains(msgs[0].Content, "also check the tests") ||
		!strings.Contains(msgs[0].Content, "and the docs") {
		t.Errorf("user group = %q, want both user messages joined", msgs[0].Content)
	}
	if strings.Contains(msgs[0].Content, "automated") {
		t.Error("a system nudge must never merge into a user message")
	}
	if got := a.PendingInterrupts(); got != 0 {
		t.Errorf("pending after drain = %d, want 0", got)
	}
}

func TestUrgentDrainLeavesNonUrgent(t *testing.T) {
	a := newTestAgent(t, provider.NewMock("mock", "chat"), nil)
	a.Interject(Interrupt{Source: SourceUser, Text: "later"})
	a.Interject(Interrupt{Source: SourceUser, Text: "now", Urgent: true})

	msgs := a.DrainInterrupts(true)
	if len(msgs) != 1 || !strings.Contains(msgs[0].Content, "now") {
		t.Fatalf("urgent drain = %+v", msgs)
	}
	if got := a.PendingInterrupts(); got != 1 {
		t.Errorf("pending = %d, want the non-urgent one left", got)
	}
}

func TestInterruptInjectedAtSafePointD(t *testing.T) {
	dir := t.TempDir()
	os.MkdirAll(filepath.Join(dir, "internal", "config"), 0o755)
	os.WriteFile(filepath.Join(dir, "internal", "config", "config.go"), []byte("package config\n"), 0o644)

	a := newTestAgent(t, provider.NewMock("mock", "tools"), tools.NewFS(dir).Tools())
	a.Interject(Interrupt{Source: SourceUser, Text: "also mention the defaults"})

	_, err := collect(t, a, func() error { return a.Run(context.Background(), "explain") })
	if err != nil {
		t.Fatal(err)
	}

	msgs := a.Conv.Messages()
	// The injected message must land after the tool result, not before it.
	toolIdx, injectIdx := -1, -1
	for i, m := range msgs {
		if m.Role == provider.RoleTool {
			toolIdx = i
		}
		if strings.Contains(m.Content, "also mention the defaults") {
			injectIdx = i
		}
	}
	if injectIdx < 0 {
		t.Fatal("the interrupt was never injected")
	}
	if toolIdx < 0 || injectIdx < toolIdx {
		t.Errorf("interrupt at %d, tool result at %d — injection must follow the results", injectIdx, toolIdx)
	}
}

func TestUrgentInterruptStubsSkippedTools(t *testing.T) {
	slow := tools.Set{{
		Name:   "read",
		Desc:   "read",
		Schema: json.RawMessage(`{"type":"object","properties":{}}`),
		Run: func(ctx context.Context, raw json.RawMessage) (tools.Result, error) {
			t := time.NewTimer(time.Second)
			defer t.Stop()
			<-t.C
			return tools.Result{Output: "should not be reached"}, nil
		},
	}}
	a := newTestAgent(t, provider.NewMock("mock", "tools"), slow)
	a.Interject(Interrupt{Source: SourceUser, Text: "stop, do this instead", Urgent: true})

	start := time.Now()
	_, err := collect(t, a, func() error { return a.Run(context.Background(), "go") })
	if err != nil {
		t.Fatal(err)
	}
	if elapsed := time.Since(start); elapsed > 900*time.Millisecond {
		t.Errorf("took %s; the urgent interrupt should have skipped the slow tool", elapsed)
	}

	msgs := a.Conv.Messages()
	var stubbed bool
	for _, m := range msgs {
		if m.Role == provider.RoleTool && strings.Contains(m.Content, stubSkipped) {
			stubbed = true
			if !m.IsError {
				t.Error("a skipped tool result must be flagged as an error")
			}
		}
	}
	if !stubbed {
		t.Error("every skipped call still needs a tool result — the wire format requires adjacency")
	}
}

func TestRetryOnTransientFailure(t *testing.T) {
	var attempts int
	p := &flakyProvider{fail: 2, attempts: &attempts}
	a := newTestAgent(t, p, nil)

	evs, err := collect(t, a, func() error { return a.Run(context.Background(), "hi") })
	if err != nil {
		t.Fatalf("a transient failure should be retried, not surfaced: %v", err)
	}
	if attempts != 3 {
		t.Errorf("attempts = %d, want 3 (two failures then success)", attempts)
	}
	if evs[len(evs)-1].Reason != EndComplete {
		t.Errorf("reason = %v", evs[len(evs)-1].Reason)
	}
}

func TestNoRetryOnAuthFailure(t *testing.T) {
	// A 401 is deterministic; retrying it spins forever (plan.md §12.6).
	var attempts int
	p := &flakyProvider{fail: 99, attempts: &attempts,
		err: &provider.HTTPError{Status: 401, Message: "invalid api key"}}
	a := newTestAgent(t, p, nil)

	_, err := collect(t, a, func() error { return a.Run(context.Background(), "hi") })
	if err == nil {
		t.Fatal("want the auth error surfaced")
	}
	if attempts != 1 {
		t.Errorf("attempts = %d, want exactly 1 — a 401 must not be retried", attempts)
	}
}

func TestRetryGivesUp(t *testing.T) {
	var attempts int
	p := &flakyProvider{fail: 99, attempts: &attempts}
	a := newTestAgent(t, p, nil)
	a.MaxRetries = 2

	_, err := collect(t, a, func() error { return a.Run(context.Background(), "hi") })
	if err == nil {
		t.Fatal("want an error after exhausting retries")
	}
	if attempts != 3 {
		t.Errorf("attempts = %d, want 1 + 2 retries", attempts)
	}
}

// flakyProvider fails its first `fail` attempts, then succeeds.
type flakyProvider struct {
	fail     int
	attempts *int
	err      error
}

func (f *flakyProvider) Name() string { return "flaky" }
func (f *flakyProvider) Embed(ctx context.Context, t []string) ([][]float32, error) {
	return nil, nil
}
func (f *flakyProvider) Models(ctx context.Context) ([]provider.ModelInfo, error) { return nil, nil }
func (f *flakyProvider) ChatStream(ctx context.Context, req provider.Req) (<-chan provider.Chunk, error) {
	*f.attempts++
	if *f.attempts <= f.fail {
		if f.err != nil {
			return nil, f.err
		}
		return nil, &provider.HTTPError{Status: 503, Message: "upstream busy"}
	}
	ch := make(chan provider.Chunk, 2)
	ch <- provider.Chunk{Text: "ok"}
	ch <- provider.Chunk{Done: true, Usage: &provider.Usage{PromptTokens: 1, CompletionTokens: 1}}
	close(ch)
	return ch, nil
}

func TestPostTurnHookCanAppend(t *testing.T) {
	var calls int
	a := newTestAgent(t, provider.NewMock("mock", "chat"), nil)
	a.Hooks = HookFunc(func(ctx context.Context, ag *Agent) (bool, error) {
		calls++
		if calls == 1 {
			ag.Conv.Append(provider.Message{
				Role:    provider.RoleUser,
				Content: "[automated todo completion gate - not a user message] keep going",
			})
			return true, nil
		}
		return false, nil
	})

	_, err := collect(t, a, func() error { return a.Run(context.Background(), "hi") })
	if err != nil {
		t.Fatal(err)
	}
	if calls != 2 {
		t.Errorf("hook calls = %d, want 2 (append once, then stop)", calls)
	}
}

func TestChainStopsAtFirstAppend(t *testing.T) {
	var second bool
	chain := Chain{
		HookFunc(func(ctx context.Context, a *Agent) (bool, error) { return true, nil }),
		HookFunc(func(ctx context.Context, a *Agent) (bool, error) { second = true; return false, nil }),
	}
	appended, err := chain.PostTurn(context.Background(), nil)
	if err != nil || !appended {
		t.Fatalf("appended = %v, err = %v", appended, err)
	}
	if second {
		t.Error("a second hook must not also append — one arguing voice at a time")
	}
}

func TestMaxStepsBreaker(t *testing.T) {
	// A model that never stops asking for tools has to be stopped somewhere.
	loop := &loopingProvider{}
	noop := tools.Set{{
		Name:   "spin",
		Desc:   "spin",
		Schema: json.RawMessage(`{"type":"object","properties":{}}`),
		Run: func(ctx context.Context, raw json.RawMessage) (tools.Result, error) {
			return tools.Result{Output: "again"}, nil
		},
	}}
	a := newTestAgent(t, loop, noop)
	evs, err := collect(t, a, func() error { return a.Run(context.Background(), "go") })
	if err != nil {
		t.Fatal(err)
	}
	if evs[len(evs)-1].Reason != EndMaxSteps {
		t.Errorf("reason = %v, want max_steps", evs[len(evs)-1].Reason)
	}
}

type loopingProvider struct{}

func (l *loopingProvider) Name() string { return "loop" }
func (l *loopingProvider) Embed(ctx context.Context, t []string) ([][]float32, error) {
	return nil, nil
}
func (l *loopingProvider) Models(ctx context.Context) ([]provider.ModelInfo, error) { return nil, nil }
func (l *loopingProvider) ChatStream(ctx context.Context, req provider.Req) (<-chan provider.Chunk, error) {
	ch := make(chan provider.Chunk, 2)
	ch <- provider.Chunk{ToolCalls: []provider.ToolCall{{ID: "c", Name: "spin", Args: json.RawMessage(`{}`)}}}
	ch <- provider.Chunk{Done: true}
	close(ch)
	return ch, nil
}

func TestConversationIsAppendOnly(t *testing.T) {
	c := NewConversation("sys")
	c.Append(provider.Message{Role: provider.RoleUser, Content: "one"})
	c.Append(provider.Message{Role: provider.RoleAssistant, Content: "two"})

	first := c.Messages()
	// Mutating the returned slice must not corrupt history.
	first[1].Content = "TAMPERED"

	second := c.Messages()
	if second[1].Content != "one" {
		t.Errorf("history was mutated through a returned slice: %q", second[1].Content)
	}
	if second[0].Role != provider.RoleSystem || second[0].Content != "sys" {
		t.Errorf("system prompt must lead: %+v", second[0])
	}
}

func TestCompactBumpsEpoch(t *testing.T) {
	c := NewConversation("sys")
	c.Append(provider.Message{Role: provider.RoleUser, Content: "a"})
	c.Append(provider.Message{Role: provider.RoleAssistant, Content: "b"})
	before := c.Epoch()

	c.Reset([]provider.Message{CompactMessage("we discussed a and b")})
	if c.Epoch() != before+1 {
		t.Errorf("epoch = %d, want %d", c.Epoch(), before+1)
	}
	if c.Len() != 1 {
		t.Errorf("length = %d, want the summary alone", c.Len())
	}
	msgs := c.Messages()
	if !strings.Contains(msgs[1].Content, "we discussed") {
		t.Errorf("summary missing: %+v", msgs[1])
	}
}

func TestLoadProjectContext(t *testing.T) {
	dir := t.TempDir()
	cfgDir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "AGENTS.md"), []byte("Use tabs."), 0o644)
	os.WriteFile(filepath.Join(cfgDir, "CLAUDE.md"), []byte("Be terse."), 0o644)

	pc := LoadProjectContext(dir, cfgDir)
	if !strings.Contains(pc.Instructions, "Use tabs.") {
		t.Error("cwd instructions missing")
	}
	if !strings.Contains(pc.Instructions, "Be terse.") {
		t.Error("config-dir instructions missing")
	}
	// The more specific file must come first.
	if strings.Index(pc.Instructions, "Use tabs.") > strings.Index(pc.Instructions, "Be terse.") {
		t.Error("cwd instructions must precede the global fallback")
	}
	if len(pc.Sources) != 2 {
		t.Errorf("sources = %v, want both files", pc.Sources)
	}
}

func TestLoadProjectContextEmpty(t *testing.T) {
	pc := LoadProjectContext(t.TempDir(), t.TempDir())
	if pc.Instructions != "" {
		t.Errorf("instructions = %q, want empty", pc.Instructions)
	}
}

func TestSystemPromptStaysLean(t *testing.T) {
	pc := LoadProjectContext(t.TempDir(), t.TempDir())
	prompt := BuildSystemPrompt(pc, nil, "")

	// A rough proxy for tokens; the budget in plan.md §15 is ~1200 tokens, and
	// a lean harness ships well under that.
	approxTokens := len(prompt) / 4
	if approxTokens > 700 {
		t.Errorf("system prompt is ~%d tokens (%d chars); the budget is well under 700",
			approxTokens, len(prompt))
	}
	if !strings.Contains(prompt, "evilcode") {
		t.Error("prompt should establish identity")
	}
}

func TestSystemPromptListsSkillsWithoutBodies(t *testing.T) {
	skills := []Skill{
		{Name: "commit", Desc: "write a commit message", Path: "/skills/commit.md"},
	}
	prompt := BuildSystemPrompt(ProjectContext{}, skills, "")
	if !strings.Contains(prompt, "commit") || !strings.Contains(prompt, "write a commit message") {
		t.Error("skill name and one-liner should be indexed")
	}
	if strings.Contains(prompt, "/skills/commit.md") {
		t.Error("the index must not leak paths or bodies — that is what keeps it cacheable")
	}
}

// TestAgentDoesNotImportBubbletea enforces plan.md invariant 1: the agent core
// emits typed events and knows nothing about any frontend. Retrofitting this
// later would be a rewrite, so it is checked rather than trusted.
func TestAgentDoesNotImportBubbletea(t *testing.T) {
	pkg, err := build.ImportDir(".", 0)
	if err != nil {
		t.Fatal(err)
	}
	for _, imp := range append(pkg.Imports, pkg.TestImports...) {
		if strings.Contains(imp, "bubbletea") || strings.Contains(imp, "lipgloss") ||
			strings.Contains(imp, "bubbles") || strings.Contains(imp, "glamour") {
			t.Errorf("internal/agent imports %q — the core must stay frontend-agnostic", imp)
		}
	}
}

func TestPokeHookDrivesTheTurnEndTree(t *testing.T) {
	// An early stop with incomplete todos must be poked back into work — the
	// headline behavior of plan.md §12.4.
	store, err := todo.NewStore(t.TempDir(), "dracula")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.Apply(todo.Write{Items: []todo.Item{
		{ID: "a", Content: "wire the auth flow", Status: todo.StatusPending},
		{ID: "b", Content: "add the retry gate", Status: todo.StatusPending},
	}}); err != nil {
		t.Fatal(err)
	}

	a := newTestAgent(t, provider.NewMock("mock", "chat"), nil)
	hook := NewPokeHook(store, true)
	a.Hooks = hook

	evs, err := collect(t, a, func() error { return a.Run(context.Background(), "do the work") })
	if err != nil {
		t.Fatal(err)
	}

	var poked bool
	for _, e := range evs {
		if e.Kind == EventNotice && strings.Contains(e.Text, "incomplete todos") {
			poked = true
		}
	}
	if !poked {
		t.Error("the harness should have poked about the incomplete todos")
	}

	// The continuation must be stored as user-role so the model reads it as a
	// normal message, while still being recognizable as harness output.
	var found bool
	for _, m := range a.Conv.Messages() {
		if m.Role == provider.RoleUser && todo.IsAutomated(m.Content) {
			found = true
			if !strings.Contains(m.Content, "Do not reply conversationally") {
				t.Error("continuations must tell the model not to answer them")
			}
		}
	}
	if !found {
		t.Error("no automated continuation was appended")
	}
}

func TestPokeHookDisarmsWhenComplete(t *testing.T) {
	store, _ := todo.NewStore(t.TempDir(), "dracula")
	done := uint8(100)
	store.Apply(todo.Write{Items: []todo.Item{
		{ID: "a", Content: "done", Status: todo.StatusCompleted, CompletionConfidence: &done},
	}})

	a := newTestAgent(t, provider.NewMock("mock", "chat"), nil)
	a.Hooks = NewPokeHook(store, true)

	evs, err := collect(t, a, func() error { return a.Run(context.Background(), "go") })
	if err != nil {
		t.Fatal(err)
	}
	var rites bool
	for _, e := range evs {
		if e.Kind == EventNotice && strings.Contains(e.Text, "All rites complete") {
			rites = true
		}
	}
	if !rites {
		t.Error("a fully validated list should end the cycle with the completion notice")
	}
}

func TestPokeHookDisabledDoesNothing(t *testing.T) {
	store, _ := todo.NewStore(t.TempDir(), "dracula")
	store.Apply(todo.Write{Items: []todo.Item{
		{ID: "a", Content: "open", Status: todo.StatusPending},
	}})

	a := newTestAgent(t, provider.NewMock("mock", "chat"), nil)
	a.Hooks = NewPokeHook(store, false)

	before := a.Conv.Len()
	if _, err := collect(t, a, func() error { return a.Run(context.Background(), "go") }); err != nil {
		t.Fatal(err)
	}
	// user + assistant only; nothing appended by the hook.
	if got := a.Conv.Len(); got != before+2 {
		t.Errorf("conversation grew to %d; a disabled hook must not append", got)
	}
}

func TestRefusalDetection(t *testing.T) {
	// Detection is deliberately conservative: a false positive disarms a
	// working session, which is worse than missing one refusal. It matches on
	// stock refusal openings only, and does not try to parse intent.
	refusals := []string{
		"I can't help with that.",
		"I cannot assist with that request.",
		"I'm not able to help with that, sorry.",
	}
	for _, s := range refusals {
		if !looksLikeRefusal(s) {
			t.Errorf("%q should read as a refusal", s)
		}
	}

	notRefusals := []string{
		"Here is the fix.",
		"I cannot reproduce the bug, but the test passes.",
		"The function refuses invalid input, which is correct.",
		"",
	}
	for _, s := range notRefusals {
		if looksLikeRefusal(s) {
			t.Errorf("%q should not read as a refusal", s)
		}
	}
}

func TestMemoryHookNeverAppends(t *testing.T) {
	// The hook's whole contract: it observes and returns false, so it can sit
	// first in the chain without starving auto-poke behind it.
	store, err := memory.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	calls := 0
	h := &MemoryHook{
		Manager: memory.NewManager(store, nil, nil, "s", true),
		extract: func(context.Context, *memory.Manager) { calls++ },
	}

	a := newTestAgent(t, provider.NewMock("mock", "chat"), nil)
	a.Conv.Append(
		provider.Message{Role: provider.RoleUser, Content: "how do I build this?"},
		provider.Message{Role: provider.RoleAssistant, Content: "run make."},
	)

	before := a.Conv.Len()
	for i := 0; i < memory.ExtractEvery; i++ {
		appended, err := h.PostTurn(context.Background(), a)
		if err != nil {
			t.Fatal(err)
		}
		if appended {
			t.Fatal("the memory hook appended to the conversation")
		}
	}
	if a.Conv.Len() != before {
		t.Errorf("conversation grew from %d to %d", before, a.Conv.Len())
	}
	if calls != 1 {
		t.Errorf("extraction ran %d times in %d turns, want once", calls, memory.ExtractEvery)
	}
}

func TestMemoryHookSkipsItsOwnInjection(t *testing.T) {
	// Feeding the `<memories>` tail back into extraction is how a bank starts
	// remembering its own output.
	a := newTestAgent(t, provider.NewMock("mock", "chat"), nil)
	a.Conv.Append(
		provider.Message{Role: provider.RoleUser, Content: "the real question"},
		provider.Message{Role: provider.RoleUser, Content: "<memories>\n- (fact) something\n</memories>"},
		provider.Message{Role: provider.RoleAssistant, Content: "the answer"},
	)
	got := turnText(a)
	if strings.Contains(got, "<memories>") {
		t.Errorf("extraction would re-read its own injection:\n%s", got)
	}
	if !strings.Contains(got, "the real question") {
		t.Errorf("the user's actual message was dropped:\n%s", got)
	}
}

func TestMemoryHookIsInertWhenDisabled(t *testing.T) {
	store, _ := memory.Open(t.TempDir())
	defer store.Close()
	calls := 0
	h := &MemoryHook{
		Manager: memory.NewManager(store, nil, nil, "s", false),
		extract: func(context.Context, *memory.Manager) { calls++ },
	}
	a := newTestAgent(t, provider.NewMock("mock", "chat"), nil)
	a.Conv.Append(provider.Message{Role: provider.RoleUser, Content: "x"})
	for i := 0; i < memory.ExtractEvery*2; i++ {
		h.PostTurn(context.Background(), a)
	}
	if calls != 0 {
		t.Errorf("disabled memory extracted %d times", calls)
	}
}

func TestRecallSeamAppendsAndEmits(t *testing.T) {
	// The seam is what gives headless run and the daemon recall without a
	// second implementation (invariant 1).
	a := newTestAgent(t, provider.NewMock("mock", "chat"), nil)
	a.Recall = func(context.Context, string) (string, any) {
		return "<memories>\n- (fact) noted\n</memories>", []string{"noted"}
	}

	events, err := collect(t, a, func() error { return a.Run(context.Background(), "hello") })
	if err != nil {
		t.Fatal(err)
	}

	// Order matters: the memories follow the user message, so the model reads
	// the question first and the notes as context for it.
	msgs := a.Conv.Messages()
	var userAt, memAt int = -1, -1
	for i, m := range msgs {
		if m.Content == "hello" {
			userAt = i
		}
		if strings.HasPrefix(m.Content, "<memories>") {
			memAt = i
		}
	}
	if userAt < 0 || memAt < 0 {
		t.Fatalf("conversation = %v", msgs)
	}
	if memAt != userAt+1 {
		t.Errorf("memories landed at %d, want right after the user message at %d", memAt, userAt)
	}

	var saw bool
	for _, e := range events {
		if e.Kind == EventMemoryRecall {
			saw = true
			if e.Display == nil {
				t.Error("the recall event carried no display payload for the tile")
			}
		}
	}
	if !saw {
		t.Error("no memory_recall event was emitted")
	}
}

func TestRecallSeamSilentWhenEmpty(t *testing.T) {
	a := newTestAgent(t, provider.NewMock("mock", "chat"), nil)
	a.Recall = func(context.Context, string) (string, any) { return "", nil }
	if _, err := collect(t, a, func() error { return a.Run(context.Background(), "hello") }); err != nil {
		t.Fatal(err)
	}
	for _, m := range a.Conv.Messages() {
		if strings.Contains(m.Content, "<memories>") {
			t.Error("an empty recall still injected a message")
		}
	}
}

func TestConversationPersistsEveryAppend(t *testing.T) {
	// §18 makes the JSONL file the source of truth — "resume = replay". Nothing
	// wrote messages to it for four phases, so every `-resume` replayed an empty
	// conversation and said nothing was wrong.
	conv := NewConversation("system")
	var written []provider.Message
	conv.Persist(func(m provider.Message) { written = append(written, m) })

	conv.Append(provider.Message{Role: provider.RoleUser, Content: "one"})
	conv.Append(
		provider.Message{Role: provider.RoleAssistant, Content: "two"},
		provider.Message{Role: provider.RoleUser, Content: "three"},
	)

	if len(written) != 3 {
		t.Fatalf("persisted %d messages, want 3", len(written))
	}
	if written[0].Content != "one" || written[2].Content != "three" {
		t.Errorf("persisted out of order: %v", written)
	}
}

func TestConversationDoesNotPersistBeforeTheSinkIsSet(t *testing.T) {
	// The replay itself must not be written back: a resumed session appends
	// what it just read, and persisting that doubles the file every resume.
	conv := NewConversation("system")
	conv.Append(provider.Message{Role: provider.RoleUser, Content: "replayed"})

	var written []provider.Message
	conv.Persist(func(m provider.Message) { written = append(written, m) })
	conv.Append(provider.Message{Role: provider.RoleUser, Content: "new"})

	if len(written) != 1 || written[0].Content != "new" {
		t.Errorf("persisted %v, want only the message appended after the sink", written)
	}
}
