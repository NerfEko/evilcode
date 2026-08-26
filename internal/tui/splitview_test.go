package tui

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"evilcode/internal/agent"
	"evilcode/internal/provider"
)

// writeEvent builds the tool-result event a write/edit produces.
func writeEvent(path, diff string) agent.Event {
	raw, _ := json.Marshal(map[string]string{"path": path})
	return agent.Event{
		Kind: agent.EventToolResult,
		Call: &provider.ToolCall{Name: "write", Args: raw},
		Diff: diff,
	}
}

func TestClickWriteOpensWholeFileScrolledToDiff(t *testing.T) {
	root := t.TempDir()
	file := "package main\n\nfunc main() {\n\tprintln(\"hi\")\n}\n"
	if err := os.WriteFile(filepath.Join(root, "main.go"), []byte(file), 0600); err != nil {
		t.Fatal(err)
	}
	diff := "@@ -1,4 +1,4 @@\n package main\n \n func main() {\n-\tprintln(\"hi\")\n+\tprintln(\"hello\")\n }\n"
	m := clickModel([]Block{{
		Kind: BlockTool, ToolName: "write", ToolTarget: "main.go", ToolPath: "main.go",
		Diff: diff,
	}}, root)

	m.openQuickViewAt(tea.Mouse{X: 2, Y: ownerRow(t, m, 0)})
	if m.quickView == nil {
		t.Fatal("write click did not open a quick view")
	}
	// The whole file is there, not just the diff's four lines.
	if got := len(m.quickView.Body); got != 5 {
		t.Fatalf("quick view body has %d lines, want the whole file (5)", got)
	}
	// And it opens scrolled to the change: the first hunk starts at line 1.
	if m.quickView.ScrollTo != 0 {
		t.Fatalf("ScrollTo = %d, want 0 (first hunk at line 1)", m.quickView.ScrollTo)
	}
}

func TestWholeFileDiffMarksChangesInTheFullFile(t *testing.T) {
	r := testRenderer(80)
	fileLines := []string{"package main", "", "func main() {", "\tprintln(\"hello\")", "}"}
	diff := "@@ -1,4 +1,4 @@\n package main\n \n func main() {\n-\tprintln(\"hi\")\n+\tprintln(\"hello\")\n }\n"

	rows := plainLines(r.wholeFileDiff("main.go", fileLines, diff, 40))
	joined := strings.Join(rows, "\n")
	// Every file line is present, numbered.
	for _, want := range []string{"package main", "func main() {", "}"} {
		if !strings.Contains(joined, want) {
			t.Fatalf("whole-file view dropped %q:\n%s", want, joined)
		}
	}
	// The deleted line is inserted where it was removed, and the added line
	// is marked.
	var sawDel, sawAdd bool
	for _, row := range rows {
		if strings.Contains(row, "println(\"hi\")") && strings.Contains(row, "-") {
			sawDel = true
		}
		if strings.Contains(row, "println(\"hello\")") && strings.Contains(row, "+") {
			sawAdd = true
		}
	}
	if !sawDel || !sawAdd {
		t.Fatalf("change markers missing: del=%v add=%v\n%s", sawDel, sawAdd, joined)
	}
}

func TestWheelOverPanelScrollsPanelNotTranscript(t *testing.T) {
	m := clickModel(nil, t.TempDir())
	m.width, m.height = 100, 20
	m.panelOpen = true
	m.panel = PanelContent{Body: make([]string, 60)}
	for i := range m.panel.Body {
		m.panel.Body[i] = strings.Repeat("x", 10)
	}
	// Render once so the panel knows its content height.
	m.View()
	if m.panelContentHeight != 60 {
		t.Fatalf("panelContentHeight = %d, want 60", m.panelContentHeight)
	}

	chat, _ := Horizontal{Width: m.width, SidePaneRatio: m.panelRatio, SidePaneOpen: true}.Split()
	before := m.scroll.Offset
	m.handleWheel(tea.MouseWheelMsg(tea.Mouse{X: chat + 2, Button: tea.MouseWheelDown}))
	if m.panelScroll.Offset == 0 {
		t.Fatal("wheel over the panel did not scroll the panel")
	}
	if m.scroll.Offset != before {
		t.Fatal("wheel over the panel scrolled the transcript")
	}
}

func TestPanelWheelDirectionIsNotInverted(t *testing.T) {
	m := clickModel(nil, t.TempDir())
	m.width, m.height = 100, 20
	m.panelOpen = true
	m.panel = PanelContent{Body: make([]string, 60)}
	m.View()

	chat, _ := Horizontal{Width: m.width, SidePaneRatio: m.panelRatio, SidePaneOpen: true}.Split()
	// Wheel down moves toward the bottom of the file.
	m.handleWheel(tea.MouseWheelMsg(tea.Mouse{X: chat + 2, Button: tea.MouseWheelDown}))
	down := m.panelScroll.Offset
	if down == 0 {
		t.Fatal("wheel down did not move toward the bottom of the file")
	}
	// Wheel up moves back toward the top.
	m.handleWheel(tea.MouseWheelMsg(tea.Mouse{X: chat + 2, Button: tea.MouseWheelUp}))
	if m.panelScroll.Offset >= down {
		t.Fatalf("wheel up moved the wrong way: offset %d, want < %d", m.panelScroll.Offset, down)
	}
}

func TestPanelBodyIsCachedAcrossFrames(t *testing.T) {
	m := clickModel(nil, t.TempDir())
	m.width, m.height = 100, 20
	m.panelOpen = true
	m.panel = PanelContent{Body: []string{"a", "b"}}

	m.View()
	first := &m.panelBodyCache[0]
	m.View()
	if got := &m.panelBodyCache[0]; got != first {
		t.Fatal("panel body re-rendered on an unchanged frame")
	}

	// New content re-renders.
	m.panel = PanelContent{Body: []string{"a", "b", "c"}}
	m.View()
	if got := &m.panelBodyCache[0]; got == first {
		t.Fatal("panel body cache did not refresh on new content")
	}
}

func TestWheelOverChatScrollsTranscriptNotPanel(t *testing.T) {
	m := clickModel(nil, t.TempDir())
	m.width, m.height = 100, 20
	m.panelOpen = true
	m.panel = PanelContent{Body: make([]string, 60)}
	for i := 0; i < 40; i++ {
		m.blocks = append(m.blocks, Block{Kind: BlockNotice, Text: "row"})
	}
	m.View()

	chat, _ := Horizontal{Width: m.width, SidePaneRatio: m.panelRatio, SidePaneOpen: true}.Split()
	before := m.panelScroll.Offset
	m.handleWheel(tea.MouseWheelMsg(tea.Mouse{X: chat - 2, Button: tea.MouseWheelUp}))
	if m.scroll.Offset == 0 {
		t.Fatal("wheel over the chat did not scroll the transcript")
	}
	if m.panelScroll.Offset != before {
		t.Fatal("wheel over the chat scrolled the panel")
	}
}

func TestCtrlLTogglesLiveViewAndCtrlQClosesIt(t *testing.T) {
	m := clickModel(nil, t.TempDir())
	m.width, m.height = 100, 20

	model, _ := m.Update(tea.KeyPressMsg(tea.Key{Code: 'l', Mod: tea.ModCtrl}))
	got := model.(*Model)
	if !got.liveView || !got.panelOpen {
		t.Fatalf("ctrl+l did not open live view: live=%v open=%v", got.liveView, got.panelOpen)
	}

	// ctrl+l again closes the split entirely, not just live mode.
	model, _ = got.Update(tea.KeyPressMsg(tea.Key{Code: 'l', Mod: tea.ModCtrl}))
	got = model.(*Model)
	if got.liveView || got.panelOpen || got.sidePaneOpen() {
		t.Fatalf("ctrl+l did not close the live split: live=%v open=%v", got.liveView, got.panelOpen)
	}

	// ctrl+q also closes it when live view is on.
	model, _ = got.Update(tea.KeyPressMsg(tea.Key{Code: 'l', Mod: tea.ModCtrl}))
	got = model.(*Model)
	model, _ = got.Update(tea.KeyPressMsg(tea.Key{Code: 'q', Mod: tea.ModCtrl}))
	got = model.(*Model)
	if got.liveView || got.panelOpen || got.sidePaneOpen() {
		t.Fatalf("ctrl+q did not close the live split: live=%v open=%v", got.liveView, got.panelOpen)
	}
}

func TestLiveViewFollowsEdits(t *testing.T) {
	root := t.TempDir()
	file := "one\ntwo\nthree\nfour\nfive\nsix\nseven\neight\nnine\nten\n"
	if err := os.WriteFile(filepath.Join(root, "f.txt"), []byte(file), 0600); err != nil {
		t.Fatal(err)
	}
	m := clickModel(nil, root)
	m.width, m.height = 100, 20
	m.liveView = true
	m.panelOpen = true

	diff := "@@ -3,1 +3,1 @@\n-three\n+THREE\n"
	m.applyEvent(writeEvent("f.txt", diff))
	if !m.panelOpen {
		t.Fatal("live view did not keep the pane open")
	}
	if len(m.panel.Body) != 10 {
		t.Fatalf("live view panel has %d body lines, want the whole file (10)", len(m.panel.Body))
	}
	if m.panel.ScrollTo != 2 {
		t.Fatalf("ScrollTo = %d, want 2 (hunk at line 3)", m.panel.ScrollTo)
	}
	if m.panelScrollPending != 2 {
		t.Fatalf("pending scroll = %d, want 2", m.panelScrollPending)
	}
}

func TestLiveViewTracksReads(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "r.txt"), []byte("read me\n"), 0600); err != nil {
		t.Fatal(err)
	}
	m := clickModel(nil, root)
	m.liveView = true

	raw, _ := json.Marshal(map[string]string{"path": "r.txt"})
	m.applyEvent(agent.Event{
		Kind: agent.EventToolResult,
		Call: &provider.ToolCall{Name: "read", Args: raw},
	})
	if !m.panelOpen || !m.panel.Code {
		t.Fatalf("live view did not open the read file: open=%v code=%v", m.panelOpen, m.panel.Code)
	}
	if !strings.Contains(strings.Join(m.panel.Body, "\n"), "read me") {
		t.Fatalf("live view panel missing file contents: %v", m.panel.Body)
	}
}

func TestClickDisablesLiveView(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "a.go"), []byte("package a\n"), 0600); err != nil {
		t.Fatal(err)
	}
	m := clickModel([]Block{{
		Kind: BlockTool, ToolName: "read", ToolTarget: "a.go", ToolPath: "a.go",
		ToolPathExists: true,
	}}, root)
	m.liveView = true

	m.openQuickViewAt(tea.Mouse{X: 2, Y: ownerRow(t, m, 0)})
	if m.liveView {
		t.Fatal("clicking a file left live view on")
	}
	if m.quickView == nil {
		t.Fatal("click did not open the quick view")
	}
}

func TestPlainQTypesWithSplitOpen(t *testing.T) {
	// Plain q is an ordinary letter again: it types even while the split is
	// open; ctrl+q is the close key.
	m := clickModel(nil, t.TempDir())
	m.panelOpen = true
	m.panel = PanelContent{Body: []string{"content"}}
	m.editor.Text = "qui"
	m.editor.Cursor = 3

	model, _ := m.Update(tea.KeyPressMsg(tea.Key{Code: 'q', Text: "q"}))
	got := model.(*Model)
	if !got.panelOpen {
		t.Fatal("plain q closed the split")
	}
	if got.editor.Text != "quiq" {
		t.Fatalf("q did not type: editor = %q", got.editor.Text)
	}

	// ctrl+q closes it even with text in the composer.
	model, _ = got.Update(tea.KeyPressMsg(tea.Key{Code: 'q', Mod: tea.ModCtrl}))
	got = model.(*Model)
	if got.panelOpen || got.sidePaneOpen() {
		t.Fatal("ctrl+q did not close the split with text in the composer")
	}
	if got.editor.Text != "quiq" {
		t.Fatalf("ctrl+q disturbed the composer: editor = %q", got.editor.Text)
	}
}

func TestReadQuickViewShowsLineNumbers(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "main.go"),
		[]byte("package main\n\nfunc main() {}\n"), 0600); err != nil {
		t.Fatal(err)
	}
	m := clickModel([]Block{{
		Kind: BlockTool, ToolName: "read", ToolTarget: "main.go", ToolPath: "main.go",
		ToolPathExists: true,
	}}, root)

	m.openQuickViewAt(tea.Mouse{X: 2, Y: ownerRow(t, m, 0)})
	if m.quickView == nil || !m.quickView.Numbers {
		t.Fatalf("read click did not request line numbers: %+v", m.quickView)
	}

	_, side := Horizontal{Width: m.width, SidePaneRatio: m.panelRatio, SidePaneOpen: true}.Split()
	rows := plainLines(m.renderer.RenderSidePanel(*m.quickView, DiffInline, side, 10, true, 0, false))
	joined := strings.Join(rows, "\n")
	for _, want := range []string{"1 │ package main", "3 │ func main() {}"} {
		if !strings.Contains(joined, want) {
			t.Fatalf("read view missing numbered line %q:\n%s", want, joined)
		}
	}
}

func TestPanelFooterShowsLiveAndHints(t *testing.T) {
	r := testRenderer(120)
	rows := plainLines(r.RenderSidePanel(
		PanelContent{Body: []string{"a", "b"}}, DiffInline, 60, 8, false, 0, true))
	last := strings.TrimPrefix(rows[len(rows)-1], "│ ")
	if !strings.HasPrefix(last, "live") {
		t.Fatalf("footer missing the live tag: %q", last)
	}
	if !strings.Contains(last, "ctrl+q to close, ctrl+L for live view") {
		t.Fatalf("footer missing the key hints: %q", last)
	}

	// Without live view the tag and the live hint are gone, but the close
	// hint stays — ctrl+q works in every split view.
	rows = plainLines(r.RenderSidePanel(
		PanelContent{Body: []string{"a", "b"}}, DiffInline, 60, 8, false, 0, false))
	last = strings.TrimPrefix(rows[len(rows)-1], "│ ")
	if strings.HasPrefix(last, "live") {
		t.Fatalf("footer shows the live tag when live view is off: %q", last)
	}
	if !strings.Contains(last, "ctrl+q to close") {
		t.Fatalf("footer missing the close hint: %q", last)
	}
	if strings.Contains(last, "ctrl+L for live view") {
		t.Fatalf("footer shows the live hint when live view is off: %q", last)
	}
}

func TestPanelScrollClampsAndWindows(t *testing.T) {
	r := testRenderer(120)
	body := make([]string, 20)
	for i := range body {
		body[i] = "line"
	}
	// Scroll past the end: the window clamps instead of panicking.
	rows := r.RenderSidePanel(PanelContent{Body: body}, DiffInline, 60, 8, false, 18, false)
	if len(rows) != 8 {
		t.Fatalf("panel rendered %d rows, want 8", len(rows))
	}
	// The last body line is visible at the bottom of the window.
	if !strings.Contains(plain(rows[len(rows)-2]), "line") {
		t.Fatalf("clamped window lost the body: %v", plainLines(rows))
	}
}

func TestPanelOpensCenteredOnScrollTo(t *testing.T) {
	m := clickModel(nil, t.TempDir())
	m.width, m.height = 100, 20
	m.panelOpen = true
	body := make([]string, 100)
	for i := range body {
		body[i] = "line"
	}
	m.panel = PanelContent{Body: body, ScrollTo: 60}
	m.panelScrollPending = 60

	m.View()
	// The window is height-2 = 18 rows; line 60 centers at 60-9 = 51.
	if m.panelScroll.Offset != 51 {
		t.Fatalf("panel offset = %d, want 51 (centered on line 60)", m.panelScroll.Offset)
	}
	if m.panelScrollPending != -1 {
		t.Fatalf("pending scroll not consumed: %d", m.panelScrollPending)
	}
}
