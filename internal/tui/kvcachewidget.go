package tui

import (
	"fmt"
	"strings"

	"charm.land/lipgloss/v2"

	"evilcode/internal/theme"
)

// KvCacheWidget renders the DeepSeek prompt-cache hit-rate meter
// (plan.md §8.5-adjacent). It is fed by the prompt_cache_hit_tokens /
// prompt_cache_miss_tokens DeepSeek reports in usage, accumulated across the
// session.
//
//	Read   tokens served from the KV cache (hits)
//	Write  tokens written into the cache this turn (misses)
//	Hit%   read / (read + write)
//
// Absent until the provider has reported cache tokens, which is what keeps it
// off-screen for non-caching providers: their counts stay at zero and the
// widget returns empty.
func (r *Renderer) KvCacheWidget(read, write int) Widget {
	if read <= 0 && write <= 0 {
		return Widget{Kind: WidgetKvCache}
	}
	label := lipgloss.NewStyle().Foreground(lipgloss.Color(theme.Hex(theme.RGB(140, 140, 150))))
	hit := lipgloss.NewStyle().
		Foreground(lipgloss.Color(theme.Hex(theme.RGB(120, 200, 140)))).Bold(true)
	miss := lipgloss.NewStyle().
		Foreground(lipgloss.Color(theme.Hex(theme.RGB(200, 160, 90))))

	total := read + write
	rate := 0
	if total > 0 {
		rate = percentOf(read, total)
	}

	// The bar fills with hits; misses are the dim track. It reads as "how
	// much of the prompt the cache already knew", the same direction the hit
	// rate number points.
	cells := 10
	filled := 0
	if total > 0 {
		filled = read * cells / total
	}
	fill := lipgloss.NewStyle().
		Foreground(lipgloss.Color(theme.Hex(theme.RGB(120, 200, 140))))
	track := lipgloss.NewStyle().
		Foreground(lipgloss.Color(theme.Hex(theme.RGB(50, 50, 60))))
	bar := fill.Render(strings.Repeat("▰", filled)) +
		track.Render(strings.Repeat("▱", cells-filled))

	// Mirrors the context meter: the bar carries its percent beside it, so
	// the one number that matters reads next to the thing it describes.
	return Widget{
		Kind: WidgetKvCache,
		Lines: []string{
			label.Render("KV cache ") +
				hit.Render(humanTokens(read)) + label.Render("/") +
				miss.Render(humanTokens(write)),
			bar + hit.Render(fmt.Sprintf(" %d%%", rate)),
		},
	}
}