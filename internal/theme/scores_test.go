package theme

import "testing"

// TestReportScores prints each palette's harmony score. It is a diagnostic
// rather than an assertion — the assertions live in harmony_test.go — but
// having the numbers in the test output makes a calibration change visible
// rather than something to go looking for.
func TestReportScores(t *testing.T) {
	for name, p := range Palettes() {
		bg := DefaultDarkBackground
		if p.Light {
			bg = RGB(250, 250, 252)
		}
		card := Score(p, bg)
		t.Logf("%-12s overall %.0f", name, card.Overall)
		for _, c := range card.Criteria {
			t.Logf("    %-18s %.0f (weight %.1f, critical=%v)",
				c.Name, c.Score, c.Weight, c.Critical)
		}
	}
	gen := Generate(RGB(0, 160, 160), DefaultDarkBackground, "generated")
	t.Logf("%-12s overall %.0f", "generated", Score(gen, DefaultDarkBackground).Overall)
}
