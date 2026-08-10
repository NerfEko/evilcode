package tui

import (
	"image/color"
	"strings"
	"sync"

	"charm.land/lipgloss/v2"
	"github.com/alecthomas/chroma/v2"
	"github.com/alecthomas/chroma/v2/lexers"
	"github.com/alecthomas/chroma/v2/styles"

	"evilcode/internal/core"
	"evilcode/internal/theme"
)

// highlightStyle is the chroma style code is coloured with. Dracula matches the
// default palette, so highlighted code sits in the same world as the chrome
// around it.
var highlightStyle = styles.Get("dracula")

// tokenCache memoizes highlighting by (lang, code). Re-lexing every frame is
// the kind of cost that only shows up once a transcript is long, which is
// exactly when it hurts (plan.md §9.2).
var (
	tokenMu         sync.Mutex
	tokenCache      = map[string][]token{}
	tokenCacheBytes int
)

const (
	maxTokenCacheEntries = 512
	maxTokenCacheBytes   = 16 << 20
)

// token is one highlighted run.
type token struct {
	Text  string
	Color color.RGBA
	Bold  bool
}

// tokenize lexes source into coloured runs, grouped per line.
func tokenize(lang, src string) [][]token {
	key := lang + "\x00" + src
	tokenMu.Lock()
	cached, ok := tokenCache[key]
	tokenMu.Unlock()

	var flat []token
	if ok {
		flat = cached
	} else {
		flat = lexTokens(lang, src)
		tokenMu.Lock()
		entryBytes := len(key)
		for _, t := range flat {
			entryBytes += len(t.Text)
		}
		// A bounded cache: a long session highlighting many blocks should not
		// grow without limit, and a handful of large generated files must not
		// turn an entry-count limit into a multi-hundred-megabyte heap.
		if entryBytes > maxTokenCacheBytes || len(tokenCache) >= maxTokenCacheEntries ||
			tokenCacheBytes+entryBytes > maxTokenCacheBytes {
			tokenCache = map[string][]token{}
			tokenCacheBytes = 0
		}
		if entryBytes <= maxTokenCacheBytes {
			tokenCache[key] = flat
			tokenCacheBytes += entryBytes
		}
		tokenMu.Unlock()
	}

	return splitTokens(flat)
}

// tokenizeUncached is for an open code fence. Every streamed prefix is a new
// source string, so caching those prefixes would evict useful finished-file
// entries and retain a second copy of a growing answer.
func tokenizeUncached(lang, src string) [][]token {
	return splitTokens(lexTokens(lang, src))
}

func splitTokens(flat []token) [][]token {
	// Split runs at newlines so callers can style per line.
	lines := [][]token{{}}
	for _, t := range flat {
		parts := strings.Split(t.Text, "\n")
		for i, p := range parts {
			if i > 0 {
				lines = append(lines, []token{})
			}
			if p != "" {
				lines[len(lines)-1] = append(lines[len(lines)-1], token{
					Text: p, Color: t.Color, Bold: t.Bold,
				})
			}
		}
	}
	return lines
}

func lexTokens(lang, src string) []token {
	lexer := lexers.Get(lang)
	if lexer == nil {
		lexer = lexers.Analyse(src)
	}
	if lexer == nil {
		lexer = lexers.Fallback
	}
	iter, err := lexer.Tokenise(nil, src)
	if err != nil {
		return []token{{Text: src, Color: theme.RGB(200, 200, 200)}}
	}

	var out []token
	for _, t := range iter.Tokens() {
		entry := highlightStyle.Get(t.Type)
		c := theme.RGB(200, 200, 200)
		if entry.Colour.IsSet() {
			c = theme.RGB(entry.Colour.Red(), entry.Colour.Green(), entry.Colour.Blue())
		}
		out = append(out, token{Text: t.Value, Color: c, Bold: entry.Bold == chroma.Yes})
	}
	return out
}

// renderTokens styles one line's runs, optionally tinting every run toward a
// diff color.
func renderTokens(line []token, tint *color.RGBA) string {
	var b strings.Builder
	for _, t := range line {
		c := t.Color
		if tint != nil {
			c = theme.TintDiff(c, *tint)
		}
		style := lipgloss.NewStyle().Foreground(lipgloss.Color(theme.Hex(c)))
		if t.Bold {
			style = style.Bold(true)
		}
		// Sanitized here, at the last point before styling: this text is
		// repository content, and a file in a cloned repo carrying OSC 52 would
		// otherwise write the user's clipboard the moment it is displayed. Only
		// the styling applied on the next line is ours.
		b.WriteString(style.Render(core.SanitizeTerminal(t.Text)))
	}
	return b.String()
}

// lineCache memoizes the styled result, where tokenCache above memoizes only
// the lexing. Styling is the larger half and the side panel re-runs it on every
// frame it is open: a read preview of a long file cost 30ms a frame, all of it
// re-styling text that had not changed. Colours come from the fixed chroma
// style rather than the palette, so a cached line survives /theme.
var (
	lineMu         sync.Mutex
	lineCache      = map[string][]string{}
	lineCacheBytes int
)

const (
	maxLineCacheEntries = 64
	maxLineCacheBytes   = 16 << 20
)

// HighlightLines returns syntax-highlighted lines for a code block. Callers
// must not mutate the result; it is shared.
func HighlightLines(lang, src string) []string {
	key := lang + "\x00" + src
	lineMu.Lock()
	cached, ok := lineCache[key]
	lineMu.Unlock()
	if ok {
		return cached
	}

	lines := tokenize(lang, src)
	out := make([]string, 0, len(lines))
	for _, line := range lines {
		out = append(out, renderTokens(line, nil))
	}

	lineMu.Lock()
	entryBytes := len(key)
	for _, line := range out {
		entryBytes += len(line)
	}
	// Bounded like tokenCache, but tighter: an entry here is a whole file's
	// worth of styled text, not its tokens.
	if entryBytes > maxLineCacheBytes || len(lineCache) >= maxLineCacheEntries ||
		lineCacheBytes+entryBytes > maxLineCacheBytes {
		lineCache = map[string][]string{}
		lineCacheBytes = 0
	}
	if entryBytes <= maxLineCacheBytes {
		lineCache[key] = out
		lineCacheBytes += entryBytes
	}
	lineMu.Unlock()
	return out
}

// HighlightLinesUncached styles a live code prefix without retaining that
// prefix in the process-wide syntax caches.
func HighlightLinesUncached(lang, src string) []string {
	lines := tokenizeUncached(lang, src)
	out := make([]string, 0, len(lines))
	for _, line := range lines {
		out = append(out, renderTokens(line, nil))
	}
	return out
}

// isMarkdown reports whether a path is prose to be rendered rather than code to
// be highlighted.
func isMarkdown(path string) bool {
	return strings.EqualFold(langFromPath(path), "md") ||
		strings.EqualFold(langFromPath(path), "markdown")
}

// langFromPath guesses a lexer name from a file path, for diffs that name a
// file but not a language.
func langFromPath(path string) string {
	if i := strings.LastIndex(path, "."); i >= 0 {
		return path[i+1:]
	}
	return ""
}
