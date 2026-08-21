package tui

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
)

// TestSelectionModeDisablesMouseTracking confirms the View drops mouse tracking
// while selection mode is on, so the terminal's native highlight-and-copy takes
// over the screen, and restores cell-motion tracking when it is off.
func TestSelectionModeDisablesMouseTracking(t *testing.T) {
	m := newTestModel(t)

	if got := m.View().MouseMode; got != tea.MouseModeCellMotion {
		t.Fatalf("idle MouseMode = %v, want cell motion", got)
	}

	m.selectionMode = true
	if got := m.View().MouseMode; got != tea.MouseModeNone {
		t.Fatalf("selection-mode MouseMode = %v, want none", got)
	}

	m.selectionMode = false
	if got := m.View().MouseMode; got != tea.MouseModeCellMotion {
		t.Fatalf("restored MouseMode = %v, want cell motion", got)
	}
}

// TestSelectionModeBannerOverlaysTopRow confirms a mode indicator is painted on
// the first row while selection mode is on and absent when it is off.
func TestSelectionModeBannerOverlaysTopRow(t *testing.T) {
	m := newTestModel(t)
	rows := []string{"header line", "body one", "body two"}

	off := m.selectionBanner(append([]string(nil), rows...))
	if off[0] != rows[0] {
		t.Fatalf("banner applied while selection mode is off: %q", off[0])
	}

	m.selectionMode = true
	on := m.selectionBanner(append([]string(nil), rows...))
	if on[0] == rows[0] {
		t.Fatalf("top row unchanged in selection mode")
	}
	if !strings.Contains(plain(on[0]), "Selection mode") {
		t.Fatalf("banner missing label: %q", on[0])
	}
	// The rest of the frame is untouched.
	for i := 1; i < len(rows); i++ {
		if on[i] != rows[i] {
			t.Fatalf("row %d changed by banner: %q != %q", i, on[i], rows[i])
		}
	}
}

// TestSelectionModeSwallowsKeysExceptExit confirms the mode is modal: only Esc
// and the bound toggle key leave it; typing into the composer is ignored.
func TestSelectionModeSwallowsKeysExceptExit(t *testing.T) {
	km, problems := NewKeymap(nil)
	if len(problems) > 0 {
		t.Fatalf("default keymap has problems: %v", problems)
	}
	m := newTestModel(t)
	m.keymap = km
	m.selectionMode = true
	m.editor.Text = "untouched"

	// A regular keystroke is swallowed and does not edit the composer.
	model, _ := m.Update(tea.KeyPressMsg{Code: 'a'})
	m = model.(*Model)
	if !m.selectionMode {
		t.Fatal("selection mode turned off by an ordinary key")
	}
	if m.editor.Text != "untouched" {
		t.Fatalf("composer edited in selection mode: %q", m.editor.Text)
	}

	// Esc leaves the mode.
	model, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyEscape})
	m = model.(*Model)
	if m.selectionMode {
		t.Fatal("Esc did not exit selection mode")
	}

	// The bound toggle key (Alt+O) also leaves the mode.
	m.selectionMode = true
	model, _ = m.Update(tea.KeyPressMsg{Code: 'o', Mod: tea.ModAlt})
	m = model.(*Model)
	if m.selectionMode {
		t.Fatal("Alt+O did not exit selection mode")
	}
}

// TestSelectionModeBindingRegistered confirms the default keymap binds the
// selection-mode action to Alt+O, so the feature is reachable and documented.
func TestSelectionModeBindingRegistered(t *testing.T) {
	km, _ := NewKeymap(nil)
	b, ok := km.Lookup("alt+o")
	if !ok {
		t.Fatal("alt+o not bound in the default keymap")
	}
	if b.Action != ActionSelectionMode {
		t.Fatalf("alt+o bound to %s, want %s", b.Action, ActionSelectionMode)
	}
}