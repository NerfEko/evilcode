package graphics

import (
	"bytes"
	"encoding/base64"
	"image"
	"image/color"
	"image/jpeg"
	"image/png"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/image/bmp"
)

func TestKittySequenceCarriesThePNG(t *testing.T) {
	png := []byte("\x89PNG\r\n\x1a\nfake")
	got := KittySequence(Image{PNG: png, Cols: 20, Rows: 10, ID: 7})

	if !strings.HasPrefix(got, "\x1b_G") || !strings.HasSuffix(got, "\x1b\\") {
		t.Fatalf("sequence is not an APC envelope: %q", got)
	}
	for _, want := range []string{"a=T", "f=100", "c=20", "r=10", "i=7", "m=0"} {
		if !strings.Contains(got, want) {
			t.Errorf("sequence is missing %q: %q", want, got)
		}
	}
	if !strings.Contains(got, base64.StdEncoding.EncodeToString(png)) {
		t.Error("the payload is not the base64 of the PNG")
	}
}

func TestCursorPositionIsOneBased(t *testing.T) {
	if got := CursorPosition(7, 3); got != "\x1b[7;3H" {
		t.Errorf("CursorPosition = %q", got)
	}
	if got := CursorPosition(0, 0); got != "\x1b[1;1H" {
		t.Errorf("CursorPosition clamps = %q", got)
	}
}

func TestKittySequenceChunksLongPayloads(t *testing.T) {
	// Exceeding the protocol's per-sequence limit does not error, it corrupts:
	// the terminal reads a truncated image and draws garbage.
	png := make([]byte, ChunkSize*3)
	for i := range png {
		png[i] = byte(i)
	}
	got := KittySequence(Image{PNG: png})

	chunks := strings.Count(got, "\x1b_G")
	if chunks < 4 {
		t.Errorf("emitted %d chunks for %d bytes of payload", chunks, len(png))
	}
	// Every chunk but the last says more is coming.
	if strings.Count(got, "m=1") != chunks-1 {
		t.Errorf("m=1 appears %d times across %d chunks", strings.Count(got, "m=1"), chunks)
	}
	if strings.Count(got, "m=0") != 1 {
		t.Errorf("m=0 appears %d times, want exactly one final chunk",
			strings.Count(got, "m=0"))
	}
	// Only the first chunk carries the image's metadata; repeating it would
	// make the terminal treat each chunk as a new image.
	if strings.Count(got, "f=100") != 1 {
		t.Errorf("f=100 appears %d times, want it on the first chunk only",
			strings.Count(got, "f=100"))
	}
	for _, part := range strings.Split(got, "\x1b_G")[1:] {
		body := part[strings.Index(part, ";")+1:]
		body = strings.TrimSuffix(body, "\x1b\\")
		if len(body) > ChunkSize {
			t.Fatalf("a chunk carried %d bytes, over the protocol's %d", len(body), ChunkSize)
		}
	}
}

func TestKittySequenceOmitsUnsetGeometry(t *testing.T) {
	got := KittySequence(Image{PNG: []byte("x")})
	for _, unwanted := range []string{"c=", "r=", "i="} {
		if strings.Contains(got, unwanted) {
			t.Errorf("sequence invented %q with nothing set: %q", unwanted, got)
		}
	}
}

func TestTmuxWrappingDoublesTheEscape(t *testing.T) {
	// tmux swallows anything it does not understand unless it is wrapped, and
	// the inner ESC has to be doubled or the passthrough ends early.
	t.Setenv("TMUX", "/tmp/tmux-1000/default,1,0")
	got := KittySequence(Image{PNG: []byte("x")})

	if !strings.HasPrefix(got, "\x1bPtmux;") {
		t.Fatalf("not wrapped for tmux: %q", got)
	}
	// Every occurrence must be the doubled form. Counting rather than a
	// Contains check, because the doubled bytes contain the single form.
	if a, b := strings.Count(got, "\x1b_G"), strings.Count(got, "\x1b\x1b_G"); a != b || b == 0 {
		t.Errorf("%d escapes, %d doubled: %q", a, b, got)
	}
}

func TestNoTmuxWrappingOutsideTmux(t *testing.T) {
	t.Setenv("TMUX", "")
	if got := KittySequence(Image{PNG: []byte("x")}); strings.HasPrefix(got, "\x1bPtmux") {
		t.Errorf("wrapped for tmux outside tmux: %q", got)
	}
}

func TestDeleteSequences(t *testing.T) {
	// Images outlive the frame that drew them, so a transcript that scrolls
	// without deleting leaves pictures floating over unrelated text.
	if got := DeleteSequence(3); !strings.Contains(got, "a=d") || !strings.Contains(got, "i=3") {
		t.Errorf("DeleteSequence = %q", got)
	}
	if got := DeleteSequence(0); got != "" {
		t.Errorf("DeleteSequence(0) = %q, want nothing to delete", got)
	}
	if got := DeleteAllSequence(); !strings.Contains(got, "d=A") {
		t.Errorf("DeleteAllSequence = %q", got)
	}
}

func TestDetectReadsTheEnvironment(t *testing.T) {
	cases := []struct {
		name string
		env  map[string]string
		want Protocol
	}{
		{"kitty by window id", map[string]string{"KITTY_WINDOW_ID": "1"}, ProtoKitty},
		{"kitty by TERM", map[string]string{"TERM": "xterm-kitty"}, ProtoKitty},
		{"ghostty", map[string]string{"GHOSTTY_RESOURCES_DIR": "/x"}, ProtoKitty},
		{"wezterm", map[string]string{"TERM_PROGRAM": "WezTerm"}, ProtoKitty},
		{"plain xterm", map[string]string{"TERM": "xterm-256color"}, ProtoNone},
		{"override", map[string]string{"EVILCODE_GRAPHICS": "none", "KITTY_WINDOW_ID": "1"}, ProtoNone},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			for _, key := range []string{"EVILCODE_GRAPHICS", "KITTY_WINDOW_ID",
				"GHOSTTY_RESOURCES_DIR", "TERM", "TERM_PROGRAM", "COLORTERM"} {
				t.Setenv(key, "")
			}
			for k, v := range c.env {
				t.Setenv(k, v)
			}
			if got := Detect(); got != c.want {
				t.Errorf("Detect() = %q, want %q", got, c.want)
			}
		})
	}
}

func TestPlaceholderExplainsAMissingProtocol(t *testing.T) {
	// "An image was here" with no explanation reads as a rendering bug rather
	// than a capability the terminal lacks.
	got := Placeholder("diagram.png", ProtoNone)
	if !strings.Contains(got, "diagram.png") {
		t.Errorf("placeholder does not name the file: %q", got)
	}
	if !strings.Contains(got, "kitty") {
		t.Errorf("placeholder does not say what would work: %q", got)
	}
}

func TestSixelCommandScalesToTheCellBox(t *testing.T) {
	got := SixelCommand(40, 20)
	if got[0] != "img2sixel" {
		t.Errorf("command = %v, want img2sixel", got)
	}
	if !strings.Contains(strings.Join(got, " "), "--width=320") {
		t.Errorf("command = %v, want a pixel width derived from the columns", got)
	}
}

func TestImageSequenceUsesSixelEncoder(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "img2sixel")
	if err := os.WriteFile(path, []byte("#!/bin/sh\nprintf '\\033Pqfake\\033\\\\'\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir)
	t.Setenv("TMUX", "")
	got := ImageSequence(ProtoSixel, Image{PNG: []byte("png"), Cols: 4})
	if got != "\x1bPqfake\x1b\\" {
		t.Errorf("sixel sequence = %q, want encoder output", got)
	}
}

// ToPNG re-encodes any decodable format as PNG for the kitty protocol, and
// rejects an image whose pixel count would balloon past the cap on decode.
func TestToPNGConvertsAndBoundsPixels(t *testing.T) {
	// A 3×2 PNG round-trips through ToPNG as PNG bytes.
	var pngBuf bytes.Buffer
	small := image.NewRGBA(image.Rect(0, 0, 3, 2))
	small.Set(0, 0, color.RGBA{1, 2, 3, 255})
	if err := png.Encode(&pngBuf, small); err != nil {
		t.Fatal(err)
	}
	if out, ok := ToPNG(pngBuf.Bytes()); !ok || len(out) == 0 {
		t.Fatalf("ToPNG(png) = %v ok=%v, want re-encoded PNG bytes", out, ok)
	}

	// A JPEG decodes and re-encodes as PNG.
	var jpgBuf bytes.Buffer
	if err := jpeg.Encode(&jpgBuf, small, &jpeg.Options{Quality: 80}); err != nil {
		t.Fatal(err)
	}
	if out, ok := ToPNG(jpgBuf.Bytes()); !ok || len(out) == 0 {
		t.Fatalf("ToPNG(jpeg) = %v ok=%v, want PNG bytes", out, ok)
	}

	// BMP is one of the read tool's accepted image formats too.
	var bmpBuf bytes.Buffer
	if err := bmp.Encode(&bmpBuf, small); err != nil {
		t.Fatal(err)
	}
	if out, ok := ToPNG(bmpBuf.Bytes()); !ok || len(out) == 0 {
		t.Fatalf("ToPNG(bmp) = %v ok=%v, want PNG bytes", out, ok)
	}

	// An image past the pixel cap is refused without decoding the bitmap.
	huge := image.NewRGBA(image.Rect(0, 0, 5000, 5000)) // 25M px > 16M cap.
	var hugeBuf bytes.Buffer
	if err := png.Encode(&hugeBuf, huge); err != nil {
		t.Fatal(err)
	}
	if _, ok := ToPNG(hugeBuf.Bytes()); ok {
		t.Error("ToPNG accepted a 25M-px image; it must reject past the cap")
	}
}

// Dimensions parses PNG, JPEG and GIF headers and reports unknown for the rest.
func TestDimensionsParsesKnownFormats(t *testing.T) {
	var buf bytes.Buffer
	if err := png.Encode(&buf, image.NewRGBA(image.Rect(0, 0, 7, 4))); err != nil {
		t.Fatal(err)
	}
	if w, h, ok := Dimensions(buf.Bytes()); !ok || w != 7 || h != 4 {
		t.Errorf("Dimensions(png) = %d %d %v, want 7 4 true", w, h, ok)
	}
	if _, _, ok := Dimensions([]byte("not an image")); ok {
		t.Error("Dimensions accepted non-image bytes")
	}
}
