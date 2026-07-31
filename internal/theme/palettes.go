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

// Palettes returns every built-in palette by name.
func Palettes() map[string]*Palette {
	return map[string]*Palette{
		"dracula": Dracula(),
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
