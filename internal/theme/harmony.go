package theme

import (
	"image/color"
	"math"
	"sort"
)

// All harmony math is in Oklab, which is perceptually uniform enough that a
// fixed distance means roughly the same thing anywhere in the space. Doing this
// in RGB produces scores that disagree with the eye (plan.md §7.5).

// Oklab is a perceptual color triple.
type Oklab struct{ L, A, B float64 }

// ToOklab converts sRGB to Oklab.
func ToOklab(c color.RGBA) Oklab {
	lr, lg, lb := srgbToLinear(c.R), srgbToLinear(c.G), srgbToLinear(c.B)

	l := 0.4122214708*lr + 0.5363325363*lg + 0.0514459929*lb
	m := 0.2119034982*lr + 0.6806995451*lg + 0.1073969566*lb
	s := 0.0883024619*lr + 0.2817188376*lg + 0.6299787005*lb

	l, m, s = math.Cbrt(l), math.Cbrt(m), math.Cbrt(s)
	return Oklab{
		L: 0.2104542553*l + 0.7936177850*m - 0.0040720468*s,
		A: 1.9779984951*l - 2.4285922050*m + 0.4505937099*s,
		B: 0.0259040371*l + 0.7827717662*m - 0.8086757660*s,
	}
}

// ToRGB converts Oklab back to sRGB, clipping channels.
func (o Oklab) ToRGB() color.RGBA {
	l := o.L + 0.3963377774*o.A + 0.2158037573*o.B
	m := o.L - 0.1055613458*o.A - 0.0638541728*o.B
	s := o.L - 0.0894841775*o.A - 1.2914855480*o.B

	l, m, s = l*l*l, m*m*m, s*s*s

	lr := 4.0767416621*l - 3.3077115913*m + 0.2309699292*s
	lg := -1.2684380046*l + 2.6097574011*m - 0.3413193965*s
	lb := -0.0041960863*l - 0.7034186147*m + 1.7076147010*s

	return color.RGBA{
		R: linearToSRGB(lr), G: linearToSRGB(lg), B: linearToSRGB(lb), A: 255,
	}
}

// Chroma is the colorfulness, and Hue the angle in radians.
func (o Oklab) Chroma() float64 { return math.Hypot(o.A, o.B) }
func (o Oklab) Hue() float64    { return math.Atan2(o.B, o.A) }

// WithChroma rescales colorfulness, keeping hue and lightness.
func (o Oklab) WithChroma(c float64) Oklab {
	cur := o.Chroma()
	if cur == 0 {
		return o
	}
	k := c / cur
	return Oklab{L: o.L, A: o.A * k, B: o.B * k}
}

// FromLCH builds an Oklab color from lightness, chroma, and hue in radians.
func FromLCH(l, c, h float64) Oklab {
	return Oklab{L: l, A: c * math.Cos(h), B: c * math.Sin(h)}
}

// Distance is the Euclidean distance in Oklab.
func (o Oklab) Distance(other Oklab) float64 {
	dl := o.L - other.L
	da := o.A - other.A
	db := o.B - other.B
	return math.Sqrt(dl*dl + da*da + db*db)
}

func srgbToLinear(v uint8) float64 {
	f := float64(v) / 255
	if f <= 0.04045 {
		return f / 12.92
	}
	return math.Pow((f+0.055)/1.055, 2.4)
}

func linearToSRGB(f float64) uint8 {
	if f <= 0 {
		return 0
	}
	if f >= 1 {
		return 255
	}
	var v float64
	if f <= 0.0031308 {
		v = f * 12.92
	} else {
		v = 1.055*math.Pow(f, 1/2.4) - 0.055
	}
	return uint8(math.Round(v * 255))
}

// GamutMap brings a color into sRGB by reducing chroma, never by clamping
// channels. Clamping shifts hue and lightness, which is exactly what makes a
// generated palette look muddy rather than merely duller (plan.md §7.5).
func GamutMap(o Oklab) color.RGBA {
	const (
		shrink   = 0.92
		maxSteps = 24
	)
	cur := o
	for i := 0; i < maxSteps; i++ {
		rgb := cur.ToRGB()
		// Round-tripping tells us whether the color survived the conversion.
		if ToOklab(rgb).Distance(cur) < 0.02 {
			return rgb
		}
		cur = cur.WithChroma(cur.Chroma() * shrink)
	}
	return cur.ToRGB()
}

// Criterion is one scored aspect of a palette.
type Criterion struct {
	Name     string
	Weight   float64
	Critical bool
	Score    float64
}

// Scorecard is a palette's full assessment.
type Scorecard struct {
	Criteria []Criterion
	Overall  float64
}

// Scoring targets from plan.md §7.5.
const (
	// ContrastTarget is the Oklab lightness contrast a role should have
	// against the terminal background.
	ContrastTarget = 0.40

	// DistinctTarget is the minimum Oklab distance between two roles that
	// must never be confused.
	DistinctTarget = 0.20
)

// MustDistinguish are the pairs whose confusion would actively mislead.
//
// The omissions are deliberate: user↔info, ai↔accent, and dim↔tool are NOT
// paired, because real palettes make those similar on purpose and penalizing
// them would score good palettes badly (plan.md §7.5).
var MustDistinguish = [][2]Role{
	{RoleSuccess, RoleError},
	{RoleSuccess, RoleWarning},
	{RoleWarning, RoleError},
	{RoleUser, RoleAI},
	{RoleAccent, RoleSystem},
	{RoleInfo, RoleSuccess},
	{RoleQueued, RoleAsap},
	{RoleSystem, RoleQueued},
}

// Score assesses a palette against a terminal background.
func Score(p *Palette, background color.RGBA) Scorecard {
	bg := ToOklab(background)

	readability := scoreReadability(p, bg)
	distinct := scoreDistinctness(p)
	harmony := scoreHueHarmony(p)
	chroma := scoreChromaCoherence(p)
	cvd := scoreColorblind(p)

	card := Scorecard{Criteria: []Criterion{
		{"readability", 3.0, true, readability},
		{"distinctness", 2.0, true, distinct},
		{"hue harmony", 2.0, false, harmony},
		{"chroma coherence", 1.5, true, chroma},
		{"colorblind safety", 1.0, false, cvd},
	}}

	// Aggregation is worst-weighted, and only critical criteria can sink the
	// score: unconventional hues are taste, not defects.
	var weighted, weight, worstCritical float64
	worstCritical = 100
	for _, c := range card.Criteria {
		weighted += c.Score * c.Weight
		weight += c.Weight
		if c.Critical && c.Score < worstCritical {
			worstCritical = c.Score
		}
	}
	mean := weighted / weight
	card.Overall = 0.75*mean + 0.25*worstCritical
	return card
}

// aggregate combines per-item scores as 0.4*mean + 0.6*worst, so one bad pair
// is not averaged away by several good ones.
func aggregate(scores []float64) float64 {
	if len(scores) == 0 {
		return 100
	}
	sum, worst := 0.0, 100.0
	for _, s := range scores {
		sum += s
		if s < worst {
			worst = s
		}
	}
	return 0.4*(sum/float64(len(scores))) + 0.6*worst
}

// foregroundRoles are the roles that carry text and therefore need contrast.
var foregroundRoles = []Role{
	RoleUser, RoleAI, RoleTool, RoleFileLink, RoleAccent, RoleSystem,
	RoleQueued, RoleAsap, RoleUserText, RoleAIText,
	RoleSuccess, RoleWarning, RoleError, RoleInfo,
}

func scoreReadability(p *Palette, bg Oklab) float64 {
	var scores []float64
	for _, r := range foregroundRoles {
		got := math.Abs(ToOklab(p.Get(r)).L - bg.L)
		scores = append(scores, ratio(got, ContrastTarget))
	}
	return aggregate(scores)
}

func scoreDistinctness(p *Palette) float64 {
	var scores []float64
	for _, pair := range MustDistinguish {
		d := ToOklab(p.Get(pair[0])).Distance(ToOklab(p.Get(pair[1])))
		scores = append(scores, ratio(d, DistinctTarget))
	}
	return aggregate(scores)
}

// ratio scores a measurement against a target, saturating at 100 rather than
// rewarding overshoot: twice the required contrast is not twice as readable.
func ratio(got, target float64) float64 {
	if target <= 0 {
		return 100
	}
	return math.Min(got/target, 1) * 100
}

// harmonyLayouts are the hue relationships a palette can be built on, as
// fractions of the circle.
var harmonyLayouts = map[string][]float64{
	"analogous":     {0, 1.0 / 12, 2.0 / 12},
	"complementary": {0, 0.5},
	"triadic":       {0, 1.0 / 3, 2.0 / 3},
	"tetradic":      {0, 0.25, 0.5, 0.75},
	"split":         {0, 5.0 / 12, 7.0 / 12},
}

// scoreHueHarmony measures how well the palette's hues fit any one layout. It
// takes the best fit, since a palette only needs to follow one scheme.
func scoreHueHarmony(p *Palette) float64 {
	var hues []float64
	for _, r := range foregroundRoles {
		o := ToOklab(p.Get(r))
		// Near-neutral colors have no meaningful hue to fit.
		if o.Chroma() < 0.02 {
			continue
		}
		hues = append(hues, math.Mod((o.Hue()/(2*math.Pi))+1, 1))
	}
	if len(hues) < 2 {
		return 100
	}
	sort.Float64s(hues)

	best := 0.0
	for _, offsets := range harmonyLayouts {
		// Try each hue as the anchor.
		for _, anchor := range hues {
			total := 0.0
			for _, h := range hues {
				nearest := 1.0
				for _, off := range offsets {
					target := math.Mod(anchor+off+1, 1)
					d := math.Abs(h - target)
					nearest = math.Min(nearest, math.Min(d, 1-d))
				}
				// A sixth of the circle away from any spoke scores zero.
				total += math.Max(0, 1-nearest/(1.0/6)) * 100
			}
			if s := total / float64(len(hues)); s > best {
				best = s
			}
		}
	}
	return best
}

// vividRoles are the roles meant to carry color. Deliberately muted roles are
// excluded from chroma coherence for the same reason user↔info is excluded from
// distinctness: `tool` and `dim` are *supposed* to be quieter than the rest, and
// scoring them as chroma outliers measures the wrong thing.
var vividRoles = []Role{
	RoleUser, RoleAI, RoleAccent, RoleSystem, RoleQueued, RoleAsap,
	RoleFileLink, RoleSuccess, RoleWarning, RoleError, RoleInfo,
}

// scoreChromaCoherence rewards saturation consistency within a comfortable
// band: a palette where one role screams and the rest whisper reads as broken.
func scoreChromaCoherence(p *Palette) float64 {
	var chromas []float64
	for _, r := range vividRoles {
		if c := ToOklab(p.Get(r)).Chroma(); c > 0.02 {
			chromas = append(chromas, c)
		}
	}
	if len(chromas) < 2 {
		return 100
	}
	mean := 0.0
	for _, c := range chromas {
		mean += c
	}
	mean /= float64(len(chromas))

	variance := 0.0
	for _, c := range chromas {
		variance += (c - mean) * (c - mean)
	}
	spread := math.Sqrt(variance / float64(len(chromas)))

	// A standard deviation of 0.08 or more in chroma is incoherent.
	consistency := math.Max(0, 1-spread/0.08) * 100

	// And the band itself should be comfortable: 0.04-0.16 reads well.
	band := 100.0
	switch {
	case mean < 0.03:
		band = mean / 0.03 * 100
	case mean > 0.20:
		band = math.Max(0, 1-(mean-0.20)/0.10) * 100
	}
	return math.Min(consistency, band)
}

// Deuteranopia and Protanopia simulate red-green color blindness by projecting
// in linear RGB (plan.md §7.5).
func Deuteranopia(c color.RGBA) color.RGBA {
	r, g, b := srgbToLinear(c.R), srgbToLinear(c.G), srgbToLinear(c.B)
	return color.RGBA{
		R: linearToSRGB(0.625*r + 0.375*g),
		G: linearToSRGB(0.700*r + 0.300*g),
		B: linearToSRGB(0.300*g + 0.700*b),
		A: 255,
	}
}

func Protanopia(c color.RGBA) color.RGBA {
	r, g, b := srgbToLinear(c.R), srgbToLinear(c.G), srgbToLinear(c.B)
	return color.RGBA{
		R: linearToSRGB(0.567*r + 0.433*g),
		G: linearToSRGB(0.558*r + 0.442*g),
		B: linearToSRGB(0.242*g + 0.758*b),
		A: 255,
	}
}

// scoreColorblind checks the must-distinguish pairs under simulated CVD.
//
// Under red-green CVD hue collapses onto a blue-yellow axis, so hue alone stops
// separating anything. Lightness survives, which is why the generator puts
// success, warning, and error on three distinct lightness levels.
func scoreColorblind(p *Palette) float64 {
	var scores []float64
	for _, sim := range []func(color.RGBA) color.RGBA{Deuteranopia, Protanopia} {
		for _, pair := range MustDistinguish {
			a := ToOklab(sim(p.Get(pair[0])))
			b := ToOklab(sim(p.Get(pair[1])))
			// Lightness counts double here, since it is what survives.
			d := math.Hypot(a.Distance(b), 2*math.Abs(a.L-b.L))
			scores = append(scores, ratio(d, DistinctTarget))
		}
	}
	return aggregate(scores)
}

// Generation bounds from plan.md §7.5.
const (
	// Lightness outside this band gets its chroma stripped by gamut mapping,
	// which turns a color into a smudge.
	GenMinLightness = 0.36
	GenMaxLightness = 0.94

	GenMinChroma = 0.06
	GenMaxChroma = 0.14

	// Foreground lightness sits a full contrast target away from the
	// background, always in the direction away from it.
	GenContrast = 0.40
)

// Generate builds a palette from a seed color and a terminal background.
func Generate(seed, background color.RGBA, name string) *Palette {
	bg := ToOklab(background)
	s := ToOklab(seed)

	// A near-neutral seed has no hue to build on, so fall back to the user
	// purple rather than generating a grayscale palette nobody wants.
	if s.Chroma() < 0.02 {
		s = ToOklab(Dracula().Get(RoleUser))
	}
	chroma := math.Max(GenMinChroma, math.Min(s.Chroma(), GenMaxChroma))
	baseHue := s.Hue()

	light := bg.L < 0.5
	fgL := clampF(bg.L+GenContrast, GenMinLightness, GenMaxLightness)
	if !light {
		fgL = clampF(bg.L-GenContrast, GenMinLightness, GenMaxLightness)
	}
	dimL := clampF(bg.L+(fgL-bg.L)*0.75, GenMinLightness, GenMaxLightness)
	panelL := clampF(bg.L+0.06, 0, 1)
	if !light {
		panelL = clampF(bg.L-0.06, 0, 1)
	}

	// Split-complementary layout, which spreads roles without the harshness of
	// a straight complement.
	const turn = 2 * math.Pi
	hue := func(frac float64) float64 { return baseHue + frac*turn }

	p := &Palette{Name: name, Light: !light}
	set := func(r Role, o Oklab) { p.Colors[r] = GamutMap(o) }

	set(RoleUser, FromLCH(fgL, chroma, hue(0)))
	set(RoleAI, FromLCH(fgL, chroma, hue(5.0/12)))
	set(RoleAccent, FromLCH(fgL, chroma*1.15, hue(7.0/12)))
	set(RoleInfo, FromLCH(fgL, chroma*0.9, hue(0.5)))
	set(RoleAsap, FromLCH(fgL, chroma*0.9, hue(0.45)))
	set(RoleFileLink, FromLCH(fgL, chroma*0.7, hue(0.5)))

	// Conventional hues are kept for the three roles whose meaning is carried
	// by color, and they sit on three distinct lightness levels so they stay
	// separable under red-green CVD.
	set(RoleSuccess, FromLCH(clampF(fgL+0.06, GenMinLightness, GenMaxLightness), chroma, 2.4))
	set(RoleWarning, FromLCH(clampF(fgL-0.02, GenMinLightness, GenMaxLightness), chroma*1.1, 1.4))
	set(RoleError, FromLCH(clampF(fgL-0.10, GenMinLightness, GenMaxLightness), chroma*1.2, 0.5))
	set(RoleSystem, FromLCH(clampF(fgL+0.02, GenMinLightness, GenMaxLightness), chroma, 1.1))
	set(RoleQueued, FromLCH(clampF(fgL+0.08, GenMinLightness, GenMaxLightness), chroma*0.9, 1.9))

	set(RoleTool, FromLCH(dimL, chroma*0.25, hue(0)))
	set(RoleDim, FromLCH(clampF(bg.L+(fgL-bg.L)*0.45, 0, 1), chroma*0.15, hue(0)))
	set(RolePending, FromLCH(dimL, chroma*0.2, hue(0)))

	set(RoleUserText, FromLCH(clampF(fgL+0.10, 0, 1), chroma*0.08, hue(0)))
	set(RoleAIText, FromLCH(clampF(fgL+0.06, 0, 1), chroma*0.06, hue(0)))
	set(RoleHeaderSession, FromLCH(clampF(fgL+0.14, 0, 1), 0, 0))

	set(RoleHeaderIcon, FromLCH(fgL, chroma*1.15, hue(7.0/12)))
	set(RoleHeaderName, FromLCH(fgL, chroma, hue(0)))

	set(RoleBorder, FromLCH(panelL, chroma*0.2, hue(0)))
	set(RoleUserBg, FromLCH(panelL, chroma*0.3, hue(0)))
	set(RoleSelectionBg, FromLCH(panelL, chroma*0.2, hue(0)))

	repair(p, background)
	return p
}

// repair nudges the palette's globally weakest must-distinguish pair apart,
// re-scoring after each move.
//
// Greedy pairwise repair — fix a pair, move on — provably cycles on the
// success/warning/error triangle: separating two of them pushes one into the
// third. Scoring candidate moves by the *global* weakest pair is what breaks
// the cycle (plan.md §7.5).
func repair(p *Palette, background color.RGBA) {
	const maxRounds = 12
	for round := 0; round < maxRounds; round++ {
		worst, worstPair := math.MaxFloat64, -1
		for i, pair := range MustDistinguish {
			d := ToOklab(p.Get(pair[0])).Distance(ToOklab(p.Get(pair[1])))
			if d < worst {
				worst, worstPair = d, i
			}
		}
		if worst >= DistinctTarget || worstPair < 0 {
			return
		}

		pair := MustDistinguish[worstPair]
		before := Score(p, background).Overall

		// Try nudging either member's lightness in either direction, and keep
		// whichever move most improves the palette as a whole.
		bestScore, bestRole, bestColor := before, Role(-1), color.RGBA{}
		for _, r := range pair {
			o := ToOklab(p.Get(r))
			for _, delta := range []float64{0.05, -0.05} {
				cand := FromLCH(clampF(o.L+delta, GenMinLightness, GenMaxLightness),
					o.Chroma(), o.Hue())
				saved := p.Colors[r]
				p.Colors[r] = GamutMap(cand)
				if s := Score(p, background).Overall; s > bestScore {
					bestScore, bestRole, bestColor = s, r, p.Colors[r]
				}
				p.Colors[r] = saved
			}
		}
		if bestRole < 0 {
			return // no move helps; stop rather than thrash
		}
		p.Colors[bestRole] = bestColor
	}
}

func clampF(v, lo, hi float64) float64 {
	return math.Max(lo, math.Min(v, hi))
}

// DefaultDarkBackground is the terminal background evilcode assumes.
var DefaultDarkBackground = RGB(18, 18, 24)
