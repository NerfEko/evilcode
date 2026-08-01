package tools

import (
	"bytes"
	"image"
	"image/color"
	"image/png"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// tinyPNG is a 3×2 PNG, the smallest picture readImage can attach.
func tinyPNG(t *testing.T) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, 3, 2))
	img.Set(0, 0, color.RGBA{255, 0, 0, 255})
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

// J1.1: reading an image attaches the bytes to the result for the vision path
// and reports dimensions and size, rather than refusing it as binary.
func TestReadImageAttachesBytesAndDimensions(t *testing.T) {
	png := tinyPNG(t)
	f := tempFS(t, nil).WithVision(true)
	full := filepath.Join(f.Root, "pic.png")
	if err := os.WriteFile(full, png, 0o644); err != nil {
		t.Fatal(err)
	}

	res, err := run(t, f.Tools(), "read", map[string]any{"path": "pic.png"})
	if err != nil {
		t.Fatalf("reading an image errored: %v", err)
	}
	if len(res.Images) != 1 || !bytes.Equal(res.Images[0], png) {
		t.Fatalf("result.Images = %v, want the file's bytes attached", res.Images)
	}
	if !strings.Contains(res.Output, "Dimensions: 3x2") {
		t.Errorf("output = %q, want the 3x2 dimensions", res.Output)
	}
	if !strings.Contains(res.Output, "sent to model for vision analysis") {
		t.Errorf("output = %q, want it to say the image was sent to the model", res.Output)
	}
}

// A JPEG by extension is treated the same as PNG: extension-keyed, so a model
// asking for photo.jpg gets the vision path even though the bytes are opaque.
func TestReadImageKeyedByExtensionNotContent(t *testing.T) {
	f := tempFS(t, nil).WithVision(true)
	// Not a real JPEG; the point is the extension routes it past isBinary.
	if err := os.WriteFile(filepath.Join(f.Root, "photo.jpg"),
		[]byte{0xff, 0xd8, 0xff, 0xe0, 0, 0x10, 'J', 'F'}, 0o644); err != nil {
		t.Fatal(err)
	}
	res, err := run(t, f.Tools(), "read", map[string]any{"path": "photo.jpg"})
	if err != nil {
		t.Fatalf("reading a .jpg by extension errored: %v", err)
	}
	if len(res.Images) != 1 {
		t.Errorf("a .jpg should attach bytes for the vision path; got %d images", len(res.Images))
	}
}

// An image over the vision ceiling is not attached: the model is told the
// dimensions and size instead, because a model that cannot see the picture
// must be told that rather than handed nothing.
func TestReadImageOverCeilingIsNotAttached(t *testing.T) {
	f := tempFS(t, nil)
	full := filepath.Join(f.Root, "big.png")
	// A real PNG header so Dimensions parses, padded well past the ceiling.
	head := tinyPNG(t)
	if err := os.WriteFile(full, append(head, make([]byte, visionImageCeiling+1)...), 0o644); err != nil {
		t.Fatal(err)
	}

	res, err := run(t, f.Tools(), "read", map[string]any{"path": "big.png"})
	if err != nil {
		t.Fatalf("over-ceiling read errored: %v", err)
	}
	if len(res.Images) != 0 {
		t.Errorf("over-ceiling image was attached (%d images); it must not be", len(res.Images))
	}
	if !strings.Contains(res.Output, "too large for vision") {
		t.Errorf("output = %q, want it to say the image is too large", res.Output)
	}
	if !strings.Contains(res.Output, "Dimensions:") {
		t.Errorf("output = %q, want dimensions reported even over the ceiling", res.Output)
	}
}

// A model without vision is not handed image bytes it would reject. It is told
// the dimensions and that it cannot see the picture, mirroring the
// user-attachment guard.
func TestReadImageWithoutVisionIsNotAttached(t *testing.T) {
	png := tinyPNG(t)
	f := tempFS(t, nil) // Vision defaults to false.
	full := filepath.Join(f.Root, "pic.png")
	if err := os.WriteFile(full, png, 0o644); err != nil {
		t.Fatal(err)
	}

	res, err := run(t, f.Tools(), "read", map[string]any{"path": "pic.png"})
	if err != nil {
		t.Fatalf("reading an image errored: %v", err)
	}
	if len(res.Images) != 0 {
		t.Errorf("a non-vision model was handed %d image(s); it must not be", len(res.Images))
	}
	if !strings.Contains(res.Output, "cannot see images") {
		t.Errorf("output = %q, want it to say the model cannot see images", res.Output)
	}
	if !strings.Contains(res.Output, "Dimensions: 3x2") {
		t.Errorf("output = %q, want dimensions reported even without vision", res.Output)
	}
}
func TestReadBinaryRefusalSaysWhatToDo(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "bin"),
		[]byte{0x7f, 'E', 'L', 'F', 0, 0, 0, 0}, 0o644); err != nil {
		t.Fatal(err)
	}
	f := NewFS(dir)
	_, err := run(t, f.Tools(), "read", map[string]any{"path": "bin"})
	if err == nil {
		t.Fatal("want an error for a binary file")
	}
	if !strings.Contains(err.Error(), "image") || !strings.Contains(err.Error(), ".png") {
		t.Errorf("error = %q, want it to point at the image extensions as the way out", err)
	}
}