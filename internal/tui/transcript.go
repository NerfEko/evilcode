package tui

import (
	"fmt"
	"image/color"
	"strings"
	"time"
	"unicode/utf8"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"evilcode/internal/core"
	"evilcode/internal/graphics"
	"evilcode/internal/memory"
	"evilcode/internal/theme"
	"evilcode/internal/todo"
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
	BlockTodoDelta
	BlockMemory
	BlockImage
)

// Block is one renderable transcript entry.
type Block struct {
	Kind BlockKind

	// Text is the message body, or the notice/error message.
	Text string

	// Number is the prompt's ordinal, 1-based and stable: prompt 1 stays
	// prompt 1 forever. It is what the band prints.
	Number int

	// TypingWPM is the user's measured words-per-minute for this prompt. Zero
	// means the prompt was too short or did not come from timed keystrokes.
	TypingWPM int

	// Decay is the distance from the newest prompt, which is what §7.7's
	// rainbow ramp is indexed by. Separate from Number because the two mean
	// different things — conflating them numbered the newest prompt 1 and
	// counted *backwards* into the history.
	Decay int

	// Tool row fields (plan.md §9.5).
	ToolName    string
	ToolTarget  string
	ToolIntent  string
	ToolPath    string
	ToolCommand string
	ToolOutput  string
	ToolTokens  int
	// Repairs names argument rewrites RunOne applied (alias, string→number).
	// Shown dim in the tool row so a quietly rewritten argument is findable.
	Repairs          []string
	Added            int
	Removed          int
	HasDiff          bool
	Failed           bool
	Held             bool
	ToolPathExists   bool
	ToolPathMarkdown bool

	// Diff is a unified diff rendered inline (§9.3).
	Diff string

	// TodoDelta is the change set shown under a todo tool call (§12.5).
	TodoDelta todo.Delta

	// Memories is what passive recall injected, shown as the 🧠 tile (§9.5).
	Memories []memory.Hit

	// Image is a picture drawn over this block's rows (Phase 5).
	Image ImageBlock

	// Streaming marks the tail block, which re-renders every frame.
	Streaming bool

	// Collapsed folds a finished reasoning trace to a single summary row, so
	// old thinking does not dominate the transcript (§9.7). The block stays in
	// the transcript — the text is retained so a future expand can restore it.
	Collapsed bool

	// Hovered and HoverCodeSegment are transient paint state. The model supplies
	// them for the block under the mouse; they are not persisted with a session.
	// Keeping the state on the block lets the normal renderer add hover
	// affordances without a second transcript rendering path.
	Hovered          bool
	HoverCodeSegment int

	// cache holds rendered lines for up to two wrap widths: the one the
	// transcript is laid out at, and the one the scrollbar hysteresis probes
	// every frame (contentHeightAtWidth). With a single slot the probe evicted
	// the real render and vice versa, so every frame rendered the whole
	// transcript twice — the scroll and streaming lag.
	cache [2]blockRender

	// streamCache is a short-lived paint cache for the live tail. A provider can
	// emit faster than a terminal can repaint; returning the last live frame for
	// one spinner interval bounds markdown/highlight work without dropping any
	// text from Block.Text. It is only used while Streaming is true.
	streamCache       blockRender
	streamCacheLayout blockCacheKey
	streamCacheAt     time.Time
}

type blockRender struct {
	valid bool
	width int
	key   blockCacheKey
	lines []string
}

func (b *Block) dropCache() {
	b.cache = [2]blockRender{}
	b.dropStreamingCache()
}

func (b *Block) dropStreamingCache() {
	b.streamCache = blockRender{}
	b.streamCacheLayout = blockCacheKey{}
	b.streamCacheAt = time.Time{}
}

// keep stores a render, preferring the slot already holding that width so the
// two widths in play settle into one slot each.
func (b *Block) keep(c blockRender) {
	for i := range b.cache {
		if !b.cache[i].valid || b.cache[i].width == c.width {
			b.cache[i] = c
			return
		}
	}
	b.cache[1] = c
}

// blockCacheKey deliberately keeps the source strings as strings instead of
// formatting the whole block into one key. String headers are cheap to copy and
// unchanged strings compare without allocating; the old fmt.Sprintf key copied
// every byte of a long reply on every repaint, even when the cache hit.
type blockCacheKey struct {
	kind, number, typingWPM                                            int
	promptColor                                                        color.RGBA
	toolTokens, added, removed                                         int
	text, toolName, toolTarget, toolIntent                             string
	toolPath, toolCommand, toolOutput                                  string
	diff                                                               string
	repairs                                                            string
	hasDiff, failed, held, collapsed, toolPathExists, toolPathMarkdown bool
	graphics                                                           graphics.Protocol
	imagesOn, centered, toolDetails                                    bool
	diffMode                                                           DiffMode
	imagePath                                                          string
	imageCols, imageRows, imageID                                      int
	imageBytes                                                         int
	hovered                                                            bool
	hoverCodeSegment                                                   int
}

// Rows is a rendered transcript plus the provenance of every line. Owner[i] is
// the index into Model.blocks of the block that rendered Lines[i], or -1 for
// chrome: the header, the inter-block gaps, the welcome art, the pinned todo
// card. The dock and the mouse handler both need to know which block a screen
// row belongs to (§1.1), and nothing else does.
type Rows struct {
	Lines []string
	Owner []int
	// First maps each block to its first rendered row, or -1 when the block
	// emitted no rows. It is assembled with Owner so dock/prompt navigation do
	// not rescan every transcript row on each frame.
	First []int32
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

	// DiffMode decides whether diffs render inline or in the side panel.
	DiffMode DiffMode

	// ThinkingLines caps how tall a live thinking trace grows before it scrolls
	// inside its own space. Zero means DefaultThinkingLines.
	ThinkingLines int

	// narrow is a cached clone for rendering into a sub-region such as the
	// session preview. It needs its own Markdown: copying a Renderer shares the
	// glamour pointer, so prose kept wrapping at the outer width and the clone
	// truncated it instead of re-wrapping.
	narrow      *Renderer
	narrowWidth int

	// Graphics and ImagesOn decide whether an image block reserves rows for a
	// picture or falls back to a one-line placeholder.
	Graphics graphics.Protocol
	ImagesOn bool

	// ToolDetails shows the technical summary on a tool row. Off by default:
	// the row already says what ran and how it went. An errored call shows its
	// detail regardless, since a row you cannot diagnose is worse than no row
	// (plan.md §9.5).
	ToolDetails bool
}

// NewRenderer builds a renderer at the given width.
func NewRenderer(p *theme.Palette, width int) *Renderer {
	return &Renderer{
		Palette:  p,
		Markdown: NewMarkdown(width, p.Prose),
		Width:    width, Animate: true, DiffMode: DiffInline,
	}
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

// jaggedUnderline uses the terminal's extended underline SGR. 4:3 is the
// curly/wavy variant supported by modern terminals; unlike a plain underline
// it reads as a hover affordance without competing with the normal link color.
func jaggedUnderline(s string) string {
	if s == "" {
		return s
	}
	// Syntax highlighting and lipgloss styles reset SGR between colored runs.
	// Re-arm the underline after those resets so a multi-token command stays
	// wavy from its first character to its last.
	s = strings.ReplaceAll(s, "\x1b[0m", "\x1b[0m\x1b[4:3m")
	s = strings.ReplaceAll(s, "\x1b[m", "\x1b[m\x1b[4:3m")
	return "\x1b[4:3m" + s + "\x1b[24m"
}

// Lines renders a block, using its settled cache when the width has not
// changed and a short-lived paint cache for a live streaming tail.
func (r *Renderer) Lines(b *Block) []string {
	key := b.cacheContentKey(r)
	if b.Streaming {
		layout := key
		layout.text = ""
		now := time.Now()
		if b.streamCache.valid && b.streamCache.width == r.Width &&
			b.streamCacheLayout == layout &&
			(b.streamCache.key.text == key.text || now.Sub(b.streamCacheAt) < StreamingRenderInterval) {
			return b.streamCache.lines
		}
		lines := r.render(b)
		b.streamCache = blockRender{valid: true, width: r.Width, key: key, lines: lines}
		b.streamCacheLayout = layout
		// Start the throttle window after the expensive work. Starting it before
		// rendering meant a slow markdown/code frame could consume its own entire
		// cache lifetime, then immediately render the same unchanged tail again.
		b.streamCacheAt = time.Now()
		return lines
	}
	if !b.Streaming {
		for _, c := range b.cache {
			if c.valid && c.width == r.Width && c.key == key {
				return c.lines
			}
		}
	}
	lines := r.render(b)
	if !b.Streaming {
		b.keep(blockRender{valid: true, width: r.Width, key: key, lines: lines})
	}
	return lines
}

// StreamingRenderInterval caps expensive markdown/syntax work for a live
// response. It matches the visible spinner cadence, so text remains responsive
// while an unusually chatty provider cannot monopolize the event loop.
const StreamingRenderInterval = SpinnerInterval

func (b *Block) cacheContentKey(r *Renderer) blockCacheKey {
	promptColor := color.RGBA{}
	if b.Kind == BlockUser {
		promptColor = theme.Rainbow(b.Decay)
	}
	return blockCacheKey{
		kind: int(b.Kind), number: b.Number, typingWPM: b.TypingWPM, promptColor: promptColor,
		toolTokens: b.ToolTokens, added: b.Added, removed: b.Removed,
		text: b.Text, toolName: b.ToolName, toolTarget: b.ToolTarget,
		toolIntent: b.ToolIntent, toolPath: b.ToolPath,
		toolCommand: b.ToolCommand, toolOutput: b.ToolOutput, diff: b.Diff,
		repairs: strings.Join(b.Repairs, ","),
		hasDiff: b.HasDiff, failed: b.Failed, held: b.Held, collapsed: b.Collapsed,
		toolPathExists: b.ToolPathExists, toolPathMarkdown: b.ToolPathMarkdown,
		graphics: r.Graphics, imagesOn: r.ImagesOn, centered: r.Centered,
		toolDetails: r.ToolDetails, diffMode: r.DiffMode,
		imagePath: b.Image.Path, imageCols: b.Image.Cols, imageRows: b.Image.Rows,
		imageID: b.Image.ID, imageBytes: len(b.Image.PNG),
		hovered: b.Hovered, hoverCodeSegment: b.HoverCodeSegment,
	}
}

// AtWidth returns a renderer that lays out into a narrower region, reusing the
// clone across frames so a preview does not build a glamour renderer per key.
//
// The clone needs its own Markdown: copying a Renderer shares the glamour
// pointer, so prose kept wrapping at the outer width and the narrower renderer
// truncated it rather than re-wrapping it.
func (r *Renderer) AtWidth(width int) *Renderer {
	if width < 1 {
		width = 1
	}
	if r.narrow != nil && r.narrowWidth == width {
		return r.narrow
	}
	clone := *r
	clone.Width = width
	clone.Markdown = NewMarkdown(width, r.Palette.Prose)
	clone.narrow, clone.narrowWidth = nil, 0
	r.narrow, r.narrowWidth = &clone, width
	return &clone
}

func (r *Renderer) render(b *Block) []string {
	// One choke point for untrusted text. Everything reaching a block came
	// from the model, from a tool's output, or from a file — none of which is
	// entitled to drive the terminal — and this is the last place before it is
	// styled and laid out. Sanitizing the finished frame instead would strip
	// the escapes evilcode itself puts there.
	clean := *b
	clean.Text = core.SanitizeTerminal(b.Text)
	clean.ToolName = core.SanitizeTerminal(b.ToolName)
	clean.ToolTarget = core.SanitizeTerminal(b.ToolTarget)
	clean.ToolIntent = core.SanitizeTerminal(b.ToolIntent)
	clean.ToolPath = core.SanitizeTerminal(b.ToolPath)
	clean.Image.Path = core.SanitizeTerminal(b.Image.Path)
	clean.Diff = core.SanitizeTerminal(b.Diff)
	b = &clean

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
	case BlockTodoDelta:
		return r.RenderTodoDelta(b.TodoDelta)
	case BlockMemory:
		return r.RenderMemoryTile(b.Memories)
	case BlockImage:
		return r.RenderImagePlaceholder(b.Image, r.Graphics, r.ImagesOn)
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
		Foreground(lipgloss.Color(theme.Hex(theme.Rainbow(b.Decay)))).
		Background(bg).Bold(true)
	arrowStyle := lipgloss.NewStyle().
		Foreground(lipgloss.Color(r.Palette.Hex(theme.RoleUser))).Background(bg)
	textStyle := lipgloss.NewStyle().
		Foreground(lipgloss.Color(r.Palette.Hex(theme.RoleUserText))).Background(bg)
	wpmStyle := lipgloss.NewStyle().
		Foreground(lipgloss.Color(r.Palette.Hex(theme.RoleDim))).Background(bg)

	// Floored at 1. Prompts are 1-based, so a zero here means a construction
	// path forgot to set Number — and a band reading "0›" in front of the user
	// is worse than quietly showing the first prompt as the first prompt.
	label := fmt.Sprint(max(b.Number, 1))
	prefix := label + "› "
	indent := strings.Repeat(" ", lipgloss.Width(prefix))

	wrapped := wrapPlain(b.Text, max(r.Width-lipgloss.Width(prefix), 1))
	out := make([]string, 0, len(wrapped)+1)
	wpm := ""
	if b.TypingWPM > 0 {
		wpm = fmt.Sprintf(" (%d wpm)", b.TypingWPM)
	}
	wpmWidth := lipgloss.Width(wpm)
	wpmAttached := false
	for i, line := range wrapped {
		var head string
		if i == 0 {
			head = numStyle.Render(label) + arrowStyle.Render("› ")
		} else {
			head = textStyle.Render(indent)
		}
		// Pad the band to full width so it reads as a continuous block rather
		// than a ragged highlight. The WPM label belongs after the final line;
		// if that line has no room, it gets a continuation row below it.
		extra := ""
		if i == len(wrapped)-1 && wpm != "" &&
			lipgloss.Width(prefix)+lipgloss.Width(line)+wpmWidth <= r.Width {
			extra = wpmStyle.Render(wpm)
			wpmAttached = true
		}
		extraWidth := 0
		if extra != "" {
			extraWidth = lipgloss.Width(extra)
		}
		pad := max(r.Width-lipgloss.Width(prefix)-lipgloss.Width(line)-extraWidth, 0)
		out = append(out, head+textStyle.Render(line+strings.Repeat(" ", pad))+extra)
	}
	if wpm != "" && !wpmAttached {
		pad := max(r.Width-lipgloss.Width(prefix)-wpmWidth, 0)
		out = append(out, textStyle.Render(indent)+wpmStyle.Render(wpm+strings.Repeat(" ", pad)))
	}
	return out
}

// renderAssistant renders prose through glamour, extracting fenced code so it
// can carry its own chrome (§9.1, §9.2).
func (r *Renderer) renderAssistant(b *Block) []string {
	// A plan fence becomes a card rather than a code block, and the card grows
	// while it streams (plan.md §12.1).
	if plans := FindPlanSegments(b.Text); len(plans) > 0 {
		return r.renderWithPlanCards(b, plans)
	}
	return r.renderProse(b, b.Text)
}

// renderWithPlanCards splices plan cards into the surrounding prose.
func (r *Renderer) renderWithPlanCards(b *Block, plans []PlanSegment) []string {
	var out []string
	cursor := 0
	for _, p := range plans {
		if before := b.Text[cursor:p.Start]; strings.TrimSpace(before) != "" {
			out = append(out, r.renderProseAt(b, before, len(SplitSegments(b.Text[:cursor])))...)
		}
		out = append(out, r.RenderPlanCard(p)...)
		cursor = p.End
	}
	if after := b.Text[cursor:]; strings.TrimSpace(after) != "" {
		out = append(out, r.renderProseAt(b, after, len(SplitSegments(b.Text[:cursor])))...)
	}
	return out
}

func (r *Renderer) renderProse(b *Block, text string) []string {
	return r.renderProseAt(b, text, 0)
}

// renderProseAt is renderProse with the segment number in the original
// assistant message. Plan cards splice prose into several calls, so retaining
// that offset keeps the hovered shell fence lined up with the block that was
// actually painted.
func (r *Renderer) renderProseAt(b *Block, text string, segmentBase int) []string {
	var out []string
	for i, seg := range SplitSegments(text) {
		if seg.Code {
			// A mermaid fence is a diagram, not code. With mmdc absent it comes
			// back as its own source plus a line saying what would render it,
			// which is more useful than an error and more honest than silence.
			if seg.Lang == "mermaid" && !seg.Open {
				out = append(out, r.RenderMermaidSource(seg.Text)...)
				continue
			}
			hovered := b.Hovered && b.HoverCodeSegment == segmentBase+i
			out = append(out, r.renderCodeBlock(seg, hovered)...)
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
func (r *Renderer) renderCodeBlock(seg Segment, hover ...bool) []string {
	hovered := len(hover) > 0 && hover[0]
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
	joined := strings.Join(body, "\n")
	var highlighted []string
	if seg.Open {
		highlighted = HighlightLinesUncached(seg.Lang, joined)
	} else {
		highlighted = HighlightLines(seg.Lang, joined)
	}
	for i := range body {
		text := body[i]
		if i < len(highlighted) {
			text = highlighted[i]
		}
		if hovered && shellLanguage(seg.Lang) {
			text = jaggedShellText(text, body[i])
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

// jaggedShellText underlines only the source prefix that a bash/fish click
// copies. Inline comments and their separating spaces remain visible as
// annotations, but are not presented as part of the clickable command.
func jaggedShellText(highlighted, source string) string {
	command := shellLineForClipboard(source)
	width := lipgloss.Width(command)
	if width <= 0 {
		return highlighted
	}
	visible := ansi.StringWidth(highlighted)
	if width >= visible {
		return jaggedUnderline(highlighted)
	}
	prefix := ansi.Cut(highlighted, 0, width)
	suffix := ansi.Cut(highlighted, width, visible)
	return jaggedUnderline(prefix) + suffix
}

// renderTool draws the one-line completed call of §9.5:
//
//	✓ read src/main.go · load entry point · 1.2k tok (+8 -5)
func (r *Renderer) renderTool(b *Block) []string {
	icon, iconStyle := "✓", r.style(theme.RoleSuccess)
	if b.Held {
		icon, iconStyle = "!", r.style(theme.RoleWarning)
	} else if b.Failed {
		icon, iconStyle = "✗", rgbStyle(220, 100, 100)
	}

	dim := r.style(theme.RoleDim)
	toolStyle := r.style(theme.RoleTool)
	link := r.style(theme.RoleFileLink)
	if b.ToolPathMarkdown && b.ToolPathExists {
		link = link.Underline(true)
	}

	var b2 strings.Builder
	toolName := toolStyle.Render(b.ToolName)
	if b.Hovered {
		toolName = jaggedUnderline(toolName)
	}
	b2.WriteString("  " + iconStyle.Render(icon) + " " + toolName)
	if b.ToolTarget != "" {
		target := link.Render(b.ToolTarget)
		if b.Hovered {
			target = jaggedUnderline(target)
		}
		b2.WriteString(" " + target)
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
	if len(b.Repairs) > 0 {
		clean := make([]string, len(b.Repairs))
		for i, r := range b.Repairs {
			clean[i] = core.SanitizeTerminal(r)
		}
		b2.WriteString(dim.Render(" · repaired: " + strings.Join(clean, ", ")))
	}

	// A tool row is assembled from parts that are each bounded but together are
	// not: a 60-cell target plus an intent plus a token count runs past a narrow
	// column, and an over-wide row wraps the terminal and drags the frame down.
	out := []string{truncateCells(b2.String(), r.Width)}
	if b.Diff != "" && r.DiffMode == DiffInline {
		path := b.ToolPath
		if path == "" {
			path = b.ToolTarget
		}
		out = append(out, r.renderDiffLang(b.Diff, langFromPath(path))...)
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
// DefaultThinkingLines is how many rows a live trace may occupy before it
// scrolls within its own space (display.thinking_lines).
//
// A trace is the one block that grows without bound while you watch it, and a
// model that thinks for thirty seconds otherwise pushes the entire conversation
// off the screen to say something it will then summarize in a sentence.
const DefaultThinkingLines = 6

// renderReasoning draws a thinking trace (plan.md §9.7).
//
// A live trace shows its tail rather than its head: the interesting part of
// thinking is where it has got to, not where it began. A finished trace that
// the reader expands is a deliberate inspection, so it renders in full.
func (r *Renderer) renderReasoning(b *Block) []string {
	style := rgbStyle(0x64, 0x64, 0x64).Italic(true)
	if b.Collapsed {
		lines := len(strings.Split(strings.TrimRight(b.Text, "\n"), "\n"))
		noun := "lines"
		if lines == 1 {
			noun = "line"
		}
		line := style.Render(fmt.Sprintf("▸ thought (%d %s)", lines, noun))
		if b.Hovered {
			line = jaggedUnderline(line)
		}
		return []string{"  " + line}
	}

	// TrimRight, or a trace ending in a newline spends one of its few rows on
	// an empty line.
	wrapped := wrapPlain(strings.TrimRight(b.Text, "\n"), max(r.Width-2, 8))
	if !b.Streaming {
		out := make([]string, 0, len(wrapped))
		for _, line := range wrapped {
			line = style.Render(line)
			if b.Hovered {
				line = jaggedUnderline(line)
			}
			out = append(out, "  "+line)
		}
		return out
	}
	window := r.ThinkingLines
	if window <= 0 {
		window = DefaultThinkingLines
	}

	var out []string
	if hidden := len(wrapped) - window; hidden > 0 {
		// Say how much scrolled past rather than silently dropping it, or a
		// truncated trace reads as a model that thought very little.
		line := style.Render(fmt.Sprintf("⋯ %d earlier lines", hidden))
		if b.Hovered {
			line = jaggedUnderline(line)
		}
		out = append(out, "  "+line)
		wrapped = wrapped[len(wrapped)-window:]
	}
	for _, line := range wrapped {
		line = style.Render(line)
		if b.Hovered {
			line = jaggedUnderline(line)
		}
		out = append(out, "  "+line)
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
				head, tail := splitPlainCells(line, width)
				out = append(out, head)
				line = tail
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
	if width <= 0 {
		return ""
	}
	if lipgloss.Width(s) <= width {
		return s
	}
	// ansi.Truncate, not a rune loop: measuring runes counts every byte of an
	// escape sequence as a cell, so a styled row's ~40-byte SGR prefix ate 40 of
	// the budget and the visible text was cut roughly 40 columns early — and the
	// result carried a severed escape sequence. It also closes any style it cuts
	// through, which a hand-rolled loop cannot.
	return ansi.Truncate(s, width, "")
}

// dropCells returns the remainder after truncateCells.
func splitPlainCells(s string, width int) (head, tail string) {
	head = truncateCells(s, width)
	if head == "" && s != "" {
		// A wide glyph cannot fit in a one-cell row, so ansi.Truncate returns
		// an empty string. Consume the first rune anyway: preserving a glyph
		// that is one cell wider is preferable to an infinite wrapping loop.
		_, size := utf8.DecodeRuneInString(s)
		head = s[:size]
	}
	return head, s[len(head):]
}

// ThinkingMode is how reasoning traces are displayed (plan.md §9.7).
type ThinkingMode string

const (
	// ThinkingOff discards traces entirely.
	ThinkingOff ThinkingMode = "off"

	// ThinkingFull keeps every trace in the transcript.
	ThinkingFull ThinkingMode = "full"

	// ThinkingCurrent keeps the live trace on screen while it streams and
	// collapses it to a summary row once the answer starts. Codex traces stay
	// expanded because their reasoning summary is already compact. The summary
	// stays in the transcript — nothing is ever garbage-collected (plan.md §4.6).
	ThinkingCurrent ThinkingMode = "current"
)

// Next cycles the mode, for the Alt+T binding.
func (t ThinkingMode) Next() ThinkingMode {
	switch t {
	case ThinkingOff:
		return ThinkingCurrent
	case ThinkingCurrent:
		return ThinkingFull
	default:
		return ThinkingOff
	}
}

// Valid reports whether a mode came from a recognized config value.
func (t ThinkingMode) Valid() bool {
	switch t {
	case ThinkingOff, ThinkingFull, ThinkingCurrent:
		return true
	}
	return false
}
