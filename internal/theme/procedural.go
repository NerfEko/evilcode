package theme

import (
	"image/color"
	"math"
)

// rainbowStops is the 7-stop ramp indexed by distance from the newest prompt
// (plan.md §7.7). Distance 0 — the newest prompt — is full red.
var rainbowStops = [7]color.RGBA{
	RGB(255, 80, 80),
	RGB(255, 160, 80),
	RGB(255, 230, 80),
	RGB(80, 220, 100),
	RGB(80, 200, 220),
	RGB(100, 140, 255),
	RGB(180, 100, 255),
}

// rainbowGray is what an old prompt number decays toward.
var rainbowGray = RGB(80, 80, 80)

// RainbowDecay is the exponential falloff rate. Older prompts fade toward gray
// rather than staying vivid, so the eye finds the recent ones first.
const RainbowDecay = 0.4

// Rainbow returns the color for a prompt number at distance d from the newest.
// This is the strongest identity cue in the transcript:
//
//	color(d) = lerp(gray, stops[min(d,6)], e^(-0.4*d))
func Rainbow(d int) color.RGBA {
	if d < 0 {
		d = 0
	}
	stop := rainbowStops[min(d, len(rainbowStops)-1)]
	t := math.Exp(-RainbowDecay * float64(d))
	return Blend(rainbowGray, stop, t)
}

// Animated tool color endpoints: a ~1.5s sine cycle from cyan to purple
// (plan.md §7.7).
var (
	toolFrom = RGB(80, 200, 220)
	toolTo   = RGB(186, 139, 255)
)

// ToolAnimationRate is the sine argument multiplier, giving a period of
// 2*pi/2.0 ≈ 3.14s over the full cycle and ~1.5s between the extremes.
const ToolAnimationRate = 2.0

// AnimatedTool returns the running-tool color for a given elapsed time. When
// decorative animation is off it returns the flat tool gray, because a color
// that never changes is better than one that changes at a distracting rate.
func AnimatedTool(elapsedSeconds float64, animate bool, flat color.RGBA) color.RGBA {
	if !animate {
		return flat
	}
	t := math.Sin(elapsedSeconds*ToolAnimationRate)*0.5 + 0.5
	return Blend(toolFrom, toolTo, t)
}

// MeterColor picks a bar color from how much is REMAINING, not how much is
// used (plan.md §8.5). A meter that turns red as it fills is a progress bar; a
// meter that turns red as it empties is a warning, which is the point.
func MeterColor(remainingFraction float64) color.RGBA {
	switch {
	case remainingFraction <= 0.20:
		return RGB(255, 100, 100)
	case remainingFraction <= 0.50:
		return RGB(255, 200, 100)
	default:
		return RGB(100, 200, 100)
	}
}

// Lighten moves a channel halfway to white: c' = c + (255-c)/2. This is the
// fuzzy-match highlight of §5.1 — matched characters lift toward white and
// stay in the palette's hue instead of being underlined.
func Lighten(c color.RGBA) color.RGBA {
	lift := func(v uint8) uint8 { return v + (255-v)/2 }
	return color.RGBA{R: lift(c.R), G: lift(c.G), B: lift(c.B), A: 255}
}

// Darken halves each channel: c' = c/2, the unmatched half of the same effect.
func Darken(c color.RGBA) color.RGBA {
	return color.RGBA{R: c.R / 2, G: c.G / 2, B: c.B / 2, A: 255}
}

// Luminance is the perceptual brightness of a color, 0..1. Used to decide
// whether a terminal background is light (plan.md §7.4 pass 1).
func Luminance(c color.RGBA) float64 {
	return (0.2126*float64(c.R) + 0.7152*float64(c.G) + 0.0722*float64(c.B)) / 255
}

// FlipLuminance is pass 1 of the two-pass substitution: a hue- and
// saturation-preserving lightness flip for light terminal backgrounds. It
// converts to HSL, maps l to 1-l, and converts back — a per-channel inversion
// would change the hue, which is the whole thing this avoids.
func FlipLuminance(c color.RGBA) color.RGBA {
	h, s, l := rgbToHSL(c)
	return hslToRGB(h, s, 1-l)
}

func rgbToHSL(c color.RGBA) (h, s, l float64) {
	r, g, b := float64(c.R)/255, float64(c.G)/255, float64(c.B)/255
	maxV := math.Max(r, math.Max(g, b))
	minV := math.Min(r, math.Min(g, b))
	l = (maxV + minV) / 2

	if maxV == minV {
		return 0, 0, l // achromatic
	}
	d := maxV - minV
	if l > 0.5 {
		s = d / (2 - maxV - minV)
	} else {
		s = d / (maxV + minV)
	}
	switch maxV {
	case r:
		h = (g - b) / d
		if g < b {
			h += 6
		}
	case g:
		h = (b-r)/d + 2
	default:
		h = (r-g)/d + 4
	}
	return h / 6, s, l
}

func hslToRGB(h, s, l float64) color.RGBA {
	if s == 0 {
		v := uint8(l*255 + 0.5)
		return color.RGBA{R: v, G: v, B: v, A: 255}
	}
	var q float64
	if l < 0.5 {
		q = l * (1 + s)
	} else {
		q = l + s - l*s
	}
	p := 2*l - q
	ch := func(t float64) uint8 {
		if t < 0 {
			t++
		}
		if t > 1 {
			t--
		}
		switch {
		case t < 1.0/6:
			return uint8((p+(q-p)*6*t)*255 + 0.5)
		case t < 1.0/2:
			return uint8(q*255 + 0.5)
		case t < 2.0/3:
			return uint8((p+(q-p)*(2.0/3-t)*6)*255 + 0.5)
		default:
			return uint8(p*255 + 0.5)
		}
	}
	return color.RGBA{R: ch(h + 1.0/3), G: ch(h), B: ch(h - 1.0/3), A: 255}
}

// HSV converts a hue/saturation/value triple to RGB. The idle art's travelling
// hue wave is expressed in HSV (plan.md §10.1).
func HSV(h, s, v float64) color.RGBA {
	h = math.Mod(math.Mod(h, 360)+360, 360)
	c := v * s
	x := c * (1 - math.Abs(math.Mod(h/60, 2)-1))
	m := v - c

	var r, g, b float64
	switch {
	case h < 60:
		r, g, b = c, x, 0
	case h < 120:
		r, g, b = x, c, 0
	case h < 180:
		r, g, b = 0, c, x
	case h < 240:
		r, g, b = 0, x, c
	case h < 300:
		r, g, b = x, 0, c
	default:
		r, g, b = c, 0, x
	}
	to8 := func(f float64) uint8 { return uint8(math.Round((f + m) * 255)) }
	return color.RGBA{R: to8(r), G: to8(g), B: to8(b), A: 255}
}
