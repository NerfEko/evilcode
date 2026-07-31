package tui

import (
	"strings"
	"testing"
	"time"

	"charm.land/lipgloss/v2"

	"evilcode/internal/memory"
	"evilcode/internal/session"
)

func sessionInfo(name, title string) session.Info {
	return session.Info{Name: name, Title: title, Messages: 12, Modified: time.Now()}
}

func memHit(text string, kind memory.Kind) memory.Hit {
	return memory.Hit{Record: memory.Record{Text: text, Kind: kind}, Score: 0.8}
}

func TestMemoryTileNamesWhatWasInjected(t *testing.T) {
	// The tile lists the memories rather than only counting them. An injection
	// the user cannot read is one they cannot notice is wrong, which is the
	// failure mode a memory bank actually has (plan.md §9.5).
	r := testRenderer(80)
	rows := plainLines(r.RenderMemoryTile([]memory.Hit{
		memHit("the user prefers tabs", memory.KindPreference),
		memHit("the build uses make release", memory.KindProject),
	}))
	joined := strings.Join(rows, "\n")

	if !strings.Contains(joined, "recalled 2 memories") {
		t.Errorf("header missing the count:\n%s", joined)
	}
	if !strings.Contains(joined, "prefers tabs") || !strings.Contains(joined, "make release") {
		t.Errorf("the tile hid what it injected:\n%s", joined)
	}
	if !strings.Contains(joined, "tok") {
		t.Error("the tile should say what recall cost")
	}
}

func TestMemoryTileSingularForOneMemory(t *testing.T) {
	r := testRenderer(80)
	rows := plainLines(r.RenderMemoryTile([]memory.Hit{memHit("x", memory.KindFact)}))
	if !strings.Contains(rows[1], "recalled 1 memory") {
		t.Errorf("row = %q, want a singular noun", rows[1])
	}
}

func TestMemoryTileEmptyRendersNothing(t *testing.T) {
	r := testRenderer(80)
	if rows := r.RenderMemoryTile(nil); rows != nil {
		t.Errorf("an empty recall drew %d rows", len(rows))
	}
}

func TestMemoryTileIsRectangular(t *testing.T) {
	r := testRenderer(80)
	rows := plainLines(r.RenderMemoryTile([]memory.Hit{
		memHit("short", memory.KindFact),
		memHit(strings.Repeat("a much longer memory ", 8), memory.KindFact),
	}))
	// Cells, not runes: the header carries an emoji, which is one rune and two
	// columns. Counting runes here is how a box that lines up on screen fails
	// its own test.
	width := lipgloss.Width(rows[0])
	for i, row := range rows {
		if got := lipgloss.Width(row); got != width {
			t.Errorf("row %d is %d cells, want %d: %q", i, got, width, row)
		}
	}
}

func TestMemoryWidgetIsAbsentWhenIdle(t *testing.T) {
	// §8.3: a permanently docked box reading "idle" is clutter. Nothing to
	// report means nothing on screen.
	r := testRenderer(80)
	w := r.MemoryActivityWidget(memory.Activity{Stage: memory.StageIdle}, 0)
	if len(w.Lines) != 0 {
		t.Errorf("an idle pipeline drew %d lines", len(w.Lines))
	}
}

func TestMemoryWidgetShowsTheFourStepPipeline(t *testing.T) {
	r := testRenderer(80)
	w := r.MemoryActivityWidget(memory.Activity{
		Stage:      memory.StageCheck,
		Candidates: 12,
		Relevant:   4,
		Tokens:     820,
		Saved:      1,
		Since:      time.Now(),
	}, 3)
	joined := strings.Join(plainLines(w.Lines), "\n")

	for _, want := range []string{
		"Find matches", "Check relevance", "Inject context", "Update memory",
		"12 candidates", "4 above threshold", "820 tok", "1 saved",
	} {
		if !strings.Contains(joined, want) {
			t.Errorf("widget is missing %q:\n%s", want, joined)
		}
	}
	// The bracket runs top to bottom exactly once (plan.md §8.8).
	for _, want := range []string{"╭", "├", "╰"} {
		if !strings.Contains(joined, want) {
			t.Errorf("bracket glyph %q missing:\n%s", want, joined)
		}
	}
	if !strings.Contains(joined, "checking relevance") {
		t.Errorf("the status line should name the live step:\n%s", joined)
	}
}

func TestMemoryWidgetSurfacesAFailedEmbedder(t *testing.T) {
	// Silence here is indistinguishable from a bank that had nothing to say,
	// so a broken embedder is the one thing that shows while otherwise idle.
	r := testRenderer(80)
	w := r.MemoryActivityWidget(memory.Activity{
		Stage:  memory.StageIdle,
		Failed: "connection refused",
	}, 0)
	if len(w.Lines) == 0 {
		t.Fatal("a failed embedder drew nothing")
	}
	if !strings.Contains(plainLines(w.Lines)[0], "unavailable") {
		t.Errorf("header = %q", plainLines(w.Lines)[0])
	}
}

func TestMemoryWidgetIsRectangularAndFits(t *testing.T) {
	r := testRenderer(80)
	w := r.MemoryActivityWidget(memory.Activity{
		Stage: memory.StageFind, Candidates: 120, Relevant: 4, Tokens: 12000, Saved: 33,
	}, 9)
	if w.Width() > WidgetMaxWidth {
		t.Errorf("widget is %d wide, over the %d cap", w.Width(), WidgetMaxWidth)
	}
	rows := plainLines(r.RenderWidget(w))
	width := lipgloss.Width(rows[0])
	for i, row := range rows {
		if got := lipgloss.Width(row); got != width {
			t.Errorf("row %d is %d cells, want %d: %q", i, got, width, row)
		}
	}
}

func TestSessionPickerFallsBackToMemory(t *testing.T) {
	// Session RAG (plan.md §19): a filter that matches no name still finds the
	// session whose remembered summary matches.
	s := SessionPickerState{
		Rows: []SessionRow{
			{Info: sessionInfo("bat", "wiring the dock")},
			{Info: sessionInfo("crypt", "palette work")},
		},
		Filter:   "auth redirect loop",
		Semantic: map[string]string{"crypt": "fixed the auth redirect loop"},
	}
	got := s.Filtered()
	if len(got) != 1 {
		t.Fatalf("filtered to %d rows, want the semantic match", len(got))
	}
	if got[0].Info.Name != "crypt" {
		t.Errorf("matched %q", got[0].Info.Name)
	}
	if got[0].Recalled == "" {
		t.Error("a semantically matched row must say why it is there")
	}
}

func TestSessionPickerPrefersLiteralMatches(t *testing.T) {
	// Typing a name must behave like typing a name. Semantic results only fill
	// in when the literal filter comes up empty.
	s := SessionPickerState{
		Rows: []SessionRow{
			{Info: sessionInfo("bat", "wiring the dock")},
			{Info: sessionInfo("crypt", "palette work")},
		},
		Filter:   "bat",
		Semantic: map[string]string{"crypt": "fixed the auth redirect loop"},
	}
	got := s.Filtered()
	if len(got) != 1 || got[0].Info.Name != "bat" {
		t.Fatalf("filtered to %v, want only the literal match", got)
	}
	if got[0].Recalled != "" {
		t.Error("a literal match should not be labelled as recalled")
	}
}
