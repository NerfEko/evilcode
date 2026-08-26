package tui

import (
	"testing"

	"evilcode/internal/theme"
)

func panelEqual(a, b PanelContent) bool {
	if a.Title != b.Title || a.Path != b.Path || a.Diff != b.Diff || len(a.Body) != len(b.Body) {
		return false
	}
	for i := range a.Body {
		if a.Body[i] != b.Body[i] {
			return false
		}
	}
	return true
}

// TestQuickViewIsTransientAndDoesNotTouchDiffState is the F1.3 build test:
// opening a quick view shows it in preference to the persistent /diff panel and
// opens the pane regardless of m.panelOpen/m.diffMode, but closes (via ctrl+q)
// leaving m.panel, m.panelOpen, and m.diffMode bit-identical to before. A quick
// view that disturbs /diff state is the bug §3.2 exists to prevent.
func TestQuickViewIsTransientAndDoesNotTouchDiffState(t *testing.T) {
	m := &Model{
		renderer:  NewRenderer(theme.Dracula(), 80),
		panel:     PanelContent{Title: "pinned", Path: "x.go", Diff: "@@ diff @@\n-old\n+new"},
		panelOpen: true,
		diffMode:  DiffPinned,
	}

	// Snapshot the /diff state the quick view must not touch.
	wantPanel := m.panel
	wantOpen := m.panelOpen
	wantMode := m.diffMode

	// Open a quick view over a pinned /diff panel.
	m.quickView = &PanelContent{Title: "read", Body: []string{"file contents here"}}

	if !m.sidePaneOpen() {
		t.Fatal("sidePaneOpen() false with a quick view open; the pane must show")
	}
	if m.quickView == nil {
		t.Fatal("quickView cleared by sidePaneOpen, which must not write it")
	}
	// The /diff state is unchanged while the quick view is up.
	if !panelEqual(m.panel, wantPanel) || m.panelOpen != wantOpen || m.diffMode != wantMode {
		t.Fatalf("quick view mutated /diff state: panel=%v open=%v mode=%v, want panel=%v open=%v mode=%v",
			m.panel, m.panelOpen, m.diffMode, wantPanel, wantOpen, wantMode)
	}

	// ctrl+q's first layer closes the quick view and nothing else.
	m.closeSplit()
	if m.quickView != nil {
		t.Fatal("closeSplit() did not clear the quick view (its first layer)")
	}
	// And the /diff panel is exactly as it was.
	if !panelEqual(m.panel, wantPanel) {
		t.Errorf("panel changed by quick-view close: got %+v, want %+v", m.panel, wantPanel)
	}
	if m.panelOpen != wantOpen {
		t.Errorf("panelOpen changed by quick-view close: got %v, want %v", m.panelOpen, wantOpen)
	}
	if m.diffMode != wantMode {
		t.Errorf("diffMode changed by quick-view close: got %v, want %v", m.diffMode, wantMode)
	}
}

// TestQuickViewOpensPaneWhenDiffClosed: with no persistent panel open at all,
// a quick view still opens the pane (sidePaneOpen true) and closing it returns
// to closed — never leaving a phantom open pane.
func TestQuickViewOpensPaneWhenDiffClosed(t *testing.T) {
	m := &Model{
		renderer:  NewRenderer(theme.Dracula(), 80),
		panelOpen: false,
		diffMode:  DiffOff,
	}
	if m.sidePaneOpen() {
		t.Fatal("pane should be closed initially")
	}
	m.quickView = &PanelContent{Title: "bash", Body: []string{"> rm -rf build/"}}
	if !m.sidePaneOpen() {
		t.Fatal("quick view must open the pane even with no /diff panel")
	}
	m.closeSplit()
	if m.sidePaneOpen() {
		t.Fatal("pane stayed open after quick view closed with no /diff panel underneath")
	}
}

// TestEscapeFallsThroughToInterruptWhenNoQuickView: Esc no longer closes the
// split (ctrl+q owns that); with an in-progress turn it interrupts — the rung
// ordering of §3.3 must not regress.
func TestEscapeFallsThroughToInterruptWhenNoQuickView(t *testing.T) {
	m := &Model{processing: true, cancelTurn: func() {}}
	m.escape() // must not panic, must not clear input mid-turn
	if !m.processing {
		t.Fatal("escape cleared processing instead of interrupting the turn")
	}
}
