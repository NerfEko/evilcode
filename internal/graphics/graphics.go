// Package graphics puts images in the terminal (plan.md Phase 5).
//
// Three tiers, in order of preference: the kitty graphics protocol, sixel
// through libsixel's img2sixel, and a text placeholder. Kitty is a base64
// payload in an escape sequence; sixel shells out to the encoder that already
// exists rather than being reimplemented (plan.md §17).
package graphics

import (
	"bytes"
	"encoding/base64"
	"fmt"
	"image"
	_ "image/gif"
	_ "image/jpeg"
	"image/png"
	"os"
	"os/exec"
	"strings"

	_ "golang.org/x/image/bmp"
	_ "golang.org/x/image/webp"
)

// Protocol is how images reach this terminal.
type Protocol string

const (
	// ProtoKitty is the kitty graphics protocol, also spoken by ghostty and
	// WezTerm.
	ProtoKitty Protocol = "kitty"

	// ProtoSixel goes through img2sixel.
	ProtoSixel Protocol = "sixel"

	// ProtoNone means images render as a placeholder.
	ProtoNone Protocol = "none"
)

// Detect reports which protocol this terminal supports.
//
// Environment sniffing rather than a terminal query: a query means writing an
// escape sequence and waiting for a reply on stdin, which fights with Bubble
// Tea for the input stream and hangs on a terminal that answers nothing. The
// environment is what every other tool uses and is wrong far less often than a
// hung startup would be annoying.
func Detect() Protocol {
	if os.Getenv("EVILCODE_GRAPHICS") != "" {
		return Protocol(os.Getenv("EVILCODE_GRAPHICS"))
	}
	if os.Getenv("KITTY_WINDOW_ID") != "" || os.Getenv("GHOSTTY_RESOURCES_DIR") != "" {
		return ProtoKitty
	}
	term := os.Getenv("TERM")
	switch {
	case strings.Contains(term, "kitty"), strings.Contains(term, "ghostty"):
		return ProtoKitty
	}
	if os.Getenv("TERM_PROGRAM") == "WezTerm" {
		return ProtoKitty
	}
	// foot, mlterm, xterm -ti vt340 and friends speak sixel, and img2sixel is
	// the encoder. Without the binary there is nothing to fall back to.
	if strings.Contains(term, "foot") || strings.Contains(term, "mlterm") ||
		os.Getenv("COLORTERM") == "sixel" {
		if _, err := exec.LookPath("img2sixel"); err == nil {
			return ProtoSixel
		}
	}
	return ProtoNone
}

// InTmux reports whether output has to be wrapped for tmux passthrough.
func InTmux() bool { return os.Getenv("TMUX") != "" }

// ChunkSize is the kitty protocol's payload limit per escape sequence.
//
// The protocol caps a single sequence's base64 payload at 4096 bytes; longer
// images are sent as chunks with m=1 on all but the last. Exceeding it does not
// error, it corrupts — the terminal reads a truncated image and draws garbage.
const ChunkSize = 4096

// CursorPosition moves the terminal cursor to a 1-based cell coordinate. Image
// protocols draw at the current cursor, so callers use this immediately before
// an image sequence instead of letting the payload land after the whole frame.
func CursorPosition(row, col int) string {
	if row < 1 {
		row = 1
	}
	if col < 1 {
		col = 1
	}
	return fmt.Sprintf("\x1b[%d;%dH", row, col)
}

// Image is a decoded picture ready to place.
type Image struct {
	// PNG is the encoded file. The kitty protocol takes PNG directly (f=100),
	// which saves decoding it here only to re-encode it as raw RGB.
	PNG []byte

	// Cols and Rows are the cell box to scale into. Zero means the terminal's
	// own choice, which is the image's natural size.
	Cols, Rows int

	// ID lets a later sequence delete or replace this image. Zero means the
	// terminal assigns one and the image cannot be addressed again.
	ID int
}

// KittySequence renders an image as kitty graphics escape sequences.
//
// The returned string is written to the terminal as-is. It contains no
// printable characters, so it occupies no cells in the frame — placement is by
// cursor position, which is why the caller has to put the cursor first.
func KittySequence(img Image) string {
	payload := base64.StdEncoding.EncodeToString(img.PNG)

	var b strings.Builder
	first := true
	for len(payload) > 0 {
		chunk := payload
		if len(chunk) > ChunkSize {
			chunk = chunk[:ChunkSize]
		}
		payload = payload[len(chunk):]

		var keys []string
		if first {
			// a=T places immediately, f=100 says the payload is a PNG file.
			keys = append(keys, "a=T", "f=100")
			if img.Cols > 0 {
				keys = append(keys, fmt.Sprintf("c=%d", img.Cols))
			}
			if img.Rows > 0 {
				keys = append(keys, fmt.Sprintf("r=%d", img.Rows))
			}
			if img.ID > 0 {
				keys = append(keys, fmt.Sprintf("i=%d", img.ID))
			}
			first = false
		}
		// m=1 means "more chunks follow"; the last chunk carries m=0.
		if len(payload) > 0 {
			keys = append(keys, "m=1")
		} else {
			keys = append(keys, "m=0")
		}
		b.WriteString(wrap("\x1b_G" + strings.Join(keys, ",") + ";" + chunk + "\x1b\\"))
	}
	return b.String()
}

// ImageSequence renders an image using the protocol selected for the terminal.
// Kitty is encoded directly; sixel uses the installed img2sixel encoder and
// reads the PNG from stdin, so the application does not carry a second image
// encoder or write a temporary file for every transcript repaint.
func ImageSequence(proto Protocol, img Image) string {
	switch proto {
	case ProtoKitty:
		return KittySequence(img)
	case ProtoSixel:
		cmdline := SixelCommand(img.Cols, img.Rows)
		if len(cmdline) == 0 {
			return ""
		}
		cmd := exec.Command(cmdline[0], cmdline[1:]...)
		cmd.Stdin = bytes.NewReader(img.PNG)
		out, err := cmd.Output()
		if err != nil {
			return ""
		}
		return wrap(string(out))
	default:
		return ""
	}
}

// DeleteSequence removes a previously placed image by id.
//
// Images outlive the frame that drew them: the terminal keeps them until told
// otherwise, so a transcript that scrolls without deleting leaves pictures
// floating over unrelated text.
func DeleteSequence(id int) string {
	if id <= 0 {
		return ""
	}
	return wrap(fmt.Sprintf("\x1b_Ga=d,d=i,i=%d\x1b\\", id))
}

// DeleteAllSequence clears every image this program placed.
func DeleteAllSequence() string { return wrap("\x1b_Ga=d,d=A\x1b\\") }

// wrap makes an escape sequence survive tmux.
//
// tmux swallows anything it does not understand unless it is wrapped in its
// passthrough sequence, and the inner ESC has to be doubled. It also needs
// `allow-passthrough on`, which is the user's setting to make — `/terminal-setup`
// says so rather than this silently doing nothing.
func wrap(seq string) string {
	if !InTmux() {
		return seq
	}
	return "\x1bPtmux;" + strings.ReplaceAll(seq, "\x1b", "\x1b\x1b") + "\x1b\\"
}

// SixelCommand is the command that converts a PNG to sixel.
//
// Never write an encoder (plan.md §17): libsixel is installed on every system
// that can display sixel anyway, and its output is correct on terminals whose
// palette handling is not.
func SixelCommand(cols, rows int) []string {
	args := []string{"img2sixel"}
	if cols > 0 {
		// Cell geometry is not knowable here, so width is given in pixels using
		// a conservative cell size. Overshooting scrolls the image; undershooting
		// merely wastes space, so undershoot.
		args = append(args, fmt.Sprintf("--width=%d", cols*8))
	}
	return args
}

// Dimensions reads an image's width and height from its header without decoding
// the whole picture. The standard library plus x/image decoders cover every
// image extension accepted by the read tool.
func Dimensions(data []byte) (w, h int, ok bool) {
	cfg, _, err := image.DecodeConfig(bytes.NewReader(data))
	if err != nil {
		return 0, 0, false
	}
	if cfg.Width <= 0 || cfg.Height <= 0 {
		return 0, 0, false
	}
	return cfg.Width, cfg.Height, true
}

// ToPNG re-encodes an image as PNG. The kitty graphics protocol's `f=100`
// declares PNG data, so a JPEG, GIF, WebP or BMP read off disk has to be
// converted before it is transmitted or a kitty-compatible terminal rejects it.
//
// A compressed image under the terminal transmit cap can still decode to
// hundreds of megabytes of pixels, so the decoded dimensions are bounded first
// and anything past the cap returns ok=false rather than allocating the bitmap.
func ToPNG(data []byte) ([]byte, bool) {
	cfg, _, err := image.DecodeConfig(bytes.NewReader(data))
	if err != nil || cfg.Width <= 0 || cfg.Height <= 0 {
		return nil, false
	}
	const maxPixels = 16 << 20 // 16M px ≈ 64 MB RGBA, the upper bound on decode.
	if int64(cfg.Width) > maxPixels/int64(cfg.Height) {
		return nil, false
	}
	img, _, err := image.Decode(bytes.NewReader(data))
	if err != nil {
		return nil, false
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		return nil, false
	}
	return buf.Bytes(), true
}

// Placeholder is what a terminal with no image support shows.
//
// It names the file and says what is missing, because "an image was here" with
// no explanation reads as a rendering bug rather than a capability the terminal
// lacks.
func Placeholder(name string, proto Protocol) string {
	switch proto {
	case ProtoNone:
		return fmt.Sprintf("🖼 %s — this terminal shows no images "+
			"(kitty, ghostty, WezTerm, or foot with img2sixel do)", name)
	default:
		return fmt.Sprintf("🖼 %s", name)
	}
}
