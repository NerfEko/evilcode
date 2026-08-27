package tui

import (
	"fmt"
	"strings"

	"charm.land/lipgloss/v2"

	"evilcode/internal/core"
	"evilcode/internal/theme"
)

// LoginPickerEntry is one row in the provider selector shown by `/login` with
// no argument. The selector lets you choose which provider's key you are about
// to paste before the masked composer takes over.
type LoginPickerEntry struct {
	Name   string
	Kind   string
	HasKey bool
}

// LoginPickerState is the `/login` provider selector's state.
type LoginPickerState struct {
	Entries  []LoginPickerEntry
	Filter   string
	Selected int
}

// Filtered returns the entries matching the filter and the matched rune
// indexes per entry, mirroring the model picker.
func (s LoginPickerState) Filtered() ([]LoginPickerEntry, [][]int) {
	if s.Filter == "" {
		return s.Entries, make([][]int, len(s.Entries))
	}
	var out []LoginPickerEntry
	var matches [][]int
	q := strings.ToLower(s.Filter)
	for _, e := range s.Entries {
		if idx, _, ok := fuzzyMatch(q, strings.ToLower(e.Name)); ok {
			out = append(out, e)
			matches = append(matches, idx)
		}
	}
	return out, matches
}

// RenderLoginPicker draws the provider selector. It reuses the model picker's
// chrome deliberately: the two are the same "choose one of these" interaction.
func (r *Renderer) RenderLoginPicker(s LoginPickerState) []string {
	entries, matches := s.Filtered()

	hint := lipgloss.NewStyle().
		Foreground(lipgloss.Color(theme.Hex(theme.RGB(120, 120, 150)))).Italic(true).
		Render("choose a provider to enter a key for · ↑↓ move · ↵ select · Esc cancel")

	accent := lipgloss.NewStyle().
		Foreground(lipgloss.Color(r.Palette.Hex(theme.RoleAccent))).Bold(true)
	dim := r.style(theme.RoleDim)

	head := accent.Render("provider") + dim.Render(" | ") + dim.Render("kind")
	if s.Filter != "" {
		head += dim.Render(fmt.Sprintf("   %q", s.Filter))
	}
	head += dim.Render(fmt.Sprintf(" (%d/%d)", len(entries), len(s.Entries)))

	var body []string
	body = append(body, head)

	if len(entries) == 0 {
		body = append(body, lipgloss.NewStyle().
			Foreground(lipgloss.Color(r.Palette.Hex(theme.RoleDim))).Italic(true).
			Render("   no matches"))
	} else {
		sel := clamp(s.Selected, 0, len(entries)-1)
		for i, e := range entries {
			body = append(body, r.loginPickerRow(e, matches[i], i == sel))
		}
	}

	return append([]string{hint}, r.roundedBox(body)...)
}

func (r *Renderer) loginPickerRow(e LoginPickerEntry, matched []int, selected bool) string {
	marker, markerStyle := " ", r.style(theme.RoleDim)
	if selected {
		marker = "▸"
		markerStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("#ffffff")).Bold(true)
	}

	nameStyle := lipgloss.NewStyle().Foreground(lipgloss.Color(theme.Hex(theme.RGB(200, 200, 220))))
	if selected {
		nameStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("#ffffff")).
			Background(lipgloss.Color(pickerSelectedBg)).Bold(true)
	}
	kindStyle := lipgloss.NewStyle().Foreground(lipgloss.Color(theme.Hex(theme.RGB(140, 180, 255))))

	var b strings.Builder
	b.WriteString(" " + markerStyle.Render(marker) + " ")
	b.WriteString(underlineMatch(core.SanitizeTerminal(e.Name), matched, nameStyle))
	if e.Kind != "" {
		b.WriteString("  " + kindStyle.Render(e.Kind))
	}
	if e.HasKey {
		b.WriteString("  " + r.style(theme.RoleDim).Render("key present"))
	} else {
		b.WriteString("  " + r.style(theme.RoleDim).Render("no key"))
	}
	return b.String()
}
