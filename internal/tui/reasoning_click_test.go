package tui

import (
	"testing"

	tea "charm.land/bubbletea/v2"
	"evilcode/internal/theme"
)

// TestClickCollapsedReasoningExpands clicks the "▸ thought (N lines)" summary
// row and expects the trace to open. A second click on the now-expanded trace
// folds it back up.
func TestClickCollapsedReasoningExpandsThenCollapses(t *testing.T) {
	m := &Model{
		renderer:   NewRenderer(theme.Dracula(), 79),
		width:      80,
		height:     20,
		blocks:     []Block{{Kind: BlockReasoning, Text: "line one\nline two\nline three", Collapsed: true}},
		cwd:        t.TempDir(),
		panelRatio: 50,
	}

	row := ownerRow(t, m, 0)

	// First click: the collapsed summary expands.
	if !m.toggleReasoningAt(tea.Mouse{X: 2, Y: row}) {
		t.Fatal("click on the collapsed thought did not register as a toggle")
	}
	if m.blocks[0].Collapsed {
		t.Fatal("collapsed thought stayed collapsed after a click")
	}

	// The expanded trace now owns several rows; any of them collapses it.
	row = ownerRow(t, m, 0)
	if !m.toggleReasoningAt(tea.Mouse{X: 2, Y: row}) {
		t.Fatal("click on the expanded thought did not register as a toggle")
	}
	if !m.blocks[0].Collapsed {
		t.Fatal("expanded thought stayed open after a second click")
	}
}

// TestClickStreamingReasoningDoesNotToggle guards the live path: finishReasoning
// re-asserts the collapsed state at turn end, so a manual fold mid-stream would
// be undone a moment later. A streaming trace ignores the click.
func TestClickStreamingReasoningDoesNotToggle(t *testing.T) {
	m := &Model{
		renderer:   NewRenderer(theme.Dracula(), 79),
		width:      80,
		height:     20,
		blocks:     []Block{{Kind: BlockReasoning, Text: "thinking…", Streaming: true}},
		cwd:        t.TempDir(),
		panelRatio: 50,
	}
	row := ownerRow(t, m, 0)

	if m.toggleReasoningAt(tea.Mouse{X: 2, Y: row}) {
		t.Fatal("a streaming trace was toggled by a click")
	}
	if m.blocks[0].Streaming != true {
		t.Fatal("the streaming flag was disturbed")
	}
}

// TestReasoningClickRoutesThroughUpdate confirms the full mouse path lands on
// the toggle, not on the tool quick-view beside it.
func TestReasoningClickRoutesThroughUpdate(t *testing.T) {
	m := &Model{
		renderer:   NewRenderer(theme.Dracula(), 79),
		width:      80,
		height:     20,
		blocks:     []Block{{Kind: BlockReasoning, Text: "a thought", Collapsed: true}},
		cwd:        t.TempDir(),
		panelRatio: 50,
	}
	row := ownerRow(t, m, 0)

	got, _ := m.Update(tea.MouseClickMsg{X: 2, Y: row, Button: tea.MouseLeft})
	if got.(*Model).blocks[0].Collapsed {
		t.Fatal("Update did not expand the collapsed thought on click")
	}
}
