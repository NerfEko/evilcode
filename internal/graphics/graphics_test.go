package graphics

import (
	"encoding/base64"
	"strings"
	"testing"
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
