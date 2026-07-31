package tui

import (
	"strings"
	"testing"

	"evilcode/internal/session"
)

func TestHelpCoversEveryVisibleCommand(t *testing.T) {
	// The sections are hand-curated for readability, which risks drift. The
	// leftovers are computed and shown, so a newly registered command can
	// never be invisible (plan.md §5.5).
	r := testRenderer(120)
	// A tall viewport so nothing is scrolled out of the assertion.
	joined := strings.Join(plainLines(r.RenderHelp(0, 120, 400)), "\n")
	for _, c := range VisibleCommands() {
		if !strings.Contains(joined, "/"+c.Name) {
			t.Errorf("help does not show /%s", c.Name)
		}
	}
}

func TestHelpSectionsNameRealCommands(t *testing.T) {
	for _, sec := range HelpSections {
		for _, n := range sec.Names {
			if _, ok := FindCommand(n); !ok {
				t.Errorf("section %q lists /%s, which is not registered", sec.Title, n)
			}
		}
	}
}

func TestHelpSectionsHaveNoDuplicates(t *testing.T) {
	seen := map[string]string{}
	for _, sec := range HelpSections {
		for _, n := range sec.Names {
			if prev, dup := seen[n]; dup {
				t.Errorf("/%s appears in both %q and %q", n, prev, sec.Title)
			}
			seen[n] = sec.Title
		}
	}
}

func TestHelpScrollPercentInTitle(t *testing.T) {
	r := testRenderer(80)
	// A short viewport guarantees something to scroll.
	top := plainLines(r.RenderHelp(0, 80, 12))
	if !strings.Contains(top[0], "Help  0%") {
		t.Errorf("title = %q, want 0%%", top[0])
	}
	bottom := plainLines(r.RenderHelp(10_000, 80, 12))
	if !strings.Contains(bottom[0], "100%") {
		t.Errorf("title = %q, want 100%% when scrolled to the end", bottom[0])
	}
}

func TestHelpBoxIsRectangular(t *testing.T) {
	r := testRenderer(80)
	rows := plainLines(r.RenderHelp(0, 80, 20))
	width := len([]rune(rows[0]))
	for i, row := range rows {
		if got := len([]rune(row)); got != width {
			t.Errorf("row %d is %d cells, want %d: %q", i, got, width, row)
		}
	}
}

func TestHelpFooterNamesItsKeys(t *testing.T) {
	r := testRenderer(100)
	rows := plainLines(r.RenderHelp(0, 100, 20))
	last := rows[len(rows)-1]
	for _, want := range []string{"Esc to close", "scroll", "/help <cmd>"} {
		if !strings.Contains(last, want) {
			t.Errorf("footer %q is missing %q", last, want)
		}
	}
}

func TestHistorySearchLifecycle(t *testing.T) {
	var h HistorySearch
	h.Open("my draft", 3)
	if !h.Active {
		t.Fatal("search should be active")
	}

	h.Matches = []string{"first", "second", "third"}
	if got := h.Current(); got != "first" {
		t.Errorf("Current() = %q, want the top match", got)
	}
	h.Move(1)
	if got := h.Current(); got != "second" {
		t.Errorf("Current() = %q", got)
	}
	// Movement clamps rather than wrapping: a history search that wraps to the
	// newest entry when you hold Up is disorienting.
	h.Move(100)
	if got := h.Current(); got != "third" {
		t.Errorf("Current() = %q, want it clamped to the last match", got)
	}
	h.Move(-100)
	if got := h.Current(); got != "first" {
		t.Errorf("Current() = %q, want it clamped to the first match", got)
	}

	draft, cursor := h.Close()
	if draft != "my draft" || cursor != 3 {
		t.Errorf("Close() = %q, %d; cancelling must restore the exact draft", draft, cursor)
	}
	if h.Active {
		t.Error("search should be closed")
	}
}

func TestHistorySearchRendering(t *testing.T) {
	r := testRenderer(80)

	var h HistorySearch
	h.Open("", 0)

	// An empty query matches nothing, as readline does.
	rows := plainLines(r.RenderHistorySearch(&h))
	if !strings.Contains(strings.Join(rows, "\n"), "type to search history") {
		t.Errorf("empty query should prompt:\n%v", rows)
	}

	h.Query = "auth"
	rows = plainLines(r.RenderHistorySearch(&h))
	if !strings.Contains(rows[0], "(history search) auth") {
		t.Errorf("header = %q", rows[0])
	}
	if !strings.Contains(strings.Join(rows, "\n"), "no matches") {
		t.Error("a query with no results should say so")
	}

	h.Matches = []string{"fix the auth redirect loop", "fix the auth token refresh"}
	rows = plainLines(r.RenderHistorySearch(&h))
	joined := strings.Join(rows, "\n")
	if !strings.Contains(joined, "▸ fix the auth redirect loop") {
		t.Errorf("selection marker missing:\n%s", joined)
	}
	if !strings.Contains(joined, "↵ insert") {
		t.Error("hints missing")
	}
}

func TestHistorySearchInactiveRendersNothing(t *testing.T) {
	r := testRenderer(80)
	if rows := r.RenderHistorySearch(&HistorySearch{}); rows != nil {
		t.Errorf("an inactive search rendered %d rows", len(rows))
	}
}

func TestHistorySearchWindowsLongResults(t *testing.T) {
	r := testRenderer(80)
	var h HistorySearch
	h.Open("", 0)
	h.Query = "x"
	for i := 0; i < 30; i++ {
		h.Matches = append(h.Matches, "prompt "+string(rune('a'+i)))
	}
	rows := plainLines(r.RenderHistorySearch(&h))
	// header + window + overflow hint
	if len(rows) > HistoryRows+2 {
		t.Errorf("rendered %d rows, want the window of %d", len(rows), HistoryRows)
	}
	if !strings.Contains(rows[len(rows)-1], "more") {
		t.Errorf("last row = %q, want an overflow hint", rows[len(rows)-1])
	}
}

func TestHistorySearchIsCrossSession(t *testing.T) {
	// Recall must reach prompts from earlier sessions (plan.md §5.2).
	dir := t.TempDir()
	first, err := session.OpenHistory(dir)
	if err != nil {
		t.Fatal(err)
	}
	first.Add("fix the auth redirect loop")

	second, err := session.OpenHistory(dir)
	if err != nil {
		t.Fatal(err)
	}
	if got := second.Search("auth", 10); len(got) != 1 {
		t.Errorf("search = %v, want the earlier session's prompt", got)
	}
}

func TestSpliceOverlayCoversWithoutGrowing(t *testing.T) {
	// The invariant: splicing over a frame that already fills the screen must
	// not add rows, or the transcript scrolls.
	rows := make([]string, 20)
	for i := range rows {
		rows[i] = "content"
	}
	overlay := []string{"a", "b", "c"}

	got := spliceOverlay(rows, overlay, 20, 2)
	if len(got) != 20 {
		t.Errorf("rows = %d, want the frame height unchanged", len(got))
	}
	// It flipped above the composer, so the last two rows are still content.
	if got[19] != "content" || got[18] != "content" {
		t.Errorf("the composer rows were overwritten: %q, %q", got[18], got[19])
	}
	joined := strings.Join(got, "\n")
	for _, line := range overlay {
		if !strings.Contains(joined, line) {
			t.Errorf("overlay row %q was not drawn", line)
		}
	}
}

func TestSpliceOverlayUsesRoomBelow(t *testing.T) {
	rows := make([]string, 5)
	for i := range rows {
		rows[i] = "content"
	}
	got := spliceOverlay(rows, []string{"a", "b"}, 40, 2)
	if len(got) != 7 {
		t.Errorf("rows = %d, want it to grow into the blank rows below", len(got))
	}
	if got[5] != "a" || got[6] != "b" {
		t.Errorf("overlay landed at %q, %q", got[5], got[6])
	}
}

func TestSpliceOverlayEmpty(t *testing.T) {
	rows := []string{"a", "b"}
	if got := spliceOverlay(rows, nil, 40, 1); len(got) != 2 {
		t.Errorf("an empty overlay changed the frame: %v", got)
	}
}

func TestDrainPendingForEditOrdersByDelivery(t *testing.T) {
	// Retrieval returns text in the order it would have reached the model.
	pending := []PendingMessage{
		{Kind: PendingQueued, Text: "third"},
		{Kind: PendingSent, Text: "first"},
		{Kind: PendingInterleave, Text: "second"},
	}
	got := drainPendingForEdit(pending)
	want := []string{"first", "second", "third"}
	for i := range want {
		if got[i].Text != want[i] {
			t.Fatalf("order = %v, want %v", texts(got), want)
		}
	}
}

func texts(msgs []PendingMessage) []string {
	out := make([]string, len(msgs))
	for i, m := range msgs {
		out[i] = m.Text
	}
	return out
}
