package theme

import "image/color"

// Dracula is the default palette (plan.md §7.1). A test holds a redundant copy
// of this table and asserts equality, so the default can never drift by
// accident — it is the one palette every ad-hoc literal in the spec was chosen
// against.
func Dracula() *Palette {
	p := &Palette{Name: "dracula"}
	set := func(r Role, hex string) {
		c, err := ParseHex(hex)
		if err != nil {
			panic("theme: bad literal in the dracula palette: " + hex)
		}
		p.Colors[r] = c
	}
	set(RoleUser, "#bd93f9")
	set(RoleAI, "#50fa7b")
	set(RoleTool, "#787878")
	set(RoleFileLink, "#b4c8ff")
	set(RoleDim, "#505050")
	set(RoleAccent, "#ff79c6")
	set(RoleSystem, "#ffb86c")
	set(RoleQueued, "#f1fa8c")
	set(RoleAsap, "#8be9fd")
	set(RolePending, "#8c8c8c")
	set(RoleBorder, "#44475a")
	set(RoleUserText, "#f8f8f2")
	set(RoleUserBg, "#2a2440")
	set(RoleAIText, "#dcdcd7")
	set(RoleHeaderIcon, "#ff79c6")
	set(RoleHeaderName, "#bd93f9")
	set(RoleHeaderSession, "#ffffff")
	set(RoleSuccess, "#50fa7b")
	set(RoleWarning, "#ffb86c")
	set(RoleError, "#ff5555")
	set(RoleInfo, "#8cb4ff")
	set(RoleSelectionBg, "#44475a")
	p.Prose = DefaultMarkdown()
	return p
}

// Nosferatu is near-monochrome with blood-red accents: the palette for when
// dracula feels too cheerful.
//
// Near-monochrome is the design, but roles still have to be told apart, and
// with almost no hue to work with that separation has to come from lightness.
// The must-distinguish pairs therefore sit on deliberately different lightness
// levels — which is the same reason the generator separates success, warning,
// and error that way for colorblind safety (§7.5).
func Nosferatu() *Palette {
	p := &Palette{Name: "nosferatu"}
	set := func(r Role, hex string) {
		c, err := ParseHex(hex)
		if err != nil {
			panic("theme: bad literal in nosferatu: " + hex)
		}
		p.Colors[r] = c
	}
	set(RoleUser, "#c4b0b4")
	set(RoleAI, "#8fa090")
	set(RoleTool, "#6e6a68")
	set(RoleFileLink, "#9aa4b4")
	set(RoleDim, "#565250")
	set(RoleAccent, "#c85462")
	set(RoleSystem, "#a8705a")
	set(RoleQueued, "#c0a868")
	set(RoleAsap, "#78a4b0")
	set(RolePending, "#7a7674")
	set(RoleBorder, "#3a3634")
	set(RoleUserText, "#ece8e4")
	set(RoleUserBg, "#241416")
	set(RoleAIText, "#c8c2be")
	set(RoleHeaderIcon, "#c85462")
	set(RoleHeaderName, "#c4b0b4")
	set(RoleHeaderSession, "#ffffff")
	set(RoleSuccess, "#8cb884")
	set(RoleWarning, "#c08a4c")
	set(RoleError, "#a83c48")
	set(RoleInfo, "#7e94b8")
	set(RoleSelectionBg, "#3a2428")
	p.Prose = DefaultMarkdown()
	return p
}

// Gloom is slate with a sickly green.
func Gloom() *Palette {
	p := &Palette{Name: "gloom"}
	set := func(r Role, hex string) {
		c, err := ParseHex(hex)
		if err != nil {
			panic("theme: bad literal in gloom: " + hex)
		}
		p.Colors[r] = c
	}
	set(RoleUser, "#8fa8b8")
	set(RoleAI, "#9ec078")
	set(RoleTool, "#6a7480")
	set(RoleFileLink, "#a8c0d0")
	set(RoleDim, "#4a5460")
	set(RoleAccent, "#b8d060")
	set(RoleSystem, "#d0a860")
	set(RoleQueued, "#c8c878")
	set(RoleAsap, "#78c0c0")
	set(RolePending, "#78828e")
	set(RoleBorder, "#3e4650")
	set(RoleUserText, "#e0e8ee")
	set(RoleUserBg, "#1e2830")
	set(RoleAIText, "#c8d2da")
	set(RoleHeaderIcon, "#b8d060")
	set(RoleHeaderName, "#8fa8b8")
	set(RoleHeaderSession, "#ffffff")
	set(RoleSuccess, "#9ec078")
	set(RoleWarning, "#d0a860")
	set(RoleError, "#c86868")
	set(RoleInfo, "#8fa8b8")
	set(RoleSelectionBg, "#2e3a44")
	p.Prose = DefaultMarkdown()
	return p
}

// Daywalker is the light-background palette, derived from dracula by a
// luminance flip and then hand-corrected where the flip alone reads badly.
func Daywalker() *Palette {
	p := &Palette{Name: "daywalker", Light: true}
	set := func(r Role, hex string) {
		c, err := ParseHex(hex)
		if err != nil {
			panic("theme: bad literal in daywalker: " + hex)
		}
		p.Colors[r] = c
	}
	set(RoleUser, "#6b3fb8")
	set(RoleAI, "#1f8a3c")
	set(RoleTool, "#6a6a6a")
	set(RoleFileLink, "#2c56b0")
	set(RoleDim, "#9a9a9a")
	set(RoleAccent, "#c4187a")
	set(RoleSystem, "#a85c10")
	set(RoleQueued, "#8a7a10")
	set(RoleAsap, "#0f7a90")
	set(RolePending, "#787878")
	set(RoleBorder, "#c4c6d0")
	set(RoleUserText, "#1a1a20")
	set(RoleUserBg, "#e6e0f6")
	set(RoleAIText, "#24242a")
	set(RoleHeaderIcon, "#c4187a")
	set(RoleHeaderName, "#6b3fb8")
	set(RoleHeaderSession, "#000000")
	set(RoleSuccess, "#1f8a3c")
	set(RoleWarning, "#a85c10")
	set(RoleError, "#c01c1c")
	set(RoleInfo, "#2c56b0")
	set(RoleSelectionBg, "#d4d8e8")
	p.Prose = DefaultMarkdown()
	return p
}

// Palettes returns every built-in palette by name.
func Palettes() map[string]*Palette {
	return map[string]*Palette{
		"catppuccin-frappe": CatppuccinFrappe(),
		"dracula":           Dracula(),
		"nosferatu":         Nosferatu(),
		"gloom":             Gloom(),
		"daywalker":         Daywalker(),
	}
}

// ByName returns a palette, falling back to the default for an unknown name so a
// typo in a config file degrades to the default rather than to a blank screen.
func ByName(name string) *Palette {
	if p, ok := Palettes()[name]; ok {
		return p
	}
	return CatppuccinFrappe()
}

// Markdown holds the prose palette of plan.md §7.2. It is separate from the
// semantic roles because markdown styling is its own vocabulary — a heading is
// not a "warning" that happens to be gold.
type Markdown struct {
	H1, H2, H3, H4         color.RGBA
	Body, BoldText         color.RGBA
	InlineCode, CodeBg     color.RGBA
	Link                   color.RGBA
	Dim                    color.RGBA
	Table                  color.RGBA
	Math, InlineMath, HTML color.RGBA
}

// DefaultMarkdown is dracula's §7.2 table, kept as the fallback for a palette
// that does not supply one.
func DefaultMarkdown() Markdown {
	must := func(hex string) color.RGBA {
		c, err := ParseHex(hex)
		if err != nil {
			panic("theme: bad markdown literal: " + hex)
		}
		return c
	}
	return Markdown{
		// Headings ride the palette's own violet→pink rather than an amber ramp
		// borrowed from nowhere: #bd93f9 is RoleUser and #ff79c6 is RoleAccent,
		// so a heading now reads as part of the theme instead of against it.
		H1:         must("#bd93f9"),
		H2:         must("#c9a3fa"),
		H3:         must("#d4b8fb"),
		H4:         must("#cbb3e8"),
		Body:       must("#c8c8c3"),
		BoldText:   must("#f0f0eb"),
		InlineCode: must("#b4b4b4"),
		CodeBg:     must("#2d2d2d"),
		Link:       must("#78b4f0"),
		Dim:        must("#646464"),
		Table:      must("#969696"),
		Math:       must("#64a0ff"),
		InlineMath: must("#b9c8e1"),
		HTML:       must("#8c8c96"),
	}
}

// Diff colors (plan.md §7.3).
var (
	DiffAdd = RGB(0x64, 0xc8, 0x64)
	DiffDel = RGB(0xc8, 0x64, 0x64)
)

// TintDiff blends a syntax-highlighted color toward a diff color so a changed
// line keeps its highlighting yet still reads unmistakably as an add or a
// delete: out = (syntax*70 + diff*30) / 100 (plan.md §9.3).
func TintDiff(syntax, diff color.RGBA) color.RGBA {
	mix := func(a, b uint8) uint8 {
		return uint8((int(a)*70 + int(b)*30) / 100)
	}
	return color.RGBA{
		R: mix(syntax.R, diff.R),
		G: mix(syntax.G, diff.G),
		B: mix(syntax.B, diff.B),
		A: 255,
	}
}

// CatppuccinFrappe is the published Catppuccin Frappé palette with the Mauve
// accent — the same one the desktop's GTK theme uses, so evilcode sits in the
// session rather than beside it.
//
// The hex values are transcribed from the published spec, not chosen: base
// #303446, text #c6d0f5, mauve #ca9ee6, lavender #babbf1, and so on. Where a
// role has no direct Catppuccin equivalent the nearest named colour is used
// rather than a blend, so the palette stays recognisably Frappé.
func CatppuccinFrappe() *Palette {
	p := &Palette{Name: "catppuccin-frappe"}
	set := func(r Role, hex string) {
		c, err := ParseHex(hex)
		if err != nil {
			panic("theme: bad literal in the catppuccin-frappe palette: " + hex)
		}
		p.Colors[r] = c
	}
	set(RoleUser, "#ca9ee6")          // mauve — the accent the GTK theme is built on
	set(RoleAI, "#a6d189")            // green
	set(RoleTool, "#838ba7")          // overlay1
	set(RoleFileLink, "#8caaee")      // blue
	set(RoleDim, "#626880")           // surface2
	set(RoleAccent, "#f4b8e4")        // pink
	set(RoleSystem, "#ef9f76")        // peach
	set(RoleQueued, "#e5c890")        // yellow
	set(RoleAsap, "#99d1db")          // sky
	set(RolePending, "#737994")       // overlay0
	set(RoleBorder, "#51576d")        // surface1
	set(RoleUserText, "#c6d0f5")      // text
	set(RoleUserBg, "#414559")        // surface0
	set(RoleAIText, "#c6d0f5")        // text
	set(RoleHeaderIcon, "#f4b8e4")    // pink
	set(RoleHeaderName, "#ca9ee6")    // mauve
	set(RoleHeaderSession, "#c6d0f5") // text
	set(RoleSuccess, "#a6d189")       // green
	set(RoleWarning, "#e5c890")       // yellow
	set(RoleError, "#e78284")         // red
	set(RoleInfo, "#8caaee")          // blue
	set(RoleSelectionBg, "#51576d")   // surface1

	must := func(hex string) color.RGBA {
		c, err := ParseHex(hex)
		if err != nil {
			panic("theme: bad markdown literal in catppuccin-frappe: " + hex)
		}
		return c
	}
	p.Prose = Markdown{
		// Mauve → lavender, so headings carry the accent the desktop already
		// uses rather than the amber the §7.2 table shipped with.
		H1:         must("#ca9ee6"),
		H2:         must("#c0a3ea"),
		H3:         must("#babbf1"),
		H4:         must("#b5bfe2"),
		Body:       must("#c6d0f5"),
		BoldText:   must("#eff1f5"),
		InlineCode: must("#a5adce"),
		CodeBg:     must("#292c3c"),
		Link:       must("#8caaee"),
		Dim:        must("#737994"),
		Table:      must("#949cbb"),
		Math:       must("#85c1dc"),
		InlineMath: must("#99d1db"),
		HTML:       must("#838ba7"),
	}
	return p
}
