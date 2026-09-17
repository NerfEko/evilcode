package tui

import (
	"strings"
	"testing"
)

// TestPanelFooterStaysOnScreenWhenChatOverflows covers the report that the
// footer text at the bottom of the live view vanished: when the chat frame is
// taller than the terminal, the panel used to be as tall as the overflowing
// frame, so its footer row landed below the last visible row and the terminal
// clipped it. The panel is capped at the terminal height, so the footer stays
// on the last visible row.
func TestPanelFooterStaysOnScreenWhenChatOverflows(t *testing.T) {
	m := clickModel(nil, t.TempDir())
	m.width, m.height = 100, 10
	m.panelOpen = true
	m.liveView = true
	m.panel = PanelContent{Body: []string{"a", "b"}}
	// Fill the chat so the frame is exactly the terminal height, then reveal
	// the overscroll panel, which reserves layout and shrinks the transcript
	// instead of appending past the terminal.
	for i := 0; i < 6; i++ {
		m.blocks = append(m.blocks, Block{Kind: BlockNotice, Text: "row"})
	}
	m.overscroll.Mode = OverscrollAlways

	m.View()
	rows := strings.Split(m.lastFrame, "\n")
	if len(rows) > m.height {
		t.Fatalf("frame has %d rows, want it to fit within %d (overscroll reserves layout)", len(rows), m.height)
	}
	// The footer must be on the last visible row, not clipped off-screen.
	last := rows[len(rows)-1]
	if !strings.Contains(last, "ctrl+q to close, ctrl+L for live view") {
		t.Fatalf("footer missing from the last visible row: %q", last)
	}
	// And the overscroll panel itself must be on screen — before the layout
	// reservation it was clipped away whenever the window was full.
	if joined := strings.Join(rows, "\n"); !strings.Contains(joined, "overscroll") {
		t.Fatalf("overscroll panel missing from a full window:\n%s", joined)
	}
}
