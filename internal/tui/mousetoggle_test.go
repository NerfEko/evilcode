package tui

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
)

func TestMouseToggleFlipsCaptureAndExplainsItself(t *testing.T) {
	m := newTestModel(t)
	if m.mousePassthrough {
		t.Fatal("a fresh model should capture the mouse")
	}

	m.toggleMouseCapture()
	if !m.mousePassthrough {
		t.Error("the toggle did not release the mouse")
	}
	if !strings.Contains(m.notice, "highlight") && !strings.Contains(m.notice, "copy") {
		t.Errorf("notice = %q, want it to name selection and copy", m.notice)
	}

	m.toggleMouseCapture()
	if m.mousePassthrough {
		t.Error("the second toggle did not recapture the mouse")
	}
	if !strings.Contains(m.notice, "hover") {
		t.Errorf("notice = %q, want it to name the app affordances", m.notice)
	}
}

func TestMouseToggleReleasesTheTerminalMouse(t *testing.T) {
	// While evilcode reports the mouse, the terminal does not select: a
	// terminal that reports events never starts a text selection on its own.
	// The toggle has to stop requesting events entirely, not merely stop
	// handling them, or highlight-and-copy stays broken.
	m := newTestModel(t)
	if v := m.View(); v.MouseMode != tea.MouseModeAllMotion {
		t.Fatalf("default MouseMode = %v, want all-motion", v.MouseMode)
	}

	m.toggleMouseCapture()
	if v := m.View(); v.MouseMode != tea.MouseModeNone {
		t.Fatalf("MouseMode after toggle = %v, want none — the terminal must own the mouse", v.MouseMode)
	}

	m.toggleMouseCapture()
	if v := m.View(); v.MouseMode != tea.MouseModeAllMotion {
		t.Fatalf("MouseMode after toggling back = %v, want all-motion", v.MouseMode)
	}
}

func TestMouseToggleClearsTransientHoverPaint(t *testing.T) {
	// Hover decoration is transient pointer state; after the mouse leaves the
	// app entirely the next frame must not still be drawing it.
	m := newTestModel(t)
	m.hover = hoverTarget{valid: true, block: 0, kind: hoverReasoning}

	m.toggleMouseCapture()
	if m.hover.valid {
		t.Error("hover paint survived the toggle")
	}
}

func TestMouseToggleChordResolvesThroughTheKeymap(t *testing.T) {
	km, problems := NewKeymap(nil)
	if len(problems) != 0 {
		t.Fatalf("keymap problems: %v", problems)
	}
	b, ok := km.Lookup("alt+shift+m")
	if !ok {
		t.Fatal("alt+shift+m is not bound")
	}
	if b.Action != ActionMouseToggle {
		t.Errorf("alt+shift+m = %s, want mouse_mode_toggle", b.Action)
	}
}
func TestDragMotionDoesNotRehover(t *testing.T) {
	// A drag reports motion with the button held (ultraviolet: "left motion
	// release" arrives as MouseMotionEvent with Button set). Re-targeting
	// hover on every step invalidated the whole transcript cache per event and
	// repainted the frame under the drag — the flicker seen while selecting
	// text. Hover must freeze until the buttons are released.
	m := clickModel([]Block{{
		Kind: BlockTool, ToolName: "read", ToolTarget: "main.go", ToolPath: "main.go",
	}}, t.TempDir())
	row := ownerRow(t, m, 0)

	m.Update(tea.MouseMotionMsg(tea.Mouse{X: 2, Y: row}))
	if !m.hover.valid {
		t.Fatal("plain motion did not establish hover")
	}
	was := m.hover

	// A drag one row down, button held: outside the block, a hover target
	// would change and invalidate the frame.
	m.Update(tea.MouseMotionMsg(tea.Mouse{X: 2, Y: row + 1, Button: tea.MouseLeft}))
	if m.hover != was {
		t.Errorf("hover = %+v during a drag, want it frozen at %+v", m.hover, was)
	}
}
