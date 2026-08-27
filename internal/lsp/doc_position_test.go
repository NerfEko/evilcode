package lsp

import (
	"os"
	"path/filepath"
	"testing"
)

// H5.21: docPosition took a 1-based rune column (the units the read tool
// prints) and sent it straight through as the protocol's UTF-16 code unit
// column. Those agree for any BMP character (an accented letter is one rune
// and one UTF-16 unit), so this only misfires with a character outside the
// BMP — an emoji needs a surrogate pair, one rune but two UTF-16 units — which
// is why the byte-offset bug (H1.4) and this one need different fixtures to
// reproduce.
func TestDocPositionConvertsRuneColumnPastAnAstralCharacter(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "a.go")
	// "old" starts at rune column 11 (x, sp, :, =, sp, ", 🔥, ", ;, sp, o = 11th
	// rune) but at UTF-16 offset 11 (0-based) because 🔥 costs two units, not
	// one — a one-unit gap between the two countings.
	text := `x := "🔥"; old := 1` + "\n"
	if err := os.WriteFile(path, []byte(text), 0o644); err != nil {
		t.Fatal(err)
	}

	params, err := docPosition(path, 1, 11)
	if err != nil {
		t.Fatal(err)
	}
	pos := params["position"].(map[string]any)
	if pos["character"] != 11 {
		t.Errorf("character = %v, want 11 (the UTF-16 offset of 'old', not rune column - 1 = 10)", pos["character"])
	}
	if pos["line"] != 0 {
		t.Errorf("line = %v, want 0", pos["line"])
	}
}

// A line with only BMP characters (bytes and UTF-16 units may still diverge,
// but runes and UTF-16 units do not) must convert to exactly rune column - 1.
func TestDocPositionAgreesWithRuneColumnWhenNoAstralCharacters(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "a.go")
	text := `x := "héllo"; old := 1` + "\n"
	if err := os.WriteFile(path, []byte(text), 0o644); err != nil {
		t.Fatal(err)
	}

	// "old" starts at rune column 15 (and UTF-16 offset 14 — no astral
	// character, so the two agree, unlike the byte offset would).
	params, err := docPosition(path, 1, 15)
	if err != nil {
		t.Fatal(err)
	}
	pos := params["position"].(map[string]any)
	if pos["character"] != 14 {
		t.Errorf("character = %v, want 14", pos["character"])
	}
}

func TestRuneToUTF16(t *testing.T) {
	cases := []struct {
		line string
		col  int
		want int
	}{
		{"old := 1", 1, 1},
		{"héllo old", 7, 7}, // é is one rune, one UTF-16 unit: no shift
		{"🔥 old", 1, 1},     // before the emoji: no shift yet
		{"🔥 old", 3, 4},     // past the emoji: one rune, two units
		{"🔥🔥 old", 5, 7},    // two astral characters before: two-unit shift
	}
	for _, tc := range cases {
		got, err := runeToUTF16(tc.line, tc.col)
		if err != nil {
			t.Fatalf("runeToUTF16(%q, %d): %v", tc.line, tc.col, err)
		}
		if got != tc.want {
			t.Errorf("runeToUTF16(%q, %d) = %d, want %d", tc.line, tc.col, got, tc.want)
		}
	}
}

func TestRuneToUTF16RefusesOutOfRange(t *testing.T) {
	if _, err := runeToUTF16("abc", 0); err == nil {
		t.Error("column 0 (not 1-based) was accepted")
	}
	if _, err := runeToUTF16("abc", 5); err == nil {
		t.Error("a column past the end of the line was accepted")
	}
}
