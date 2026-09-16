package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"evilcode/internal/agent/compact"
	"evilcode/internal/provider"
	"evilcode/internal/tools"
	"testing"
)

// R2-14: manual compactions used to consume the same allowance the automatic
// loop is capped by, so three `/compact` calls disabled automatic protection
// for the rest of the session. And the automatic check ran once per turn,
// before the first provider request — tool results landing later in the turn
// could overflow the window before the next request saw it.

func TestManualCompactionsDoNotConsumeTheAutoBudget(t *testing.T) {
	c := &Compactor{Summarize: summarizer("summary", nil)}
	conv := compactableConversation()

	// Three manual compactions — the old MaxAutoCompactions — must not disable
	// the automatic path. Each round adds fresh turns: omp's rule (and ours)
	// is that a second compaction with nothing new since the summary is a
	// no-op, not a repeat.
	for i := 0; i < MaxAutoCompactions; i++ {
		if _, err := c.Compact(context.Background(), conv); err != nil {
			t.Fatal(err)
		}
		for j := 0; j < 3; j++ {
			conv.Append(
				provider.Message{Role: provider.RoleUser, Content: fmt.Sprintf("fresh turn %d-%d prompt %s", i, j, strings.Repeat("p", 9000))},
				provider.Message{Role: provider.RoleAssistant, Content: fmt.Sprintf("fresh turn %d-%d answer %s", i, j, strings.Repeat("a", 9000))},
			)
		}
	}
	if got := c.Count(); got != MaxAutoCompactions {
		t.Fatalf("Count() = %d, want %d manual compactions recorded", got, MaxAutoCompactions)
	}
	if !c.ShouldCompact(99, 100) {
		t.Fatal("manual compactions consumed the automatic budget")
	}

	// The automatic path's own budget still trips after its own cap.
	for i := 0; i < MaxAutoCompactions; i++ {
		c.noteAutoCompaction()
	}
	if c.ShouldCompact(99, 100) {
		t.Error("the automatic path is unbounded")
	}
}

// The provider answers once with a tool call whose result is enormous, then
// with text. The turn-start check sees a small context; the check between
// rounds must see the grown conversation and compact it before the second
// request goes out with results that blow the window.
func TestAutoCompactionIsCheckedEveryRound(t *testing.T) {
	const window = 8192 // tokens
	p := &toolThenTextProvider{
		toolCall: provider.ToolCall{ID: "c1", Name: "big", Args: json.RawMessage(`{}`)},
	}
	compacted := 0
	conv := NewConversation("system")
	// omp trigger semantics: the between-rounds check compares pending
	// tokens against the threshold (window − reserve). With reserve 16384 a
	// 8192-token window never crosses, so this fixture uses a window past
	// the floor AND pre-existing content that crosses it when the tool
	// result lands. 12 turns × ~2.2k tokens ≈ 27k; the window here is
	// effectively "used 90000 of 100000" — the provider serves the mock, so
	// the NumCtx override drives the check.
	for i := 0; i < compactionFixtureTurns; i++ {
		conv.Append(
			provider.Message{Role: provider.RoleUser, Content: fmt.Sprintf("prompt %d %s", i, strings.Repeat("p", 6000))},
			provider.Message{Role: provider.RoleAssistant, Content: fmt.Sprintf("ok %d %s", i, strings.Repeat("a", 3000))},
		)
	}
	a := New("compact-every-round", p, "test-model", tools.Set{{
		Name: "big", Schema: json.RawMessage(`{"type":"object"}`),
		Run: func(ctx context.Context, args json.RawMessage) (tools.Result, error) {
			return tools.Result{Output: strings.Repeat("y", window*8)}, nil
		},
	}}, conv)
	t.Cleanup(a.Close)
	a.BaseDelay = time.Millisecond
	a.NumCtx = window
	a.Compactor = &Compactor{
		// omp's fixed 20000 keep-recent budget presumes real windows; the
		// settings are the knob for a fixture-sized one.
		Settings: func() compact.Settings {
			s := compact.DefaultSettings()
			s.KeepRecentTokens = 2000
			return s
		}(),
		Summarize: func(ctx context.Context, system, user string) (string, error) {
			compacted++
			return "summary", nil
		},
	}

	if _, err := collect(t, a, func() error { return a.Run(context.Background(), "go") }); err != nil {
		t.Fatal(err)
	}
	if compacted == 0 {
		t.Fatal("a turn that overflowed its window mid-turn never compacted")
	}
}

// toolThenTextProvider serves one round with a tool call, then a text round.
type toolThenTextProvider struct {
	toolCall provider.ToolCall
	served   int
}

func (p *toolThenTextProvider) Name() string { return "tool-then-text" }
func (p *toolThenTextProvider) Embed(ctx context.Context, t []string) ([][]float32, error) {
	return nil, nil
}
func (p *toolThenTextProvider) Models(ctx context.Context) ([]provider.ModelInfo, error) {
	return nil, nil
}
func (p *toolThenTextProvider) ChatStream(ctx context.Context, req provider.Req) (<-chan provider.Chunk, error) {
	ch := make(chan provider.Chunk, 4)
	go func() {
		defer close(ch)
		defer func() { p.served++ }()
		if p.served == 0 {
			ch <- provider.Chunk{ToolCalls: []provider.ToolCall{p.toolCall}}
		} else {
			ch <- provider.Chunk{Text: "done"}
		}
		ch <- provider.Chunk{Done: true}
	}()
	return ch, nil
}
