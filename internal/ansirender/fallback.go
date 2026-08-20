package ansirender

import (
	"os"
	"path/filepath"
	"strings"
	"sync"

	"golang.org/x/image/font"
	"golang.org/x/image/font/opentype"
	"golang.org/x/image/font/sfnt"
)

// Go Mono is a programming font: it has the box-drawing runs and the arrows, but
// none of the geometric shapes, braille, or dingbats that plan.md §2.3 leans on
// (● ○ ▰ ▱ ▸ ⊳ ↻ ⠋ …). Those runes would render as .notdef boxes, which is the
// one thing a probe PNG must not do — an agent comparing a frame against the
// spec has to be able to tell ● from ○.
//
// So glyphs Go Mono lacks are looked up in whatever system fonts are installed.
// Probe PNGs are for eyes only (golden frames are plain text, plan.md §14), so
// depending on system fonts here costs no reproducibility.
//
// Color emoji fonts are deliberately not usable: they carry their artwork in
// COLR/CBDT tables that x/image cannot rasterize, so their glyphs have outlines
// of zero ink. See docs/DEVIATIONS.md.
var defaultFallbackFonts = []string{
	"/usr/share/fonts/symbols-nerd-font/SymbolsNerdFontMono-Regular.ttf",
	"~/.local/share/fonts/symbols-nerd-font/SymbolsNerdFontMono-Regular.ttf",
	"/usr/share/fonts/noto/NotoSansSymbols2-Regular.ttf",
	"/usr/share/fonts/noto/NotoSansSymbols-Regular.ttf",
	"/usr/share/fonts/noto/NotoEmoji-Regular.ttf",
	"/usr/share/fonts/noto-emoji/NotoEmoji-Regular.ttf",
	"/usr/share/fonts/TTF/DejaVuSans.ttf",
	"/usr/share/fonts/dejavu/DejaVuSans.ttf",
	"/usr/share/fonts/truetype/dejavu/DejaVuSans.ttf",
	"/usr/share/fonts/liberation-fonts/LiberationSans-Regular.ttf",
}

// FallbackFontsEnv names the environment variable that overrides the search
// list, colon-separated. Point it at a monochrome emoji font (e.g. Noto Emoji)
// to get emoji artwork in probe PNGs instead of placeholder boxes.
const FallbackFontsEnv = "EVILCODE_PROBE_FONTS"

// primaryFamilies are four-variant monospace families preferred over the
// embedded Go Mono when installed. Go Mono is a fine programming font but has
// no rounded box corners (╭ ╮ ╰ ╯, used by every box in plan.md §3.3) and none
// of the Nerd Font glyphs the spec calls for (§6.1, §8.9). A patched terminal
// font has both, and it is what the TUI is actually looked at in.
//
// Order is preference; the first family whose four files all load wins.
var primaryFamilies = [][4]string{
	{
		"~/.local/share/fonts/jetbrains-mono-nerd/JetBrainsMonoNerdFontMono-Regular.ttf",
		"~/.local/share/fonts/jetbrains-mono-nerd/JetBrainsMonoNerdFontMono-Bold.ttf",
		"~/.local/share/fonts/jetbrains-mono-nerd/JetBrainsMonoNerdFontMono-Italic.ttf",
		"~/.local/share/fonts/jetbrains-mono-nerd/JetBrainsMonoNerdFontMono-BoldItalic.ttf",
	},
	{
		"/usr/share/fonts/jetbrains-mono-nerd/JetBrainsMonoNerdFontMono-Regular.ttf",
		"/usr/share/fonts/jetbrains-mono-nerd/JetBrainsMonoNerdFontMono-Bold.ttf",
		"/usr/share/fonts/jetbrains-mono-nerd/JetBrainsMonoNerdFontMono-Italic.ttf",
		"/usr/share/fonts/jetbrains-mono-nerd/JetBrainsMonoNerdFontMono-BoldItalic.ttf",
	},
}

// PrimaryFontEnv overrides the primary family with four colon-separated paths
// in the order regular:bold:italic:bolditalic.
const PrimaryFontEnv = "EVILCODE_PROBE_FONT"

type fallbackFont struct {
	font *sfnt.Font
	face font.Face
}

// expandHome resolves a leading ~/ against the current user's home directory.
func expandHome(p string) string {
	if !strings.HasPrefix(p, "~/") {
		return p
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return p
	}
	return filepath.Join(home, p[2:])
}

// loadFaceFile parses one font file into a face at the given size.
func loadFaceFile(path string, size float64) (*sfnt.Font, font.Face, error) {
	data, err := os.ReadFile(expandHome(path))
	if err != nil {
		return nil, nil, err
	}
	f, err := opentype.Parse(data)
	if err != nil {
		return nil, nil, err
	}
	face, err := opentype.NewFace(f, &opentype.FaceOptions{
		Size:    size,
		DPI:     72,
		Hinting: font.HintingFull,
	})
	if err != nil {
		return nil, nil, err
	}
	return f, face, nil
}

// loadSystemPrimary fills the face set from a preferred system family, and
// reports whether it succeeded. All four variants must load, so a partly
// installed family never yields a set where bold silently differs in metrics.
func (fs *faceSet) loadSystemPrimary(size float64) bool {
	families := primaryFamilies
	if env := os.Getenv(PrimaryFontEnv); env != "" {
		parts := strings.Split(env, ":")
		if len(parts) != 4 {
			return false
		}
		families = [][4]string{{parts[0], parts[1], parts[2], parts[3]}}
	}

	for _, fam := range families {
		var faces [4]font.Face
		var first *sfnt.Font
		ok := true
		for i, path := range fam {
			f, face, err := loadFaceFile(path, size)
			if err != nil {
				ok = false
				break
			}
			if i == 0 {
				first = f
			}
			faces[i] = face
		}
		if !ok {
			continue
		}
		fs.regular, fs.bold, fs.italic, fs.boldItalic = faces[0], faces[1], faces[2], faces[3]
		fs.primary = first
		fs.primaryName = expandHome(fam[0])
		return true
	}
	return false
}

type fallbackChain struct {
	mu     sync.Mutex
	fonts  []fallbackFont
	loaded []string
	buf    sfnt.Buffer
	// cache memoizes the resolved face per rune; a nil value means "no fallback
	// has this rune", which is worth remembering too.
	cache map[rune]font.Face
}

func loadFallbacks(size float64) *fallbackChain {
	fc := &fallbackChain{cache: map[rune]font.Face{}}
	for _, path := range fallbackPaths() {
		f, face, err := loadFaceFile(path, size)
		if err != nil {
			continue
		}
		fc.fonts = append(fc.fonts, fallbackFont{font: f, face: face})
		fc.loaded = append(fc.loaded, expandHome(path))
	}
	return fc
}

func fallbackPaths() []string {
	if env := os.Getenv(FallbackFontsEnv); env != "" {
		var out []string
		for _, p := range strings.Split(env, ":") {
			if p = strings.TrimSpace(p); p != "" {
				out = append(out, p)
			}
		}
		return out
	}
	// Also sweep the common font roots for a monochrome emoji font, since its
	// exact path varies by distro.
	paths := append([]string(nil), defaultFallbackFonts...)
	for _, dir := range []string{"/usr/share/fonts", "/usr/local/share/fonts"} {
		matches, _ := filepath.Glob(filepath.Join(dir, "*", "NotoEmoji-Regular.ttf"))
		paths = append(paths, matches...)
	}
	return paths
}

// faceFor returns a face able to draw r, or nil when nothing in the chain has
// it. Callers fall back to drawing whatever the primary face produces.
func (fc *fallbackChain) faceFor(r rune) font.Face {
	fc.mu.Lock()
	defer fc.mu.Unlock()
	if face, ok := fc.cache[r]; ok {
		return face
	}
	var found font.Face
	for _, ff := range fc.fonts {
		if hasGlyph(ff.font, &fc.buf, r) {
			found = ff.face
			break
		}
	}
	fc.cache[r] = found
	return found
}

// Resolve reports whether any loaded face can draw r. It backs the
// `evilcode probe fonts` diagnostic, so a tofu box in a frame has a one-command
// explanation instead of being a mystery.
func Resolve(r rune) bool {
	fs, err := defaultFaces()
	if err != nil {
		return false
	}
	fs.bufMu.Lock()
	ok := hasGlyph(fs.primary, &fs.buf, r)
	fs.bufMu.Unlock()
	if ok {
		return true
	}
	return fs.fallback != nil && fs.fallback.faceFor(r) != nil
}

// LoadedFonts lists the font files backing the renderer, primary first.
func LoadedFonts() []string {
	fs, err := defaultFaces()
	if err != nil {
		return nil
	}
	return append([]string{fs.primaryName}, fs.fallback.names()...)
}

func (fc *fallbackChain) names() []string {
	if fc == nil {
		return nil
	}
	return fc.loaded
}

// hasGlyph reports whether the font maps r to a real glyph rather than .notdef.
// Testing glyph bounds is not enough: .notdef is the tofu box, which has ink.
func hasGlyph(f *sfnt.Font, buf *sfnt.Buffer, r rune) bool {
	idx, err := f.GlyphIndex(buf, r)
	return err == nil && idx != 0
}
