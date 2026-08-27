package tui

import (
	"strings"
	"testing"

	"evilcode/internal/agent"
	"evilcode/internal/core"
)

// R2-05 (root cause of the tui-todos/tui-widgets probe failures): a boundary
// notice between two model rounds used to clear streamingIdx without freezing
// the streamed block. The block stayed flagged Streaming with no index, so
// hasStreamingBlock went false, a settled frame cached the pre-notice paint,
// and the early transcript-cache hit — which did not re-check for a streaming
// block — served that stale frame on every tick for the rest of the session.
// In the probe, the auto-poke boundary notice cut the round's streamed answer
// in half and the pane never recovered.

// TestNoticeBoundaryFreezesTheStreamedBlock pins the freeze: after a notice
// lands between two streamed rounds, the first block is finished (not orphaned)
// and the round's remaining deltas open their own block.
func TestNoticeBoundaryFreezesTheStreamedBlock(t *testing.T) {
	m := newTestModel(t)
	m.applyEventBatch([]agent.Event{
		{Kind: agent.EventTurnStart},
		{Kind: agent.EventTextDelta, Text: "Tracked. Working through the "},
		{Kind: agent.EventNotice, Level: agent.LevelInfo, Text: "poked"},
		{Kind: agent.EventTextDelta, Text: "refresh path next."},
		{Kind: agent.EventTurnEnd},
	})

	if len(m.blocks) < 2 {
		t.Fatalf("blocks = %d, want the streamed block and the post-notice block", len(m.blocks))
	}
	if got := m.blocks[0].Text; got != "Tracked. Working through the " {
		t.Errorf("first streamed block = %q, want everything before the notice", got)
	}
	if m.blocks[0].Streaming {
		t.Error("the streamed block was left flagged Streaming after the boundary notice")
	}
	if got := m.blocks[1].Text; got != "refresh path next." {
		t.Errorf("post-notice block = %q, want the round's remaining words", got)
	}
}

// The orphaned-block sweep: whichever block was streaming when the boundary
// hit must not stay flagged Streaming after the turn ends, or the settled
// paint cache freezes on its last live frame forever.
func TestTurnEndFreezesOrphanedStreamBlocks(t *testing.T) {
	m := newTestModel(t)
	m.applyEvent(agent.Event{Kind: agent.EventTurnStart})
	m.applyEvent(agent.Event{Kind: agent.EventTextDelta, Text: "first"})
	// Simulate the old index-only notice clear: the block loses its owner.
	m.streamingIdx = -1
	m.applyEvent(agent.Event{Kind: agent.EventTextDelta, Text: "second"})
	m.applyEvent(agent.Event{Kind: agent.EventTurnEnd})

	for i, b := range m.blocks {
		if b.Streaming {
			t.Errorf("block %d is still flagged Streaming after the turn ended", i)
		}
	}
	// The pre-boundary block keeps its streamed words and renders settled.
	found := false
	for _, b := range m.blocks {
		if b.Kind == BlockAssistant && strings.Contains(b.Text, "first") {
			found = true
		}
	}
	if !found {
		t.Fatalf("the streamed block is gone: %+v", m.blocks)
	}
}

// The settled transcript cache must not be served while a block is streaming —
// the guard the early hit's own comment always claimed.
func TestSettledCacheIsNotServedPastAStreamedBlock(t *testing.T) {
	m := newTestModel(t)
	m.width, m.height = 100, 40
	m.applyEventBatch([]agent.Event{
		{Kind: agent.EventTextDelta, Text: "settled answer"},
		{Kind: agent.EventTurnEnd},
	})
	_ = m.transcriptLines() // caches the settled rows
	if !m.transcriptCacheValid {
		t.Fatal("setup: the settled transcript was not cached")
	}

	m.applyEvent(agent.Event{Kind: agent.EventTurnStart})
	m.applyEvent(agent.Event{Kind: agent.EventTextDelta, Text: "streaming now"})
	rows := m.transcriptLines()
	for _, ln := range rows.Lines {
		plain := core.SanitizeTerminal(ln)
		if strings.Contains(plain, "streaming now") {
			return // the fresh text rendered
		}
	}
	t.Fatalf("the streamed block never rendered past the settled cache")
}
