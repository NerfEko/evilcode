package tui

import (
	"fmt"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
)

// TestWheelFrameCostDoesNotGrowWithSession measures View() cost after a wheel
// event at three session sizes. A cache that is invalidated per wheel event
// would make the cost grow linearly with the transcript.
func TestWheelFrameCostDoesNotGrowWithSession(t *testing.T) {
	body := make([]string, 1000)
	for i := range body {
		body[i] = "func example" + strings.Repeat("x", 40) + "() { return " + strings.Repeat("y", 20) + " }"
	}

	measure := func(blocks int) time.Duration {
		m := clickModel(nil, t.TempDir())
		m.width, m.height = 140, 40
		m.panelOpen = true
		m.panel = PanelContent{Title: "big.go", Path: "big.go", Body: body}
		for i := 0; i < blocks; i++ {
			m.blocks = append(m.blocks, Block{Kind: BlockNotice, Text: fmt.Sprintf("row %d", i)})
		}
		m.View() // warm caches
		chat, _ := Horizontal{Width: m.width, SidePaneRatio: m.panelRatio, SidePaneOpen: true}.Split()
		start := time.Now()
		for i := 0; i < 50; i++ {
			m.handleWheel(tea.MouseWheelMsg(tea.Mouse{X: chat - 2, Button: tea.MouseWheelUp}))
			m.View()
		}
		return time.Since(start) / 50
	}

	small := measure(50)
	mid := measure(400)
	big := measure(2000)
	t.Logf("per-frame: 50 blocks=%v 400 blocks=%v 2000 blocks=%v", small, mid, big)
	if big > mid*3 && big > 5*time.Millisecond {
		t.Fatalf("frame cost grows with session: 400=%v 2000=%v", mid, big)
	}
}
