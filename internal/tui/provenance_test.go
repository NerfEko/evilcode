package tui

import (
	"testing"

	"evilcode/internal/theme"
)

// TestTranscriptLinesOwnerProvenance is the F1.2 reproduction: transcriptLines
// must record, for every line it emits, the index of the block that produced it
// (or -1 for chrome). A misalignment here misplaces every widget and every
// click, so the invariant is asserted at construction; this test asserts the
// *values* are right — that a row in the middle of each block names that block,
// and that an inter-block gap row is chrome (-1).
func TestTranscriptLinesOwnerProvenance(t *testing.T) {
	m := &Model{
		renderer: NewRenderer(theme.Dracula(), 80),
		blocks: []Block{
			{Kind: BlockUser, Text: "hello there this is a prompt", Number: 1},
			{Kind: BlockAssistant, Text: "here is a multi line answer\nfrom the model\nyes"},
			{Kind: BlockTool, ToolName: "read", ToolTarget: "a.go", ToolTokens: 12},
			{Kind: BlockTool, ToolName: "read", ToolTarget: "b.go", ToolTokens: 12},
		},
	}

	rows := m.transcriptLines()

	// Invariant: every line has an owner entry.
	if len(rows.Lines) != len(rows.Owner) {
		t.Fatalf("len(Lines)=%d != len(Owner)=%d", len(rows.Lines), len(rows.Owner))
	}

	// Every Owner value is in range (-1 or a valid block index).
	for i, o := range rows.Owner {
		if o < -1 || o >= len(m.blocks) {
			t.Fatalf("Owner[%d]=%d out of range [-1,%d)", i, o, len(m.blocks))
		}
	}

	// The header is chrome: its rows are owned by nothing.
	if len(rows.Owner) > 0 && rows.Owner[0] != -1 {
		t.Errorf("header row 0 has Owner %d, want -1 (chrome)", rows.Owner[0])
	}

	// Find the run of rows owned by each block and confirm they are contiguous
	// and in order — a block's rows must not be interleaved with another's.
	seen := map[int]int{} // block idx -> first row index
	for r, o := range rows.Owner {
		if o < 0 {
			continue
		}
		if first, ok := seen[o]; ok {
			// Any row between first and here owned by a different block is an
			// interleaving.
			for rr := first; rr < r; rr++ {
				if rows.Owner[rr] >= 0 && rows.Owner[rr] != o {
					t.Errorf("block %d rows are not contiguous: row %d owned by block %d between rows of block %d",
						o, rr, rows.Owner[rr], o)
				}
			}
		} else {
			seen[o] = r
		}
	}

	// Owner must be non-decreasing where it is not -1: blocks render in order.
	last := -1
	for r, o := range rows.Owner {
		if o < 0 {
			continue
		}
		if o < last {
			t.Errorf("Owner[%d]=%d goes backward (last block was %d); blocks must render in order",
				r, o, last)
		}
		last = o
	}

	// There is a gap between BlockAssistant (idx 1) and the first BlockTool
	// (idx 2) — different subjects, so needsGapAfter inserts a chrome "" row.
	// Confirm at least one -1 sits between the last row owned by block 1 and the
	// first row owned by block 2.
	assistantRows := rowsForBlock(rows.Owner, 1)
	toolRows := rowsForBlock(rows.Owner, 2)
	if len(assistantRows) == 0 || len(toolRows) == 0 {
		t.Fatalf("expected rows for blocks 1 and 2, got assistant=%d tool=%d",
			len(assistantRows), len(toolRows))
	}
	gapEnd := toolRows[0]
	gotGap := false
	for r := assistantRows[len(assistantRows)-1] + 1; r < gapEnd; r++ {
		if rows.Owner[r] == -1 {
			gotGap = true
		}
	}
	if !gotGap {
		t.Errorf("no chrome gap between block 1 and block 2; needsGapAfter should insert one")
	}

	// The two consecutive tool blocks are the same subject, so no gap: the row
	// immediately before the first row of block 3 is owned by block 2, not -1.
	tool2Rows := rowsForBlock(rows.Owner, 3)
	if len(tool2Rows) == 0 {
		t.Fatalf("expected rows for block 3")
	}
	prev := tool2Rows[0] - 1
	if prev < 0 || rows.Owner[prev] != 2 {
		t.Errorf("row before first block-3 row has Owner %d, want 2 (consecutive tools pack with no gap)",
			ownerOrSentinel(rows.Owner, prev))
	}
}

// rowsForBlock returns the row indices owned by the given block, in order.
func rowsForBlock(owner []int, block int) []int {
	var out []int
	for r, o := range owner {
		if o == block {
			out = append(out, r)
		}
	}
	return out
}

func ownerOrSentinel(owner []int, r int) int {
	if r < 0 || r >= len(owner) {
		return -999
	}
	return owner[r]
}

// TestTranscriptLinesWelcomeOwnerIsChrome covers the empty-transcript path: the
// welcome art is chrome, so every line is owned by -1.
func TestTranscriptLinesWelcomeOwnerIsChrome(t *testing.T) {
	m := &Model{
		renderer: NewRenderer(theme.Dracula(), 80),
	}
	rows := m.transcriptLines()
	if len(rows.Lines) == 0 {
		t.Fatal("welcome rendered no lines")
	}
	if len(rows.Lines) != len(rows.Owner) {
		t.Fatalf("len(Lines)=%d != len(Owner)=%d", len(rows.Lines), len(rows.Owner))
	}
	for i, o := range rows.Owner {
		if o != -1 {
			t.Errorf("welcome Owner[%d]=%d, want -1 (chrome)", i, o)
		}
	}
}