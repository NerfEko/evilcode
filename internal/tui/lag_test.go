package tui

import (
	"fmt"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"evilcode/internal/agent"
	"evilcode/internal/theme"
)

func TestApplyEventBatchPreservesOrderAndMergesDeltas(t *testing.T) {
	m := newTestModel(t)
	m.applyEventBatch([]agent.Event{
		{Kind: agent.EventTextDelta, Text: "one"},
		{Kind: agent.EventTextDelta, Text: " two"},
		{Kind: agent.EventNotice, Level: agent.LevelWarning, Text: "boundary"},
		{Kind: agent.EventTextDelta, Text: "three"},
	})

	if len(m.blocks) != 3 {
		t.Fatalf("blocks = %d, want assistant, notice, assistant", len(m.blocks))
	}
	if got := m.blocks[0].Text; got != "one two" {
		t.Errorf("first streamed block = %q", got)
	}
	if got := m.blocks[1].Text; got != "boundary" {
		t.Errorf("notice = %q", got)
	}
	if got := m.blocks[2].Text; got != "three" {
		t.Errorf("second streamed block = %q", got)
	}
}

func TestStreamingDeltaAppendDoesNotCopyTheWholePrefix(t *testing.T) {
	m := newTestModel(t)
	for i := 0; i < 256; i++ {
		m.applyEvent(agent.Event{Kind: agent.EventTextDelta, Text: "chunk "})
	}
	if m.streamingIdx < 0 || !strings.HasPrefix(m.blocks[m.streamingIdx].Text, "chunk chunk") {
		t.Fatal("streamed text was not accumulated")
	}
	if m.streamBuilder == nil || m.streamBuilderIdx != m.streamingIdx {
		t.Fatal("stream builder was not retained for the live block")
	}
}

func TestFinishedStreamReleasesLivePaintCache(t *testing.T) {
	m := newTestModel(t)
	m.width, m.height = 80, 24
	m.applyEvent(agent.Event{Kind: agent.EventTextDelta, Text: "live answer"})
	m.View()
	idx := m.streamingIdx
	if idx < 0 || !m.blocks[idx].streamCache.valid {
		t.Fatal("live paint cache was not populated")
	}
	m.finishStreaming()
	if m.blocks[idx].streamCache.valid {
		t.Fatal("finished block retained the live paint cache")
	}
}

func TestMarkdownCacheStaysBoundedByEntriesAndBytes(t *testing.T) {
	md := NewMarkdown(80, theme.Dracula().Prose)
	for i := 0; i < maxMarkdownCacheEntries+32; i++ {
		md.Render(fmt.Sprintf("unique message %d %s", i, strings.Repeat("x", 64)), true)
	}
	if len(md.cache) > maxMarkdownCacheEntries {
		t.Fatalf("markdown cache has %d entries, limit %d", len(md.cache), maxMarkdownCacheEntries)
	}
	if md.cacheBytes > maxMarkdownCacheBytes {
		t.Fatalf("markdown cache has %d bytes, limit %d", md.cacheBytes, maxMarkdownCacheBytes)
	}
}

func TestComposerEditsKeepSettledTranscriptCache(t *testing.T) {
	m := perfModelForLagTest(t, 120)
	m.View()
	if !m.transcriptCacheValid {
		t.Fatal("setup did not populate transcript cache")
	}
	m.update(tea.KeyPressMsg(tea.Key{Code: 'x', Text: "x"}))
	if !m.transcriptCacheValid {
		t.Fatal("typing invalidated the settled transcript cache")
	}
}

func perfModelForLagTest(t *testing.T, turns int) *Model {
	t.Helper()
	m := NewModel(nil, HeaderState{Model: "mock", SessionName: "s", Provider: "mock"})
	m.width, m.height = 140, 45
	m.applyWrapWidth()
	for i := 0; i < turns; i++ {
		m.blocks = append(m.blocks,
			Block{Kind: BlockUser, Text: fmt.Sprintf("question %d", i)},
			Block{Kind: BlockAssistant, Text: "a settled answer"},
		)
	}
	m.invalidateTranscriptCache()
	return m
}
