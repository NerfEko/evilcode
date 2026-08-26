package theme

import (
	"image/color"
	"math"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
)

// Substituter re-colors a finished frame (plan.md §7.4).
//
// The architecture exists so widgets can emit default-palette colors and ad-hoc
// literals freely, without every call site knowing about theming. Substitution
// happens once per frame, at the buffer level, in this exact order:
//
//	widgets → frame → pass 1: light/dark adapt → pass 2: user palette → terminal
//
// Pass 2 runs *after* pass 1 and is exempt from it. A deliberately dark red
// must not become pale pink on a light terminal, and it would if the configured
// color went through the luminance flip.
type Substituter struct {
	// LightBackground enables pass 1.
	LightBackground bool

	target *Palette

	// active is an atomic fast path: an unconfigured palette must cost
	// essentially nothing, since this runs over every frame.
	active atomic.Bool

	mu    sync.Mutex
	cache map[color.RGBA]color.RGBA

	// mapping is the default-palette color each role should become.
	mapping map[color.RGBA]color.RGBA

	// literals maps a default literal to its re-expressed value.
	literals map[color.RGBA]color.RGBA
}

// NewSubstituter builds a substituter targeting a palette. Passing the default
// palette (or nil) leaves it inactive.
func NewSubstituter(target *Palette, lightBackground bool) *Substituter {
	s := &Substituter{
		LightBackground: lightBackground,
		target:          target,
		cache:           map[color.RGBA]color.RGBA{},
	}
	s.rebuild()
	return s
}

// LiteralRadius is how close an ad-hoc color must be to a role's default for it
// to count as a variation of that role. Beyond this it is left alone: not every
// color in the UI is a shade of something.
//
// The value is euclidean RGB distance, sized so the spec's own shades qualify —
// the status amber sits about 100 from the warning role — while genuinely
// unrelated colors do not.
const LiteralRadius = 130.0

func (s *Substituter) rebuild() {
	if s.target == nil || s.target.Name == "catppuccin-frappe" {
		s.active.Store(false)
		return
	}
	def := CatppuccinFrappe()

	s.mapping = make(map[color.RGBA]color.RGBA, numRoles)
	for _, r := range AllRoles() {
		from, to := def.Get(r), s.target.Get(r)
		if from != to {
			s.mapping[from] = to
		}
	}

	// An ad-hoc literal is re-expressed relative to its anchor role, keeping
	// its own lightness and chroma offset. "A slightly dimmer warning" stays a
	// slightly dimmer warning under any palette (plan.md §7.4).
	s.literals = make(map[color.RGBA]color.RGBA, len(Literals))
	for _, l := range Literals {
		anchorFrom, anchorTo := def.Get(l.Anchor), s.target.Get(l.Anchor)
		if anchorFrom == anchorTo {
			continue
		}
		if colorDistance(l.Color, anchorFrom) > LiteralRadius {
			continue
		}
		s.literals[l.Color] = reanchor(l.Color, anchorFrom, anchorTo)
	}

	s.active.Store(len(s.mapping) > 0 || len(s.literals) > 0)
}

// reanchor moves a literal by the same HSL offset that separates it from its
// anchor's default, applied to the anchor's new value.
func reanchor(literal, anchorFrom, anchorTo color.RGBA) color.RGBA {
	lh, ls, ll := rgbToHSL(literal)
	fh, fs, fl := rgbToHSL(anchorFrom)
	th, ts, tl := rgbToHSL(anchorTo)

	h := th + (lh - fh)
	for h < 0 {
		h++
	}
	for h > 1 {
		h--
	}
	return hslToRGB(h, clamp01(ts+(ls-fs)), clamp01(tl+(ll-fl)))
}

func clamp01(v float64) float64 {
	if v < 0 {
		return 0
	}
	if v > 1 {
		return 1
	}
	return v
}

func colorDistance(a, b color.RGBA) float64 {
	dr := float64(a.R) - float64(b.R)
	dg := float64(a.G) - float64(b.G)
	db := float64(a.B) - float64(b.B)
	return math.Sqrt(dr*dr + dg*dg + db*db)
}

// Active reports whether substitution would change anything.
func (s *Substituter) Active() bool {
	return s != nil && (s.active.Load() || s.LightBackground)
}

// Color maps one color through both passes.
func (s *Substituter) Color(c color.RGBA) color.RGBA {
	if !s.Active() {
		return c
	}
	s.mu.Lock()
	if got, ok := s.cache[c]; ok {
		s.mu.Unlock()
		return got
	}
	s.mu.Unlock()

	out := c
	// Pass 2 first in lookup order but conceptually second: a color that is a
	// configured role or a registered literal is replaced outright and is then
	// exempt from the light-background flip, which is the whole point of the
	// ordering rule.
	if mapped, ok := s.mapping[c]; ok {
		out = mapped
	} else if mapped, ok := s.literals[c]; ok {
		out = mapped
	} else if s.LightBackground {
		out = FlipLuminance(c)
	}

	s.mu.Lock()
	s.cache[c] = out
	s.mu.Unlock()
	return out
}

// Frame rewrites the truecolor SGR sequences in a rendered frame.
//
// It is a hand-rolled tokenizer rather than a regex: this runs over every byte
// of every frame, and the shape being matched is trivially simple.
func (s *Substituter) Frame(frame string) string {
	if !s.Active() || !strings.Contains(frame, "\x1b[") {
		return frame
	}

	var b strings.Builder
	b.Grow(len(frame))

	for i := 0; i < len(frame); {
		if frame[i] != 0x1b || i+1 >= len(frame) || frame[i+1] != '[' {
			b.WriteByte(frame[i])
			i++
			continue
		}
		// Find the sequence's final byte.
		j := i + 2
		for j < len(frame) && (frame[j] < '@' || frame[j] > '~') {
			j++
		}
		if j >= len(frame) || frame[j] != 'm' {
			// Not an SGR sequence; copy it through untouched.
			end := min(j+1, len(frame))
			b.WriteString(frame[i:end])
			i = end
			continue
		}
		b.WriteString(s.rewriteSGR(frame[i : j+1]))
		i = j + 1
	}
	return b.String()
}

// rewriteSGR maps the truecolor components of one `CSI…m` sequence.
func (s *Substituter) rewriteSGR(seq string) string {
	body := seq[2 : len(seq)-1]
	if body == "" {
		return seq
	}
	parts := strings.Split(body, ";")

	out := make([]string, 0, len(parts))
	for i := 0; i < len(parts); i++ {
		// Only 38;2;r;g;b and 48;2;r;g;b carry colors we can map. Indexed
		// colors are left alone: the terminal owns that palette, and
		// second-guessing it is how themed output stops matching its terminal.
		if (parts[i] == "38" || parts[i] == "48") && i+4 < len(parts) && parts[i+1] == "2" {
			r, okR := atoi(parts[i+2])
			g, okG := atoi(parts[i+3])
			bl, okB := atoi(parts[i+4])
			if okR && okG && okB {
				mapped := s.Color(color.RGBA{R: uint8(r), G: uint8(g), B: uint8(bl), A: 255})
				out = append(out, parts[i], "2",
					strconv.Itoa(int(mapped.R)),
					strconv.Itoa(int(mapped.G)),
					strconv.Itoa(int(mapped.B)))
				i += 4
				continue
			}
		}
		out = append(out, parts[i])
	}
	return "\x1b[" + strings.Join(out, ";") + "m"
}

func atoi(s string) (int, bool) {
	if s == "" || len(s) > 3 {
		return 0, false
	}
	n := 0
	for _, r := range s {
		if r < '0' || r > '9' {
			return 0, false
		}
		n = n*10 + int(r-'0')
	}
	if n > 255 {
		return 0, false
	}
	return n, true
}
