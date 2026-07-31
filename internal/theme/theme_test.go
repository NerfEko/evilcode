package theme

import (
	"image/color"
	"math"
	"testing"
)

// TestDraculaPaletteIsFrozen holds a deliberately redundant copy of the §7.1
// table. Every ad-hoc rgb() literal in the spec was chosen against these
// values, so the default palette drifting silently would quietly break the
// look of things that never mention it.
func TestDraculaPaletteIsFrozen(t *testing.T) {
	want := map[Role]string{
		RoleUser:          "#bd93f9",
		RoleAI:            "#50fa7b",
		RoleTool:          "#787878",
		RoleFileLink:      "#b4c8ff",
		RoleDim:           "#505050",
		RoleAccent:        "#ff79c6",
		RoleSystem:        "#ffb86c",
		RoleQueued:        "#f1fa8c",
		RoleAsap:          "#8be9fd",
		RolePending:       "#8c8c8c",
		RoleBorder:        "#44475a",
		RoleUserText:      "#f8f8f2",
		RoleUserBg:        "#2a2440",
		RoleAIText:        "#dcdcd7",
		RoleHeaderIcon:    "#ff79c6",
		RoleHeaderName:    "#bd93f9",
		RoleHeaderSession: "#ffffff",
		RoleSuccess:       "#50fa7b",
		RoleWarning:       "#ffb86c",
		RoleError:         "#ff5555",
		RoleInfo:          "#8cb4ff",
		RoleSelectionBg:   "#44475a",
	}
	p := Dracula()
	if len(want) != int(numRoles) {
		t.Fatalf("the frozen table has %d roles but there are %d — add the new role here too",
			len(want), int(numRoles))
	}
	for role, hex := range want {
		if got := p.Hex(role); got != hex {
			t.Errorf("role %s = %s, want %s", role, got, hex)
		}
	}
}

func TestEveryRoleIsNamedAndSet(t *testing.T) {
	p := Dracula()
	seen := map[string]Role{}
	for _, r := range AllRoles() {
		name := r.String()
		if name == "" {
			t.Errorf("role %d has no name", int(r))
		}
		if prev, dup := seen[name]; dup {
			t.Errorf("roles %d and %d share the name %q", int(prev), int(r), name)
		}
		seen[name] = r

		if got, ok := RoleByName(name); !ok || got != r {
			t.Errorf("RoleByName(%q) = %d, %v; want %d", name, int(got), ok, int(r))
		}
		if c := p.Get(r); c.A == 0 {
			t.Errorf("role %s has no color set in the default palette", name)
		}
	}
}

func TestParseHex(t *testing.T) {
	tests := []struct {
		in      string
		want    color.RGBA
		wantErr bool
	}{
		{"#ff79c6", RGB(0xff, 0x79, 0xc6), false},
		{"ff79c6", RGB(0xff, 0x79, 0xc6), false},
		{"#FF79C6", RGB(0xff, 0x79, 0xc6), false},
		{"#000000", RGB(0, 0, 0), false},
		{"#fff", color.RGBA{}, true},
		{"#gggggg", color.RGBA{}, true},
		{"", color.RGBA{}, true},
	}
	for _, tt := range tests {
		got, err := ParseHex(tt.in)
		if tt.wantErr {
			if err == nil {
				t.Errorf("ParseHex(%q) = %v, want an error", tt.in, got)
			}
			continue
		}
		if err != nil {
			t.Errorf("ParseHex(%q): %v", tt.in, err)
			continue
		}
		if got != tt.want {
			t.Errorf("ParseHex(%q) = %v, want %v", tt.in, got, tt.want)
		}
	}
}

func TestHexRoundTrip(t *testing.T) {
	p := Dracula()
	for _, r := range AllRoles() {
		hex := p.Hex(r)
		back, err := ParseHex(hex)
		if err != nil {
			t.Fatalf("role %s: %v", r, err)
		}
		if back != p.Get(r) {
			t.Errorf("role %s did not round-trip: %v -> %s -> %v", r, p.Get(r), hex, back)
		}
	}
}

func TestBlend(t *testing.T) {
	black, white := RGB(0, 0, 0), RGB(255, 255, 255)
	if got := Blend(black, white, 0); got != black {
		t.Errorf("t=0 should be the start color, got %v", got)
	}
	if got := Blend(black, white, 1); got != white {
		t.Errorf("t=1 should be the end color, got %v", got)
	}
	mid := Blend(black, white, 0.5)
	if mid.R < 126 || mid.R > 129 {
		t.Errorf("t=0.5 should be about halfway, got %v", mid)
	}
	// Out-of-range t must clamp rather than overshoot into a wrapped channel.
	if got := Blend(black, white, 2); got != white {
		t.Errorf("t>1 should clamp, got %v", got)
	}
	if got := Blend(black, white, -1); got != black {
		t.Errorf("t<0 should clamp, got %v", got)
	}
}

func TestRainbowNewestIsFullRed(t *testing.T) {
	// Distance 0 is the newest prompt, and the spec is explicit that it is
	// full red — it is the anchor the whole ramp is read against.
	if got := Rainbow(0); got != RGB(255, 80, 80) {
		t.Errorf("Rainbow(0) = %v, want rgb(255,80,80)", got)
	}
}

func TestRainbowDecaysTowardGray(t *testing.T) {
	var prev float64 = -1
	for d := 0; d < 10; d++ {
		c := Rainbow(d)
		dist := colorDistance(c, rainbowGray)
		if prev >= 0 && dist > prev {
			t.Errorf("distance from gray grew at d=%d (%f -> %f); older prompts must fade",
				d, prev, dist)
		}
		prev = dist
	}
	// Far enough out, it is essentially gray.
	if d := colorDistance(Rainbow(20), rainbowGray); d > 5 {
		t.Errorf("Rainbow(20) is still %f from gray", d)
	}
}

func TestRainbowClampsIndex(t *testing.T) {
	// Beyond the last stop the index must clamp, not panic or wrap.
	for _, d := range []int{-5, 6, 7, 100} {
		got := Rainbow(d)
		if got.A != 255 {
			t.Errorf("Rainbow(%d) = %v", d, got)
		}
	}
}

func TestAnimatedToolCycles(t *testing.T) {
	flat := RGB(120, 120, 120)

	// With animation off, the color is flat — but never so that the UI reads
	// as frozen elsewhere; that is the spinner's job.
	if got := AnimatedTool(0.7, false, flat); got != flat {
		t.Errorf("animation off = %v, want the flat color", got)
	}

	// Over a cycle it must actually move, and stay between the endpoints.
	seen := map[color.RGBA]bool{}
	for i := 0; i < 40; i++ {
		c := AnimatedTool(float64(i)*0.05, true, flat)
		seen[c] = true
		if c.R < 79 || c.R > 187 {
			t.Errorf("R channel %d left the cyan..purple range", c.R)
		}
	}
	if len(seen) < 10 {
		t.Errorf("only %d distinct colors over a cycle; the animation is not moving", len(seen))
	}
}

func TestMeterColorFollowsRemaining(t *testing.T) {
	// A meter that reddens as it empties is a warning; one that reddens as it
	// fills is a progress bar. The spec wants the warning.
	tests := []struct {
		remaining float64
		want      color.RGBA
	}{
		{1.0, RGB(100, 200, 100)},
		{0.51, RGB(100, 200, 100)},
		{0.50, RGB(255, 200, 100)},
		{0.21, RGB(255, 200, 100)},
		{0.20, RGB(255, 100, 100)},
		{0.0, RGB(255, 100, 100)},
	}
	for _, tt := range tests {
		if got := MeterColor(tt.remaining); got != tt.want {
			t.Errorf("MeterColor(%.2f) = %v, want %v", tt.remaining, got, tt.want)
		}
	}
}

func TestLightenAndDarken(t *testing.T) {
	c := RGB(100, 100, 100)
	// c + (255-c)/2
	if got := Lighten(c); got.R != 100+(255-100)/2 {
		t.Errorf("Lighten = %v", got)
	}
	if got := Darken(c); got.R != 50 {
		t.Errorf("Darken = %v", got)
	}
	// White cannot overflow, black cannot underflow.
	if got := Lighten(RGB(255, 255, 255)); got.R != 255 {
		t.Errorf("Lighten(white) = %v", got)
	}
	if got := Darken(RGB(0, 0, 0)); got.R != 0 {
		t.Errorf("Darken(black) = %v", got)
	}
}

func TestTintDiffKeepsSyntaxDominant(t *testing.T) {
	// 70% syntax / 30% diff: the code keeps its highlighting but reads
	// unmistakably as an add or a delete (plan.md §9.3).
	syntax := RGB(200, 200, 200)
	got := TintDiff(syntax, DiffAdd)
	wantR := uint8((200*70 + int(DiffAdd.R)*30) / 100)
	if got.R != wantR {
		t.Errorf("tinted R = %d, want %d", got.R, wantR)
	}
	// The tint must be visible but not overwhelming.
	if colorDistance(got, syntax) < 5 {
		t.Error("tint is invisible")
	}
	if colorDistance(got, DiffAdd) < colorDistance(got, syntax) {
		t.Error("tint overwhelmed the syntax color; syntax must stay dominant")
	}
}

func TestFlipLuminancePreservesHue(t *testing.T) {
	// This is why pass 1 is an HSL flip and not a per-channel inversion: the
	// hue has to survive.
	for _, c := range []color.RGBA{
		RGB(0xff, 0x79, 0xc6),
		RGB(0x50, 0xfa, 0x7b),
		RGB(0xbd, 0x93, 0xf9),
	} {
		h1, s1, l1 := rgbToHSL(c)
		flipped := FlipLuminance(c)
		h2, s2, l2 := rgbToHSL(flipped)

		if math.Abs(h1-h2) > 0.01 && math.Abs(h1-h2) < 0.99 {
			t.Errorf("%v: hue moved %.3f -> %.3f", c, h1, h2)
		}
		if math.Abs(s1-s2) > 0.15 {
			t.Errorf("%v: saturation moved %.3f -> %.3f", c, s1, s2)
		}
		if math.Abs((1-l1)-l2) > 0.02 {
			t.Errorf("%v: lightness %.3f should flip to %.3f, got %.3f", c, l1, 1-l1, l2)
		}
	}
}

func TestHSLRoundTrip(t *testing.T) {
	for _, c := range []color.RGBA{
		RGB(0, 0, 0), RGB(255, 255, 255), RGB(128, 128, 128),
		RGB(255, 0, 0), RGB(0, 255, 0), RGB(0, 0, 255),
		RGB(0xbd, 0x93, 0xf9),
	} {
		h, s, l := rgbToHSL(c)
		back := hslToRGB(h, s, l)
		if colorDistance(c, back) > 2 {
			t.Errorf("%v did not round-trip through HSL: got %v", c, back)
		}
	}
}

func TestHSV(t *testing.T) {
	tests := []struct {
		h, s, v float64
		want    color.RGBA
	}{
		{0, 1, 1, RGB(255, 0, 0)},
		{120, 1, 1, RGB(0, 255, 0)},
		{240, 1, 1, RGB(0, 0, 255)},
		{0, 0, 1, RGB(255, 255, 255)},
		{0, 0, 0, RGB(0, 0, 0)},
		// Hue must wrap rather than clamp — the idle art sweeps past 360.
		{360, 1, 1, RGB(255, 0, 0)},
		{-120, 1, 1, RGB(0, 0, 255)},
	}
	for _, tt := range tests {
		if got := HSV(tt.h, tt.s, tt.v); colorDistance(got, tt.want) > 2 {
			t.Errorf("HSV(%.0f,%.1f,%.1f) = %v, want %v", tt.h, tt.s, tt.v, got, tt.want)
		}
	}
}

func TestLuminance(t *testing.T) {
	if got := Luminance(RGB(0, 0, 0)); got != 0 {
		t.Errorf("black luminance = %f", got)
	}
	if got := Luminance(RGB(255, 255, 255)); math.Abs(got-1) > 0.001 {
		t.Errorf("white luminance = %f", got)
	}
	// Green reads brighter than blue at the same channel value.
	if Luminance(RGB(0, 255, 0)) <= Luminance(RGB(0, 0, 255)) {
		t.Error("luminance weighting looks wrong")
	}
}

func TestByNameFallsBack(t *testing.T) {
	if got := ByName("dracula"); got.Name != "dracula" {
		t.Errorf("ByName(dracula) = %q", got.Name)
	}
	// A typo in a config file must degrade to the default, not a blank screen.
	if got := ByName("draclua"); got.Name != "catppuccin-frappe" {
		t.Errorf("ByName(typo) = %q, want the default fallback", got.Name)
	}
}

func TestMarkdownPaletteMatchesSpec(t *testing.T) {
	m := DefaultMarkdown()
	want := map[string]struct {
		got  color.RGBA
		want string
	}{
		// Headings deliberately leave the §7.2 amber ramp — see DEVIATIONS #12.
		// Everything else below is still the spec table verbatim.
		"body":        {m.Body, "#c8c8c3"},
		"bold":        {m.BoldText, "#f0f0eb"},
		"inline_code": {m.InlineCode, "#b4b4b4"},
		"code_bg":     {m.CodeBg, "#2d2d2d"},
		"link":        {m.Link, "#78b4f0"},
		"dim":         {m.Dim, "#646464"},
		"table":       {m.Table, "#969696"},
		"math":        {m.Math, "#64a0ff"},
		"inline_math": {m.InlineMath, "#b9c8e1"},
		"html":        {m.HTML, "#8c8c96"},
	}
	for name, tt := range want {
		if got := Hex(tt.got); got != tt.want {
			t.Errorf("markdown %s = %s, want %s", name, got, tt.want)
		}
	}
}

func TestHeadingsFollowTheirPalette(t *testing.T) {
	// Prose used to be a package-level constant, so `/theme` recolored the
	// chrome and left every heading in every reply the same amber. Each palette
	// now carries its own §7.2 table, and this is the regression that keeps it
	// from quietly collapsing back to one shared ramp.
	seen := map[string]string{}
	for name, p := range Palettes() {
		if p.Prose.H1 == (color.RGBA{}) {
			t.Errorf("%s supplies no prose table", name)
			continue
		}
		seen[name] = Hex(p.Prose.H1)
	}
	if len(seen) < 2 {
		t.Fatal("expected several palettes")
	}
	if Hex(CatppuccinFrappe().Prose.H1) == Hex(Gloom().Prose.H1) {
		t.Error("two palettes share a heading color; prose is not following the theme")
	}
	// And the default's headings sit on its own accent rather than on amber.
	if got := Hex(CatppuccinFrappe().Prose.H1); got != "#ca9ee6" {
		t.Errorf("catppuccin h1 = %s, want the mauve accent", got)
	}
}

func TestEveryPaletteIsComplete(t *testing.T) {
	for name, p := range Palettes() {
		if p.Name != name {
			t.Errorf("palette registered as %q calls itself %q", name, p.Name)
		}
		for _, r := range AllRoles() {
			if c := p.Get(r); c.A == 0 {
				t.Errorf("palette %q has no color for role %s", name, r)
			}
		}
	}
}

func TestPalettesAreReadableAgainstTheirBackground(t *testing.T) {
	// Every foreground role must stand off the background it was designed for,
	// or the palette is unusable however nice it looks in a swatch.
	for name, p := range Palettes() {
		bg := RGB(18, 18, 24)
		if p.Light {
			bg = RGB(250, 250, 252)
		}
		bgLum := Luminance(bg)
		for _, r := range []Role{RoleUser, RoleAI, RoleAccent, RoleError, RoleWarning, RoleSuccess} {
			if diff := math.Abs(Luminance(p.Get(r)) - bgLum); diff < 0.12 {
				t.Errorf("palette %q role %s has luminance contrast %.3f against its background",
					name, r, diff)
			}
		}
	}
}
