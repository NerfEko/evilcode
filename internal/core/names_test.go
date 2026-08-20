package core

import (
	"strings"
	"testing"
	"unicode/utf8"
)

// TestEmojiAreSingleSafeCodepoints enforces plan.md invariant 7. Emoji width
// bugs are the single largest source of TUI corruption, and a multi-codepoint
// glyph in a repaint-patched cell is the specific failure.
func TestEmojiAreSingleSafeCodepoints(t *testing.T) {
	tables := map[string][]Named{
		"creatures": Creatures,
		"modifiers": Modifiers,
	}
	extra := map[string]string{
		"fallback creature": FallbackCreature,
		"fallback modifier": FallbackModifier,
	}

	check := func(t *testing.T, label, glyph string) {
		t.Helper()
		if glyph == "" {
			t.Errorf("%s: empty glyph", label)
			return
		}
		if n := utf8.RuneCountInString(glyph); n != 1 {
			t.Errorf("%s: %q is %d codepoints, want exactly 1", label, glyph, n)
		}
		for _, r := range glyph {
			switch {
			case r == 0x200D:
				t.Errorf("%s: %q contains a zero-width joiner", label, glyph)
			case r == 0xFE0F:
				t.Errorf("%s: %q contains VS16; it is repaint-unstable and must be normalized away",
					label, glyph)
			case r == 0xFE0E:
				t.Errorf("%s: %q contains VS15", label, glyph)
			case r >= 0x1F3FB && r <= 0x1F3FF:
				t.Errorf("%s: %q contains a skin-tone modifier", label, glyph)
			}
		}
	}

	for name, table := range tables {
		for _, entry := range table {
			check(t, name+"/"+entry.Name, entry.Emoji)
		}
	}
	for label, glyph := range extra {
		check(t, label, glyph)
	}
}

// TestEmojiPredateUnicode13 is the second half of invariant 7: a codepoint
// assigned in Unicode 13 or later is not widely supported by installed fonts
// and renders as a placeholder box.
func TestEmojiPredateUnicode13(t *testing.T) {
	// The Unicode 13 additions live in these ranges; anything at or above
	// U+1FA70 was assigned in 12.0 or later, and the 13.0 additions that
	// matter here start at U+1FA80.
	unicode13OrLater := func(r rune) bool {
		switch {
		case r >= 0x1FA80 && r <= 0x1FAFF: // Symbols and Pictographs Extended-A
			return true
		case r >= 0x1F972 && r <= 0x1F977: // scattered 13.0 face additions
			return true
		}
		return false
	}
	for _, table := range [][]Named{Creatures, Modifiers} {
		for _, entry := range table {
			for _, r := range entry.Emoji {
				if unicode13OrLater(r) {
					t.Errorf("%s: %q (U+%04X) was added in Unicode 13 or later; "+
						"pick a widely-supported alternative", entry.Name, entry.Emoji, r)
				}
			}
		}
	}
}

func TestNamesAreUniqueAndWellFormed(t *testing.T) {
	for label, table := range map[string][]Named{"creatures": Creatures, "modifiers": Modifiers} {
		seen := map[string]bool{}
		for _, entry := range table {
			if entry.Name == "" {
				t.Errorf("%s: empty name", label)
			}
			if seen[entry.Name] {
				t.Errorf("%s: duplicate name %q", label, entry.Name)
			}
			seen[entry.Name] = true
			if strings.ContainsAny(entry.Name, " \t/\\") {
				t.Errorf("%s: name %q must be filesystem-safe — it becomes a session filename",
					label, entry.Name)
			}
			if entry.Name != strings.ToLower(entry.Name) {
				t.Errorf("%s: name %q should be lowercase", label, entry.Name)
			}
		}
	}
}

func TestCreatureTableIsLargeEnough(t *testing.T) {
	// plan.md §2.2 asks for about forty, so collisions stay rare.
	if len(Creatures) < 40 {
		t.Errorf("creatures = %d, want at least 40", len(Creatures))
	}
	if len(Modifiers) < 10 {
		t.Errorf("modifiers = %d, want at least 10", len(Modifiers))
	}
}

func TestEmojiLookup(t *testing.T) {
	if got := CreatureEmoji("bat"); got != "🦇" {
		t.Errorf("CreatureEmoji(bat) = %q", got)
	}
	if got := ModifierEmoji("crypt"); got != "⚰" {
		t.Errorf("ModifierEmoji(crypt) = %q", got)
	}
	if got := CreatureEmoji("unknown-thing"); got != FallbackCreature {
		t.Errorf("unknown creature = %q, want the fallback", got)
	}
	if got := ModifierEmoji("nope"); got != FallbackModifier {
		t.Errorf("unknown modifier = %q, want the fallback", got)
	}
	// A collision suffix still names the same creature.
	if got := CreatureEmoji("bat-2"); got != "🦇" {
		t.Errorf("CreatureEmoji(bat-2) = %q, want the bat glyph", got)
	}
	// A hyphen that is not a collision suffix must not be stripped.
	if got := CreatureEmoji("bat-wing"); got != FallbackCreature {
		t.Errorf("CreatureEmoji(bat-wing) = %q, want the fallback", got)
	}
}

func TestTitle(t *testing.T) {
	if got := Title("bat", "🦇"); got != "Bat 🦇" {
		t.Errorf("Title() = %q, want %q", got, "Bat 🦇")
	}
	if got := Title("crypt", "⚰"); got != "Crypt ⚰" {
		t.Errorf("Title() = %q", got)
	}
	if got := Title("éclair", "✨"); got != "Éclair ✨" {
		t.Errorf("Title() split a Unicode character: %q", got)
	}
}

func TestPickNameAvoidsCollisions(t *testing.T) {
	taken := map[string]bool{}
	seen := map[string]bool{}
	for i := 0; i < len(Creatures); i++ {
		name := PickName(Creatures, uint64(i*7), taken)
		if seen[name] {
			t.Fatalf("PickName returned %q twice", name)
		}
		seen[name] = true
		taken[name] = true
	}
	// Every name is now taken, so the next must get a numeric suffix.
	next := PickName(Creatures, 0, taken)
	if !strings.Contains(next, "-") {
		t.Errorf("PickName = %q, want a -N suffix once the table is exhausted", next)
	}
	if taken[next] {
		t.Errorf("PickName returned a taken name %q", next)
	}
}

func TestPickNameIsDeterministic(t *testing.T) {
	// EVILCODE_DETERMINISTIC depends on the same seed always naming the same
	// session (plan.md invariant 5).
	seed := SeedFrom("dracula")
	first := PickName(Creatures, seed, nil)
	for i := 0; i < 5; i++ {
		if got := PickName(Creatures, seed, nil); got != first {
			t.Fatalf("PickName is not deterministic: %q then %q", first, got)
		}
	}
}

func TestPickNameEmptyTable(t *testing.T) {
	if got := PickName(nil, 1, nil); got == "" {
		t.Error("PickName must always return something usable")
	}
}
