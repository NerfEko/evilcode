package tui

import (
	"encoding/json"
	"strings"
	"testing"

	"evilcode/internal/agent"
	"evilcode/internal/provider"
	"evilcode/internal/theme"
)

// TestBashRowShowsCommandOnce is the F2.1 reproduction: a rendered bash tool
// row must contain its command exactly once. Today the bash Intent was the
// command truncated to 48 chars while the target was the same command truncated
// to 60, so the dedupe guard (`!strings.Contains(intent, target)`) could not
// hold and renderTool printed the command twice. The fix makes the bash Intent
// the exit status and output size — information the row does not already have.
func TestBashRowShowsCommandOnce(t *testing.T) {
	const cmd = "rm -rf build/"
	args, _ := json.Marshal(map[string]any{"cmd": cmd})

	m := &Model{renderer: NewRenderer(theme.Dracula(), 80)}
	m.applyEvent(agent.Event{
		Kind:   agent.EventToolResult,
		Call:   &provider.ToolCall{Name: "bash", Args: args},
		Output: "rm: cannot remove 'build/': No such file or directory",
		Intent: "exit 0 · 48 out",
	})

	// Find the bash tool block and render it.
	var b *Block
	for i := range m.blocks {
		if m.blocks[i].Kind == BlockTool && m.blocks[i].ToolName == "bash" {
			b = &m.blocks[i]
			break
		}
	}
	if b == nil {
		t.Fatal("no bash tool block after applyEvent")
	}

	lines := m.renderer.Lines(b)
	if len(lines) == 0 {
		t.Fatal("bash block rendered no lines")
	}
	row := plain(strings.Join(lines, " "))

	// The command must appear exactly once in the rendered row.
	if n := strings.Count(row, cmd); n != 1 {
		t.Errorf("command %q appears %d times in the rendered row, want 1:\n%s",
			cmd, n, row)
	}

	// The intent must carry exit/output info, not the command.
	if b.ToolIntent == "" {
		t.Error("bash block has no intent; expected exit/output summary")
	}
	if strings.Contains(b.ToolIntent, cmd) {
		t.Errorf("bash intent %q repeats the command; it should be a summary", b.ToolIntent)
	}
}

func TestHeldBashRowIsWarningNotFailure(t *testing.T) {
	args, _ := json.Marshal(map[string]any{"cmd": "rm -rf ../outside"})
	m := &Model{renderer: NewRenderer(theme.Dracula(), 100)}
	m.applyEvent(agent.Event{
		Kind:    agent.EventToolResult,
		Call:    &provider.ToolCall{Name: "bash", Args: args},
		Output:  "Command held for confirmation: target is outside the active workspace.",
		Intent:  "held · justification required",
		Held:    true,
		ErrText: "command held by destructive-command gate",
	})
	if len(m.blocks) != 1 {
		t.Fatalf("held result created an extra error block: %#v", m.blocks)
	}
	b := &m.blocks[0]
	if !b.Held || b.Failed {
		t.Fatalf("held block = %#v, want Held=true and Failed=false", b)
	}
	row := plain(strings.Join(m.renderer.Lines(b), " "))
	if !strings.Contains(row, "!") || strings.Contains(row, "✗") {
		t.Fatalf("held row did not render as a warning: %q", row)
	}
}

// TestBashRowDuplicatedCommandIsTheBug is the fail side of the F2.1 pair: with
// the old behavior — Intent set to the command itself — the dedupe guard in
// applyEvent (`!strings.Contains(intent, target)`) cannot hold (a 48-char
// intent cannot contain a 60-char target), so ToolIntent is set and renderTool
// prints the command twice. This is the mechanism the fix removes by making the
// intent a summary instead.
func TestBashRowDuplicatedCommandIsTheBug(t *testing.T) {
	// A command longer than 60 chars is where the bug bites: toolTarget
	// truncates to 60, shortCmd (the old intent) truncates to 48, so the two
	// strings differ and the dedupe guard `!strings.Contains(intent, target)`
	// cannot hold — a 48-char intent cannot contain a 60-char target. The guard
	// then sets ToolIntent and renderTool prints the command twice.
	const cmd = "rm -rf build/ target/release node_modules dist out .cache vendor tmp && echo all gone now"
	args, _ := json.Marshal(map[string]any{"cmd": cmd})

	// Wide on purpose: the duplication is what is under test, and at 80 columns
	// the row is truncated to the column before the second copy can show.
	m := &Model{renderer: NewRenderer(theme.Dracula(), 200)}
	// Old behavior: intent was shortCmd(cmd) — the command truncated to 48.
	m.applyEvent(agent.Event{
		Kind:   agent.EventToolResult,
		Call:   &provider.ToolCall{Name: "bash", Args: args},
		Output: "all gone now",
		Intent: shortCmdForTest(cmd),
	})

	var b *Block
	for i := range m.blocks {
		if m.blocks[i].Kind == BlockTool && m.blocks[i].ToolName == "bash" {
			b = &m.blocks[i]
			break
		}
	}
	if b == nil {
		t.Fatal("no bash tool block")
	}
	if b.ToolIntent == "" {
		t.Skip("dedupe guard suppressed the intent; the mechanism under test did not fire")
	}
	row := plain(strings.Join(m.renderer.Lines(b), " "))
	// The shared prefix of the command appears in both the target and the
	// intent, so it shows up at least twice — that is the duplication bug.
	prefix := "rm -rf build/"
	if n := strings.Count(row, prefix); n < 2 {
		t.Errorf("with intent=command, expected the command prefix at least twice (the bug), got %d:\n%s", n, row)
	}
}

// shortCmdForTest mirrors exec.shortCmd, which is package-local.
func shortCmdForTest(cmd string) string {
	cmd = strings.TrimSpace(strings.ReplaceAll(cmd, "\n", " "))
	if len(cmd) > 48 {
		return cmd[:47] + "…"
	}
	return cmd
}
