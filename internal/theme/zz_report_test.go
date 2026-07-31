package theme

import "testing"

// TestPaletteReport is a diagnostic, not an assertion: it prints each built-in
// palette's scorecard so tuning a palette is a measurement rather than a guess.
// Run with -v to read it.
func TestPaletteReport(t *testing.T) {
	for name, p := range Palettes() {
		bg := DefaultDarkBackground
		if p.Light {
			bg = RGB(250, 250, 248)
		}
		card := Score(p, bg)
		t.Logf("%-10s overall %.1f", name, card.Overall)
		for _, c := range card.Criteria {
			t.Logf("    %-18s %.1f", c.Name, c.Score)
		}
		for _, pair := range MustDistinguish {
			if d := ToOklab(p.Get(pair[0])).Distance(ToOklab(p.Get(pair[1]))); d < DistinctTarget {
				t.Logf("    weak pair %v/%v = %.3f", pair[0], pair[1], d)
			}
		}
	}
}
