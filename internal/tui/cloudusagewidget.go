package tui

import (
	"errors"
	"fmt"
	"hash/fnv"
	"image/color"
	"strconv"
	"strings"
	"time"

	"charm.land/lipgloss/v2"

	"evilcode/internal/cloudusage"
	"evilcode/internal/theme"
)

// CloudUsageWidget renders the Ollama Cloud quota bars (plan.md §8.5-adjacent):
// one bar per window (Session, Hourly, Weekly), each filled cell painted with
// the color Ollama itself assigns to the model that consumed it, plus a legend
// mapping the colors back to model names.
//
// It is fed by cloudusage.Fetch and stays absent until a fetch has produced
// data — which is what keeps it off-screen when no OLLAMA_SESSION_COOKIE is
// set. An actionable fetch failure (expired session, page change) earns a dim
// note beside whatever data is left.
func (r *Renderer) CloudUsageWidget(snap *cloudusage.Snapshot, err error, now time.Time) Widget {
	if snap == nil && err == nil {
		return Widget{Kind: WidgetCloudUsage}
	}

	label := lipgloss.NewStyle().Foreground(lipgloss.Color(theme.Hex(theme.RGB(140, 140, 150))))
	dim := r.style(theme.RoleDim)

	var lines []string
	if snap != nil {
		for _, w := range snap.Windows {
			pct := clamp(int(w.UsedPercent+0.5), 0, 100)
			strong := lipgloss.NewStyle().
				Foreground(lipgloss.Color(theme.Hex(theme.MeterColor(1 - w.UsedPercent/100)))).Bold(true)

			head := label.Render(w.Label+" ") + strong.Render(fmt.Sprintf("%d%%", pct))
			if total := w.Requests(); total > 0 {
				head += label.Render(fmt.Sprintf(" · %d req", total))
			}
			if !w.ResetsAt.IsZero() && w.ResetsAt.After(now) {
				head += label.Render(" · resets in " + resetsIn(w.ResetsAt.Sub(now)))
			}
			lines = append(lines, head)
			lines = append(lines, cloudBar(w, pct)+strong.Render(fmt.Sprintf(" %d%%", pct)))
		}
		if legend := cloudLegend(snap.Windows); legend != "" {
			lines = append(lines, legend)
		}
	}
	switch {
	case errors.Is(err, cloudusage.ErrNotLoggedIn):
		lines = append(lines, dim.Render("session expired — refresh OLLAMA_SESSION_COOKIE"))
	case errors.Is(err, cloudusage.ErrNoUsageData):
		lines = append(lines, dim.Render("no usage data — the page may have changed"))
	}
	return Widget{Kind: WidgetCloudUsage, Lines: lines}
}

const cloudBarCells = 12

// cloudBar draws one window's bar: the first usedPct/100 cells are filled in
// the per-model colors, the rest are the dim track. Cells map to segments by
// request share, so a model that made 68 of 70 requests owns ~97% of the fill.
func cloudBar(w cloudusage.Window, usedPct int) string {
	filled := clamp(usedPct*cloudBarCells/100, 0, cloudBarCells)
	track := lipgloss.NewStyle().
		Foreground(lipgloss.Color(theme.Hex(theme.RGB(50, 50, 60))))

	cellColors := make([]lipgloss.Style, filled)
	if filled > 0 && len(w.Segments) == 0 {
		def := lipgloss.NewStyle().
			Foreground(lipgloss.Color(theme.Hex(theme.RGB(120, 140, 180))))
		for i := range cellColors {
			cellColors[i] = def
		}
	} else if filled > 0 {
		total := 0
		weights := make([]int, len(w.Segments))
		for i, seg := range w.Segments {
			weights[i] = max(seg.Requests, 1)
			total += weights[i]
		}
		// Give each segment a run of cells proportional to its request share;
		// integer division can underfill, so the last segment takes the rest.
		assigned := 0
		for i, wgt := range weights {
			n := 0
			if i < len(weights)-1 {
				n = wgt * filled / total
			} else {
				n = filled - assigned
			}
			for j := 0; j < n; j++ {
				cellColors[assigned+j] = segmentStyle(w.Segments[i])
			}
			assigned += n
		}
	}

	var sb strings.Builder
	for i := 0; i < filled; i++ {
		sb.WriteString(cellColors[i].Render("▰"))
	}
	sb.WriteString(track.Render(strings.Repeat("▱", cloudBarCells-filled)))
	return sb.String()
}

// segmentStyle resolves a segment's color: the page's own hex when present,
// otherwise a stable hash of the model name.
func segmentStyle(seg cloudusage.Segment) lipgloss.Style {
	if rgba, ok := parseHexColor(seg.ColorHex); ok {
		return lipgloss.NewStyle().Foreground(lipgloss.Color(theme.Hex(rgba)))
	}
	return lipgloss.NewStyle().Foreground(lipgloss.Color(theme.Hex(modelFallbackColor(seg.Model))))
}

// cloudLegend lists each distinct model once (first-seen order) with a colored
// ● swatch matching its bar segments.
func cloudLegend(windows []cloudusage.Window) string {
	var parts []string
	seen := map[string]bool{}
	for _, w := range windows {
		for _, seg := range w.Segments {
			if seg.Model == "" || seen[seg.Model] {
				continue
			}
			seen[seg.Model] = true
			parts = append(parts, segmentStyle(seg).Render("●")+" "+truncateCells(seg.Model, 22))
		}
	}
	if len(parts) == 0 {
		return ""
	}
	return strings.Join(parts, "  ")
}

// resetsIn formats a countdown the way the settings page's own line reads.
func resetsIn(d time.Duration) string {
	switch {
	case d <= 0:
		return "now"
	case d < time.Minute:
		return fmt.Sprintf("%ds", int(d/time.Second))
	case d < time.Hour:
		return fmt.Sprintf("%dm", int(d/time.Minute))
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh", int(d/time.Hour))
	default:
		return fmt.Sprintf("%dd", int(d/(24*time.Hour)))
	}
}

// parseHexColor parses "#rrggbb" into an opaque RGBA color.
func parseHexColor(s string) (color.RGBA, bool) {
	s = strings.TrimPrefix(s, "#")
	if len(s) != 6 {
		return color.RGBA{}, false
	}
	v, err := strconv.ParseUint(s, 16, 32)
	if err != nil {
		return color.RGBA{}, false
	}
	return color.RGBA{R: uint8(v >> 16), G: uint8(v >> 8), B: uint8(v), A: 255}, true
}

// cloudFallbackColors are the fill colors used when the page gave a segment
// no background color: a fixed, distinguishable set picked from the theme.
var cloudFallbackColors = []color.RGBA{
	theme.RGB(240, 120, 130),
	theme.RGB(120, 200, 140),
	theme.RGB(120, 170, 240),
	theme.RGB(240, 200, 100),
	theme.RGB(190, 140, 240),
	theme.RGB(110, 220, 210),
	theme.RGB(250, 150, 90),
	theme.RGB(160, 160, 220),
}

// modelFallbackColor derives a stable color from a model name, so the same
// model always maps to the same swatch across windows and refreshes.
func modelFallbackColor(model string) color.RGBA {
	h := fnv.New32a()
	_, _ = h.Write([]byte(model))
	return cloudFallbackColors[int(h.Sum32())%len(cloudFallbackColors)]
}
