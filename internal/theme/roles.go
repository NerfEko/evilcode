// Package theme owns evilcode's color vocabulary: the semantic roles, the
// palettes that fill them, and the procedural colors that carry identity
// (plan.md §7).
package theme

import (
	"fmt"
	"image/color"
	"strings"
)

// Role is a semantic color slot. Widgets ask for roles, never for hex values,
// so a palette swap reaches everything at once.
type Role int

// The 22 roles of plan.md §7.1.
const (
	RoleUser Role = iota
	RoleAI
	RoleTool
	RoleFileLink
	RoleDim
	RoleAccent
	RoleSystem
	RoleQueued
	RoleAsap
	RolePending
	RoleBorder
	RoleUserText
	RoleUserBg
	RoleAIText
	RoleHeaderIcon
	RoleHeaderName
	RoleHeaderSession
	RoleSuccess
	RoleWarning
	RoleError
	RoleInfo
	RoleSelectionBg

	numRoles
)

// roleNames maps roles to their config spelling.
var roleNames = [numRoles]string{
	RoleUser:          "user",
	RoleAI:            "ai",
	RoleTool:          "tool",
	RoleFileLink:      "file_link",
	RoleDim:           "dim",
	RoleAccent:        "accent",
	RoleSystem:        "system",
	RoleQueued:        "queued",
	RoleAsap:          "asap",
	RolePending:       "pending",
	RoleBorder:        "border",
	RoleUserText:      "user_text",
	RoleUserBg:        "user_bg",
	RoleAIText:        "ai_text",
	RoleHeaderIcon:    "header_icon",
	RoleHeaderName:    "header_name",
	RoleHeaderSession: "header_session",
	RoleSuccess:       "success",
	RoleWarning:       "warning",
	RoleError:         "error",
	RoleInfo:          "info",
	RoleSelectionBg:   "selection_bg",
}

func (r Role) String() string {
	if r < 0 || r >= numRoles {
		return fmt.Sprintf("role(%d)", int(r))
	}
	return roleNames[r]
}

// RoleByName resolves a config spelling to a role.
func RoleByName(name string) (Role, bool) {
	for i, n := range roleNames {
		if n == name {
			return Role(i), true
		}
	}
	return 0, false
}

// AllRoles lists every role, in declaration order.
func AllRoles() []Role {
	out := make([]Role, numRoles)
	for i := range out {
		out[i] = Role(i)
	}
	return out
}

// Palette assigns a color to every role.
type Palette struct {
	Name   string
	Colors [numRoles]color.RGBA

	// Light marks a palette designed for a light terminal background.
	Light bool
}

// Get returns the color for a role.
func (p *Palette) Get(r Role) color.RGBA {
	if r < 0 || r >= numRoles {
		return p.Colors[RoleDim]
	}
	return p.Colors[r]
}

// Hex renders a role as `#rrggbb`, which is what lipgloss wants.
func (p *Palette) Hex(r Role) string { return Hex(p.Get(r)) }

// Hex renders a color as `#rrggbb`.
func Hex(c color.RGBA) string {
	return fmt.Sprintf("#%02x%02x%02x", c.R, c.G, c.B)
}

// RGB builds an opaque color, for the ad-hoc literals the spec quotes as
// `rgb(r,g,b)` throughout.
func RGB(r, g, b uint8) color.RGBA {
	return color.RGBA{R: r, G: g, B: b, A: 255}
}

// ParseHex reads `#rrggbb` or `rrggbb`.
func ParseHex(s string) (color.RGBA, error) {
	s = strings.TrimPrefix(strings.TrimSpace(s), "#")
	if len(s) != 6 {
		return color.RGBA{}, fmt.Errorf("theme: %q is not a #rrggbb color", s)
	}
	var v [3]uint8
	for i := 0; i < 3; i++ {
		hi, ok1 := hexDigit(s[i*2])
		lo, ok2 := hexDigit(s[i*2+1])
		if !ok1 || !ok2 {
			return color.RGBA{}, fmt.Errorf("theme: %q is not a #rrggbb color", s)
		}
		v[i] = hi<<4 | lo
	}
	return color.RGBA{R: v[0], G: v[1], B: v[2], A: 255}, nil
}

func hexDigit(b byte) (uint8, bool) {
	switch {
	case b >= '0' && b <= '9':
		return b - '0', true
	case b >= 'a' && b <= 'f':
		return b - 'a' + 10, true
	case b >= 'A' && b <= 'F':
		return b - 'A' + 10, true
	}
	return 0, false
}

// Blend is a per-channel linear interpolation, used everywhere procedural
// color is computed (plan.md §7.7).
func Blend(from, to color.RGBA, t float64) color.RGBA {
	if t <= 0 {
		return from
	}
	if t >= 1 {
		return to
	}
	lerp := func(a, b uint8) uint8 {
		return uint8(float64(a) + (float64(b)-float64(a))*t + 0.5)
	}
	return color.RGBA{
		R: lerp(from.R, to.R),
		G: lerp(from.G, to.G),
		B: lerp(from.B, to.B),
		A: 255,
	}
}
