package tui

import (
	"strings"
	"testing"

	"charm.land/lipgloss/v2"

	"evilcode/internal/provider"
)

// TestRenderOverscrollFactsShowsMoreInfo pins the expanded elastic panel:
// identity (model + effort + thinking), context window, cache hit rate, and
// location, plus the countdown.
func TestRenderOverscrollFactsShowsMoreInfo(t *testing.T) {
	r := testRenderer(100)
	f := FactStack{
		Provider:    "deepseek",
		Auth:        "api",
		Model:       "deepseek-chat",
		Cwd:         "/home/user/proj",
		Branch:      "main",
		Used:        12000,
		Total:       200000,
		CacheRead:   8000,
		CacheWrite:  2000,
		CacheActive: true,
		Effort:      provider.ReasoningEffortHigh,
		Thinking:    ThinkingCurrent,
	}
	rows := plainLines(r.RenderOverscrollFacts(f, 1.2))
	joined := strings.Join(rows, "\n")
	for _, want := range []string{
		"deepseek-chat",
		"high",
		"thinking current",
		"deepseek",
		"ctx",
		"12.0k",
		"200k",
		"cache",
		"8.0k",
		"80% hit",
		"/home/user/proj",
		"main",
		"overscroll",
	} {
		if !strings.Contains(joined, want) {
			t.Errorf("overscroll panel missing %q:\n%s", want, joined)
		}
	}
}

// TestRenderOverscrollFactsCacheNA makes the absence intentional: providers
// without KV-cache reporting still get a dim row naming the reason.
func TestRenderOverscrollFactsCacheNA(t *testing.T) {
	r := testRenderer(100)
	rows := plainLines(r.RenderOverscrollFacts(FactStack{
		Provider: "ollama",
		Model:    "qwen",
		Total:    128000,
		Used:     1000,
	}, 1.0))
	joined := strings.Join(rows, "\n")
	if !strings.Contains(joined, "cache n/a") {
		t.Errorf("expected a cache n/a row:\n%s", joined)
	}
}

// TestRenderOverscrollFactsOmitsCountdownWithoutADwell: always mode shows
// the panel continuously, so a permanent "(overscroll 0.0s)" is noise.
func TestRenderOverscrollFactsOmitsCountdownWithoutADwell(t *testing.T) {
	r := testRenderer(80)
	rows := plainLines(r.RenderOverscrollFacts(FactStack{
		Provider: "deepseek",
		Model:    "deepseek-chat",
	}, 0))
	joined := strings.Join(rows, "\n")
	if strings.Contains(joined, "(overscroll") {
		t.Errorf("no dwell, yet a countdown rendered:\n%s", joined)
	}
	if !strings.Contains(joined, "deepseek-chat") {
		t.Errorf("identity lost when the countdown was dropped:\n%s", joined)
	}
}

// TestRenderOverscrollFactsKeepsTheCountdownWhenNarrow: on a narrow terminal
// the identity is elided before the countdown, which explains the panel
// itself.
func TestRenderOverscrollFactsKeepsTheCountdownWhenNarrow(t *testing.T) {
	r := testRenderer(30)
	rows := plainLines(r.RenderOverscrollFacts(FactStack{
		Provider: "deepseek",
		Auth:     "api",
		Model:    "deepseek-chat",
		Effort:   provider.ReasoningEffortHigh,
	}, 1.2))
	joined := strings.Join(rows, "\n")
	if !strings.Contains(joined, "(overscroll 1.2s)") {
		t.Errorf("countdown was cut before the identity:\n%s", joined)
	}
	for i, row := range rows {
		if lipgloss.Width(row) > 30 {
			t.Errorf("row %d is wider than the 30-cell terminal: %q", i, row)
		}
	}
}

// TestBackgroundFailureBlockSurvivesTheTickCache pins the cache exception:
// tickMsg deliberately keeps the settled transcript cache, so the failure
// block appended there must invalidate it or it never reaches the frame.
func TestBackgroundFailureBlockSurvivesTheTickCache(t *testing.T) {
	m := clickModel(nil, t.TempDir())
	m.blocks = append(m.blocks, Block{Kind: BlockNotice, Text: "seed"})
	m.View()
	if !m.transcriptCacheValid {
		t.Fatal("setup: the transcript cache did not settle")
	}
	m.bgDone.Store(&bgCompletion{Label: "probe", Failed: true})
	m.update(tickMsg{})
	m.View()
	if !strings.Contains(m.lastFrame, "✗ background: probe failed") {
		t.Fatal("the failure block appended during tickMsg never reached the frame")
	}
}

// TestFactStackCarriesOverscrollFields ensures the model feeds the panel.
func TestFactStackCarriesOverscrollFields(t *testing.T) {
	m := clickModel(nil, t.TempDir())
	m.header.Provider = "deepseek"
	m.header.Model = "deepseek-chat"
	m.cacheRead, m.cacheWrite = 8000, 2000
	m.ctxUsed = 12000
	m.thinking = ThinkingCurrent
	m.reasoningEffort = provider.ReasoningEffortHigh
	m.reasoningLevels = []provider.ReasoningEffort{provider.ReasoningEffortHigh}
	m.setReasoningEffort = func(provider.ReasoningEffort) error { return nil }

	f := m.factStack()
	if !f.CacheActive {
		t.Error("deepseek should report an active cache")
	}
	if f.CacheRead != 8000 || f.CacheWrite != 2000 {
		t.Errorf("cache = %d/%d, want 8000/2000", f.CacheRead, f.CacheWrite)
	}
	if f.Effort != provider.ReasoningEffortHigh {
		t.Errorf("effort = %q, want high", f.Effort)
	}
	if f.Thinking != ThinkingCurrent {
		t.Errorf("thinking = %q, want current", f.Thinking)
	}
}

// TestOverscrollReservesLayout ensures the reveal fits inside a full window
// instead of overflowing past it.
func TestOverscrollReservesLayout(t *testing.T) {
	m := clickModel(nil, t.TempDir())
	m.width, m.height = 100, 10
	for i := 0; i < 6; i++ {
		m.blocks = append(m.blocks, Block{Kind: BlockNotice, Text: "row"})
	}
	m.overscroll.Mode = OverscrollAlways
	// Pin to the bottom so the Always reveal shows.
	m.scroll.Offset = 0

	m.View()
	// overscrollHeight reads the per-frame paint cache View fills; a frame
	// with the reveal showing must have reserved rows.
	if h := m.overscrollHeight(); h == 0 {
		t.Fatal("overscrollHeight = 0, want reserved rows while visible")
	}
	rows := strings.Split(m.lastFrame, "\n")
	if len(rows) > m.height {
		t.Fatalf("frame has %d rows, want it to fit within %d", len(rows), m.height)
	}
	if joined := strings.Join(rows, "\n"); !strings.Contains(joined, "overscroll") {
		t.Fatalf("overscroll panel clipped from a full window:\n%s", joined)
	}
}
