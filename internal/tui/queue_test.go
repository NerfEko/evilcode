package tui

import (
	"strings"
	"testing"

	"evilcode/internal/agent"
	"evilcode/internal/theme"
	"evilcode/internal/todo"
)

// A prompt submitted while a turn is running must not appear in the
// transcript as if it were sent: it waits on the queue strip above the
// composer until the daemon actually starts its turn.
func TestSubmitWhileProcessingQueuesAboveComposer(t *testing.T) {
	m := newTestModel(t)
	m.processing = true
	before := m.promptCount
	m.submit("hold this", 60)

	if len(m.blocks) != 0 {
		t.Fatalf("blocks = %+v, want the queued prompt kept out of the transcript", m.blocks)
	}
	if m.promptCount != before {
		t.Errorf("prompt count advanced %d -> %d while queued", before, m.promptCount)
	}
	if len(m.queuedTexts) != 1 || m.queuedTexts[0] != "hold this" {
		t.Fatalf("queuedTexts = %v, want [hold this]", m.queuedTexts)
	}
	if m.editor.Text != "" {
		t.Errorf("editor = %q, want cleared after queueing", m.editor.Text)
	}

	// The daemon starts the queued turn: the matching TurnStart draws the
	// prompt and takes it off the strip.
	m.applyEvent(agent.Event{Kind: agent.EventTurnStart, Text: "hold this"})
	if len(m.queuedTexts) != 0 {
		t.Errorf("queuedTexts = %v, want consumed by the turn start", m.queuedTexts)
	}
	if len(m.blocks) != 1 || m.blocks[0].Kind != BlockUser || m.blocks[0].Text != "hold this" {
		t.Fatalf("blocks = %+v, want the prompt drawn when its turn starts", m.blocks)
	}
	if m.promptCount != before+1 {
		t.Errorf("prompt count = %d, want %d", m.promptCount, before+1)
	}
}

// An attached session keeps its queue strip across turn end: the daemon
// launches each queued prompt as its own turn after the running one ends.
func TestAttachedTurnEndKeepsQueuedStrip(t *testing.T) {
	m := newTestModel(t)
	m.remoteCommand = func(kind, arg, secret string) error { return nil }
	m.processing = true
	m.submit("hold this", 60)

	m.applyEvent(agent.Event{Kind: agent.EventTurnEnd})
	if len(m.queuedTexts) != 1 {
		t.Fatalf("queuedTexts = %v, want the entry kept for the daemon's queued turn", m.queuedTexts)
	}
}

// The daemon rejects the newest queued prompt when its queue is full; the
// strip entry for it must not linger.
func TestQueuedPromptDroppedOnRejectionNotice(t *testing.T) {
	m := newTestModel(t)
	m.processing = true
	m.submit("first", 60)
	m.submit("second", 60)

	m.applyEvent(agent.Event{
		Kind:  agent.EventNotice,
		Level: agent.LevelError,
		Text:  "queued input rejected: the session's queue is full (max 64 prompts or 16 MiB); wait for the current turn to end",
	})
	if len(m.queuedTexts) != 1 || m.queuedTexts[0] != "first" {
		t.Fatalf("queuedTexts = %v, want the rejected newest entry dropped", m.queuedTexts)
	}
}

// Queued prompts render as a numbered strip in the same slot as the local
// pending rows.
func TestRenderQueuedPrompts(t *testing.T) {
	r := NewRenderer(theme.Dracula(), 60)
	rows := r.RenderQueuedPrompts([]string{"one", "two"})
	if len(rows) != 2 {
		t.Fatalf("rendered %d rows, want 2", len(rows))
	}
	if !strings.Contains(rows[0], "one") || !strings.Contains(rows[1], "two") {
		t.Errorf("rows = %q, want each queued text shown", rows)
	}
}

// Editor.Up/Down navigate between lines of a multi-line prompt, keeping the
// column where the line is long enough and clamping to a shorter line, with
// no-ops at the first/last line.
func TestEditorUpDownMovesBetweenLines(t *testing.T) {
	e := &Editor{Text: "alpha\nbeta\ngamma"}

	e.Cursor = len("alpha\nbeta\ngamma") - 1 // end of "gamma" (col 4)
	e.Down()                             // no-op on the last line
	if e.Cursor != len("alpha\nbeta\ngamma")-1 {
		t.Fatalf("Down on last line moved cursor to %d", e.Cursor)
	}
	e.Up() // end of "beta" (col 4 preserved)
	if e.Cursor != len("alpha\nbeta") {
		t.Fatalf("Up to the third line: cursor = %d, want %d", e.Cursor, len("alpha\nbeta"))
	}
	e.Up() // "alpha" col 4 (clamped to its end)
	if e.Cursor != 4 {
		t.Fatalf("Up to the second line: cursor = %d, want 4", e.Cursor)
	}
	e.Up() // no-op on the first line
	if e.Cursor != 4 {
		t.Fatalf("Up on the first line moved cursor to %d", e.Cursor)
	}
	e.Down() // back to "beta" col 4
	if e.Cursor != len("alpha\nbeta") {
		t.Fatalf("Down from the first line: cursor = %d, want %d", e.Cursor, len("alpha\nbeta"))
	}
	e.Down() // and to "gamma" col 4
	if e.Cursor != len("alpha\nbeta\ngamma")-1 {
		t.Fatalf("Down to the last line: cursor = %d, want %d", e.Cursor, len("alpha\nbeta\ngamma")-1)
	}
}

func TestEditorUpDownKeepsColumn(t *testing.T) {
	e := &Editor{Text: "abc\ndefgh\nij"}
	e.Cursor = len("abc\n") + 3 // "defgh" column 3
	e.Up()
	if want := 3; e.Cursor != want {
		t.Fatalf("Up: cursor = %d, want %d (column kept)", e.Cursor, want)
	}
	e.Down()
	if want := len("abc\n") + 3; e.Cursor != want {
		t.Fatalf("Down: cursor = %d, want %d (column kept)", e.Cursor, want)
	}
	// A shorter line clamps: "ij" col 2 up to "defgh" col 2, then up to
	// "abc" (3 chars) which clamps to its end.
	e2 := &Editor{Text: "abc\ndefgh\nij"}
	e2.Cursor = len("abc\ndefgh\nij") // end of "ij"
	e2.Up()
	if e2.Cursor != 6 { // "defgh" col 2 (column preserved)
		t.Fatalf("clamped Up: cursor = %d, want 6", e2.Cursor)
	}
	e2.Up()
	if e2.Cursor != 2 {
		t.Fatalf("clamped Up to a 3-char line: cursor = %d, want 2", e2.Cursor)
	}
	e2.Down()
	if want := len("abc\n") + 2; e2.Cursor != want {
		t.Fatalf("Down after clamp: cursor = %d, want %d", e2.Cursor, want)
	}
}

// percentOf rounds to the nearest whole percent instead of flooring, so a
// meter reads 95% at 94.6% and caps at 100 when the window is full.
func TestPercentOfRounds(t *testing.T) {
	cases := []struct {
		used, total, want int
	}{
		{0, 200000, 0},
		{406, 200000, 0},
		{189200, 200000, 95},  // 94.6% rounds up
		{190000, 200000, 95},  // 95.0%
		{194000, 200000, 97},  // 97.0%
		{199999, 200000, 100}, // 99.9995% rounds to 100
		{200000, 200000, 100},
		{201000, 200000, 100}, // overshoot clamps
	}
	for _, c := range cases {
		if got := percentOf(c.used, c.total); got != c.want {
			t.Errorf("percentOf(%d, %d) = %d, want %d", c.used, c.total, got, c.want)
		}
	}
}

// The todo progress figure is exact status-based math (not the per-item
// confidence scores), carried on the pips row so an all-closed group reads
// 100% without adding a row that would shift the layout.
func TestTodoProgressIsExact(t *testing.T) {
	r := NewRenderer(theme.Dracula(), 60)
	items := []todo.Item{
		{ID: "1", Content: "a", Status: todo.StatusCompleted},
		{ID: "2", Content: "b", Status: todo.StatusCompleted},
		{ID: "3", Content: "c", Status: todo.StatusCompleted},
		{ID: "4", Content: "d", Status: todo.StatusCompleted},
	}
	if got := todoProgressSuffix(4, 4); !strings.Contains(got, "4/4 done") || !strings.Contains(got, "100%") {
		t.Errorf("all-done suffix = %q, want 4/4 done · 100%%", got)
	}
	header := r.todoGroupHeader("auth flow", items)
	if !strings.Contains(header, "●●●●") || !strings.Contains(header, "100%") {
		t.Errorf("all-done group header = %q, want full pips and 100%%", header)
	}
	mixed := r.todoGroupHeader("auth flow", []todo.Item{
		{ID: "1", Content: "a", Status: todo.StatusCompleted},
		{ID: "2", Content: "b", Status: todo.StatusPending},
		{ID: "3", Content: "c", Status: todo.StatusPending},
		{ID: "4", Content: "d", Status: todo.StatusPending},
	})
	if !strings.Contains(mixed, "1/4 done") || !strings.Contains(mixed, "25%") {
		t.Errorf("mixed group header = %q, want 1/4 done · 25%%", mixed)
	}
	if empty := r.todoPips(nil); empty != "" {
		t.Errorf("empty pips = %q, want none", empty)
	}
}
