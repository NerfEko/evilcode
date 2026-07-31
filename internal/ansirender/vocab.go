package ansirender

// GlyphVocabulary is the proven single-cell set from plan.md §2.3, plus the
// emoji inventory and the session-name glyphs from §2.2. It exists so the probe
// rig can report, in one command, which of the design's glyphs the current font
// setup can actually draw — a tofu box in a frame then has an explanation.
var GlyphVocabulary = []struct {
	Name   string
	Glyphs []rune
}{
	{"shapes", []rune("●○◖◗▰▱█░▸▶▎▌")},
	{"marks", []rune("✓✗⊳↻↗⚠ⓘ×❯›»⛭📌")},
	{"box", []rune("╭╮╰╯├│─┌└┐┘╷╵")},
	{"spinner", []rune("⠋⠙⠹⠸⠼⠴⠦⠧⠇⠏")},
	{"emoji", []rune("💡🔍💰🧠⚡🦇👻💀🧛⏳❌💥🔄📦🧭👉🔎🛑⌨☁")},
	{"creatures", []rune("🦇🐍🧛☾👻🐍🕷🐦🐺👿💀✴☄🌀⚱🦋🐉🕸🌑🗡🪶🌹🔥❄")},
	{"modifiers", []rune("⚰🕯🌋🕳🪦🏚🦴🔮🌘🌲")},
}
