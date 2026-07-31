package tui

import (
	"fmt"
	"image/color"
	"strings"

	"charm.land/lipgloss/v2"

	"evilcode/internal/theme"
)

// BlockKind tags a transcript entry.
type BlockKind int

const (
	BlockUser BlockKind = iota
	BlockAssistant
	BlockTool
	BlockError
	BlockNotice
	BlockReasoning
)

// Block is one renderable transcript entry.
type Block struct {
	Kind BlockKind

	// Text is the message body, or the notice/error message.
	Text string

	// Number is the prompt number for a user block, 1-based.
	Number int

	// Tool row fields (plan.md §9.5).
	ToolName   string
	ToolTarget string
	ToolIntent string
	ToolTokens int
	Added      int
	Removed    int
	HasDiff    bool
	Failed     bool

	// Diff is a unified diff rendered inline (§9.3).
	Diff string

	// Streaming marks the tail block, which re-renders every frame.
	Streaming bool

	// cache holds the rendered lines, keyed by the width they were made for.
	cache      []string
	cacheWidth int
	cacheKey   string
}

// Renderer turns blocks into styled lines.
type Renderer struct {
	Palette  *theme.Palette
	Markdown *Markdown

	// Width is the content width blocks wrap to.
	Width int

	// Centered shifts alignment-exempt rows (tool, code, system) to stay left.
	Centered bool

	// Animate gates decorative color.
	Animate bool
}

// NewRenderer builds a renderer at the given width.
func NewRenderer(p *theme.Palette, width int) *Renderer {
	return &Renderer{Palette: p, Markdown: NewMarkdown(width), Width: width, Animate: true}
}

// SetWidth updates the wrap width, dropping caches that were built for the old
// one.
func (r *Renderer) SetWidth(width int) {
	r.Width = width
	r.Markdown.SetWidth(width)
}

func (r *Renderer) style(role theme.Role) lipgloss.Style {
	return lipgloss.NewStyle().Foreground(lipgloss.Color(r.Palette.Hex(role)))
}

// rgbStyle builds a style from an ad-hoc rgb() literal, which the spec uses
// throughout for shades that are not semantic roles.
func rgbStyle(r, g, b uint8) lipgloss.Style {
	return lipgloss.NewStyle().Foreground(lipgloss.Color(theme.Hex(theme.RGB(r, g, b))))
}

// Lines renders a block, using its cache when the width has not changed.
func (r *Renderer) Lines(b *Block) []string {
	key := b.cacheContentKey()
	if !b.Streaming && b.cache != nil && b.cacheWidth == r.Width && b.cacheKey == key {
		return b.cache
	}
	lines := r.render(b)
	if !b.Streaming {
		b.cache, b.cacheWidth, b.cacheKey = lines, r.Width, key
	}
	return lines
}

func (b *Block) cacheContentKey() string {
	return fmt.Sprintf("%d|%d|%s|%s", b.Kind, b.Number, b.Text, b.Diff)
}

func (r *Renderer) render(b *Block) []string {
	switch b.Kind {
	case BlockUser:
		return r.renderUser(b)
	case BlockTool:
		return r.renderTool(b)
	case BlockError:
		return r.renderError(b)
	case BlockNotice:
		return r.renderNotice(b)
	case BlockReasoning:
		return r.renderReasoning(b)
	default:
		return r.renderAssistant(b)
	}
}

// renderUser draws the prompt band of plan.md §9.6:
//
//	7› what does this function do?
//
// The number is rainbow-decayed by distance from the newest prompt, the whole
// row sits on the user background, and continuations keep the band. This band
// is the design's only "bubble".
func (r *Renderer) renderUser(b *Block) []string {
	bg := lipgloss.Color(r.Palette.Hex(theme.RoleUserBg))

	numStyle := lipgloss.NewStyle().
		Foreground(lipgloss.Color(theme.Hex(theme.Rainbow(b.Number)))).
		Background(bg).Bold(true)
	arrowStyle := lipgloss.NewStyle().
		Foreground(lipgloss.Color(r.Palette.Hex(theme.RoleUser))).Background(bg)
	textStyle := lipgloss.NewStyle().
		Foreground(lipgloss.Color(r.Palette.Hex(theme.RoleUserText))).Background(bg)

	label := fmt.Sprint(b.Number + 1)
	prefix := label + "› "
	indent := strings.Repeat(" ", lipgloss.Width(prefix))

	wrapped := wrapPlain(b.Text, max(r.Width-lipgloss.Width(prefix), 1))
	out := make([]string, 0, len(wrapped))
	for i, line := range wrapped {
		var head string
		if i == 0 {
			head = numStyle.Render(label) + arrowStyle.Render("› ")
		} else {
			head = textStyle.Render(indent)
		}
		// Pad the band to full width so it reads as a continuous block rather
		// than a ragged highlight.
		pad := max(r.Width-lipgloss.Width(prefix)-lipgloss.Width(line), 0)
		out = append(out, head+textStyle.Render(line+strings.Repeat(" ", pad)))
	}
	return out
}

// renderAssistant renders prose through glamour, extracting fenced code so it
// can carry its own chrome (§9.1, §9.2).
func (r *Renderer) renderAssistant(b *Block) []string {
	var out []string
	for _, seg := range SplitSegments(b.Text) {
		if seg.Code {
			out = append(out, r.renderCodeBlock(seg)...)
			continue
		}
		rendered := r.Markdown.Render(seg.Text, !b.Streaming)
		out = append(out, strings.Split(rendered, "\n")...)
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// renderCodeBlock draws the §9.2 chrome. It is always left-aligned, even in
// centered mode, because code with a shifting left edge is unreadable.
func (r *Renderer) renderCodeBlock(seg Segment) []string {
	chrome := rgbStyle(0x64, 0x64, 0x64)

	header := "┌─ " + seg.Lang
	if seg.Open {
		header = "┌─ " + seg.Lang + " (streaming...)"
	}

	out := []string{chrome.Render(header)}
	body := strings.Split(seg.Text, "\n")
	// A trailing blank line inside a fence is an artifact of the fence, not
	// content.
	for len(body) > 0 && strings.TrimSpace(body[len(body)-1]) == "" {
		body = body[:len(body)-1]
	}
	highlighted := HighlightLines(seg.Lang, strings.Join(body, "\n"))
	for i := range body {
		text := body[i]
		if i < len(highlighted) {
			text = highlighted[i]
		}
		out = append(out, chrome.Render("│ ")+text)
	}
	if seg.Open {
		// The live cursor row shows the block is still arriving.
		out = append(out, chrome.Render("│ ")+chrome.Render("▌"))
	} else {
		out = append(out, chrome.Render("└─"))
	}
	return out
}

// renderTool draws the one-line completed call of §9.5:
//
//	✓ read src/main.go · load entry point · 1.2k tok (+8 -5)
func (r *Renderer) renderTool(b *Block) []string {
	icon, iconStyle := "✓", r.style(theme.RoleSuccess)
	if b.Failed {
		icon, iconStyle = "✗", rgbStyle(220, 100, 100)
	}

	dim := r.style(theme.RoleDim)
	toolStyle := r.style(theme.RoleTool)
	link := r.style(theme.RoleFileLink)

	var b2 strings.Builder
	b2.WriteString("  " + iconStyle.Render(icon) + " " + toolStyle.Render(b.ToolName))
	if b.ToolTarget != "" {
		b2.WriteString(" " + link.Render(b.ToolTarget))
	}
	if b.ToolIntent != "" {
		b2.WriteString(dim.Render(" · ") + dim.Render(b.ToolIntent))
	}
	if b.ToolTokens > 0 {
		b2.WriteString(dim.Render(" · " + humanTokens(b.ToolTokens) + " tok"))
	}
	if b.HasDiff {
		add := lipgloss.NewStyle().Foreground(lipgloss.Color(theme.Hex(theme.DiffAdd)))
		del := lipgloss.NewStyle().Foreground(lipgloss.Color(theme.Hex(theme.DiffDel)))
		b2.WriteString(" " + dim.Render("(") +
			add.Render(fmt.Sprintf("+%d", b.Added)) + " " +
			del.Render(fmt.Sprintf("-%d", b.Removed)) + dim.Render(")"))
	}

	out := []string{b2.String()}
	if b.Diff != "" {
		out = append(out, r.renderDiffLang(b.Diff, langFromPath(b.ToolTarget))...)
	}
	return out
}

// MaxInlineDiffLines is how many diff body lines render before the middle is
// elided (plan.md §9.3).
const MaxInlineDiffLines = 14

// renderDiff draws the inline diff frame of §9.3. The body is tinted rather
// than recolored outright, so code keeps its shape while reading unmistakably
// as an add or a delete.
func (r *Renderer) renderDiff(diff string) []string {
	return r.renderDiffLang(diff, "")
}

// renderDiffLang renders a diff with an explicit lexer for its body.
func (r *Renderer) renderDiffLang(diff, lang string) []string {
	chrome := rgbStyle(0x64, 0x64, 0x64)
	addStyle := lipgloss.NewStyle().Foreground(lipgloss.Color(theme.Hex(theme.DiffAdd)))
	delStyle := lipgloss.NewStyle().Foreground(lipgloss.Color(theme.Hex(theme.DiffDel)))
	body := r.style(theme.RoleAIText)

	var kept []string
	var added, removed int
	for _, line := range strings.Split(diff, "\n") {
		switch {
		case strings.HasPrefix(line, "---") || strings.HasPrefix(line, "+++"),
			strings.HasPrefix(line, "@@"), line == "":
			continue
		case strings.HasPrefix(line, "+"):
			added++
		case strings.HasPrefix(line, "-"):
			removed++
		}
		kept = append(kept, line)
	}
	if len(kept) == 0 {
		return nil
	}

	// The body is highlighted then tinted rather than recoloured outright, so
	// code keeps its shape while still reading unmistakably as an add or a
	// delete (plan.md §9.3).
	styleLine := func(line string) string {
		marker, code := "", line
		if line != "" && (line[0] == '+' || line[0] == '-' || line[0] == ' ') {
			marker, code = line[:1], line[1:]
		}
		code = truncateCells(code, max(r.Width-3, 8))

		var tint *color.RGBA
		var markerStyle lipgloss.Style
		switch marker {
		case "+":
			t := theme.DiffAdd
			tint, markerStyle = &t, addStyle
		case "-":
			t := theme.DiffDel
			tint, markerStyle = &t, delStyle
		default:
			markerStyle = body
		}

		lines := tokenize(lang, code)
		text := code
		if len(lines) > 0 {
			text = renderTokens(lines[0], tint)
		}
		return chrome.Render("│ ") + markerStyle.Render(marker) + text
	}

	out := []string{chrome.Render("┌─ diff")}
	if len(kept) <= MaxInlineDiffLines {
		for _, line := range kept {
			out = append(out, styleLine(line))
		}
	} else {
		// Show both ends: the start says what changed, the end says how it
		// landed. The middle is the part nobody reads.
		half := MaxInlineDiffLines / 2
		for _, line := range kept[:half] {
			out = append(out, styleLine(line))
		}
		out = append(out, chrome.Render(fmt.Sprintf("│ ... %d more changes ...", len(kept)-2*half)))
		for _, line := range kept[len(kept)-half:] {
			out = append(out, styleLine(line))
		}
	}
	out = append(out, chrome.Render(fmt.Sprintf("└─ (+%d -%d total)", added, removed)))
	return out
}

// renderError draws the §9.8 row.
func (r *Renderer) renderError(b *Block) []string {
	style := r.style(theme.RoleError)
	var out []string
	for i, line := range wrapPlain(b.Text, max(r.Width-4, 8)) {
		if i == 0 {
			out = append(out, "  "+style.Render("✗ "+line))
		} else {
			out = append(out, "    "+style.Render(line))
		}
	}
	return out
}

func (r *Renderer) renderNotice(b *Block) []string {
	style := r.style(theme.RoleSystem)
	var out []string
	for _, line := range wrapPlain(b.Text, max(r.Width-2, 8)) {
		out = append(out, "  "+style.Render(line))
	}
	return out
}

// renderReasoning draws streamed thinking as dim italic (§9.7).
func (r *Renderer) renderReasoning(b *Block) []string {
	style := rgbStyle(0x64, 0x64, 0x64).Italic(true)
	var out []string
	for _, line := range wrapPlain(b.Text, max(r.Width-2, 8)) {
		out = append(out, "  "+style.Render(line))
	}
	return out
}

// Scrollbar glyphs (plan.md §3.5).
const (
	ScrollbarThumbSingle = "•"
	ScrollbarThumbTop    = "╷"
	ScrollbarThumbBottom = "╵"
	ScrollbarThumbMiddle = "│"
)

// RenderScrollbar returns one column of scrollbar cells for a viewport.
//
// The track is blank rather than drawn: a visible track competes with the
// transcript for attention, and the thumb alone answers the only question
// being asked — where am I.
func (r *Renderer) RenderScrollbar(offset, contentHeight, viewport int, focused bool) []string {
	if viewport <= 0 {
		return nil
	}
	out := make([]string, viewport)
	for i := range out {
		out[i] = " "
	}
	if contentHeight <= viewport {
		return out
	}

	color := theme.RGB(136, 148, 172)
	if focused {
		color = theme.RGB(188, 208, 240)
	}
	style := lipgloss.NewStyle().Foreground(lipgloss.Color(theme.Hex(color)))

	// Thumb size is proportional, with a floor of one cell so it never
	// vanishes on a very long transcript.
	thumb := max(viewport*viewport/contentHeight, 1)
	maxOffset := contentHeight - viewport
	// Offset counts from the bottom, so invert it for a top-down track.
	fromTop := maxOffset - clamp(offset, 0, maxOffset)
	top := 0
	if maxOffset > 0 {
		top = fromTop * (viewport - thumb) / maxOffset
	}

	for i := 0; i < thumb; i++ {
		row := top + i
		if row < 0 || row >= viewport {
			continue
		}
		switch {
		case thumb == 1:
			out[row] = style.Render(ScrollbarThumbSingle)
		case i == 0:
			out[row] = style.Render(ScrollbarThumbTop)
		case i == thumb-1:
			out[row] = style.Render(ScrollbarThumbBottom)
		default:
			out[row] = style.Render(ScrollbarThumbMiddle)
		}
	}
	return out
}

// humanTokens formats a token count for the §9.5 badge.
func humanTokens(n int) string {
	switch {
	case n >= 1_000_000:
		return fmt.Sprintf("%.1fM", float64(n)/1_000_000)
	case n >= 1000:
		return fmt.Sprintf("%.1fk", float64(n)/1000)
	default:
		return fmt.Sprint(n)
	}
}

// wrapPlain wraps unstyled text to a cell width, preserving explicit newlines.
func wrapPlain(s string, width int) []string {
	if width < 1 {
		width = 1
	}
	var out []string
	for _, para := range strings.Split(s, "\n") {
		if para == "" {
			out = append(out, "")
			continue
		}
		line := ""
		for _, word := range strings.Fields(para) {
			switch {
			case line == "":
				line = word
			case lipgloss.Width(line)+1+lipgloss.Width(word) <= width:
				line += " " + word
			default:
				out = append(out, line)
				line = word
			}
			// A single word longer than the width has to be broken, or it
			// pushes the whole layout sideways.
			for lipgloss.Width(line) > width {
				out = append(out, truncateCells(line, width))
				line = dropCells(line, width)
			}
		}
		if line != "" {
			out = append(out, line)
		}
	}
	if len(out) == 0 {
		return []string{""}
	}
	return out
}

// truncateCells cuts a string to a cell width, respecting wide glyphs.
func truncateCells(s string, width int) string {
	if lipgloss.Width(s) <= width {
		return s
	}
	var b strings.Builder
	used := 0
	for _, r := range s {
		w := lipgloss.Width(string(r))
		if used+w > width {
			break
		}
		b.WriteRune(r)
		used += w
	}
	return b.String()
}

// dropCells returns the remainder after truncateCells.
func dropCells(s string, width int) string {
	head := truncateCells(s, width)
	return s[len(head):]
}
