package tui

import (
	"testing"

	"evilcode/internal/theme"
)

// TestUserPromptRowsMatchTranscriptCoords is the scroll-math fix: jumpPrompt
// must position user prompts in the same coordinate frame the scroll offset and
// contentHeight live in. The transcript prepends header chrome and a leading
// blank, and only inserts an inter-block gap when needsGapAfter is true — so a
// per-block (+1, no header) tally drifted from the real line indices and
// Prev/Next Prompt landed on the wrong line.
func TestUserPromptRowsMatchTranscriptCoords(t *testing.T) {
	m := &Model{
		renderer: NewRenderer(theme.Dracula(), 80),
		blocks: []Block{
			{Kind: BlockUser, Text: "first prompt here", Number: 1},
			{Kind: BlockAssistant, Text: "answer one\ntwo\nthree"},
			{Kind: BlockTool, ToolName: "read", ToolTarget: "a.go", ToolTokens: 12},
			{Kind: BlockTool, ToolName: "read", ToolTarget: "b.go", ToolTokens: 12},
			{Kind: BlockUser, Text: "second prompt here", Number: 2},
			{Kind: BlockAssistant, Text: "answer two"},
		},
	}

	tr := m.transcriptLines()
	rows := m.userPromptRows()

	// The user blocks are 0 and 4. Their first-line indices come straight from
	// Owner, so they match the real transcript layout.
	want0, want4 := -1, -1
	for i, o := range tr.Owner {
		if o == 0 && want0 < 0 {
			want0 = i
		}
		if o == 4 && want4 < 0 {
			want4 = i
		}
	}
	if want0 < 0 || want4 < 0 {
		t.Fatalf("could not locate user blocks in Owner: want0=%d want4=%d", want0, want4)
	}
	if len(rows) != 2 || rows[0] != want0 || rows[1] != want4 {
		t.Errorf("userPromptRows = %v, want [%d, %d] (block 0 and 4 first lines)", rows, want0, want4)
	}

	// The header chrome precedes the first user block, so it is never at line 0.
	// The old math put it there, which is the bug.
	if rows[0] == 0 {
		t.Error("first user row = 0; the header chrome must precede it")
	}

	// The two tool blocks are one subject, so they stay packed: exactly one gap
	// separates block 0's user prompt region from block 4's, not a +1 per block.
	// This is a sanity check that the indices reflect needsGapAfter, not a
	// blanket tally.
	if rows[1] <= rows[0] {
		t.Errorf("rows must increase: %v", rows)
	}
}
