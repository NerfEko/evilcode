package tui

import (
	"strings"
	"testing"

	"evilcode/internal/theme"
)

func BenchmarkPanelBodyWholeFile(b *testing.B) {
	r := NewRenderer(theme.Dracula(), 80)
	body := make([]string, 1000)
	for i := range body {
		body[i] = "func example" + strings.Repeat("x", 40) + "() { return " + strings.Repeat("y", 20) + " }"
	}
	diff := "@@ -10,2 +10,2 @@\n-old\n+new\n"
	c := PanelContent{Title: "big.go", Path: "big.go", Diff: diff, Body: body}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		r.panelBody(c, DiffInline, 60)
	}
}

// BenchmarkPanelFrameWithCache measures a full frame with the pane open and
// the body already cached — the cost of one wheel notch.
func BenchmarkPanelFrameWithCache(b *testing.B) {
	m := clickModel(nil, b.TempDir())
	m.width, m.height = 140, 40
	m.panelOpen = true
	body := make([]string, 1000)
	for i := range body {
		body[i] = "func example" + strings.Repeat("x", 40) + "() { return " + strings.Repeat("y", 20) + " }"
	}
	m.panel = PanelContent{Title: "big.go", Path: "big.go", Body: body}
	m.View() // warm the cache
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		m.panelScroll.Offset = (m.panelScroll.Offset + 3) % 900
		m.View()
	}
}
