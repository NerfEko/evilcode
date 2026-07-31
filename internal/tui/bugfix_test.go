package tui

import (
	"strings"
	"testing"

	"evilcode/internal/agent"
	"evilcode/internal/config"
	"evilcode/internal/provider"
)

// These cover the six things found in real use. Each names the wrong behaviour
// rather than just the right one, because the right one is easy to re-break.

func TestThinkingCollapsesWhenTheAnswerStarts(t *testing.T) {
	// It used to stay expanded under the whole reply and only collapse at turn
	// end, pushing the answer down the screen while it streamed.
	m := newTestModel(t)
	m.applyEvent(agent.Event{Kind: agent.EventReasoningDelta, Text: "weighing it up\n"})
	if m.reasoningIdx < 0 {
		t.Fatal("no reasoning block was opened")
	}
	if m.blocks[m.reasoningIdx].Collapsed {
		t.Error("the trace collapsed while it was still being written")
	}

	idx := m.reasoningIdx
	m.applyEvent(agent.Event{Kind: agent.EventTextDelta, Text: "The answer is"})
	if !m.blocks[idx].Collapsed {
		t.Error("the trace is still expanded after the answer started")
	}
	if m.blocks[idx].Streaming {
		t.Error("the trace is still marked streaming")
	}
}

func TestKeepThinkingLeavesTheTraceOpen(t *testing.T) {
	m := newTestModel(t)
	m.WithDisplay(config.Display{KeepThinking: true, ThinkingDisplay: "current"})
	m.applyEvent(agent.Event{Kind: agent.EventReasoningDelta, Text: "weighing it up\n"})
	idx := m.reasoningIdx
	m.applyEvent(agent.Event{Kind: agent.EventTextDelta, Text: "The answer is"})

	if m.blocks[idx].Collapsed {
		t.Error("keep_thinking is on but the trace collapsed anyway")
	}
}

func TestLiveThinkingIsCappedAndSaysWhatItHid(t *testing.T) {
	// A model that thinks for thirty seconds otherwise pushes the whole
	// conversation off the screen.
	r := testRenderer(60)
	r.ThinkingLines = 4

	var text strings.Builder
	for i := 0; i < 12; i++ {
		text.WriteString("a line of reasoning\n")
	}
	rows := plainLines(r.render(&Block{Kind: BlockReasoning, Text: text.String(), Streaming: true}))

	if len(rows) != r.ThinkingLines+1 {
		t.Fatalf("rendered %d rows, want %d plus the elision notice", len(rows), r.ThinkingLines)
	}
	if !strings.Contains(rows[0], "earlier lines") {
		t.Errorf("first row = %q, want it to say what scrolled past", rows[0])
	}
	// The tail, not the head: where thinking has got to is the interesting part.
	if !strings.Contains(rows[len(rows)-1], "a line of reasoning") {
		t.Errorf("last row = %q", rows[len(rows)-1])
	}
}

func TestThinkingWindowDefaultsWhenUnset(t *testing.T) {
	r := testRenderer(60)
	var text strings.Builder
	for i := 0; i < 40; i++ {
		text.WriteString("line\n")
	}
	rows := plainLines(r.render(&Block{Kind: BlockReasoning, Text: text.String(), Streaming: true}))
	if len(rows) != DefaultThinkingLines+1 {
		t.Errorf("rendered %d rows, want the default window of %d", len(rows), DefaultThinkingLines)
	}
}

func TestPromptNumbersCountUpAndStayPut(t *testing.T) {
	// They used to be the *distance from newest*, so the first prompt carried
	// the highest number and every prompt was renumbered on each turn.
	m := newTestModel(t)
	for _, text := range []string{"first", "second", "third"} {
		m.applyEvent(agent.Event{Kind: agent.EventTurnStart, Text: text})
	}

	var got []int
	for _, b := range m.blocks {
		if b.Kind == BlockUser {
			got = append(got, b.Number)
		}
	}
	want := []int{1, 2, 3}
	if len(got) != len(want) {
		t.Fatalf("drew %d prompts, want %d", len(got), len(want))
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("prompt %d is numbered %d, want %d", i+1, got[i], want[i])
		}
	}
}

func TestPromptDecayIsSeparateFromItsNumber(t *testing.T) {
	// The rainbow ramp is indexed by distance from the newest (§7.7); the label
	// is the ordinal. Conflating them is what broke the numbering.
	m := newTestModel(t)
	for _, text := range []string{"first", "second"} {
		m.applyEvent(agent.Event{Kind: agent.EventTurnStart, Text: text})
	}
	var users []Block
	for _, b := range m.blocks {
		if b.Kind == BlockUser {
			users = append(users, b)
		}
	}
	if users[len(users)-1].Decay != 0 {
		t.Errorf("newest prompt has decay %d, want 0", users[len(users)-1].Decay)
	}
	if users[0].Decay != 1 {
		t.Errorf("older prompt has decay %d, want 1", users[0].Decay)
	}
}

func TestTokenSpendAccumulatesAcrossAToolTurn(t *testing.T) {
	// A turn with tool calls makes one request per round. Assigning here showed
	// only the last round, and the context meter read far below the truth.
	m := newTestModel(t)
	m.applyEvent(agent.Event{Kind: agent.EventTurnStart, Text: "go"})
	m.applyEvent(agent.Event{Kind: agent.EventTokenUsage,
		Usage: &agent.Usage{In: 100, Out: 20, CtxUsed: 120, GenMS: 1000}})
	m.applyEvent(agent.Event{Kind: agent.EventTokenUsage,
		Usage: &agent.Usage{In: 300, Out: 40, CtxUsed: 340, GenMS: 1000}})

	if m.status.TokensOut != 60 {
		t.Errorf("out = %d, want the sum of both rounds", m.status.TokensOut)
	}
	if m.status.TokensIn != 400 {
		t.Errorf("in = %d, want the sum of both rounds", m.status.TokensIn)
	}
	// Context is the newest request's size, not the sum: prompt tokens already
	// carry the whole conversation, so summing double-counts it.
	if m.ctxUsed != 340 {
		t.Errorf("context = %d, want the latest request's size", m.ctxUsed)
	}
}

func TestTokensPerSecondMeasuresGenerationNotWallClock(t *testing.T) {
	// Wall-clock counts tool execution as generation and reports a rate that is
	// not the model's.
	m := newTestModel(t)
	m.applyEvent(agent.Event{Kind: agent.EventTurnStart, Text: "go"})
	m.applyEvent(agent.Event{Kind: agent.EventTokenUsage,
		Usage: &agent.Usage{In: 10, Out: 200, CtxUsed: 210, GenMS: 2000}})

	if got := m.status.TokensPerSecond; got != 100 {
		t.Errorf("tps = %.1f, want 100 (200 tokens over 2s of generation)", got)
	}
}

func TestGenerationClockResetsEachTurn(t *testing.T) {
	m := newTestModel(t)
	m.applyEvent(agent.Event{Kind: agent.EventTurnStart, Text: "one"})
	m.applyEvent(agent.Event{Kind: agent.EventTokenUsage,
		Usage: &agent.Usage{Out: 100, GenMS: 1000}})
	m.applyEvent(agent.Event{Kind: agent.EventTurnStart, Text: "two"})
	if m.genMS != 0 {
		t.Errorf("generation clock = %dms at turn start, want it reset", m.genMS)
	}
}

func TestIdleHintCarriesLiveStateNotADeadKeybinding(t *testing.T) {
	// "Ctrl+Enter to queue" at rest is untrue — there is no turn to queue
	// behind — and it was the one always-visible row.
	got := idleHint(ComposerState{
		Model: "glm-5.2:cloud", CtxUsed: 12_000, CtxMax: 200_000, Session: "dracula",
	})
	for _, want := range []string{"glm-5.2:cloud", "12.0k/200k ctx", "6%", "dracula"} {
		if !strings.Contains(got, want) {
			t.Errorf("hint %q is missing %q", got, want)
		}
	}
}

func TestIdleHintOmitsAPercentageNotWorthShowing(t *testing.T) {
	got := idleHint(ComposerState{Model: "m", CtxUsed: 100, CtxMax: 200_000})
	if strings.Contains(got, "0%") {
		t.Errorf("hint = %q, want no percentage at 0", got)
	}
	if !strings.Contains(got, "ctx") {
		t.Errorf("hint = %q, want the raw counts", got)
	}
}

func TestIdleHintFallsBackToSomethingUseful(t *testing.T) {
	got := idleHint(ComposerState{})
	if got == "" || !strings.Contains(got, "Enter") {
		t.Errorf("hint = %q, want a usable default before anything resolves", got)
	}
}

func TestRoundTokensDropsAPointlessDecimal(t *testing.T) {
	cases := map[int]string{200_000: "200k", 1_000_000: "1M", 262_144: "262.1k", 512: "512"}
	for in, want := range cases {
		if got := roundTokens(in); got != want {
			t.Errorf("roundTokens(%d) = %q, want %q", in, got, want)
		}
	}
}

func TestQueueHintStillShowsWhileProcessing(t *testing.T) {
	// The binding is real *during* a turn, which is the case the row is for.
	r := testRenderer(80)
	got := plain(r.hintLine(ComposerState{Processing: true}))
	if !strings.Contains(got, "queue") {
		t.Errorf("hint = %q, want the queue binding while a turn runs", got)
	}
}

// newTestModel builds a Model with a real agent behind it, which applyEvent
// needs: turn start reads the conversation to decide whether to draw a prompt.
func newTestModel(t *testing.T) *Model {
	t.Helper()
	a := agent.New("dracula", provider.NewMock("mock", "chat"), "mock-large", nil,
		agent.NewConversation("system"))
	t.Cleanup(a.Close)
	m := NewModel(a, HeaderState{SessionName: "dracula", Model: "mock-large"})
	m.width, m.height = 100, 40
	return m
}

func TestSidePanelUsesTheWholeTerminalHeight(t *testing.T) {
	// It used to be attached only for as many rows as the chat column happened
	// to be tall, so a short conversation next to a long file cut the panel off
	// at the composer — the top rendered and everything below it vanished.
	m := newTestModel(t)
	m.panelOpen = true
	m.diffMode = DiffPinned

	var diff strings.Builder
	diff.WriteString("--- a/x.go\n+++ b/x.go\n")
	for i := 0; i < 60; i++ {
		diff.WriteString("+a changed line\n")
	}
	m.panel = PanelContent{Title: "x.go", Path: "x.go", Diff: diff.String()}

	// A chat column far shorter than the terminal, which is the case that broke.
	rows := []string{"header", "1› hi", "", "an answer", "> "}
	got := m.attachSidePanel(rows, len(rows))

	if len(got) <= len(rows) {
		t.Fatalf("frame is %d rows; the panel was truncated to the chat's %d",
			len(got), len(rows))
	}
	// The property that broke: panel rows exist *below* where the chat column
	// ended. Pinned mode condenses a long diff, so this counts placement rather
	// than how many lines the diff chose to show.
	below := 0
	for _, row := range got[len(rows):] {
		if strings.TrimSpace(plain(row)) != "" {
			below++
		}
	}
	if below == 0 {
		t.Error("nothing was drawn past the end of the chat column — the panel " +
			"is still being cut off at the composer")
	}
}
