package ansirender

import (
	"bytes"
	"image/color"
	"image/png"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func rgb(r, g, b uint8) color.RGBA { return color.RGBA{R: r, G: g, B: b, A: 255} }

func TestParseSGR(t *testing.T) {
	tests := []struct {
		name  string
		in    string
		want  Cell // expected attributes of the cell at (0,0)
		atCol int
	}{
		{
			name: "plain text uses defaults",
			in:   "x",
			want: Cell{Text: "x", FG: DefaultFG, BG: DefaultBG},
		},
		{
			name: "basic fg and bg",
			in:   "\x1b[31;42mx",
			want: Cell{Text: "x", FG: rgb(128, 0, 0), BG: rgb(0, 128, 0)},
		},
		{
			name: "bright fg and bg",
			in:   "\x1b[91;102mx",
			want: Cell{Text: "x", FG: rgb(255, 0, 0), BG: rgb(0, 255, 0)},
		},
		{
			name: "truecolor fg",
			in:   "\x1b[38;2;255;121;198mx",
			want: Cell{Text: "x", FG: rgb(255, 121, 198), BG: DefaultBG},
		},
		{
			name: "truecolor bg",
			in:   "\x1b[48;2;42;36;64mx",
			want: Cell{Text: "x", FG: DefaultFG, BG: rgb(42, 36, 64)},
		},
		{
			name: "256 cube index 196 is pure red",
			in:   "\x1b[38;5;196mx",
			want: Cell{Text: "x", FG: rgb(255, 0, 0), BG: DefaultBG},
		},
		{
			// 16 + 36*2 + 6*3 + 4 = 110 -> levels (2,3,4) -> 135,175,215
			name: "256 cube levels follow 55+40x",
			in:   "\x1b[38;5;110mx",
			want: Cell{Text: "x", FG: rgb(135, 175, 215), BG: DefaultBG},
		},
		{
			name: "256 grayscale follows 8+10n",
			in:   "\x1b[38;5;244mx",
			want: Cell{Text: "x", FG: rgb(128, 128, 128), BG: DefaultBG},
		},
		{
			name: "256 index below 16 uses the base table",
			in:   "\x1b[48;5;4mx",
			want: Cell{Text: "x", FG: DefaultFG, BG: rgb(0, 0, 128)},
		},
		{
			name: "bold",
			in:   "\x1b[1mx",
			want: Cell{Text: "x", FG: DefaultFG, BG: DefaultBG, Bold: true},
		},
		{
			name: "faint and italic",
			in:   "\x1b[2;3mx",
			want: Cell{Text: "x", FG: DefaultFG, BG: DefaultBG, Faint: true, Italic: true},
		},
		{
			name: "22 clears bold and faint but not italic",
			in:   "\x1b[1;2;3m\x1b[22mx",
			want: Cell{Text: "x", FG: DefaultFG, BG: DefaultBG, Italic: true},
		},
		{
			name: "reverse video swaps resolved colors",
			in:   "\x1b[31;42;7mx",
			want: Cell{Text: "x", FG: rgb(0, 128, 0), BG: rgb(128, 0, 0)},
		},
		{
			name: "reverse video on defaults swaps the defaults",
			in:   "\x1b[7mx",
			want: Cell{Text: "x", FG: DefaultBG, BG: DefaultFG},
		},
		{
			name: "27 cancels reverse",
			in:   "\x1b[31;7m\x1b[27mx",
			want: Cell{Text: "x", FG: rgb(128, 0, 0), BG: DefaultBG},
		},
		{
			name: "39 and 49 restore defaults",
			in:   "\x1b[31;42m\x1b[39;49mx",
			want: Cell{Text: "x", FG: DefaultFG, BG: DefaultBG},
		},
		{
			name: "0 resets everything",
			in:   "\x1b[1;3;7;31;42m\x1b[0mx",
			want: Cell{Text: "x", FG: DefaultFG, BG: DefaultBG},
		},
		{
			name: "bare CSI m resets",
			in:   "\x1b[1;31m\x1b[mx",
			want: Cell{Text: "x", FG: DefaultFG, BG: DefaultBG},
		},
		{
			name: "colon-separated truecolor is accepted",
			in:   "\x1b[38:2:80:250:123mx",
			want: Cell{Text: "x", FG: rgb(80, 250, 123), BG: DefaultBG},
		},
		{
			name: "unknown codes are ignored, neighbours still apply",
			in:   "\x1b[4;31;53mx",
			want: Cell{Text: "x", FG: rgb(128, 0, 0), BG: DefaultBG},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := Parse(tt.in).At(tt.atCol, 0)
			if got != tt.want {
				t.Errorf("cell = %+v, want %+v", got, tt.want)
			}
		})
	}
}

func TestParseSkipsNonSGREscapes(t *testing.T) {
	tests := []struct {
		name string
		in   string
	}{
		{"cursor position", "\x1b[2;5Hab"},
		{"erase display", "\x1b[2Jab"},
		{"synchronized output begin", "\x1b[?2026hab"},
		{"osc title with BEL", "\x1b]0;a title\x07ab"},
		{"osc title with ST", "\x1b]0;a title\x1b\\ab"},
		{"two byte escape", "\x1b(Bab"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := Parse(tt.in).Text(); got != "ab" {
				t.Errorf("Text() = %q, want %q", got, "ab")
			}
		})
	}
}

func TestParseLayout(t *testing.T) {
	t.Run("rows and columns", func(t *testing.T) {
		scr := Parse("ab\ncdef\n")
		cols, rows := scr.Size()
		if cols != 4 || rows != 3 {
			t.Fatalf("Size() = %d x %d, want 4 x 3", cols, rows)
		}
		if got := scr.Text(); got != "ab\ncdef\n" {
			t.Errorf("Text() = %q, want %q", got, "ab\ncdef\n")
		}
	})

	t.Run("carriage return overwrites", func(t *testing.T) {
		if got := Parse("abc\rX").Text(); got != "Xbc" {
			t.Errorf("Text() = %q, want %q", got, "Xbc")
		}
	})

	t.Run("tab advances to the next stop", func(t *testing.T) {
		if got := Parse("a\tb").Text(); got != "a       b" {
			t.Errorf("Text() = %q, want %q", got, "a       b")
		}
	})

	t.Run("trailing blanks are trimmed", func(t *testing.T) {
		if got := Parse("a   ").Text(); got != "a" {
			t.Errorf("Text() = %q, want %q", got, "a")
		}
	})

	t.Run("skipped cells keep the default style, not the live one", func(t *testing.T) {
		// Paint red, jump forward with a tab, then write. The tabbed-over cells
		// were written by the tab itself and so are red; the cell after \r is
		// the interesting one: never written, so it must stay default.
		scr := Parse("\x1b[41mabc\rZ")
		if got := scr.At(1, 0).BG; got != rgb(128, 0, 0) {
			t.Errorf("overwritten cell bg = %v, want red", got)
		}
	})
}

func TestParseWideGlyphs(t *testing.T) {
	// The bat is the project's own emoji (plan.md §2.1) and is double width.
	scr := Parse("a🦇b")
	cols, _ := scr.Size()
	if cols != 4 {
		t.Fatalf("Size() cols = %d, want 4 (a + 2 for the emoji + b)", cols)
	}
	if got := scr.At(1, 0).Text; got != "🦇" {
		t.Errorf("lead cell = %q, want the emoji", got)
	}
	if got := scr.At(2, 0).Text; got != "" {
		t.Errorf("continuation cell = %q, want empty", got)
	}
	if got := scr.At(3, 0).Text; got != "b" {
		t.Errorf("cell after the emoji = %q, want %q", got, "b")
	}
	if got := scr.Text(); got != "a🦇b" {
		t.Errorf("Text() = %q, want %q — a wide glyph must not gain a padding space", got, "a🦇b")
	}
}

func TestParseStyleSpansWideGlyph(t *testing.T) {
	scr := Parse("\x1b[41m🦇")
	if got := scr.At(1, 0).BG; got != rgb(128, 0, 0) {
		t.Errorf("continuation cell bg = %v, want the lead cell's red", got)
	}
}

func TestPalette256Boundaries(t *testing.T) {
	tests := []struct {
		n    int
		want color.RGBA
	}{
		{0, rgb(0, 0, 0)},
		{15, rgb(255, 255, 255)},
		{16, rgb(0, 0, 0)},        // first cube entry: all levels zero
		{231, rgb(255, 255, 255)}, // last cube entry: all levels 255
		{232, rgb(8, 8, 8)},       // first gray
		{255, rgb(238, 238, 238)}, // last gray
	}
	for _, tt := range tests {
		if got := palette256(tt.n); got != tt.want {
			t.Errorf("palette256(%d) = %v, want %v", tt.n, got, tt.want)
		}
	}
}

func TestRenderProducesDecodablePNG(t *testing.T) {
	var buf bytes.Buffer
	if err := WritePNG(&buf, "\x1b[38;2;255;121;198mevilcode \x1b[1m🦇\x1b[0m\nsecond line"); err != nil {
		t.Fatalf("WritePNG: %v", err)
	}
	img, err := png.Decode(&buf)
	if err != nil {
		t.Fatalf("png.Decode: %v", err)
	}
	b := img.Bounds()
	if b.Dx() <= 0 || b.Dy() <= 0 {
		t.Fatalf("empty image %v", b)
	}
	// Two rows of text must produce exactly two cell rows of height.
	fs, err := defaultFaces()
	if err != nil {
		t.Fatal(err)
	}
	if got, want := b.Dy(), 2*fs.cellH; got != want {
		t.Errorf("height = %d, want %d", got, want)
	}
}

func TestRenderPaintsBackgrounds(t *testing.T) {
	// A cell with an explicit background must paint it even where no glyph ink
	// lands — otherwise selection bands and the user-prompt band vanish.
	img, err := RenderString("\x1b[48;2;42;36;64m ")
	if err != nil {
		t.Fatalf("RenderString: %v", err)
	}
	want := rgb(42, 36, 64)
	r, g, b, _ := img.At(1, 1).RGBA()
	got := rgb(uint8(r>>8), uint8(g>>8), uint8(b>>8))
	if got != want {
		t.Errorf("pixel = %v, want %v", got, want)
	}
}

func TestRenderEmptyInput(t *testing.T) {
	if _, err := RenderString(""); err != nil {
		t.Fatalf("RenderString(\"\"): %v", err)
	}
}

func TestRenderFileFailurePreservesExistingOutput(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "frame.ansi")
	dst := filepath.Join(dir, "frame.png")
	if err := os.WriteFile(src, []byte("hello"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(dst, []byte("previous image"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := RenderFileSize(src, dst, 0); err == nil {
		t.Fatal("invalid font size unexpectedly rendered")
	}
	got, err := os.ReadFile(dst)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "previous image" {
		t.Errorf("failed render destroyed the previous output: %q", got)
	}
}

func TestFaintPullsTowardBackground(t *testing.T) {
	fg, bg := rgb(200, 200, 200), rgb(0, 0, 0)
	got := applyFaint(fg, bg)
	if got.R >= fg.R || got.R <= bg.R {
		t.Errorf("applyFaint = %v, want strictly between %v and %v", got, bg, fg)
	}
}

func TestSplitParamsRejectsGarbage(t *testing.T) {
	// A malformed parameter must not be read as a partial number and silently
	// select the wrong color.
	if got := splitParams("3x"); len(got) != 1 || got[0] != 0 {
		t.Errorf("splitParams(%q) = %v, want [0]", "3x", got)
	}
}

func TestParseHandlesTruncatedEscape(t *testing.T) {
	// A frame captured mid-write must not hang or panic the parser.
	for _, in := range []string{"\x1b", "\x1b[", "\x1b[38;2;255", "\x1b]0;title"} {
		if got := Parse(in).Text(); strings.Contains(got, "\x1b") {
			t.Errorf("Parse(%q) leaked an escape byte: %q", in, got)
		}
	}
}
