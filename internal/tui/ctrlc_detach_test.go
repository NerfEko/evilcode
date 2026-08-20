package tui

import (
	"testing"

	tea "charm.land/bubbletea/v2"
)

// ctrlC builds a Ctrl+C key press whose String() is "ctrl+c".
func ctrlC() tea.KeyPressMsg { return tea.KeyPressMsg{Code: 'c', Mod: tea.ModCtrl} }

// TestCtrlCDetachesWithoutStoppingAgent is the feedback loop for the Ctrl+C
// change: two presses detach the window (quit the client) and never send an
// interrupt to the server, even while a turn is in flight. Esc cancels an armed
// detach.
func TestCtrlCDetachesWithoutStoppingAgent(t *testing.T) {
	interrupted := false
	m := NewModel(nil, HeaderState{SessionName: "s", Model: "mock"})
	m.width, m.height = 90, 24
	m.remoteInterrupt = func(disarm bool) error {
		interrupted = true
		return nil
	}
	// A turn is in flight. Under the old behavior the first Ctrl+C interrupted.
	m.processing = true

	// First Ctrl+C arms the detach but does NOT interrupt and does NOT quit.
	m.handleKey(ctrlC())
	if interrupted {
		t.Fatal("first Ctrl+C interrupted the agent; it should only arm a detach")
	}
	if m.quitting {
		t.Fatal("first Ctrl+C quit; it should only arm")
	}
	if !m.confirmQuit {
		t.Error("first Ctrl+C did not arm confirmQuit")
	}

	// Second Ctrl+C detaches (quits the client). The agent is never interrupted.
	m.handleKey(ctrlC())
	if !m.quitting {
		t.Fatal("second Ctrl+C did not detach (quit)")
	}
	if interrupted {
		t.Fatal("second Ctrl+C interrupted the agent; detaching must leave it running")
	}

	// Esc cancels an armed detach so a reflexive "never mind" disarms it.
	m2 := NewModel(nil, HeaderState{SessionName: "s", Model: "mock"})
	m2.width, m2.height = 90, 24
	m2.processing = true
	m2.remoteInterrupt = func(disarm bool) error { interrupted = true; return nil }
	interrupted = false
	m2.handleKey(ctrlC())
	if !m2.confirmQuit {
		t.Fatal("setup: first Ctrl+C did not arm")
	}
	m2.handleKey(escKey())
	if m2.confirmQuit {
		t.Error("Esc did not cancel the armed detach")
	}
	// Esc while processing still interrupts the agent (Esc is the stop key).
	if !interrupted {
		t.Error("Esc did not interrupt while processing — Esc must remain the stop gesture")
	}
}

func escKey() tea.KeyPressMsg { return tea.KeyPressMsg{Code: tea.KeyEscape} }
