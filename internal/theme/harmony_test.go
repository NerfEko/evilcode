package theme

import (
	"image/color"
	"math"
	"testing"
	"time"
)

func TestOklabRoundTrip(t *testing.T) {
	for _, c := range []color.RGBA{
		RGB(0, 0, 0), RGB(255, 255, 255), RGB(128, 128, 128),
		RGB(255, 0, 0), RGB(0, 255, 0), RGB(0, 0, 255),
		RGB(0xbd, 0x93, 0xf9), RGB(0x50, 0xfa, 0x7b),
	} {
		back := ToOklab(c).ToRGB()
		if colorDistance(c, back) > 2 {
			t.Errorf("%v did not round-trip through Oklab: got %v", c, back)
		}
	}
}

func TestOklabLightnessIsPerceptual(t *testing.T) {
	// The reason for using Oklab at all: pure blue and pure yellow have the
	// same RGB magnitude but nothing like the same apparent brightness.
	blue := ToOklab(RGB(0, 0, 255)).L
	yellow := ToOklab(RGB(255, 255, 0)).L
	if yellow <= blue {
		t.Errorf("yellow L=%.3f should exceed blue L=%.3f", yellow, blue)
	}
}

func TestGamutMapReducesChromaNotChannels(t *testing.T) {
	// Clamping channels shifts hue and lightness, which is what makes a
	// generated palette look muddy. Chroma reduction preserves both.
	wild := FromLCH(0.7, 0.9, 1.0) // far outside sRGB
	got := GamutMap(wild)

	gotLab := ToOklab(got)
	if math.Abs(gotLab.L-wild.L) > 0.12 {
		t.Errorf("lightness moved %.3f -> %.3f", wild.L, gotLab.L)
	}
	dh := math.Abs(gotLab.Hue() - wild.Hue())
	if dh > 0.25 && math.Abs(dh-2*math.Pi) > 0.25 {
		t.Errorf("hue moved %.3f -> %.3f", wild.Hue(), gotLab.Hue())
	}
	if gotLab.Chroma() >= wild.Chroma() {
		t.Error("chroma should have been reduced")
	}
}

func TestCalibrationOrdering(t *testing.T) {
	// The pins from plan.md §7.5. Absolute values will drift as the scorer is
	// tuned; the *ordering* is the contract, because a scorer that ranks a
	// neon-chaos palette above Dracula is measuring the wrong thing.
	bg := DefaultDarkBackground

	dracula := Score(Dracula(), bg).Overall
	gloom := Score(Gloom(), bg).Overall
	nosferatu := Score(Nosferatu(), bg).Overall

	neonChaos := &Palette{Name: "neon-chaos"}
	for _, r := range AllRoles() {
		neonChaos.Colors[r] = RGB(255, 0, 255)
	}
	neonChaos.Colors[RoleSuccess] = RGB(0, 255, 0)
	neonChaos.Colors[RoleError] = RGB(255, 0, 0)

	mud := &Palette{Name: "unreadable-mud"}
	for _, r := range AllRoles() {
		mud.Colors[r] = RGB(30, 28, 26)
	}

	mudScore := Score(mud, bg).Overall
	chaosScore := Score(neonChaos, bg).Overall

	if mudScore >= chaosScore {
		t.Errorf("unreadable mud (%.1f) should score below neon chaos (%.1f)",
			mudScore, chaosScore)
	}
	for name, s := range map[string]float64{
		"dracula": dracula, "gloom": gloom, "nosferatu": nosferatu,
	} {
		if s <= mudScore {
			t.Errorf("%s (%.1f) should score above unreadable mud (%.1f)", name, s, mudScore)
		}
	}
	if dracula <= chaosScore {
		t.Errorf("dracula (%.1f) should score above neon chaos (%.1f)", dracula, chaosScore)
	}
}

func TestUnreadablePaletteScoresBadly(t *testing.T) {
	// A palette whose text is the same lightness as the background must sink,
	// because readability is a critical criterion.
	bg := DefaultDarkBackground
	p := &Palette{Name: "invisible"}
	for _, r := range AllRoles() {
		p.Colors[r] = bg
	}
	if got := Score(p, bg).Overall; got > 40 {
		t.Errorf("an invisible palette scored %.1f, want it sunk", got)
	}
}

func TestOnlyCriticalCriteriaCanSink(t *testing.T) {
	// Unconventional hues are taste, not defects: a palette that is readable
	// and distinct but hue-scattered should still score respectably.
	card := Score(Dracula(), DefaultDarkBackground)
	var sawCritical, sawOptional bool
	for _, c := range card.Criteria {
		if c.Critical {
			sawCritical = true
		} else {
			sawOptional = true
		}
		if c.Score < 0 || c.Score > 100 {
			t.Errorf("criterion %s scored %.1f, outside 0..100", c.Name, c.Score)
		}
	}
	if !sawCritical || !sawOptional {
		t.Error("the scorecard should have both critical and optional criteria")
	}
	if card.Overall < 0 || card.Overall > 100 {
		t.Errorf("overall = %.1f", card.Overall)
	}
}

func TestMustDistinguishOmitsDeliberateSimilarities(t *testing.T) {
	// Real palettes make these pairs similar on purpose; penalizing them would
	// score good palettes badly (plan.md §7.5).
	forbidden := [][2]Role{
		{RoleUser, RoleInfo},
		{RoleAI, RoleAccent},
		{RoleDim, RoleTool},
	}
	for _, f := range forbidden {
		for _, pair := range MustDistinguish {
			if (pair[0] == f[0] && pair[1] == f[1]) || (pair[0] == f[1] && pair[1] == f[0]) {
				t.Errorf("%v/%v must NOT be a must-distinguish pair", f[0], f[1])
			}
		}
	}
}

func TestCVDSimulationCollapsesRedGreen(t *testing.T) {
	// Under deuteranopia pure red and pure green converge; that is the whole
	// reason success/warning/error are separated by lightness too.
	red, green := RGB(255, 0, 0), RGB(0, 255, 0)
	before := ToOklab(red).Distance(ToOklab(green))
	after := ToOklab(Deuteranopia(red)).Distance(ToOklab(Deuteranopia(green)))
	if after >= before {
		t.Errorf("deuteranopia distance %.3f should be below normal %.3f", after, before)
	}
}

func TestGenerateFromSeeds(t *testing.T) {
	// The floor from plan.md §7.5: every seed, on either background, must
	// generate something usable.
	seeds := map[string]color.RGBA{
		"pure red":   RGB(255, 0, 0),
		"pure gray":  RGB(128, 128, 128),
		"near black": RGB(8, 8, 10),
		"purple":     RGB(0xbd, 0x93, 0xf9),
		"teal":       RGB(0, 180, 180),
	}
	backgrounds := map[string]color.RGBA{
		"dark":  DefaultDarkBackground,
		"light": RGB(250, 250, 248),
	}

	for seedName, seed := range seeds {
		for bgName, bg := range backgrounds {
			t.Run(seedName+"/"+bgName, func(t *testing.T) {
				p := Generate(seed, bg, "generated")
				score := Score(p, bg).Overall
				if score < 70 {
					t.Errorf("generated palette scored %.1f, want at least 70", score)
				}
				for _, r := range AllRoles() {
					if p.Get(r).A == 0 {
						t.Errorf("role %s was left unset", r)
					}
				}
			})
		}
	}
}

func TestGeneratedForegroundsContrastWithBackground(t *testing.T) {
	for _, bg := range []color.RGBA{DefaultDarkBackground, RGB(250, 250, 248)} {
		p := Generate(RGB(0xbd, 0x93, 0xf9), bg, "generated")
		bgL := ToOklab(bg).L
		for _, r := range foregroundRoles {
			got := math.Abs(ToOklab(p.Get(r)).L - bgL)
			// A little slack below the target: gamut mapping and repair can
			// shave a few points off.
			if got < GenContrast*0.55 {
				t.Errorf("bg %v: role %s has contrast %.3f, want near %.2f",
					bg, r, got, GenContrast)
			}
		}
	}
}

func TestGenerateKeepsConventionalHues(t *testing.T) {
	// Green means good and red means bad in every terminal on earth; a
	// generator that reassigns those is technically consistent and practically
	// useless.
	p := Generate(RGB(0, 120, 255), DefaultDarkBackground, "generated")
	success := ToOklab(p.Get(RoleSuccess))
	errC := ToOklab(p.Get(RoleError))
	if success.Hue() < 1.5 || success.Hue() > 3.2 {
		t.Errorf("success hue %.2f is not in the green range", success.Hue())
	}
	if math.Abs(errC.Hue()) > 1.0 {
		t.Errorf("error hue %.2f is not in the red range", errC.Hue())
	}
}

func TestRepairTerminates(t *testing.T) {
	// Greedy pairwise repair provably cycles on the success/warning/error
	// triangle. This asserts the global-weakest-pair strategy converges.
	bg := DefaultDarkBackground
	p := &Palette{Name: "clumped"}
	for _, r := range AllRoles() {
		p.Colors[r] = RGB(140, 140, 140)
	}
	done := make(chan struct{})
	go func() {
		repair(p, bg)
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("repair did not terminate")
	}
}

func TestNearNeutralSeedFallsBack(t *testing.T) {
	// A gray seed has no hue to build on; generating a grayscale palette would
	// satisfy the code and nobody's eyes.
	p := Generate(RGB(128, 128, 128), DefaultDarkBackground, "generated")
	var colorful int
	for _, r := range foregroundRoles {
		if ToOklab(p.Get(r)).Chroma() > 0.03 {
			colorful++
		}
	}
	if colorful < len(foregroundRoles)/2 {
		t.Errorf("only %d of %d roles are colorful; the neutral-seed fallback did not fire",
			colorful, len(foregroundRoles))
	}
}
