package agent

import (
	"context"
	"errors"
	"strings"
	"testing"

	"evilcode/internal/agent/compact"
	"evilcode/internal/provider"
)

// overflowThenOKProvider serves one round that fails with a context-overflow
// error, then a normal completion. The recovery path must compact inline and
// retry without ending the turn.
// toolRoundOverflowProvider mirrors omp's overflow scenario: round 1 serves
// a tool call; the tool result lands between rounds; round 2 overflows (the
// pre-dispatch context fit, the result did not); round 3 completes after
// the between-rounds check compacts. Recovery has real new content — the
// fat tool result — to fold.
type toolRoundOverflowProvider struct{ served int }

func (p *toolRoundOverflowProvider) Name() string { return "tool-round-overflow" }
func (p *toolRoundOverflowProvider) Embed(ctx context.Context, t []string) ([][]float32, error) {
	return nil, nil
}
func (p *toolRoundOverflowProvider) Models(ctx context.Context) ([]provider.ModelInfo, error) {
	return nil, nil
}
func (p *toolRoundOverflowProvider) ChatStream(ctx context.Context, req provider.Req) (<-chan provider.Chunk, error) {
	ch := make(chan provider.Chunk, 4)
	go func() {
		defer close(ch)
		p.served++
		switch p.served {
		case 1:
			ch <- provider.Chunk{ToolCalls: []provider.ToolCall{{ID: "c1", Name: "big", Args: []byte(`{}`)}}}
			ch <- provider.Chunk{Done: true}
		case 2:
			ch <- provider.Chunk{Err: errors.New("prompt is too long: 200000 tokens > 8192 maximum context")}
		default:
			ch <- provider.Chunk{Text: "recovered"}
			ch <- provider.Chunk{Done: true}
		}
	}()
	return ch, nil
}

func TestOverflowRecoveryCompactsAndRetries(t *testing.T) {
	p := &toolRoundOverflowProvider{}
	conv := NewConversation("sys")
	// 3 fat turns: below the between-rounds compaction threshold, so the
	// fat tool result is still unsummarized content when round 2 overflows —
	// the recovery has something real to fold.
	for i := 0; i < 3; i++ {
		conv.Append([]provider.Message{
			{Role: provider.RoleUser, Content: strings.Repeat("history ", 700)},
			{Role: provider.RoleAssistant, Content: strings.Repeat("history ", 700)},
		}...)
	}
	a := New("overflow", p, "test-model", nil, conv)
	defer a.Close()
	a.BaseDelay = 1
	a.CompactionWindow = 30000
	a.Compactor = &Compactor{
		Settings: func() compact.Settings {
			s := compact.DefaultSettings()
			s.KeepRecentTokens = 2000
			return s
		}(),
		Summarize: func(context.Context, string, string) (string, error) {
			return "## Goal\ncontinued\n\n## Progress\n\n### Done\n- [x] x\n\n## Next Steps\n1. next", nil
		},
	}
	if err := a.Run(context.Background(), "go"); err != nil {
		t.Fatalf("the turn should have recovered: %v", err)
	}
	// The retry served a real completion: the model answered after compaction.
	msgs := conv.Messages()
	var sawRecovered bool
	for _, m := range msgs {
		if m.Role == provider.RoleAssistant && m.Content == "recovered" {
			sawRecovered = true
		}
	}
	if !sawRecovered {
		t.Error("the retried dispatch never completed")
	}
	// Whole-mode: no raw fat run survives.
	joined := strings.Join(messageContents(msgs), "\n")
	if strings.Contains(joined, strings.Repeat("history ", 40)) {
		t.Error("the fat history survived a recovery compaction verbatim")
	}
}

// lengthCapProvider serves a request that fits but whose completion is cut:
// the recovery keeps the tail and retries with more room.
type lengthCapProvider struct{ served int }

func (p *lengthCapProvider) Name() string { return "length-cap" }
func (p *lengthCapProvider) Embed(ctx context.Context, t []string) ([][]float32, error) {
	return nil, nil
}
func (p *lengthCapProvider) Models(ctx context.Context) ([]provider.ModelInfo, error) {
	return nil, nil
}
func (p *lengthCapProvider) ChatStream(ctx context.Context, req provider.Req) (<-chan provider.Chunk, error) {
	ch := make(chan provider.Chunk, 4)
	go func() {
		defer close(ch)
		p.served++
		if p.served == 1 {
			ch <- provider.Chunk{Err: errors.New("response exceeded the max_tokens length limit")}
		} else {
			ch <- provider.Chunk{Text: "ok"}
			ch <- provider.Chunk{Done: true}
		}
	}()
	return ch, nil
}

func TestLengthCapRecoveryKeepsTailAndRetries(t *testing.T) {
	p := &lengthCapProvider{}
	conv := NewConversation("sys")
	conv.Append([]provider.Message{
		{Role: provider.RoleUser, Content: strings.Repeat("h ", 3000)},
		{Role: provider.RoleAssistant, Content: strings.Repeat("a ", 3000)},
		{Role: provider.RoleUser, Content: "final ask"},
	}...)
	a := New("lengthcap", p, "test-model", nil, conv)
	defer a.Close()
	a.BaseDelay = 1
	a.CompactionWindow = 30000
	a.Compactor = &Compactor{
		Settings: func() compact.Settings {
			s := compact.DefaultSettings()
			s.KeepRecentTokens = 2000
			return s
		}(),
		Summarize: func(context.Context, string, string) (string, error) {
			return "## Goal\nkept going\n\n## Progress\n\n### Done\n- [x] x\n\n## Next Steps\n1. next", nil
		},
	}
	if err := a.Run(context.Background(), "go"); err != nil {
		t.Fatalf("length-cap recovery failed: %v", err)
	}
	found := false
	for _, m := range conv.Messages() {
		if m.Role == provider.RoleAssistant && m.Content == "ok" {
			found = true
		}
	}
	if !found {
		t.Error("the retried dispatch never completed")
	}
}
