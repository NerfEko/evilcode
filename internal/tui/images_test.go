package tui

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"evilcode/internal/graphics"
)

func TestLoadImageRefusesSomethingTooLargeToSend(t *testing.T) {
	// A large image is not slow to encode, it is slow to transmit: base64 over
	// a pty at a few megabytes stalls the render loop with no way to interrupt.
	dir := t.TempDir()
	path := filepath.Join(dir, "huge.png")
	if err := os.WriteFile(path, make([]byte, MaxImageBytes+1), 0o644); err != nil {
		t.Fatal(err)
	}

	got, err := LoadImage(path, 20, 10)
	if err == nil {
		t.Fatal("an oversized image was accepted")
	}
	if !strings.Contains(err.Error(), "huge.png") {
		t.Errorf("err = %v, want it to name the file", err)
	}
	// The block still comes back so the transcript can show a placeholder
	// rather than nothing at all.
	if got.Path != path {
		t.Errorf("block = %+v, want the path preserved for the placeholder", got)
	}
}

func TestLoadImageReadsASmallFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "small.png")
	if err := os.WriteFile(path, []byte("\x89PNG\r\n\x1a\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	got, err := LoadImage(path, 20, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.PNG) == 0 || got.Cols != 20 || got.Rows != 10 {
		t.Errorf("block = %+v", got)
	}
}

func TestImagePlaceholderExplainsATerminalWithNoSupport(t *testing.T) {
	r := testRenderer(80)
	rows := plainLines(r.RenderImagePlaceholder(
		ImageBlock{Path: "/tmp/diagram.png"}, graphics.ProtoNone, true))
	if len(rows) != 1 {
		t.Fatalf("rows = %v, want one placeholder line", rows)
	}
	if !strings.Contains(rows[0], "diagram.png") || !strings.Contains(rows[0], "kitty") {
		t.Errorf("placeholder = %q", rows[0])
	}
}

func TestImageReservesItsRowsWhenDrawn(t *testing.T) {
	// The picture is painted over the frame, so the block still has to occupy
	// its rows — without them the transcript below would sit under the image
	// rather than after it.
	r := testRenderer(80)
	rows := r.RenderImagePlaceholder(
		ImageBlock{Path: "/tmp/d.png", PNG: []byte("x"), Cols: 20, Rows: 8},
		graphics.ProtoKitty, true)
	if len(rows) != 8 {
		t.Errorf("reserved %d rows, want 8", len(rows))
	}
	if !strings.Contains(plain(rows[7]), "d.png") {
		t.Errorf("last row = %q, want the caption", plain(rows[7]))
	}
}

func TestImageGraphicsPositionsAndCachesVisibleImage(t *testing.T) {
	m := NewModel(nil, HeaderState{SessionName: "s", Model: "m"})
	m.width, m.height = 80, 30
	m.graphics, m.imagesOn = graphics.ProtoKitty, true
	m.renderer.Graphics, m.renderer.ImagesOn = m.graphics, m.imagesOn
	m.blocks = []Block{{Kind: BlockImage, Image: ImageBlock{
		Path: "photo.png", PNG: []byte("png"), Cols: 20, Rows: 2, ID: 7,
	}}}
	tr := m.transcriptLines()
	got := m.imageGraphics(tr, 0, len(tr.Lines), 20, len(tr.Lines))
	imageLine := -1
	for line, owner := range tr.Owner {
		if owner == 0 {
			imageLine = line
			break
		}
	}
	if imageLine < 0 {
		t.Fatal("transcript did not contain the image block")
	}
	if !strings.Contains(got, graphics.CursorPosition(imageLine+1, 2)) {
		t.Errorf("graphics = %q, want a cursor move to the image block", got)
	}
	if !strings.Contains(got, "i=7") {
		t.Errorf("graphics = %q, want image id 7", got)
	}
	if again := m.imageGraphics(tr, 0, len(tr.Lines), 20, len(tr.Lines)); again != "" {
		t.Errorf("cached image was retransmitted: %q", again)
	}
}

func TestImagesOffFallsBackToThePlaceholder(t *testing.T) {
	r := testRenderer(80)
	rows := r.RenderImagePlaceholder(
		ImageBlock{Path: "/tmp/d.png", PNG: []byte("x"), Rows: 8},
		graphics.ProtoKitty, false)
	if len(rows) != 1 {
		t.Errorf("rows = %d with images off, want the one-line placeholder", len(rows))
	}
}

func TestToggleImagesSaysSoOnATerminalWithout(t *testing.T) {
	m := &Model{graphics: graphics.ProtoNone}
	m.toggleImages()
	if m.imagesOn {
		t.Error("images were turned on for a terminal that shows none")
	}
	if !strings.Contains(m.notice, "no images") {
		t.Errorf("notice = %q", m.notice)
	}
}

func TestToggleImagesClearsWhatIsOnScreen(t *testing.T) {
	// Leaving them means pictures floating over text right after the toggle
	// said images were off.
	m := &Model{graphics: graphics.ProtoKitty, imagesOn: true}
	m.toggleImages()
	if m.imagesOn {
		t.Error("the toggle did not turn images off")
	}
	if !strings.Contains(m.pendingImages, "d=A") {
		t.Errorf("pending = %q, want a delete-all sequence", m.pendingImages)
	}
}

func TestMermaidSourceFallbackShowsTheSource(t *testing.T) {
	// When a diagram cannot be drawn the source is shown styled rather than an
	// error: the diagram text is still the most useful thing on offer.
	r := testRenderer(80)
	joined := strings.Join(plainLines(r.RenderMermaidSource("graph TD;\n A-->B;")), "\n")
	if !strings.Contains(joined, "A-->B") {
		t.Errorf("the source was not shown:\n%s", joined)
	}
	if !strings.Contains(joined, "↻ mermaid") {
		t.Errorf("the fallback does not explain itself:\n%s", joined)
	}
}

func TestMermaidHintNamesTheActualObstacle(t *testing.T) {
	// Two different problems need two different answers — install mmdc, or use
	// a terminal that shows images. One message blaming mmdc sends someone who
	// already has it to reinstall it.
	//
	// Asserted against the function rather than the rendered frame, because the
	// frame's answer depends on whether this machine happens to have mmdc.
	if got := MermaidHint(graphics.ProtoNone, true); !strings.Contains(got, "terminal") {
		if MermaidAvailable() {
			t.Errorf("hint = %q, want it to blame the terminal", got)
		}
	}
	if got := MermaidHint(graphics.ProtoKitty, false); MermaidAvailable() &&
		!strings.Contains(got, "Alt+Shift+I") {
		t.Errorf("hint = %q, want it to name the toggle", got)
	}
	if !MermaidAvailable() {
		if got := MermaidHint(graphics.ProtoKitty, true); !strings.Contains(got, MermaidCommand) {
			t.Errorf("hint = %q, want it to name the missing renderer", got)
		}
	}
}

func TestRenderMermaidWithoutMmdcIsAnError(t *testing.T) {
	if MermaidAvailable() {
		t.Skip("mmdc is installed; the absent path cannot be exercised")
	}
	if _, err := RenderMermaid(t.TempDir(), "graph TD; A-->B;"); err == nil {
		t.Error("rendering succeeded with no renderer installed")
	}
}

func TestHumanBytes(t *testing.T) {
	cases := map[int]string{
		512:           "512B",
		2048:          "2K",
		5 << 20:       "5.0M",
		MaxImageBytes: "4.0M",
	}
	for in, want := range cases {
		if got := humanBytes(in); got != want {
			t.Errorf("humanBytes(%d) = %q, want %q", in, got, want)
		}
	}
}

func TestMermaidCacheKeyDependsOnSource(t *testing.T) {
	// mmdc starts a headless browser, so an unchanged diagram must not be
	// re-rendered — which only works if the key is the source.
	if hashSource("graph TD; A-->B;") == hashSource("graph TD; A-->C;") {
		t.Error("two different diagrams share a cache key")
	}
	if hashSource("x") != hashSource("x") {
		t.Error("the same diagram hashed differently twice")
	}
}
