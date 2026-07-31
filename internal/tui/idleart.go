package tui

import (
	"hash/fnv"
	"math"
	"strings"

	"charm.land/lipgloss/v2"

	"evilcode/internal/theme"
)

// Subpixel sampling (plan.md §10.1). Each terminal cell is sampled as a 3×3
// grid, and the glyph is chosen from the 9-bit occupancy pattern. That is what
// lets a character cell suggest a diagonal or a curve rather than only "on" or
// "off".
const (
	SubX = 3
	SubY = 3
)

// ArtHeight is the welcome screen's art block.
const ArtHeight = 18

// HueDegreesPerSecond is how fast the rainbow travels across the shape.
const HueDegreesPerSecond = 40

// Variant selects which idle animation runs.
type Variant string

const (
	VariantEye       Variant = "eye"
	VariantBlackhole Variant = "blackhole"
)

// PickVariant chooses a variant per process, so a given session keeps one
// animation rather than flickering between them.
func PickVariant(seed string) Variant {
	h := fnv.New32a()
	h.Write([]byte(seed))
	if h.Sum32()%2 == 0 {
		return VariantEye
	}
	return VariantBlackhole
}

// sample is one subpixel's contribution.
type sample struct {
	hit bool
	lum float64
}

// shapeChar picks a glyph from a 9-bit occupancy pattern and a brightness tier.
//
// The pattern is read as three rows of three bits. Recognizing diagonals and
// bands rather than only density is what makes the art read as a shape instead
// of as noise (plan.md §10.1).
func shapeChar(pattern uint16, brightness float64) string {
	tier := 0
	switch {
	case brightness > 0.66:
		tier = 2
	case brightness > 0.33:
		tier = 1
	}

	count := 0
	for i := 0; i < 9; i++ {
		if pattern&(1<<i) != 0 {
			count++
		}
	}
	if count == 0 {
		return " "
	}

	switch {
	case count >= 9:
		return pick("@#%", tier)
	case count >= 7:
		return pick("#%*", tier)
	}

	// Row and column occupancy tell bands from diagonals.
	rows := [3]int{}
	cols := [3]int{}
	for i := 0; i < 9; i++ {
		if pattern&(1<<i) != 0 {
			rows[i/3]++
			cols[i%3]++
		}
	}

	// A diagonal has its mass on one of the two corners-to-corner runs.
	down := bit(pattern, 0) + bit(pattern, 4) + bit(pattern, 8)
	up := bit(pattern, 2) + bit(pattern, 4) + bit(pattern, 6)
	if down >= 2 && down > up {
		return pick(`\\.`, tier)
	}
	if up >= 2 && up > down {
		return pick("//.", tier)
	}

	// A horizontal band: one row carries most of the mass.
	for r := 0; r < 3; r++ {
		if rows[r] >= 2 && rows[r] == count {
			if r == 2 {
				return pick("=_.", tier)
			}
			return pick("=-~", tier)
		}
	}
	// A vertical band.
	for c := 0; c < 3; c++ {
		if cols[c] >= 2 && cols[c] == count {
			return "|"
		}
	}
	return pick(".:*", tier)
}

func bit(pattern uint16, i int) int {
	if pattern&(1<<i) != 0 {
		return 1
	}
	return 0
}

// pick indexes a three-character ramp, brightest last.
func pick(ramp string, tier int) string {
	r := []rune(ramp)
	return string(r[clamp(tier, 0, len(r)-1)])
}

// Sampler answers whether a subpixel is inside the shape, and how bright.
type Sampler func(x, y float64, elapsed float64) sample

// RenderArt rasterizes a sampler into colored terminal rows.
//
// Color is a travelling hue wave: the shape rotates while a rainbow flows
// across it at HueDegreesPerSecond, which is what keeps a static silhouette
// alive without moving anything (plan.md §10.1).
func RenderArt(s Sampler, cols, rows int, elapsed float64, animate bool) []string {
	if cols <= 0 || rows <= 0 {
		return nil
	}
	sw, sh := cols*SubX, rows*SubY

	if !animate {
		// Frozen at frame 0 keeps golden frames reproducible (invariant 5).
		elapsed = 0
	}

	out := make([]string, 0, rows)
	for row := 0; row < rows; row++ {
		var b strings.Builder
		for col := 0; col < cols; col++ {
			var pattern uint16
			var lumSum float64
			var hits int

			for sy := 0; sy < SubY; sy++ {
				for sx := 0; sx < SubX; sx++ {
					// Normalize to -1..1 with square-ish aspect: terminal cells
					// are about twice as tall as they are wide.
					px := (float64(col*SubX+sx)/float64(sw))*2 - 1
					py := ((float64(row*SubY+sy)/float64(sh))*2 - 1) * 0.5

					if got := s(px, py, elapsed); got.hit {
						pattern |= 1 << (sy*3 + sx)
						lumSum += got.lum
						hits++
					}
				}
			}
			if hits == 0 {
				b.WriteByte(' ')
				continue
			}
			lum := lumSum / float64(hits)
			coverage := float64(hits) / float64(SubX*SubY)
			glyph := shapeChar(pattern, lum)

			hue := math.Mod(elapsed*HueDegreesPerSecond+lum*160, 360)
			sat := 0.5 + lum*0.4
			val := (0.10 + lum*lum*0.90) * (0.55 + coverage*0.45)

			style := lipgloss.NewStyle().
				Foreground(lipgloss.Color(theme.Hex(theme.HSV(hue, sat, val))))
			b.WriteString(style.Render(glyph))
		}
		out = append(out, strings.TrimRight(b.String(), " "))
	}
	return out
}

// BlackholeSampler draws an accretion disk with a gravitational lens arc.
func BlackholeSampler(x, y, elapsed float64) sample {
	r := math.Hypot(x, y*2)

	// The event horizon is a hole, not a shape: nothing inside it draws.
	const horizon = 0.28
	if r < horizon {
		return sample{}
	}

	// Concentric rings, rotating. The disk is brightest just outside the
	// horizon and fades outward.
	angle := math.Atan2(y*2, x)
	spin := elapsed * 0.6
	ring := math.Sin((r-horizon)*18 - spin*3)

	// Doppler-ish brightening on one side, which is what makes it read as
	// rotating rather than pulsing. The base is high enough that the far side
	// stays drawn: dimming it below the visibility threshold erases half the
	// disk and the shape reads as broken rather than as lit from one side.
	side := 0.78 + 0.22*math.Cos(angle-spin)

	falloff := math.Max(0, 1-(r-horizon)/0.75)
	lum := math.Max(0, ring) * falloff * side

	// The lens arc: a thin bright ring hugging the horizon, drawn unconditionally
	// so the hole always has a defined edge.
	if d := math.Abs(r - horizon*1.12); d < 0.045 {
		lum = math.Max(lum, 0.95*(1-d/0.045))
	}
	if lum < 0.10 {
		return sample{}
	}
	return sample{hit: true, lum: math.Min(lum, 1)}
}

// EyeSampler draws a 2D signed-distance eye: two lids, an iris, and a pupil,
// with a slow blink.
func EyeSampler(x, y, elapsed float64) sample {
	// The blink is rare and quick — every 7-13 seconds, closing over about a
	// fifth of a second. A regular fast blink reads as a glitch.
	blinkPeriod := 7.0 + math.Mod(math.Floor(elapsed/10), 6)
	phase := math.Mod(elapsed, blinkPeriod) / blinkPeriod
	open := 1.0
	if phase > 0.97 {
		open = math.Max(0.06, 1-(phase-0.97)/0.03*2)
	}

	yScaled := y * 2 / open

	// Two arcs meeting at the corners define the lid.
	const halfWidth = 0.78
	if math.Abs(x) > halfWidth {
		return sample{}
	}
	lid := 0.52 * math.Sqrt(math.Max(0, 1-(x/halfWidth)*(x/halfWidth)))
	if math.Abs(yScaled) > lid {
		return sample{}
	}

	// Sclera falls off toward the lids, so the eye has volume.
	edge := 1 - math.Abs(yScaled)/math.Max(lid, 1e-6)
	lum := 0.25 + edge*0.35

	r := math.Hypot(x, yScaled)
	const irisR, pupilR = 0.30, 0.13

	if r < pupilR {
		return sample{hit: true, lum: 0.06}
	}
	if r < irisR {
		// The iris carries its own hue wave, running opposite the global one so
		// the eye does not pulse uniformly with the background.
		ripple := 0.5 + 0.5*math.Sin(r*40-elapsed*2)
		return sample{hit: true, lum: 0.45 + ripple*0.45}
	}
	return sample{hit: true, lum: math.Min(lum, 1)}
}

// SamplerFor returns the sampler for a variant.
func SamplerFor(v Variant) Sampler {
	switch v {
	case VariantBlackhole:
		return BlackholeSampler
	default:
		return EyeSampler
	}
}
