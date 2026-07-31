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
	tokenMu    sync.Mutex
	tokenCache = map[string][]token{}
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
		// A bounded cache: a long session highlighting many blocks should not
		// grow without limit.
		if len(tokenCache) > 512 {
			tokenCache = map[string][]token{}
		}
		tokenCache[key] = flat
		tokenMu.Unlock()
	}

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

// HighlightLines returns syntax-highlighted lines for a code block.
func HighlightLines(lang, src string) []string {
	lines := tokenize(lang, src)
	out := make([]string, 0, len(lines))
	for _, line := range lines {
		out = append(out, renderTokens(line, nil))
	}
	return out
}

// langFromPath guesses a lexer name from a file path, for diffs that name a
// file but not a language.
func langFromPath(path string) string {
	if i := strings.LastIndex(path, "."); i >= 0 {
		return path[i+1:]
	}
	return ""
}
