package tui

import (
	"context"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"evilcode/internal/agent"
	"evilcode/internal/agent/compact"
	"evilcode/internal/provider"
)

func compactTestModel(t *testing.T) *Model {
	t.Helper()
	a := agent.New("bat", provider.NewMock("mock", "chat"), "mock-large", nil,
		agent.NewConversation("system"))
	t.Cleanup(a.Close)
	// Two fat turns (~1200 tokens each) plus a small live tail: with a 2000
	// token preserve budget the cutoff lands between the second and third
	// turn, so there is something to summarize.
	fat := strings.Repeat("history ", 600)
	a.Conv.Append([]provider.Message{
		{Role: provider.RoleUser, Content: fat},
		{Role: provider.RoleAssistant, Content: fat},
		{Role: provider.RoleUser, Content: fat},
		{Role: provider.RoleAssistant, Content: fat},
		{Role: provider.RoleUser, Content: "three"},
		{Role: provider.RoleAssistant, Content: "tres"},
	}...)
	m := NewModel(a, HeaderState{SessionName: "bat", Model: "mock-large"})
	m.width, m.height = 100, 40
	m.ctxMax = 2000
	// omp engine: keep-recent is a settings knob; the old 2000-token
	// preserve budget maps to KeepRecentTokens here.
	m.WithCompactor(&agent.Compactor{
		Settings: func() compact.Settings {
			s := compact.DefaultSettings()
			s.KeepRecentTokens = 500
			return s
		}(),
		Summarize: func(context.Context, string, string) (string, error) {
			return "## Goal\nThe conversation covered counting in Spanish.\n\n## Progress\n\n### Done\n- [x] x\n\n## Next Steps\n1. next", nil
		},
	})
	return m
}

func TestCompactionShowsAProgressBarWhileRunning(t *testing.T) {
	// Compaction is one side-call with no percentage. The bar exists so the
	// gap is legible as work in progress — a silent thirty seconds reads as a
	// hang — and it must vanish the moment the result lands.
	m := compactTestModel(t)

	_, cmd := m.runCompact()
	if !m.compacting {
		t.Fatal("/compact did not arm the compacting state")
	}
	frame := m.View().Content
	if !strings.Contains(frame, "Compacting") || !strings.Contains(frame, "messages") {
		t.Errorf("the compacting bar is not on screen:\n%s", frame)
	}
	if got := strings.Count(frame, "Compacting"); got != 1 {
		t.Errorf("compaction rendered %d status rows, want one progress row", got)
	}

	msg := cmd()
	done, ok := msg.(compactDone)
	if !ok {
		t.Fatalf("the compact command produced %T", msg)
	}
	if done.err != nil {
		t.Fatalf("compaction failed: %v", done.err)
	}
	m.applyCompaction(done)

	if m.compacting {
		t.Error("the compacting state survived the finished compaction")
	}
	frame = m.View().Content
	if strings.Contains(frame, "Compacting") {
		t.Errorf("the bar is still on screen after completion:\n%s", frame)
	}
}

func TestCompactedRecordExpandsOnAClick(t *testing.T) {
	// "context compacted" means nothing until the summary the model will see
	// is readable. Collapsed is one row naming what happened; clicking shows
	// the summary itself.
	m := compactTestModel(t)
	m.applyCompaction(compactDone{
		summary: "The conversation covered counting in Spanish.",
		before:  6,
	})

	last := len(m.blocks) - 1
	if m.blocks[last].Kind != BlockCompacted {
		t.Fatalf("block kind = %v, want BlockCompacted", m.blocks[last].Kind)
	}
	if !m.blocks[last].Collapsed {
		t.Error("the record should start collapsed")
	}

	tr := m.transcriptLines()
	first := int(tr.First[last])
	collapsed := strings.Join(tr.Lines[first:first+1], "\n")
	if !strings.Contains(collapsed, "context compacted") ||
		!strings.Contains(collapsed, "click to view") {
		t.Errorf("the collapsed row does not explain itself: %q", collapsed)
	}
	if strings.Contains(collapsed, "counting in Spanish") {
		t.Errorf("the collapsed row leaks the whole summary: %q", collapsed)
	}

	if !m.toggleCompactedAt(tea.Mouse{X: 2, Y: first}) {
		t.Fatal("the click did not land on the compaction record")
	}
	if m.blocks[last].Collapsed {
		t.Error("the click did not expand the record")
	}

	tr = m.transcriptLines()
	first = int(tr.First[last])
	expanded := strings.Join(tr.Lines[first:], "\n")
	if !strings.Contains(expanded, "counting in Spanish") {
		t.Errorf("the expanded record does not show the summary:\n%s", expanded)
	}
	if !strings.Contains(expanded, "click to collapse") {
		t.Errorf("the expanded record does not say how to close it:\n%s", expanded)
	}

	// A second click folds it back.
	if !m.toggleCompactedAt(tea.Mouse{X: 2, Y: first}) {
		t.Fatal("the second click did not land on the record")
	}
	if !m.blocks[last].Collapsed {
		t.Error("the second click did not collapse the record")
	}
}

func TestCompactionErrorAlsoDisarmsTheBar(t *testing.T) {
	// A failed side-call must not leave a progress bar on screen forever.
	a := agent.New("bat", provider.NewMock("mock", "chat"), "mock-large", nil,
		agent.NewConversation("system"))
	t.Cleanup(a.Close)
	m := newTestModel(t)
	m.compacting = true
	m.compactingSince = time.Now()

	m.applyCompaction(compactDone{err: context.DeadlineExceeded})

	if m.compacting {
		t.Error("the bar outlived a failed compaction")
	}
	if len(m.blocks) == 0 || m.blocks[len(m.blocks)-1].Kind != BlockError {
		t.Error("a failed compaction did not report itself as an error block")
	}
}

func TestRenderCompactingIsABarNotACounter(t *testing.T) {
	// Deterministic mode freezes the sweep so captures and goldens settle.
	t.Setenv("EVILCODE_DETERMINISTIC", "1")
	out := plainLines(testRenderer(80).RenderCompacting(9*time.Second, 84))
	if len(out) != 1 {
		t.Fatalf("the bar rendered %d rows, want one", len(out))
	}
	row := out[0]
	if !strings.Contains(row, "Compacting 84 messages") || !strings.Contains(row, "9s") {
		t.Errorf("the bar lost its label or clock: %q", row)
	}
	if !strings.Contains(row, "█") || !strings.Contains(row, "░") {
		t.Errorf("the track is not a bar: %q", row)
	}
}
