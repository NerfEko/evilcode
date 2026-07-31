package tui

import (
	"strings"
	"testing"

	"charm.land/lipgloss/v2"
)

// widgetColumns reports the column each widget line starts at, which is what a
// torn box shows up as.
func widgetColumns(rows []string, markers string) []int {
	var out []int
	for _, row := range rows {
		if i := strings.IndexAny(row, markers); i >= 0 {
			out = append(out, lipgloss.Width(row[:i]))
		}
	}
	return out
}

func TestPaintWidgetUsesOneColumn(t *testing.T) {
	// The bug: lines over prose were padded against their own row and landed at
	// a different column from their blank neighbours, so one box rendered as
	// three fragments at three columns.
	rows := []string{
		"",
		"Starting the build.",
		"",
		"  ✓ bash sleep 2 && echo built",
		"",
	}
	lines := []string{"╭──╮", "│ab│", "│cd│", "│ef│", "╰──╯"}
	paintWidget(rows, lines, 0, 60, len(rows), 140)

	cols := widgetColumns(rows, "╭│╰")
	if len(cols) != 5 {
		t.Fatalf("drew %d of 5 lines:\n%s", len(cols), strings.Join(rows, "\n"))
	}
	for _, c := range cols[1:] {
		if c != cols[0] {
			t.Fatalf("box columns = %v, want all equal:\n%s", cols, strings.Join(rows, "\n"))
		}
	}
}

func TestPaintWidgetClearsTheWidestRow(t *testing.T) {
	// A row wider than the requested column pushes the whole box right, rather
	// than the box overlapping that one row's text.
	rows := []string{"", strings.Repeat("x", 70), ""}
	paintWidget(rows, []string{"╭╮", "││", "╰╯"}, 0, 20, len(rows), 140)

	col := widgetColumns(rows, "╭")[0]
	if col < 70+WidgetGap {
		t.Errorf("box starts at %d, overlapping a %d-cell row", col, 70)
	}
}

func TestPaintWidgetDropsABoxThatNoLongerFits(t *testing.T) {
	// Drawing past the right edge makes the terminal wrap the line, which
	// pushes every row below it down — the exact jump invariant 4 forbids.
	rows := []string{strings.Repeat("x", 130), strings.Repeat("x", 130)}
	before := append([]string(nil), rows...)
	paintWidget(rows, []string{"╭────────╮", "╰────────╯"}, 0, 100, len(rows), 140)

	for i := range rows {
		if rows[i] != before[i] {
			t.Errorf("row %d was drawn over: %q", i, rows[i])
		}
	}
}

func TestPaintWidgetSkipsRowsPastTheLimit(t *testing.T) {
	// The composer and status line own their rows outright.
	rows := []string{"", "", ""}
	paintWidget(rows, []string{"╭╮", "││", "╰╯"}, 1, 10, 2, 140)

	if strings.Contains(rows[2], "╰") {
		t.Errorf("a widget line landed past the transcript region: %q", rows[2])
	}
	if !strings.Contains(rows[1], "╭") {
		t.Errorf("the first line should still be drawn: %q", rows[1])
	}
}
