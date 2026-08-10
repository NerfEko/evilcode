package tui

import (
	"fmt"
	"strings"
	"testing"
)

func perfModel(b *testing.B, turns int) *Model {
	b.Helper()
	m := NewModel(nil, HeaderState{Model: "mock", SessionName: "s", Provider: "mock"})
	m.width, m.height = 140, 45
	m.applyWrapWidth()
	for i := 0; i < turns; i++ {
		m.blocks = append(m.blocks,
			Block{Kind: BlockUser, Text: fmt.Sprintf("question %d", i)},
			Block{Kind: BlockTool, ToolName: "read", ToolTarget: fmt.Sprintf("file%d.go", i)},
			Block{Kind: BlockAssistant, Text: strings.Repeat("some prose about the answer. ", 20)},
		)
	}
	m.invalidateTranscriptCache()
	return m
}

func BenchmarkView(b *testing.B) {
	for _, turns := range []int{10, 100, 400} {
		b.Run(fmt.Sprintf("turns=%d", turns), func(b *testing.B) {
			m := perfModel(b, turns)
			m.View()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				m.widgetClock++
				m.View()
			}
		})
	}
}

func BenchmarkViewScrolledUp(b *testing.B) {
	m := perfModel(b, 400)
	m.View()
	m.scroll.Offset = 300
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		m.widgetClock++
		m.View()
	}
}

// BenchmarkViewScrolling measures an intentionally invalidated transcript
// (resize/theme/content changes). Ordinary wheel events only move the window
// and now retain the settled Rows cache.
func BenchmarkViewScrolling(b *testing.B) {
	m := perfModel(b, 400)
	m.View()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		m.invalidateTranscriptCache()
		m.scroll.Offset = 100 + i%50
		m.View()
	}
}

func BenchmarkViewStreaming(b *testing.B) {
	m := perfModel(b, 400)
	m.blocks = append(m.blocks, Block{Kind: BlockAssistant, Streaming: true,
		Text: strings.Repeat("streaming words ", 30)})
	m.View()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		m.widgetClock++
		m.View()
	}
}

func BenchmarkDockWidgets(b *testing.B) {
	m := perfModel(b, 400)
	m.View()
	tr := m.transcriptLines()
	rows := make([]string, 40)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		m.widgetClock++
		m.dockWidgets(rows, tr.Lines, 40, 0, tr.Owner)
	}
}

func BenchmarkTranscriptLines(b *testing.B) {
	m := perfModel(b, 400)
	m.View()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		m.transcriptLines()
	}
}

func BenchmarkContentHeightAtWidth(b *testing.B) {
	m := perfModel(b, 400)
	m.View()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		m.contentHeightAtWidth(m.renderer.Width - 1)
	}
}

// BenchmarkViewQuickView is a read preview open beside the transcript.
func BenchmarkViewQuickView(b *testing.B) {
	m := perfModel(b, 400)
	m.View()
	body := make([]string, 400)
	for i := range body {
		body[i] = fmt.Sprintf("line %d of the previewed file", i)
	}
	m.quickView = &PanelContent{Title: "main.go", Path: "main.go", Body: body, Code: true}
	m.View()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		m.widgetClock++
		m.View()
	}
}

func BenchmarkViewQuickViewScrolling(b *testing.B) {
	m := perfModel(b, 400)
	body := make([]string, 400)
	for i := range body {
		body[i] = fmt.Sprintf("line %d of the previewed file", i)
	}
	m.quickView = &PanelContent{Title: "main.go", Path: "main.go", Body: body, Code: true}
	m.View()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		m.invalidateTranscriptCache()
		m.scroll.Offset = 100 + i%50
		m.View()
	}
}
