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
	"evilcode/internal/theme"
	"evilcode/internal/tools"
)

func clickModel(blocks []Block, cwd string) *Model {
	return &Model{
		renderer:   NewRenderer(theme.Dracula(), 79),
		width:      80,
		height:     20,
		blocks:     blocks,
		cwd:        cwd,
		panelRatio: 50,
	}
}

func ownerRow(t *testing.T, m *Model, owner int) int {
	t.Helper()
	rows := m.transcriptLines()
	for i, got := range rows.Owner {
		if got == owner {
			return i
		}
	}
	t.Fatalf("block %d has no rendered row", owner)
	return -1
}

func TestClickReadOpensFileQuickView(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(root+"/main.go", []byte("package main\n\nfunc main() {}\n"), 0600); err != nil {
		t.Fatal(err)
	}
	m := clickModel([]Block{{
		Kind: BlockTool, ToolName: "read", ToolTarget: "main.go", ToolPath: "main.go",
		ToolPathExists: true,
	}}, root)

	m.openQuickViewAt(tea.Mouse{X: 2, Y: ownerRow(t, m, 0)})
	if m.quickView == nil || !m.quickView.Code {
		t.Fatalf("read click did not open a highlighted quick view: %+v", m.quickView)
	}
	if !strings.Contains(strings.Join(m.quickView.Body, "\n"), "package main") {
		t.Fatalf("quick view omitted file contents: %+v", m.quickView.Body)
	}
}

func TestMouseUpdateRoutesClickToQuickView(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "main.go"), []byte("package main\n"), 0600); err != nil {
		t.Fatal(err)
	}
	m := clickModel([]Block{{
		Kind: BlockTool, ToolName: "read", ToolTarget: "main.go", ToolPath: "main.go",
		ToolPathExists: true,
	}}, root)

	model, _ := m.Update(tea.MouseClickMsg{X: 2, Y: ownerRow(t, m, 0), Button: tea.MouseLeft})
	got, ok := model.(*Model)
	if !ok || got.quickView == nil {
		t.Fatalf("mouse update did not route to a quick view: %#v", model)
	}
}

func TestClickMissingReadShowsError(t *testing.T) {
	m := clickModel([]Block{{
		Kind: BlockTool, ToolName: "read", ToolTarget: "gone.go", ToolPath: "gone.go",
	}}, t.TempDir())
	m.openQuickViewAt(tea.Mouse{X: 2, Y: ownerRow(t, m, 0)})
	if m.quickView == nil || !strings.HasPrefix(strings.Join(m.quickView.Body, "\n"), "error:") {
		t.Fatalf("missing read was silently ignored: %+v", m.quickView)
	}
}

func TestClickWriteAndBashReplaceQuickViewWithoutTouchingDiff(t *testing.T) {
	m := clickModel([]Block{
		{Kind: BlockTool, ToolName: "write", ToolTarget: "main.go", ToolPath: "main.go", Diff: "@@ -1 +1 @@\n-old\n+new"},
		{Kind: BlockTool, ToolName: "bash", ToolTarget: "printf hi", ToolCommand: "printf hi", ToolOutput: "hi"},
	}, t.TempDir())
	m.panel = PanelContent{Title: "pinned", Diff: "@@ pinned @@"}
	m.panelOpen, m.diffMode = true, DiffPinned
	wantPanel, wantOpen, wantMode := m.panel, m.panelOpen, m.diffMode

	m.openQuickViewAt(tea.Mouse{X: 2, Y: ownerRow(t, m, 0)})
	if m.quickView == nil || m.quickView.Diff == "" {
		t.Fatalf("write click did not open diff quick view: %+v", m.quickView)
	}
	m.openQuickViewAt(tea.Mouse{X: 2, Y: ownerRow(t, m, 1)})
	if m.quickView == nil || !strings.Contains(strings.Join(m.quickView.Body, "\n"), "> printf hi") {
		t.Fatalf("bash click did not replace quick view: %+v", m.quickView)
	}
	if !panelEqual(m.panel, wantPanel) || m.panelOpen != wantOpen || m.diffMode != wantMode {
		t.Fatal("quick-view clicks changed persistent diff state")
	}
}

func TestCtrlQClosesQuickViewWithoutTouchingDiff(t *testing.T) {
	m := clickModel(nil, t.TempDir())
	m.panel = PanelContent{Title: "pinned", Diff: "@@ pinned @@"}
	m.panelOpen, m.diffMode = true, DiffPinned
	wantPanel, wantOpen, wantMode := m.panel, m.panelOpen, m.diffMode
	m.quickView = &PanelContent{Title: "read", Body: []string{"content"}}

	model, _ := m.Update(tea.KeyPressMsg(tea.Key{Code: 'q', Mod: tea.ModCtrl}))
	got := model.(*Model)
	if got.quickView != nil {
		t.Fatal("ctrl+q left the quick view open")
	}
	if !panelEqual(got.panel, wantPanel) || got.panelOpen != wantOpen || got.diffMode != wantMode {
		t.Fatal("ctrl+q changed persistent diff state while closing quick view")
	}
}

func TestEscapeNoLongerClosesTheSplit(t *testing.T) {
	// Esc means stop/clear; ctrl+q is the key that closes the split.
	m := clickModel(nil, t.TempDir())
	m.quickView = &PanelContent{Title: "read", Body: []string{"content"}}

	model, _ := m.Update(tea.KeyPressMsg{Code: tea.KeyEscape})
	got := model.(*Model)
	if got.quickView == nil {
		t.Fatal("Esc closed the quick view; ctrl+q owns that now")
	}
}

func TestApplyEventRetainsBoundedBashViewData(t *testing.T) {
	command := strings.Repeat("x", tools.MaxResultBytes+100)
	output := "HEAD" + strings.Repeat("-", tools.MaxResultBytes*2) + "TAIL"
	raw, err := json.Marshal(map[string]string{"cmd": command})
	if err != nil {
		t.Fatal(err)
	}
	m := clickModel(nil, t.TempDir())
	m.applyEvent(agent.Event{
		Kind:   agent.EventToolResult,
		Call:   &provider.ToolCall{Name: "bash", Args: raw},
		Output: output,
	})
	if len(m.blocks) != 1 {
		t.Fatalf("got %d blocks, want one", len(m.blocks))
	}
	b := m.blocks[0]
	if len(b.ToolOutput) > tools.MaxResultBytes || !strings.Contains(b.ToolOutput, "output truncated") {
		t.Fatalf("bash output was not bounded and marked: len=%d", len(b.ToolOutput))
	}
	if len(b.ToolCommand) > tools.MaxResultBytes || !strings.Contains(b.ToolCommand, "command truncated") {
		t.Fatalf("bash command was not bounded and marked: len=%d", len(b.ToolCommand))
	}
}

func TestExistingMarkdownToolPathIsUnderlined(t *testing.T) {
	r := NewRenderer(theme.Dracula(), 80)
	lines := r.renderTool(&Block{
		Kind: BlockTool, ToolName: "read", ToolTarget: "README.md", ToolPath: "README.md",
		ToolPathExists: true, ToolPathMarkdown: true,
	})
	if !strings.Contains(lines[0], "\x1b[4;") {
		t.Fatalf("existing markdown target was not underlined: %q", lines[0])
	}
	plain := r.renderTool(&Block{
		Kind: BlockTool, ToolName: "read", ToolTarget: "gone.md", ToolPath: "gone.md",
		ToolPathMarkdown: false,
	})
	if strings.Contains(plain[0], "\x1b[4;") {
		t.Fatalf("missing markdown target was underlined: %q", plain[0])
	}
}

func TestMarkdownClickOpensTheSidePanelRendered(t *testing.T) {
	// It used to shell out to glow in a detached terminal, which put the file in
	// a window of its own instead of beside the conversation.
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "README.md"),
		[]byte("# hello\n\nsome **bold** prose\n"), 0600); err != nil {
		t.Fatal(err)
	}

	m := clickModel([]Block{{
		Kind: BlockTool, ToolName: "read", ToolTarget: "README.md", ToolPath: "README.md",
		ToolPathExists: true, ToolPathMarkdown: true,
	}}, root)
	model, _ := m.Update(tea.MouseClickMsg{X: 2, Y: ownerRow(t, m, 0), Button: tea.MouseLeft})
	got := model.(*Model)
	if got.quickView == nil {
		t.Fatal("markdown click did not open the quick view")
	}
	if !got.sidePaneOpen() {
		t.Fatal("quick view is set but the side pane is closed")
	}

	// Rendered as a document, not as highlighted source: the heading's hashes
	// are gone and the bold marks with them.
	_, side := Horizontal{Width: m.width, SidePaneRatio: m.panelRatio, SidePaneOpen: true}.Split()
	panel := plain(strings.Join(
		m.renderer.RenderSidePanel(*got.quickView, DiffInline, side, 10, true, 0, false), "\n"))
	if !strings.Contains(panel, "hello") || !strings.Contains(panel, "bold") {
		t.Fatalf("panel is missing the file's prose:\n%s", panel)
	}
	if strings.Contains(panel, "# hello") || strings.Contains(panel, "**bold**") {
		t.Fatalf("markdown was highlighted as source rather than rendered:\n%s", panel)
	}

	// ctrl+q closes it, like every other quick view.
	got.closeSplit()
	if got.quickView != nil {
		t.Fatal("ctrl+q did not close the markdown quick view")
	}
}
