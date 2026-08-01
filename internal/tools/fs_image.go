package tools

import (
	"fmt"
	"io"
	"os"
	"strings"

	"evilcode/internal/graphics"
)

// imageExts are the extensions `read` treats as images rather than text or
// binary. The set matches jcode's `is_image_file` plus the plan's §1.1 list;
// anything not here falls through to the binary refusal.
var imageExts = map[string]bool{
	"png": true, "jpg": true, "jpeg": true, "gif": true,
	"webp": true, "bmp": true,
}

// isImageExt reports whether a path names an image `read` should attach to the
// model's vision path.
func isImageExt(path string) bool {
	if i := strings.LastIndexByte(path, '.'); i >= 0 {
		return imageExts[strings.ToLower(path[i+1:])]
	}
	return false
}

// visionImageCeiling is the largest image sent to a vision model. jcode uses
// 20 MB; the session store's blob cap is raised to match so an image this size
// survives a resume. Above it the bytes are not attached — the model is told
// the dimensions and size instead, because a model that cannot see the picture
// must be told that rather than handed nothing.
const visionImageCeiling = 20 << 20

// readImage handles an image file: attach the bytes to the result for the
// vision path and report dimensions + size, or — over the ceiling — report
// them without attaching.
//
// Extension-keyed rather than content-keyed: a model asks for `photo.png` by
// name, and sniffing magic bytes for a `.png` that happens to start with a NUL
// would route it to the binary refusal. The binary check stays for everything
// without a known image extension.
//
// When the active model cannot see images (Vision is false) the bytes are not
// attached: the result reports the dimensions and size and says the model
// cannot see the picture, because a text-only backend handed image bytes will
// reject the request, and a model that cannot see an image must be told that
// rather than handed nothing.
func (f *FS) readImage(full, rel string) (Result, error) {
	info, err := os.Stat(full)
	if err != nil {
		return Result{}, err
	}
	size := info.Size()

	// Read through the confined descriptor with a hard cap one byte past the
	// ceiling, so a file that grows or is replaced after Stat cannot push past
	// the limit while the stale size still selects the attach branch. The
	// header needed for dimensions is always within this many bytes.
	data, err := f.readHead(full, visionImageCeiling+1)
	if err != nil {
		return Result{}, err
	}

	w, h, dimsOK := graphics.Dimensions(data)
	dimStr := "unknown"
	if dimsOK {
		dimStr = fmt.Sprintf("%dx%d", w, h)
	}
	sizeStr := humanBytes(size)

	res := Result{Intent: fmt.Sprintf("reading %s", rel)}
	switch {
	case len(data) > visionImageCeiling:
		res.Output = fmt.Sprintf(
			"Image: %s (%s)\nDimensions: %s\nImage too large for vision (max %s) — not attached.",
			rel, sizeStr, dimStr, humanBytes(int64(visionImageCeiling)))
	case !f.visionOK():
		res.Output = fmt.Sprintf(
			"Image: %s (%s)\nDimensions: %s\nThis model cannot see images (vision is off); "+
				"switch to a vision model with /model to attach it.",
			rel, sizeStr, dimStr)
	default:
		res.Output = fmt.Sprintf(
			"Image: %s (%s)\nDimensions: %s\nImage sent to model for vision analysis.",
			rel, sizeStr, dimStr)
		res.Images = [][]byte{data}
	}
	return res, nil
}

// readHead reads the first n bytes of a file through the confined open, for the
// case where only a header is needed. A short read is not an error.
func (f *FS) readHead(full string, n int) ([]byte, error) {
	file, err := f.openConfined(full)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	return io.ReadAll(io.LimitReader(file, int64(n)))
}