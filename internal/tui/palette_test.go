package tui

import (
	"strings"
	"testing"
)

func names(sugs []Suggestion) []string {
	out := make([]string, len(sugs))
	for i, s := range sugs {
		out[i] = s.Name
	}
	return out
}

func TestPrefixMatchBeatsFuzzy(t *testing.T) {
	// Exact typing must always win. `mod` is a literal prefix of `model`, and
	// however well another command fuzzy-matches, it cannot outrank that.
	cmds := []Command{
		{Name: "model", Help: "switch model"},
		{Name: "my-other-docs", Help: "a strong fuzzy match for m-o-d"},
	}
	got := names(RankCommands("mod", cmds))
	if len(got) == 0 || got[0] != "model" {
		t.Errorf("ranking = %v, want the prefix match first", got)
	}
}

func TestRankingPrefersShorterThenAlphabetical(t *testing.T) {
	cmds := []Command{
		{Name: "models"},
		{Name: "model"},
		{Name: "modelz"},
	}
	got := names(RankCommands("model", cmds))
	if got[0] != "model" {
		t.Errorf("ranking = %v, want the shortest exact prefix first", got)
	}
}

func TestEmptyQueryKeepsRegistryOrder(t *testing.T) {
	// The registry groups related commands; sorting an unfiltered list by
	// length would scatter that grouping for no benefit.
	cmds := []Command{{Name: "zebra"}, {Name: "a"}, {Name: "middle"}}
	got := names(RankCommands("", cmds))
	want := []string{"zebra", "a", "middle"}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("ranking = %v, want registry order %v", got, want)
		}
	}
}

func TestFuzzyMatchesSubsequence(t *testing.T) {
	cmds := []Command{
		{Name: "terminal-setup"},
		{Name: "quit"},
	}
	got := names(RankCommands("tsetup", cmds))
	if len(got) != 1 || got[0] != "terminal-setup" {
		t.Errorf("ranking = %v, want the subsequence match", got)
	}
}

func TestNoMatchesReturnsNothing(t *testing.T) {
	cmds := []Command{{Name: "model"}, {Name: "quit"}}
	if got := RankCommands("zzzz", cmds); len(got) != 0 {
		t.Errorf("ranking = %v, want nothing", names(got))
	}
}

func TestMatchedIndexesArePositions(t *testing.T) {
	cmds := []Command{{Name: "terminal-setup"}}
	got := RankCommands("tset", cmds)
	if len(got) != 1 {
		t.Fatalf("ranking = %v", names(got))
	}
	name := []rune("terminal-setup")
	for _, idx := range got[0].Matched {
		if idx < 0 || idx >= len(name) {
			t.Fatalf("matched index %d is out of range", idx)
		}
	}
	// The matched characters, in order, must spell the query.
	var spelled strings.Builder
	for _, idx := range got[0].Matched {
		spelled.WriteRune(name[idx])
	}
	if spelled.String() != "tset" {
		t.Errorf("matched indexes spell %q, want %q", spelled.String(), "tset")
	}
}

func TestPaletteRendersRows(t *testing.T) {
	r := testRenderer(80)
	rows := plainLines(r.RenderPalette(PaletteState{Query: "mod"}, VisibleCommands()))
	if len(rows) == 0 {
		t.Fatal("no rows")
	}
	joined := strings.Join(rows, "\n")
	if !strings.Contains(joined, "/model") {
		t.Errorf("palette missing /model:\n%s", joined)
	}
}

func TestPaletteSuppressed(t *testing.T) {
	// While an interactive prompt owns the composer, the palette must stay out
	// of the way entirely (plan.md §5.1).
	r := testRenderer(80)
	if rows := r.RenderPalette(PaletteState{Query: "m", Suppressed: true}, VisibleCommands()); rows != nil {
		t.Errorf("suppressed palette rendered %d rows", len(rows))
	}
}

func TestSingleSuggestionIsOneGoldRow(t *testing.T) {
	r := testRenderer(80)
	cmds := []Command{{Name: "model", Help: "switch model"}}
	rows := r.RenderPalette(PaletteState{Query: "model"}, cmds)
	if len(rows) != 1 {
		t.Fatalf("rows = %d, want 1", len(rows))
	}
	// One suggestion is the whole answer, so it is not split into
	// selected/unselected styling.
	if strings.Count(rows[0], "\x1b[38;2;") > 2 {
		t.Errorf("a single suggestion should be one uniform row:\n%q", rows[0])
	}
}

func TestPaletteWindowsLongLists(t *testing.T) {
	var cmds []Command
	for i := 0; i < 30; i++ {
		cmds = append(cmds, Command{Name: "cmd" + string(rune('a'+i)), Help: "x"})
	}
	r := testRenderer(80)
	rows := plainLines(r.RenderPalette(PaletteState{}, cmds))
	if len(rows) != PaletteRows {
		t.Errorf("rows = %d, want the window of %d", len(rows), PaletteRows)
	}
	if !strings.Contains(rows[len(rows)-1], "more") {
		t.Errorf("last row = %q, want an overflow hint", rows[len(rows)-1])
	}
}

func TestPaletteShowsScrolledPastCount(t *testing.T) {
	var cmds []Command
	for i := 0; i < 30; i++ {
		cmds = append(cmds, Command{Name: "cmd" + string(rune('a'+i)), Help: "x"})
	}
	r := testRenderer(80)
	rows := plainLines(r.RenderPalette(PaletteState{Selected: 20}, cmds))
	if !strings.Contains(rows[0], "↑") {
		t.Errorf("first row = %q, want an indicator for the items above", rows[0])
	}
}

func TestPaletteHighlightRecolorsRatherThanUnderlines(t *testing.T) {
	// §5.1 is explicit that the match is a recolor, not an underline: an
	// underline would fight the color.
	r := testRenderer(80)
	rows := r.RenderPalette(PaletteState{Query: "mod"}, VisibleCommands())
	joined := strings.Join(rows, "\n")
	if strings.Contains(joined, "\x1b[4m") {
		t.Error("palette used an underline; the match must be a recolor")
	}
	if !strings.Contains(joined, "\x1b[1;") && !strings.Contains(joined, ";1m") {
		t.Error("matched characters should be bold")
	}
}

func TestSelectionWraps(t *testing.T) {
	// Wrapping is what makes a short list feel like a ring rather than a dead
	// end at either edge.
	if got := MovePaletteSelection(0, -1, 5); got != 4 {
		t.Errorf("moving up from the top = %d, want 4", got)
	}
	if got := MovePaletteSelection(4, 1, 5); got != 0 {
		t.Errorf("moving down from the bottom = %d, want 0", got)
	}
	if got := MovePaletteSelection(2, 1, 5); got != 3 {
		t.Errorf("ordinary move = %d, want 3", got)
	}
	if got := MovePaletteSelection(3, 1, 0); got != 0 {
		t.Errorf("empty list = %d, want 0", got)
	}
}

func TestCommandRegistryIsWellFormed(t *testing.T) {
	seen := map[string]bool{}
	for _, c := range Commands {
		if c.Name == "" {
			t.Error("a command has no name")
		}
		if seen[c.Name] {
			t.Errorf("duplicate command %q", c.Name)
		}
		seen[c.Name] = true
		if strings.HasPrefix(c.Name, "/") {
			t.Errorf("command %q should not include its slash", c.Name)
		}
		if !c.Hidden && c.Help == "" {
			t.Errorf("visible command %q has no help text", c.Name)
		}
	}
}

func TestHiddenCommandsAreFindableButNotOffered(t *testing.T) {
	// An alias should work when typed without cluttering the list.
	if _, ok := FindCommand("cls"); !ok {
		t.Error("hidden alias /cls should still resolve")
	}
	for _, c := range VisibleCommands() {
		if c.Name == "cls" {
			t.Error("hidden alias /cls should not be offered in the palette")
		}
	}
}

func TestHelpTextCoversEveryVisibleCommand(t *testing.T) {
	// Built from the registry, so a newly registered command can never be
	// invisible (plan.md §5.5).
	help := helpText()
	for _, c := range VisibleCommands() {
		if !strings.Contains(help, "/"+c.Name) {
			t.Errorf("help is missing /%s", c.Name)
		}
	}
}
