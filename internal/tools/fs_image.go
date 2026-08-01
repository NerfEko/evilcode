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
func (f *FS) readImage(full, rel string) (Result, error) {
	info, err := os.Stat(full)
	if err != nil {
		return Result{}, err
	}
	size := info.Size()

	var data []byte
	if size <= int64(visionImageCeiling) {
		data, err = f.readConfined(full)
		if err != nil {
			return Result{}, err
		}
	} else {
		// Over the ceiling the bytes are not attached, but the dimensions are
		// still worth reporting. image.DecodeConfig reads only the header, so
		// pulling it from the file costs a few kilobytes rather than the whole
		// picture.
		data, _ = f.readHead(full, 64<<10)
	}

	w, h, dimsOK := graphics.Dimensions(data)
	dimStr := "unknown"
	if dimsOK {
		dimStr = fmt.Sprintf("%dx%d", w, h)
	}
	sizeStr := humanBytes(size)

	res := Result{Intent: fmt.Sprintf("reading %s", rel)}
	if size <= int64(visionImageCeiling) {
		res.Output = fmt.Sprintf(
			"Image: %s (%s)\nDimensions: %s\nImage sent to model for vision analysis.",
			rel, sizeStr, dimStr)
		res.Images = [][]byte{data}
		return res, nil
	}
	res.Output = fmt.Sprintf(
		"Image: %s (%s)\nDimensions: %s\nImage too large for vision (max %s) — not attached.",
		rel, sizeStr, dimStr, humanBytes(int64(visionImageCeiling)))
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