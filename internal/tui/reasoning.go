package tui

import (
	"strings"

	"evilcode/internal/agent"
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
