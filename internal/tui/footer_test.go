package tui

import (
	"strings"
	"testing"
)

// TestPanelFooterStaysOnScreenWhenChatOverflows reproduces the report that the
// footer text at the bottom of the live view vanished: when the chat frame is
// taller than the terminal (an overscroll facts line, a notice, an ask
// picker), the panel used to be as tall as the overflowing frame, so its
// footer row landed below the last visible row and the terminal clipped it.
func TestPanelFooterStaysOnScreenWhenChatOverflows(t *testing.T) {
	m := clickModel(nil, t.TempDir())
	m.width, m.height = 100, 10
	m.panelOpen = true
	m.liveView = true
	m.panel = PanelContent{Body: []string{"a", "b"}}
	// Fill the chat so the frame is exactly the terminal height, then reveal
	// the overscroll facts line, which appends one more row.
	for i := 0; i < 6; i++ {
		m.blocks = append(m.blocks, Block{Kind: BlockNotice, Text: "row"})
	}
	m.overscroll.Mode = OverscrollAlways

	m.View()
	rows := strings.Split(m.lastFrame, "\n")
	if len(rows) <= m.height {
		t.Fatalf("frame has %d rows, want an overflow past %d", len(rows), m.height)
	}
	// The footer must be on the last visible row, not clipped off-screen.
	last := rows[m.height-1]
	if !strings.Contains(last, "ctrl+q to close, ctrl+L for live view") {
		t.Fatalf("footer missing from the last visible row: %q", last)
	}
}
