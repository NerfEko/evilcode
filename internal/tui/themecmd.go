package tui

import (
	"fmt"
	"image/color"
	"sort"
	"strings"

	"evilcode/internal/theme"
)

// runTheme implements `/theme` (plan.md §7.5): list, switch, score, or generate.
func (m *Model) runTheme(arg string) string {
	fields := strings.Fields(arg)
	bg := theme.DefaultDarkBackground

	switch {
	case len(fields) == 0:
		return m.themeList(bg)
	case fields[0] == "score":
		return m.themeScore(bg)
	case fields[0] == "generate":
		return m.themeGenerate(fields, bg)
	default:
		return m.themeSwitch(fields[0])
	}
}

func (m *Model) themeList(bg color.RGBA) string {
	names := make([]string, 0, len(theme.Palettes()))
	for name := range theme.Palettes() {
		names = append(names, name)
	}
	sort.Strings(names)

	var b strings.Builder
	fmt.Fprintf(&b, "Palettes (current: %s)\n", m.renderer.Palette.Name)
	for _, name := range names {
		p := theme.Palettes()[name]
		pbg := bg
		if p.Light {
			pbg = theme.RGB(250, 250, 248)
		}
		marker := " "
		if name == m.renderer.Palette.Name {
			marker = "▸"
		}
		fmt.Fprintf(&b, "  %s %-12s %.0f\n", marker, name, theme.Score(p, pbg).Overall)
	}
	b.WriteString("\n/theme <name> to switch · /theme score · /theme generate #hex")
	return b.String()
}

func (m *Model) themeScore(bg color.RGBA) string {
	p := m.renderer.Palette
	if p.Light {
		bg = theme.RGB(250, 250, 248)
	}
	card := theme.Score(p, bg)

	var b strings.Builder
	fmt.Fprintf(&b, "%s scores %.1f\n", p.Name, card.Overall)
	for _, c := range card.Criteria {
		flag := ""
		if c.Critical {
			flag = "  (critical)"
		}
		fmt.Fprintf(&b, "  %-18s %5.1f   weight %.1f%s\n", c.Name, c.Score, c.Weight, flag)
	}
	// Naming the weak pairs turns a number into something actionable.
	for _, pair := range theme.MustDistinguish {
		d := theme.ToOklab(p.Get(pair[0])).Distance(theme.ToOklab(p.Get(pair[1])))
		if d < theme.DistinctTarget {
			fmt.Fprintf(&b, "  close pair: %s / %s (%.3f, want %.2f)\n",
				pair[0], pair[1], d, theme.DistinctTarget)
		}
	}
	return strings.TrimRight(b.String(), "\n")
}

func (m *Model) themeGenerate(fields []string, bg color.RGBA) string {
	if len(fields) < 2 {
		return "/theme generate needs a seed color, e.g. /theme generate #bd93f9"
	}
	seed, err := theme.ParseHex(fields[1])
	if err != nil {
		return err.Error()
	}
	p := theme.Generate(seed, bg, "generated")
	m.setPalette(p)
	return fmt.Sprintf("generated a palette from %s, scoring %.1f",
		fields[1], theme.Score(p, bg).Overall)
}

func (m *Model) themeSwitch(name string) string {
	p, ok := theme.Palettes()[name]
	if !ok {
		return "no palette named " + name + " · /theme to list them"
	}
	m.setPalette(p)
	return "theme: " + p.Name
}

// setPalette swaps the active palette and drops the render caches, which were
// built against the old colors.
func (m *Model) setPalette(p *theme.Palette) {
	m.renderer.Palette = p
	// Prose follows the palette too. It used to be a package-level constant, so
	// switching themes recolored the chrome and left every heading in every
	// reply the same amber.
	m.renderer.Markdown.SetProse(p.Prose)
	for i := range m.blocks {
		m.blocks[i].cache = nil
	}
	m.invalidateTranscriptCache()
	m.dock.Reset()
}
