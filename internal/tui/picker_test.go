package tui

import (
	"strings"
	"testing"

	"evilcode/internal/agent"
	"evilcode/internal/provider"
)

func sampleEntries() []ModelEntry {
	return []ModelEntry{
		{Name: "qwen3-coder:480b-cloud", Provider: "ollama-cloud", Via: "api-key", Current: true},
		{Name: "deepseek-v3.1:671b-cloud", Provider: "ollama-cloud", Via: "api-key", Recommended: true},
		{Name: "gpt-oss:120b-cloud", Provider: "ollama-cloud", Via: "api-key", Favorite: true},
		{Name: "llama2:7b", Provider: "ollama-local", Via: "local", Old: true},
		{Name: "gone:1b", Provider: "ollama-local", Unavailable: true, Detail: "not pulled"},
	}
}

func TestPickerHintLivesOutsideTheBox(t *testing.T) {
	// §5.3 is specific: the key hints describe what the box does, so they sit
	// above it rather than inside its chrome.
	r := testRenderer(120)
	rows := plainLines(r.RenderPicker(PickerState{Entries: sampleEntries()}))
	if !strings.Contains(rows[0], "Ctrl+N favorite") {
		t.Errorf("first row = %q, want the key hints", rows[0])
	}
	if strings.HasPrefix(rows[0], "╭") || strings.Contains(rows[0], "│") {
		t.Errorf("the hint row must not be inside the box: %q", rows[0])
	}
	if !strings.HasPrefix(rows[1], "╭") {
		t.Errorf("row 1 = %q, want the box top", rows[1])
	}
	if !strings.HasPrefix(rows[len(rows)-1], "╰") {
		t.Errorf("last row = %q, want the box bottom", rows[len(rows)-1])
	}
}

func TestPickerBoxIsRectangular(t *testing.T) {
	// A ragged right border is the classic box-drawing bug.
	r := testRenderer(120)
	rows := plainLines(r.RenderPicker(PickerState{Entries: sampleEntries()}))
	box := rows[1:]
	width := len([]rune(box[0]))
	for i, row := range box {
		if got := len([]rune(row)); got != width {
			t.Errorf("box row %d is %d cells, want %d:\n%q", i, got, width, row)
		}
	}
}

func TestPickerShowsCounts(t *testing.T) {
	r := testRenderer(120)
	joined := strings.Join(plainLines(r.RenderPicker(PickerState{Entries: sampleEntries()})), "\n")
	if !strings.Contains(joined, "(5/5)") {
		t.Errorf("header should show the filtered/total count:\n%s", joined)
	}
}

func TestPickerFilters(t *testing.T) {
	s := PickerState{Entries: sampleEntries(), Filter: "cloud"}
	got, _ := s.Filtered()
	if len(got) != 3 {
		t.Fatalf("filtered to %d entries, want the 3 cloud models", len(got))
	}
	for _, e := range got {
		if !strings.Contains(e.Name, "cloud") {
			t.Errorf("unexpected match %q", e.Name)
		}
	}
}

func TestPickerEmptyFilterResult(t *testing.T) {
	r := testRenderer(120)
	joined := strings.Join(plainLines(r.RenderPicker(
		PickerState{Entries: sampleEntries(), Filter: "zzzz"})), "\n")
	if !strings.Contains(joined, "no matches") {
		t.Errorf("want a no-matches message:\n%s", joined)
	}
}

func TestPickerMarkerGutter(t *testing.T) {
	r := testRenderer(120)
	rows := plainLines(r.RenderPicker(PickerState{Entries: sampleEntries(), Selected: 0}))
	joined := strings.Join(rows, "\n")
	if !strings.Contains(joined, "▸") {
		t.Error("the selected row needs its cursor marker")
	}
	if !strings.Contains(joined, "×") {
		t.Error("an unavailable entry needs its × marker")
	}
}

func TestPickerNameSuffixes(t *testing.T) {
	r := testRenderer(120)
	joined := strings.Join(plainLines(r.RenderPicker(PickerState{Entries: sampleEntries()})), "\n")
	for _, want := range []string{"♥", "★", "old"} {
		if !strings.Contains(joined, want) {
			t.Errorf("missing the %q suffix:\n%s", want, joined)
		}
	}
}

func TestPickerNoticeForUnavailableSelection(t *testing.T) {
	entries := sampleEntries()
	r := testRenderer(120)
	// Select the unavailable entry.
	joined := strings.Join(plainLines(r.RenderPicker(
		PickerState{Entries: entries, Selected: len(entries) - 1})), "\n")
	if !strings.Contains(joined, "unavailable") || !strings.Contains(joined, "not pulled") {
		t.Errorf("want a caveat line for the unavailable selection:\n%s", joined)
	}
}

func TestPickerKeepsSelectionCentered(t *testing.T) {
	// A selection pinned to an edge gives no sense of position in a long list.
	var entries []ModelEntry
	for i := 0; i < 40; i++ {
		entries = append(entries, ModelEntry{Name: "model-" + string(rune('a'+i%26)) + string(rune('0'+i/26))})
	}
	r := testRenderer(120)
	rows := plainLines(r.RenderPicker(PickerState{Entries: entries, Selected: 20, Height: 10}))

	selIdx := -1
	for i, row := range rows {
		if strings.Contains(row, "▸") {
			selIdx = i
		}
	}
	if selIdx < 0 {
		t.Fatal("selection not rendered")
	}
	// The selection should be near the middle of the box, not at an edge.
	body := len(rows) - 4 // hint, top border, header, bottom border
	rel := selIdx - 3
	if rel < body/2-2 || rel > body/2+2 {
		t.Errorf("selection at row %d of %d body rows, want it centered", rel, body)
	}
}

func TestPickerFocusedColumn(t *testing.T) {
	r := testRenderer(120)
	for _, col := range []PickerColumn{ColModel, ColProvider, ColVia} {
		rows := r.RenderPicker(PickerState{Entries: sampleEntries(), Column: col})
		header := rows[2] // hint, top border, header
		// The focused column is accent-bold; at least one bold run must exist.
		if !strings.Contains(header, "\x1b[1;") && !strings.Contains(header, ";1m") {
			t.Errorf("column %d: header has no emphasized column:\n%q", col, header)
		}
	}
}

func TestPickerUsesUnderlineForFilterMatches(t *testing.T) {
	// Deliberately different from the palette's recolor: the picker's rows
	// already carry meaning in their color, so underline is the mark left.
	r := testRenderer(120)
	rows := r.RenderPicker(PickerState{Entries: sampleEntries(), Filter: "qwen"})
	joined := strings.Join(rows, "\n")
	if !strings.Contains(joined, "\x1b[4m") && !strings.Contains(joined, ";4m") {
		t.Errorf("filter matches should be underlined here:\n%q", joined)
	}
}

func TestBoxTitledCentersItsTitle(t *testing.T) {
	r := testRenderer(80)
	rows := plainLines(r.BoxTitled("⛭ Wire the auth flow", []string{"a plan body"}, "#9e87ff"))
	if !strings.Contains(rows[0], "⛭ Wire the auth flow") {
		t.Errorf("title should sit in the top border: %q", rows[0])
	}
	if !strings.HasPrefix(rows[0], "╭") || !strings.HasSuffix(rows[0], "╮") {
		t.Errorf("top border = %q", rows[0])
	}
	width := len([]rune(rows[0]))
	for i, row := range rows {
		if got := len([]rune(row)); got != width {
			t.Errorf("row %d is %d cells, want %d: %q", i, got, width, row)
		}
	}
}

func TestBoxTitledFitsLongTitles(t *testing.T) {
	r := testRenderer(40)
	rows := plainLines(r.BoxTitled(strings.Repeat("x", 100), []string{"body"}, "#9e87ff"))
	for i, row := range rows {
		if len([]rune(row)) > 40 {
			t.Errorf("row %d overflows the renderer width: %d cells", i, len([]rune(row)))
		}
	}
}

// TestPickerFavoritePersists drives the Ctrl+N binding the way a keypress does
// and checks that the saved prefs land in the config saver, the in-memory
// state, and the row marks the picker renders. The old Ctrl+O default-setter
// is gone: there is no picker-set default, a new session starts on last_model.
func TestPickerFavoritePersists(t *testing.T) {
	var savedFavs []string
	m := NewModel(nil, HeaderState{Model: "qwen3-coder:480b-cloud", Provider: "ollama-cloud"})
	m.pickerOpen = true
	m.picker.Entries = []ModelEntry{
		{Name: "qwen3-coder:480b-cloud", Provider: "ollama-cloud", Current: true},
		{Name: "deepseek-chat", Provider: "deepseek"},
	}
	m.models = m.picker.Entries
	m.picker.Selected = 1
	m.saveModelPrefs = func(favorites []string) error {
		savedFavs = append([]string(nil), favorites...)
		return nil
	}

	if _, _ = m.handlePickerKey("ctrl+n"); len(savedFavs) != 1 || savedFavs[0] != "deepseek-chat@deepseek" {
		t.Errorf("saved favorites = %v, want the pinned ref", savedFavs)
	}
	if !m.picker.Entries[1].Favorite {
		t.Error("the favorited row should carry the ♥ mark")
	}
	if m.notice != "♥ deepseek-chat favorited" {
		t.Errorf("notice = %q, want a favorite confirmation", m.notice)
	}

	// Toggling again must unpin and persist the removal.
	if _, _ = m.handlePickerKey("ctrl+n"); len(savedFavs) != 0 {
		t.Errorf("saved favorites after unpin = %v, want none", savedFavs)
	}
	if m.picker.Entries[1].Favorite {
		t.Error("the row should lose its ♥ mark after unpinning")
	}
}

// TestPickerShiftTabCyclesFavorites checks the global favorite cycle: the
// active model steps through the pinned refs and wraps around the end.
func TestPickerShiftTabCyclesFavorites(t *testing.T) {
	a := agent.New("s", provider.NewMock("mock", "chat"), "m1", nil,
		agent.NewConversation("system"))
	t.Cleanup(a.Close)
	m := NewModel(a, HeaderState{Model: "m1", Provider: "mock"})
	m.models = []ModelEntry{
		{Name: "m1", Provider: "mock"},
		{Name: "m2", Provider: "mock"},
		{Name: "m3", Provider: "mock"},
	}
	m.favorites = []string{"m2@mock", "m3@mock"}
	m.pickerOpen = true
	m.picker.Entries = m.models

	for _, want := range []string{"m2", "m3", "m2"} {
		if _, _ = m.handlePickerKey("shift+tab"); m.header.Model != want {
			t.Fatalf("header.Model = %q, want %q", m.header.Model, want)
		}
	}

	// With no favorites the cycle says so instead of doing nothing silently.
	m.favorites = nil
	if _, _ = m.handlePickerKey("shift+tab"); m.header.Model != "m2" {
		t.Errorf("no-op cycle must not move the active model")
	}
	if !strings.Contains(m.notice, "no favorites") {
		t.Errorf("notice = %q, want a no-favorites hint", m.notice)
	}
}
