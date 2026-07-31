package tui

import (
	"fmt"
	"strings"
	"sync"

	"charm.land/glamour/v2"

	"evilcode/internal/theme"
)

// markdownStyleJSON is the glamour style implementing plan.md §7.2. Code
// blocks are deliberately unstyled here: they are extracted and rendered by
// renderCodeBlock so they can carry streaming chrome (§9.2).
func markdownStyleJSON(m theme.Markdown) []byte {
	return []byte(fmt.Sprintf(`{
  "document":      {"block_prefix": "", "block_suffix": "", "color": %q, "margin": 0},
  "block_quote":   {"color": %q, "indent": 1, "indent_token": "│ "},
  "paragraph":     {},
  "list":          {"level_indent": 2},
  "heading":       {"block_suffix": "", "color": %q, "bold": true},
  "h1":            {"prefix": "", "color": %q, "bold": true, "underline": true},
  "h2":            {"prefix": "", "color": %q, "bold": true, "underline": true},
  "h3":            {"prefix": "", "color": %q, "bold": true},
  "h4":            {"prefix": "", "color": %q, "bold": true},
  "h5":            {"prefix": "", "color": %q, "bold": true},
  "h6":            {"prefix": "", "color": %q, "bold": true},
  "text":          {},
  "strong":        {"color": %q, "bold": true},
  "emph":          {"italic": true},
  "hr":            {"color": %q, "format": "\n────────────────────────\n"},
  "item":          {"block_prefix": "• "},
  "enumeration":   {"block_prefix": ". "},
  "task":          {"ticked": "[x] ", "unticked": "[ ] "},
  "link":          {"color": %q, "underline": true},
  "link_text":     {"color": %q},
  "image":         {"color": %q, "underline": true},
  "image_text":    {"color": %q, "format": "[image: {{.text}}]"},
  "code":          {"color": %q, "background_color": %q},
  "code_block":    {"margin": 0},
  "table":         {"center_separator": "┼", "column_separator": "│", "row_separator": "─"},
  "definition_list": {},
  "html_block":    {"color": %q},
  "html_span":     {"color": %q}
}`,
		theme.Hex(m.Body),
		theme.Hex(m.Dim),
		theme.Hex(m.H2),
		theme.Hex(m.H1), theme.Hex(m.H2), theme.Hex(m.H3),
		theme.Hex(m.H4), theme.Hex(m.H4), theme.Hex(m.H4),
		theme.Hex(m.BoldText),
		theme.Hex(m.Dim),
		theme.Hex(m.Link), theme.Hex(m.Link),
		theme.Hex(m.Link), theme.Hex(m.Link),
		theme.Hex(m.InlineCode), theme.Hex(m.CodeBg),
		theme.Hex(m.HTML), theme.Hex(m.HTML),
	))
}

// Markdown renders prose. Finished messages are rendered once and cached; only
// a streaming tail re-renders per frame, because re-rendering the whole
// transcript every frame is O(total length) and shows up immediately on a long
// conversation (plan.md §9.1).
type Markdown struct {
	mu       sync.Mutex
	prose    theme.Markdown
	width    int
	renderer *glamour.TermRenderer
	cache    map[string]string
}

// NewMarkdown builds a renderer for the given wrap width.
func NewMarkdown(width int, prose theme.Markdown) *Markdown {
	md := &Markdown{cache: map[string]string{}, prose: prose}
	md.setWidth(width)
	return md
}

// SetProse swaps the palette's §7.2 table and rebuilds. Every cached render
// carries the old colors, so the cache goes with it — otherwise `/theme` would
// recolor only the messages that arrive afterwards.
func (m *Markdown) SetProse(prose theme.Markdown) {
	m.mu.Lock()
	m.prose = prose
	m.cache = map[string]string{}
	width := m.width
	m.mu.Unlock()
	m.setWidth(width)
}

func (m *Markdown) setWidth(width int) {
	if width < 1 {
		width = 1
	}
	r, err := glamour.NewTermRenderer(
		glamour.WithStylesFromJSONBytes(markdownStyleJSON(m.prose)),
		glamour.WithWordWrap(width),
		glamour.WithPreservedNewLines(),
	)
	if err != nil {
		// A broken style must not take the UI down; prose falls back to plain
		// text, which is ugly but readable.
		m.renderer = nil
	} else {
		m.renderer = r
	}
	m.width = width
	m.cache = map[string]string{}
}

// SetWidth re-creates the renderer when the terminal resizes, dropping the
// cache since every cached string was wrapped to the old width.
func (m *Markdown) SetWidth(width int) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if width == m.width {
		return
	}
	m.setWidth(width)
}

// Width reports the current wrap width.
func (m *Markdown) Width() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.width
}

// Render renders markdown prose, caching the result. Pass cache=false for a
// streaming tail, which changes every frame and would otherwise fill the cache
// with garbage.
func (m *Markdown) Render(src string, cache bool) string {
	m.mu.Lock()
	defer m.mu.Unlock()

	if cache {
		if got, ok := m.cache[src]; ok {
			return got
		}
	}
	out := m.renderLocked(src)
	if cache {
		m.cache[src] = out
	}
	return out
}

func (m *Markdown) renderLocked(src string) string {
	if m.renderer == nil {
		return src
	}
	out, err := m.renderer.Render(src)
	if err != nil {
		return src
	}
	// Glamour emits a leading and trailing blank line for the document block;
	// the transcript controls its own spacing.
	return strings.Trim(out, "\n")
}

// Segment is one piece of an assistant message: either prose or a fenced code
// block. Splitting them is what lets code blocks carry their own chrome while
// prose goes through glamour (plan.md §9.1, §9.2).
type Segment struct {
	Code bool
	Lang string
	Text string

	// Open marks a fence that never closed, which happens constantly while
	// streaming. It renders anyway, with a streaming header.
	Open bool
}

// SplitSegments separates fenced code blocks from prose. An unterminated fence
// is returned as an open code segment rather than being held back, so a code
// block materializes and grows while streaming instead of popping in at the
// end.
func SplitSegments(src string) []Segment {
	lines := strings.Split(src, "\n")
	var out []Segment
	var prose []string
	var code []string
	var lang string
	inCode := false

	flushProse := func() {
		if len(prose) > 0 {
			if text := strings.Trim(strings.Join(prose, "\n"), "\n"); text != "" {
				out = append(out, Segment{Text: text})
			}
			prose = nil
		}
	}

	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if !inCode && strings.HasPrefix(trimmed, "```") {
			flushProse()
			inCode = true
			lang = strings.TrimSpace(strings.TrimPrefix(trimmed, "```"))
			code = nil
			continue
		}
		if inCode && trimmed == "```" {
			out = append(out, Segment{Code: true, Lang: lang, Text: strings.Join(code, "\n")})
			inCode = false
			code = nil
			continue
		}
		if inCode {
			code = append(code, line)
			continue
		}
		prose = append(prose, line)
	}

	if inCode {
		out = append(out, Segment{Code: true, Lang: lang, Text: strings.Join(code, "\n"), Open: true})
	}
	flushProse()
	return out
}
