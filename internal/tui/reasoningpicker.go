package tui

import (
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"evilcode/internal/config"
	"evilcode/internal/core"
	"evilcode/internal/provider"
	"evilcode/internal/theme"
)

// reasoningPickerState is the inline reasoning-level picker shown after a model
// is chosen. It confirms the effort before the selection is applied, defaulting
// to the model's last used level or high so a second Enter accepts it without
// navigation.
type reasoningPickerState struct {
	sel      ModelEntry
	levels   []provider.ReasoningEffort
	selected int
}

// defaultPickerEffort returns the effort to highlight when the reasoning picker
// opens: the model's last used level when one is remembered, then high, then
// the provider's preferred default. High is the requested default rather than
// the shared medium because the picker is an explicit, deliberate selection.
func (m *Model) defaultPickerEffort(ref string, levels []provider.ReasoningEffort) provider.ReasoningEffort {
	if effort, ok := m.reasoningPrefs[ref]; ok && hasReasoningEffort(levels, effort) {
		return effort
	}
	if hasReasoningEffort(levels, provider.ReasoningEffortHigh) {
		return provider.ReasoningEffortHigh
	}
	return preferredReasoningEffort(levels, provider.DefaultReasoningEffort)
}

// candidateReasoningLevels returns the reasoning levels a candidate model would
// expose, without applying the selection. It mirrors applyModel's resolution so
// the picker shows the same levels the apply path will set.
func (m *Model) candidateReasoningLevels(sel ModelEntry) []provider.ReasoningEffort {
	levels := provider.NormalizeReasoningEfforts(sel.ReasoningEfforts)
	if len(levels) == 0 && m.agent != nil {
		// A cross-provider selection may need a different provider's fallback
		// levels. Build a temporary client to ask without committing the switch.
		p := m.agent.Provider
		if sel.Provider != "" && sel.Provider != m.header.Provider {
			if pc := m.providerConfig(sel.Provider); pc != nil {
				if built, err := pc.Build(); err == nil {
					p = built
				}
			}
		}
		levels = provider.ReasoningEffortLevelsForProvider(p, sel.Name)
	}
	if len(levels) == 0 && m.setReasoningEffort != nil && sel.Name == m.header.Model {
		levels = m.reasoningLevels
	}
	return levels
}

// openReasoningPicker shows the reasoning-level picker for the chosen model. It
// returns false when the model has no reasoning levels, in which case the caller
// applies the selection directly with no second menu.
func (m *Model) openReasoningPicker(sel ModelEntry) bool {
	levels := m.candidateReasoningLevels(sel)
	if len(levels) == 0 {
		return false
	}
	ref := config.ModelRef(sel.Name, sel.Provider)
	defaultEffort := m.defaultPickerEffort(ref, levels)
	selected := 0
	for i, level := range levels {
		if level == defaultEffort {
			selected = i
			break
		}
	}
	m.reasoningPicker = reasoningPickerState{sel: sel, levels: levels, selected: selected}
	m.reasoningPickerOpen = true
	return true
}

// handleReasoningPickerKey drives the inline reasoning-level picker.
func (m *Model) handleReasoningPickerKey(key string) (tea.Model, tea.Cmd) {
	switch key {
	case "esc", "ctrl+c":
		m.reasoningPickerOpen = false
		m.reasoningPicker = reasoningPickerState{}
		return m, nil

	case "up", "ctrl+k":
		m.reasoningPicker.selected = MovePaletteSelection(m.reasoningPicker.selected, -1, len(m.reasoningPicker.levels))
		return m, nil

	case "down", "ctrl+j":
		m.reasoningPicker.selected = MovePaletteSelection(m.reasoningPicker.selected, 1, len(m.reasoningPicker.levels))
		return m, nil

	case "enter":
		rp := m.reasoningPicker
		m.reasoningPickerOpen = false
		m.reasoningPicker = reasoningPickerState{}
		if len(rp.levels) == 0 {
			return m, nil
		}
		effort := rp.levels[clamp(rp.selected, 0, len(rp.levels)-1)]
		m.applyModelWithEffort(rp.sel, effort, true)
		return m, nil
	}
	return m, nil
}

// RenderReasoningPicker draws the inline reasoning-level picker. It reuses the
// model picker's chrome deliberately: the two are the same "choose one of
// these" interaction.
func (r *Renderer) RenderReasoningPicker(s reasoningPickerState) []string {
	accent := lipgloss.NewStyle().
		Foreground(lipgloss.Color(r.Palette.Hex(theme.RoleAccent))).Bold(true)
	dim := r.style(theme.RoleDim)
	label := lipgloss.NewStyle().Foreground(lipgloss.Color(theme.Hex(theme.RGB(200, 200, 220))))
	selected := lipgloss.NewStyle().
		Foreground(lipgloss.Color("#ffffff")).
		Background(lipgloss.Color(pickerSelectedBg)).Bold(true)

	var body []string
	title := "Reasoning effort for " + core.SanitizeTerminal(s.sel.Name)
	body = append(body, accent.Render(title))
	body = append(body, "")

	for i, level := range s.levels {
		marker := " "
		if i == s.selected {
			marker = "▸"
		}
		row := " " + selectedIf(i == s.selected, marker, selected, dim) + " "
		if i == s.selected {
			row += selected.Render(string(level))
		} else {
			row += label.Render(string(level))
		}
		body = append(body, row)
	}

	hint := "↑↓ choose · ↵ confirm · Esc cancel"
	body = append(body, "", dim.Render(hint))

	return r.roundedBox(body)
}
