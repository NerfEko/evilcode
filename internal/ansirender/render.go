package ansirender

import (
	"fmt"
	"image"
	"image/color"
	"image/draw"
	"image/png"
	"io"
	"math"
	"os"
	"path/filepath"
	"sync"

	"golang.org/x/image/font"
	"golang.org/x/image/font/gofont/gomono"
	"golang.org/x/image/font/gofont/gomonobold"
	"golang.org/x/image/font/gofont/gomonobolditalic"
	"golang.org/x/image/font/gofont/gomonoitalic"
	"golang.org/x/image/font/opentype"
	"golang.org/x/image/font/sfnt"
	"golang.org/x/image/math/fixed"
)

// DefaultFontSize is the em size in points at 72 DPI, i.e. pixels. Large enough
// that an agent can read the PNG, small enough that a 140x40 frame is not huge.
const DefaultFontSize = 16

// faceSet holds the four styles a terminal actually distinguishes, plus the
// fallback chain for runes the primary font lacks.
type faceSet struct {
	regular, bold, italic, boldItalic font.Face
	primary                           *sfnt.Font
	primaryName                       string
	fallback                          *fallbackChain
	buf                               sfnt.Buffer
	bufMu                             sync.Mutex
	cellW, cellH, baseline            int
}

// Faces are cached per size: loading four TTFs plus the fallback chain is slow
// enough that re-doing it per frame would show up in a probe loop.
var (
	facesMu    sync.Mutex
	facesBySiz = map[float64]*faceSet{}
)

func loadFaces(size float64) (*faceSet, error) {
	var fs faceSet
	if !fs.loadSystemPrimary(size) {
		var err error
		var parsed *sfnt.Font
		mk := func(ttf []byte) (font.Face, error) {
			f, err := opentype.Parse(ttf)
			if err != nil {
				return nil, err
			}
			parsed = f
			return opentype.NewFace(f, &opentype.FaceOptions{
				Size:    size,
				DPI:     72,
				Hinting: font.HintingFull,
			})
		}
		if fs.regular, err = mk(gomono.TTF); err != nil {
			return nil, err
		}
		fs.primary = parsed
		fs.primaryName = "embedded Go Mono"
		if fs.bold, err = mk(gomonobold.TTF); err != nil {
			return nil, err
		}
		if fs.italic, err = mk(gomonoitalic.TTF); err != nil {
			return nil, err
		}
		if fs.boldItalic, err = mk(gomonobolditalic.TTF); err != nil {
			return nil, err
		}
	}
	fs.fallback = loadFallbacks(size)

	// The cell box comes from the font's own monospace advance and metrics, so
	// changing DefaultFontSize rescales everything consistently.
	adv, ok := fs.regular.GlyphAdvance('M')
	if !ok {
		return nil, fmt.Errorf("ansirender: monospace advance unavailable")
	}
	m := fs.regular.Metrics()
	fs.cellW = adv.Ceil()
	fs.cellH = m.Ascent.Ceil() + m.Descent.Ceil()
	fs.baseline = m.Ascent.Ceil()
	return &fs, nil
}

func facesFor(size float64) (*faceSet, error) {
	if size <= 0 || size > 256 || math.IsNaN(size) || math.IsInf(size, 0) {
		return nil, fmt.Errorf("ansirender: font size must be between 0 and 256 pixels")
	}
	facesMu.Lock()
	defer facesMu.Unlock()
	if fs, ok := facesBySiz[size]; ok {
		return fs, nil
	}
	fs, err := loadFaces(size)
	if err != nil {
		return nil, err
	}
	facesBySiz[size] = fs
	return fs, nil
}

func defaultFaces() (*faceSet, error) { return facesFor(DefaultFontSize) }

func (fs *faceSet) primaryFace(c Cell) font.Face {
	switch {
	case c.Bold && c.Italic:
		return fs.boldItalic
	case c.Bold:
		return fs.bold
	case c.Italic:
		return fs.italic
	default:
		return fs.regular
	}
}

// pick chooses the face to draw a cell with. When Go Mono has no glyph for the
// cell's leading rune, the fallback chain is consulted so the frame shows the
// real shape instead of a tofu box. Weight and slant are dropped in that case:
// the fallback fonts have no matching variants, and the wrong shape is a worse
// lie than the wrong weight.
func (fs *faceSet) pick(c Cell) font.Face {
	face := fs.primaryFace(c)
	r := []rune(c.Text)
	if len(r) == 0 || fs.fallback == nil {
		return face
	}
	fs.bufMu.Lock()
	ok := hasGlyph(fs.primary, &fs.buf, r[0])
	fs.bufMu.Unlock()
	if ok {
		return face
	}
	if alt := fs.fallback.faceFor(r[0]); alt != nil {
		return alt
	}
	return face
}

// faintFactor is how much a faint (SGR 2) cell's foreground is pulled toward
// its background. Terminals vary; this matches the common "about 60%" look.
const faintFactor = 0.55

func applyFaint(fg, bg color.RGBA) color.RGBA {
	mix := func(a, b uint8) uint8 {
		return uint8(float64(b) + (float64(a)-float64(b))*faintFactor)
	}
	return color.RGBA{R: mix(fg.R, bg.R), G: mix(fg.G, bg.G), B: mix(fg.B, bg.B), A: 255}
}

// Render draws a parsed screen into an image. Every cell paints its background
// first, so the result matches what the terminal showed rather than what the
// text alone implies.
func Render(scr *Screen) (image.Image, error) {
	return RenderSize(scr, DefaultFontSize)
}

// RenderSize is Render at an explicit em size in pixels. Bigger frames are
// easier to judge by eye; the cell grid scales with the font's own metrics.
func RenderSize(scr *Screen, size float64) (image.Image, error) {
	fs, err := facesFor(size)
	if err != nil {
		return nil, err
	}
	cols, rows := scr.Size()
	if cols == 0 {
		cols = 1
	}
	if rows == 0 {
		rows = 1
	}

	img := image.NewRGBA(image.Rect(0, 0, cols*fs.cellW, rows*fs.cellH))
	draw.Draw(img, img.Bounds(), &image.Uniform{DefaultBG}, image.Point{}, draw.Src)

	for y := 0; y < rows; y++ {
		for x := 0; x < cols; x++ {
			c := scr.At(x, y)
			rect := image.Rect(x*fs.cellW, y*fs.cellH, (x+1)*fs.cellW, (y+1)*fs.cellH)
			if c.BG != DefaultBG {
				draw.Draw(img, rect, &image.Uniform{c.BG}, image.Point{}, draw.Src)
			}
			if c.Text == "" || c.Text == " " {
				continue
			}
			fg := c.FG
			if c.Faint {
				fg = applyFaint(fg, c.BG)
			}
			d := &font.Drawer{
				Dst:  img,
				Src:  &image.Uniform{fg},
				Face: fs.pick(c),
				Dot: fixed.Point26_6{
					X: fixed.I(x * fs.cellW),
					Y: fixed.I(y*fs.cellH + fs.baseline),
				},
			}
			d.DrawString(c.Text)
		}
	}
	return img, nil
}

// RenderString parses terminal output and renders it in one step.
func RenderString(s string) (image.Image, error) {
	return Render(Parse(s))
}

// WritePNG renders terminal output straight to w as a PNG.
func WritePNG(w io.Writer, ansiText string) error {
	return WritePNGSize(w, ansiText, DefaultFontSize)
}

// WritePNGSize is WritePNG at an explicit em size in pixels.
func WritePNGSize(w io.Writer, ansiText string, size float64) error {
	img, err := RenderSize(Parse(ansiText), size)
	if err != nil {
		return err
	}
	return png.Encode(w, img)
}

// RenderFile reads captured ANSI text from src and writes a PNG to dst.
func RenderFile(src, dst string) error {
	return RenderFileSize(src, dst, DefaultFontSize)
}

// RenderFileSize is RenderFile at an explicit em size in pixels.
func RenderFileSize(src, dst string, size float64) error {
	in, err := os.ReadFile(src)
	if err != nil {
		return err
	}
	mode := os.FileMode(0o644)
	if info, statErr := os.Stat(dst); statErr == nil {
		mode = info.Mode().Perm()
	}
	out, err := os.CreateTemp(filepath.Dir(dst), "."+filepath.Base(dst)+".*")
	if err != nil {
		return err
	}
	tmp := out.Name()
	defer os.Remove(tmp)
	if err := out.Chmod(mode); err != nil {
		out.Close()
		return err
	}
	if err := WritePNGSize(out, string(in), size); err != nil {
		out.Close()
		return err
	}
	if err := out.Sync(); err != nil {
		out.Close()
		return err
	}
	if err := out.Close(); err != nil {
		return err
	}
	return os.Rename(tmp, dst)
}
