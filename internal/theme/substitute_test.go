package theme

import (
	"fmt"
	"image/color"
	"strings"
	"testing"
)

func sgr(c color.RGBA) string {
	return fmt.Sprintf("\x1b[38;2;%d;%d;%dm", c.R, c.G, c.B)
}

func TestSubstituterHandlesBackgroundColors(t *testing.T) {
	target := Gloom()
	s := NewSubstituter(target, false)
	from := CatppuccinFrappe().Get(RoleUserBg)
	to := target.Get(RoleUserBg)

	frame := fmt.Sprintf("\x1b[48;2;%d;%d;%dmband\x1b[m", from.R, from.G, from.B)
	want := fmt.Sprintf("\x1b[48;2;%d;%d;%dm", to.R, to.G, to.B)
	if got := s.Frame(frame); !strings.Contains(got, want) {
		t.Errorf("background not mapped:\n%q", got)
	}
}

func TestSubstituterHandlesCompoundSGR(t *testing.T) {
	// lipgloss emits bold and color in one sequence; the non-color parameters
	// must survive.
	target := Gloom()
	s := NewSubstituter(target, false)
	from := CatppuccinFrappe().Get(RoleAccent)

	frame := fmt.Sprintf("\x1b[1;38;2;%d;%d;%d;4mtext\x1b[m", from.R, from.G, from.B)
	got := s.Frame(frame)
	if !strings.HasPrefix(got, "\x1b[1;38;2;") {
		t.Errorf("bold parameter was lost:\n%q", got)
	}
	if !strings.HasSuffix(strings.SplitN(got, "m", 2)[0]+"m", ";4m") {
		t.Errorf("underline parameter was lost:\n%q", got)
	}
}

func TestLiteralsAreReanchoredNotLeftBehind(t *testing.T) {
	// The point of the registry: a themed palette moves the roles, and every
	// literal that is a shade of one must move with it. Otherwise the UI is
	// half-themed, which looks broken rather than merely different.
	target := Gloom()
	s := NewSubstituter(target, false)

	var moved int
	for _, l := range Literals {
		if s.Color(l.Color) != l.Color {
			moved++
		}
	}
	if moved < len(Literals)/2 {
		t.Errorf("only %d of %d literals moved under a new palette", moved, len(Literals))
	}
}

// TestEveryLiteralReachesARole is the first of the two registry invariants
// plan.md §7.4 asks for.
func TestEveryLiteralReachesARole(t *testing.T) {
	seen := map[string]bool{}
	for _, l := range Literals {
		if l.Name == "" {
			t.Error("a literal has no name")
		}
		if seen[l.Name] {
			t.Errorf("duplicate literal name %q", l.Name)
		}
		seen[l.Name] = true

		if l.Anchor < 0 || l.Anchor >= numRoles {
			t.Errorf("literal %q anchors to an invalid role", l.Name)
		}
		if l.Color.A == 0 {
			t.Errorf("literal %q has no color", l.Name)
		}
	}
}

func TestSubstituterHandlesTruncatedSequences(t *testing.T) {
	// A frame captured mid-write must not hang or corrupt the tokenizer.
	s := NewSubstituter(Gloom(), false)
	for _, frame := range []string{"\x1b", "\x1b[", "\x1b[38;2;255", "\x1b[38;2;"} {
		got := s.Frame(frame)
		if got == "" && frame != "" {
			t.Errorf("Frame(%q) returned empty", frame)
		}
	}
}

func TestAllPalettesScoreReasonably(t *testing.T) {
	for name, p := range Palettes() {
		bg := DefaultDarkBackground
		if p.Light {
			bg = RGB(250, 250, 248)
		}
		if got := Score(p, bg).Overall; got < 60 {
			t.Errorf("built-in palette %q scored %.1f", name, got)
		}
	}
}
