// Package core holds the small shared vocabulary: session names and IDs.
package core

import (
	"fmt"
	"hash/fnv"
	"strings"
)

// Named is one entry in a name table: a word and exactly one safe emoji.
//
// "Safe" is load-bearing. Every glyph here is a single codepoint with no
// variation selector and no zero-width joiner, because a multi-codepoint glyph
// in a repaint-patched cell corrupts the terminal (plan.md invariant 7). A unit
// test enforces this; do not add an entry without reading it.
type Named struct {
	Name  string
	Emoji string
}

// Creatures name clients and sessions (plan.md §2.2).
var Creatures = []Named{
	{"bat", "🦇"}, {"snake", "🐍"}, {"dracula", "🧛"}, {"raven", "☾"},
	{"wraith", "👻"}, {"viper", "🐍"}, {"spider", "🕷"}, {"crow", "🐦"},
	{"wolf", "🐺"}, {"imp", "👿"}, {"ghoul", "💀"}, {"hex", "✴"},
	{"omen", "☄"}, {"banshee", "🌀"}, {"lich", "⚱"}, {"moth", "🦋"},
	{"serpent", "🐉"}, {"widow", "🕸"}, {"shade", "🌑"}, {"fang", "🗡"},
	{"talon", "🦅"}, {"thorn", "🌹"}, {"ember", "🔥"}, {"frost", "❄"},
	// Extended to forty so collisions stay rare in a long-lived install.
	{"gargoyle", "🗿"}, {"kraken", "🦑"}, {"leech", "🐛"}, {"scorpion", "🦂"},
	{"owl", "🦉"}, {"toad", "🐸"}, {"rat", "🐀"}, {"shark", "🦈"},
	{"wisp", "✨"}, {"blight", "🥀"}, {"storm", "⛈"}, {"nebula", "🌌"},
	{"dusk", "🌇"}, {"venom", "🧪"}, {"curse", "🧿"}, {"dread", "😈"},
}

// Modifiers name servers (plan.md §2.2).
var Modifiers = []Named{
	{"crypt", "⚰"}, {"coven", "🕯"}, {"lair", "🌋"}, {"abyss", "🕳"},
	{"tomb", "🏛"}, {"manor", "🏚"}, {"catacomb", "🦴"}, {"altar", "🔮"},
	{"gallows", "🌘"}, {"hollow", "🌲"},
}

// Fallback glyphs for a name that is not in either table.
const (
	FallbackCreature = "💫"
	FallbackModifier = "🔮"
)

// CreatureEmoji returns the glyph for a creature name, or the fallback.
func CreatureEmoji(name string) string {
	return lookup(Creatures, name, FallbackCreature)
}

// ModifierEmoji returns the glyph for a modifier name, or the fallback.
func ModifierEmoji(name string) string {
	return lookup(Modifiers, name, FallbackModifier)
}

func lookup(table []Named, name, fallback string) string {
	// A collision suffix (`bat-2`) still names the same creature.
	base := name
	if i := strings.LastIndex(base, "-"); i > 0 && isDigits(base[i+1:]) {
		base = base[:i]
	}
	for _, n := range table {
		if n.Name == base {
			return n.Emoji
		}
	}
	return fallback
}

func isDigits(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

// Title renders a name for display: `Bat 🦇`.
func Title(name, emoji string) string {
	if name == "" {
		return emoji
	}
	return strings.ToUpper(name[:1]) + name[1:] + " " + emoji
}

// PickName chooses a name from a table, avoiding those already taken. A taken
// name gets a `-2`, `-3` suffix rather than being skipped, so a long-running
// install keeps recognizable names instead of drifting into the obscure tail.
func PickName(table []Named, seed uint64, taken map[string]bool) string {
	if len(table) == 0 {
		return "session"
	}
	start := int(seed % uint64(len(table)))
	for i := 0; i < len(table); i++ {
		name := table[(start+i)%len(table)].Name
		if !taken[name] {
			return name
		}
	}
	// Every name is in use; suffix the seeded one.
	base := table[start].Name
	for n := 2; ; n++ {
		candidate := fmt.Sprintf("%s-%d", base, n)
		if !taken[candidate] {
			return candidate
		}
	}
}

// SeedFrom hashes a string into a table seed, so the same input always yields
// the same name — which is what makes EVILCODE_DETERMINISTIC reproducible.
func SeedFrom(s string) uint64 {
	h := fnv.New64a()
	h.Write([]byte(s))
	return h.Sum64()
}
