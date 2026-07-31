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
	return p
}

// Palettes returns every built-in palette by name.
func Palettes() map[string]*Palette {
	return map[string]*Palette{
		"dracula":   Dracula(),
		"nosferatu": Nosferatu(),
		"gloom":     Gloom(),
		"daywalker": Daywalker(),
	}
}

// ByName returns a palette, falling back to dracula for an unknown name so a
// typo in a config file degrades to the default rather than to a blank screen.
func ByName(name string) *Palette {
	if p, ok := Palettes()[name]; ok {
		return p
	}
	return Dracula()
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

// DefaultMarkdown is the §7.2 table.
func DefaultMarkdown() Markdown {
	must := func(hex string) color.RGBA {
		c, err := ParseHex(hex)
		if err != nil {
			panic("theme: bad markdown literal: " + hex)
		}
		return c
	}
	return Markdown{
		H1:         must("#ffd764"),
		H2:         must("#f0be5a"),
		H3:         must("#dcaa50"),
		H4:         must("#c89b4b"),
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
