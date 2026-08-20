package tui

import (
	"strings"

	"evilcode/internal/agent"
	"evilcode/internal/config"
	"evilcode/internal/provider"
)

func providerForAgent(a *agent.Agent) provider.Provider {
	if a == nil {
		return nil
	}
	return a.Provider
}

func preferredReasoningEffort(levels []provider.ReasoningEffort, current provider.ReasoningEffort) provider.ReasoningEffort {
	levels = provider.NormalizeReasoningEfforts(levels)
	for _, level := range levels {
		if level == current {
			return current
		}
	}
	for _, level := range levels {
		if level == provider.DefaultReasoningEffort {
			return level
		}
	}
	if len(levels) > 0 {
		return levels[0]
	}
	return provider.DefaultReasoningEffort
}

func hasReasoningEffort(levels []provider.ReasoningEffort, effort provider.ReasoningEffort) bool {
	for _, level := range levels {
		if level == effort {
			return true
		}
	}
	return false
}

func reasoningEffortsText(levels []provider.ReasoningEffort) string {
	parts := make([]string, 0, len(levels))
	for _, level := range provider.NormalizeReasoningEfforts(levels) {
		parts = append(parts, string(level))
	}
	return strings.Join(parts, ", ")
}

func (m *Model) activeModelRef() string {
	return config.ModelRef(m.header.Model, m.header.Provider)
}

func (m *Model) rememberedEffort(ref string, fallback provider.ReasoningEffort) provider.ReasoningEffort {
	if effort, ok := m.reasoningPrefs[ref]; ok {
		return effort
	}
	return fallback
}

func (m *Model) rememberModel(ref string) error {
	if m.saveLastModel != nil && ref != m.lastModel {
		if err := m.saveLastModel(ref); err != nil {
			return err
		}
	}
	m.lastModel = ref
	return nil
}

func (m *Model) rememberEffort(ref string, effort provider.ReasoningEffort) error {
	if m.reasoningPrefs == nil {
		m.reasoningPrefs = map[string]provider.ReasoningEffort{}
	}
	m.reasoningPrefs[ref] = effort
	if m.saveReasoningEffort != nil {
		if err := m.saveReasoningEffort(ref, effort); err != nil {
			return err
		}
	}
	return nil
}
