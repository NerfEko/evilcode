package tui

import (
	"context"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
)

func shellRow(t *testing.T, m *Model, needle string) int {
	t.Helper()
	rows := m.transcriptLines()
	for i, line := range rows.Lines {
		if rows.Owner[i] == 0 && strings.Contains(plain(line), needle) {
			return i
		}
	}
	t.Fatalf("could not find %q in the rendered shell block", needle)
	return -1
}

func TestShellFenceClickCopiesCommand(t *testing.T) {
	m := clickModel([]Block{{
		Kind: BlockAssistant,
		Text: "Run this:\n\n```bash\nprintf '%s\\n' hello\n```",
	}}, t.TempDir())
	row := shellRow(t, m, "printf")

	oldWriter := clipboardTextWriter
	t.Cleanup(func() { clipboardTextWriter = oldWriter })
	var got string
	clipboardTextWriter = func(_ context.Context, text string) error {
		got = text
		return nil
	}

	model, cmd := m.Update(tea.MouseClickMsg{
		X: 2, Y: row, Button: tea.MouseLeft,
	})
	if cmd == nil {
		t.Fatal("clicking a bash fence did not start a clipboard command")
	}
	msg, ok := cmd().(clipboardText)
	if !ok {
		t.Fatalf("clipboard command returned %T, want clipboardText", cmd())
	}
	model.(*Model).applyClipboardText(msg)
	if got != "printf '%s\\n' hello" {
		t.Fatalf("copied command = %q", got)
	}
	if model.(*Model).notice != "Command copied to clipboard" {
		t.Fatalf("notice = %q, want copy confirmation", model.(*Model).notice)
	}
}

func TestShellFenceHoverUsesJaggedUnderline(t *testing.T) {
	m := clickModel([]Block{{
		Kind: BlockAssistant,
		Text: "```fish\necho hello\n```",
	}}, t.TempDir())
	row := shellRow(t, m, "echo")

	m.Update(tea.MouseMotionMsg(tea.Mouse{X: 2, Y: row}))
	rows := m.transcriptLines()
	if !strings.Contains(rows.Lines[row], "\x1b[4:3m") {
		t.Fatalf("hovered shell line lacks jagged underline: %q", rows.Lines[row])
	}

	m.Update(tea.MouseMotionMsg(tea.Mouse{X: 2, Y: 0}))
	rows = m.transcriptLines()
	for _, line := range rows.Lines {
		if strings.Contains(line, "\x1b[4:3m") {
			t.Fatalf("jagged underline remained after leaving the fence: %q", line)
		}
	}
}

func TestToolHoverUsesJaggedUnderline(t *testing.T) {
	m := clickModel([]Block{{
		Kind: BlockTool, ToolName: "read", ToolTarget: "main.go", ToolPath: "main.go",
	}}, t.TempDir())
	row := ownerRow(t, m, 0)
	m.Update(tea.MouseMotionMsg(tea.Mouse{X: 2, Y: row}))
	rows := m.transcriptLines()
	if !strings.Contains(rows.Lines[row], "\x1b[4:3m") {
		t.Fatalf("hovered tool target lacks jagged underline: %q", rows.Lines[row])
	}
}

func TestShellCommandForClipboardDropsCommentsAndTrailingWhitespace(t *testing.T) {
	source := "  # section note\n" +
		"sudo -ll                 # more verbose list\n" +
		"sudo -k\t# reset the cached timestamp\n" +
		"sudo -v                 # refreshes the timestamp\n" +
		"\n"
	got := shellCommandForClipboard(source)
	want := "sudo -ll\nsudo -k\nsudo -v"
	if got != want {
		t.Fatalf("cleaned command = %q, want %q", got, want)
	}
}

func TestShellCommandForClipboardPreservesQuotedHashes(t *testing.T) {
	source := "printf '%s\\n' '# keep this' # display note\n" +
		"echo \"# also keep this\" # display note\n" +
		"echo \\#literal # display note"
	want := "printf '%s\\n' '# keep this'\necho \"# also keep this\"\necho \\#literal"
	if got := shellCommandForClipboard(source); got != want {
		t.Fatalf("quoted hash handling = %q, want %q", got, want)
	}
}
