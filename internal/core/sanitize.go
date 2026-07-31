package core

import "strings"

// SanitizeTerminal strips control and escape sequences from text that did not
// come from evilcode.
//
// Repository content and provider output both reach the terminal, and neither
// is trusted to drive it. A file in a cloned repo, or a model persuaded to emit
// one, can carry OSC 52 — which writes the user's clipboard — or CSI sequences
// that move the cursor, recolour the screen, or clear it. "The workspace is
// trusted" is a claim about the code being edited, not about a byte sequence
// found inside it.
//
// Newline and tab survive because they are layout, not control. Everything else
// below 0x20, plus DEL and the C1 range, is dropped, and an escape starts a
// sequence that is consumed to its terminator rather than emitted.
func SanitizeTerminal(s string) string {
	if !needsSanitizing(s) {
		return s
	}
	var b strings.Builder
	b.Grow(len(s))

	runes := []rune(s)
	for i := 0; i < len(runes); i++ {
		r := runes[i]
		switch {
		case r == '\n' || r == '\t':
			b.WriteRune(r)
		case r == 0x1b:
			i += escapeLen(runes[i:]) - 1
		case r < 0x20 || r == 0x7f:
			// Dropped: BEL, backspace, carriage return, the lot. A stray
			// carriage return alone would overwrite the line just drawn.
		case r >= 0x80 && r <= 0x9f:
			// C1 controls, including the eight-bit forms of CSI and OSC.
		default:
			b.WriteRune(r)
		}
	}
	return b.String()
}

// stringTerminator finds the end of an OSC or DCS-style sequence, in runes.
//
// Three ways to end one, and the first version of this knew two: BEL, the
// seven-bit ST (ESC backslash), and the eight-bit C1 ST at U+009C. Missing the
// last meant a sequence terminated that way ran to the end of the string, so
// everything after it was consumed and dropped — the payload never reached the
// terminal, but neither did the rest of the file.
//
// alt is an additional single-rune terminator (BEL for OSC), or -1 for none.
func stringTerminator(runes []rune, alt rune) int {
	for i := 2; i < len(runes); i++ {
		switch {
		case alt >= 0 && runes[i] == alt:
			return i + 1
		case runes[i] == 0x9c: // C1 ST
			return i + 1
		case runes[i] == 0x1b && i+1 < len(runes) && runes[i+1] == '\\':
			return i + 2
		}
	}
	return 0
}

func needsSanitizing(s string) bool {
	for _, r := range s {
		if r == '\n' || r == '\t' {
			continue
		}
		if r < 0x20 || r == 0x7f || (r >= 0x80 && r <= 0x9f) {
			return true
		}
	}
	return false
}

// escapeLen reports how many runes the escape sequence at the start of runes
// occupies, so the whole thing is dropped rather than its introducer alone —
// leaving the payload of an OSC 52 to be printed as text would be its own kind
// of mess.
func escapeLen(runes []rune) int {
	if len(runes) < 2 {
		return 1
	}
	switch runes[1] {
	case '[': // CSI: parameters and intermediates, then a final byte.
		for i := 2; i < len(runes); i++ {
			if runes[i] >= 0x40 && runes[i] <= 0x7e {
				return i + 1
			}
		}
		return len(runes)
	case ']': // OSC: runs to BEL or ST.
		if n := stringTerminator(runes, 0x07); n > 0 {
			return n
		}
		return len(runes)
	case 'P', 'X', '^', '_': // DCS, SOS, PM, APC: run to ST.
		if n := stringTerminator(runes, -1); n > 0 {
			return n
		}
		return len(runes)
	default:
		// Two-byte escape, or a lone ESC at the end.
		return 2
	}
}
