package tui

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	tea "charm.land/bubbletea/v2"

	"evilcode/internal/graphics"
	"evilcode/internal/theme"
)

// ImageBlock is one picture in the transcript.
//
// The escape sequence is not part of the rendered rows: it carries no printable
// cells, and putting it in a row would make every width calculation in the
// layout wrong. It is emitted after the frame, positioned by cursor moves.
type ImageBlock struct {
	// Path is the file on disk, which is what the placeholder names.
	Path string

	// PNG is the encoded image.
	PNG []byte

	// Cols and Rows are the cell box it was scaled into.
	Cols, Rows int

	// ID addresses the image for deletion. Images outlive the frame that drew
	// them, so every one that scrolls away has to be deleted by id.
	ID int
}

// MaxImageBytes caps what will be sent to the terminal.
//
// A large image is not slow to encode, it is slow to *transmit*: base64 over a
// pty at a few megabytes stalls the render loop for seconds with no way to
// interrupt it. Anything bigger renders as a placeholder naming the size.
const MaxImageBytes = 4 << 20

// WithGraphics turns images on, given the protocol the terminal speaks and the
// directory rendered diagrams are cached in.
func (m *Model) WithGraphics(proto graphics.Protocol, cacheDir string) *Model {
	m.graphics = proto
	m.imagesOn = proto != graphics.ProtoNone
	m.diagramDir = cacheDir
	return m
}

// toggleImages implements Alt+Shift+I.
//
// Off is a real state rather than a no-op: images make a transcript slow to
// scroll on a remote terminal, and the whole point of the toggle is to get out
// of that without restarting.
func (m *Model) toggleImages() tea.Cmd {
	if m.graphics == graphics.ProtoNone {
		m.notice = "this terminal shows no images (kitty, ghostty, WezTerm, or foot with img2sixel do)"
		return nil
	}
	m.imagesOn = !m.imagesOn
	if m.imagesOn {
		m.notice = "🖼 Images ON · " + string(m.graphics)
	} else {
		m.notice = "🖼 Images OFF · placeholders only"
		// Whatever is on screen has to go now: leaving it means pictures
		// floating over text after the toggle said they were off.
		m.pendingImages = graphics.DeleteAllSequence()
	}
	return nil
}

// LoadImage reads a picture for the transcript, scaling it into a cell box.
func LoadImage(path string, cols, rows int) (ImageBlock, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return ImageBlock{}, err
	}
	if len(data) > MaxImageBytes {
		return ImageBlock{Path: path}, fmt.Errorf(
			"%s is %s, over the %s this will send to a terminal",
			filepath.Base(path), humanBytes(len(data)), humanBytes(MaxImageBytes))
	}
	return ImageBlock{Path: path, PNG: data, Cols: cols, Rows: rows}, nil
}

// humanBytes renders a size the way a person reads it.
func humanBytes(n int) string {
	switch {
	case n < 1<<10:
		return fmt.Sprintf("%dB", n)
	case n < 1<<20:
		return fmt.Sprintf("%.0fK", float64(n)/(1<<10))
	default:
		return fmt.Sprintf("%.1fM", float64(n)/(1<<20))
	}
}

// RenderImagePlaceholder is the text an image block occupies in the frame.
//
// Even when the image is drawn, the block still reserves its rows: the picture
// is painted over them, and without them the transcript below would be under
// the image rather than after it.
func (r *Renderer) RenderImagePlaceholder(b ImageBlock, proto graphics.Protocol, on bool) []string {
	label := filepath.Base(b.Path)
	if !on || proto == graphics.ProtoNone || len(b.PNG) == 0 {
		return []string{r.style(theme.RoleDim).Render(graphics.Placeholder(label, proto))}
	}

	rows := max(b.Rows, 1)
	out := make([]string, rows)
	for i := range out {
		out[i] = ""
	}
	// The caption rides the last row so the picture is identifiable when
	// several are on screen.
	out[rows-1] = r.style(theme.RoleDim).Render("🖼 " + label)
	return out
}

// MermaidCommand is the renderer mermaid diagrams shell out to.
//
// Never write a renderer (plan.md §17, and the task says so twice). mmdc is the
// reference implementation; without it the source is shown styled, which is
// still more useful than an error.
const MermaidCommand = "mmdc"

// MermaidAvailable reports whether diagrams can be rendered.
func MermaidAvailable() bool {
	_, err := exec.LookPath(MermaidCommand)
	return err == nil
}

// RenderMermaidSource is the fallback when a diagram cannot be drawn: the
// source, styled as code, with a line saying what would render it.
//
// The line names the actual obstacle. There are two, and they need different
// answers — install mmdc, or use a terminal that shows images — so a single
// message blaming mmdc sends someone who already has it to reinstall it.
func (r *Renderer) RenderMermaidSource(source string) []string {
	out := r.renderCodeBlock(Segment{Code: true, Lang: "mermaid", Text: source})
	return append(out, r.style(theme.RoleDim).Render(MermaidHint(r.Graphics, r.ImagesOn)))
}

// MermaidHint explains why a diagram is showing as source.
func MermaidHint(proto graphics.Protocol, imagesOn bool) string {
	switch {
	case !MermaidAvailable():
		return "↻ mermaid (render requires " + MermaidCommand + ")"
	case proto == graphics.ProtoNone:
		return "↻ mermaid (this terminal shows no images — kitty, ghostty, " +
			"WezTerm, or foot with img2sixel do)"
	case !imagesOn:
		return "↻ mermaid (images are off — Alt+Shift+I)"
	default:
		return "↻ mermaid (rendering…)"
	}
}

// RenderMermaid turns diagram source into a PNG via mmdc.
//
// The temp file is the interface mmdc offers; there is no stdin mode that also
// takes a theme. It is written under the session's own directory rather than a
// shared /tmp path, so two evilcode processes rendering at once cannot collide.
func RenderMermaid(dir, source string) (string, error) {
	if !MermaidAvailable() {
		return "", fmt.Errorf("%s is not installed", MermaidCommand)
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	in := filepath.Join(dir, fmt.Sprintf("mermaid-%d.mmd", hashSource(source)))
	out := strings.TrimSuffix(in, ".mmd") + ".png"

	// Rendering is deterministic in the source, so an unchanged diagram is not
	// re-rendered — mmdc starts a headless browser, which is not cheap.
	if _, err := os.Stat(out); err == nil {
		return out, nil
	}
	if err := os.WriteFile(in, []byte(source), 0o644); err != nil {
		return "", err
	}

	cmd := exec.Command(MermaidCommand, "-i", in, "-o", out, "-b", "transparent")
	if combined, err := cmd.CombinedOutput(); err != nil {
		return "", fmt.Errorf("%s: %w\n%s", MermaidCommand, err, combined)
	}
	return out, nil
}

// mermaidRendered carries a finished diagram from its goroutine into the render
// loop. Rendering shells out to a headless browser, so it cannot happen inside
// View — the frame would block for seconds.
type mermaidRendered struct {
	Source string
	Path   string
	Err    error
}

// renderDiagrams kicks off a render for every closed mermaid fence in a
// finished block that has not been rendered already.
//
// Only closed fences: a diagram still streaming is syntactically incomplete, and
// mmdc on incomplete input fails slowly and noisily.
func (m *Model) renderDiagrams(text string) {
	if m.graphics == graphics.ProtoNone || !m.imagesOn || !MermaidAvailable() {
		return
	}
	for _, seg := range SplitSegments(text) {
		if !seg.Code || seg.Lang != "mermaid" || seg.Open {
			continue
		}
		if m.diagrams == nil {
			m.diagrams = map[string]string{}
		}
		if _, done := m.diagrams[seg.Text]; done {
			continue
		}
		// Marked before the render starts, so a repaint between kickoff and
		// completion does not start a second headless browser.
		m.diagrams[seg.Text] = ""

		source, dir := seg.Text, m.diagramDir
		go func() {
			path, err := RenderMermaid(dir, source)
			m.finishDiagram(&mermaidRendered{Source: source, Path: path, Err: err})
		}()
	}
}

// finishDiagram hands a completed render to the render loop.
//
// Buffered and non-blocking: a render goroutine must not wedge on a UI that has
// stopped draining, and the queue is sized well past any plausible number of
// diagrams in one reply.
func (m *Model) finishDiagram(done *mermaidRendered) {
	m.diagramMu.Lock()
	if m.diagramInbox == nil {
		m.diagramInbox = make(chan *mermaidRendered, 64)
	}
	inbox := m.diagramInbox
	m.diagramMu.Unlock()

	select {
	case inbox <- done:
	default:
		// Full. Unmark the source so the next repaint can start it again,
		// rather than leaving it marked as started forever.
		m.diagramMu.Lock()
		delete(m.diagrams, done.Source)
		m.diagramMu.Unlock()
	}
}

// drainDiagrams moves a finished render into the transcript. It runs on the
// render goroutine, which is the only one allowed to touch blocks.
func (m *Model) drainDiagrams() {
	m.diagramMu.Lock()
	inbox := m.diagramInbox
	m.diagramMu.Unlock()
	if inbox == nil {
		return
	}
	var done *mermaidRendered
	select {
	case done = <-inbox:
	default:
		return
	}
	if done.Err != nil {
		// The styled source is already in the transcript, so a failed render
		// costs a notice rather than leaving a gap.
		m.notice = "mermaid: " + done.Err.Error()
		return
	}
	m.diagrams[done.Source] = done.Path

	img, err := LoadImage(done.Path, m.chatWidth(), DiagramRows)
	if err != nil {
		m.notice = "mermaid: " + err.Error()
		return
	}
	m.nextImageID++
	img.ID = m.nextImageID
	m.blocks = append(m.blocks, Block{Kind: BlockImage, Image: img})
	m.pendingImages += graphics.KittySequence(graphics.Image{
		PNG: img.PNG, Cols: img.Cols, Rows: img.Rows, ID: img.ID,
	})
	m.followIfPinned()
}

// DiagramRows is how tall a rendered diagram is drawn. Fixed rather than
// derived from the image: the cell size is not knowable from inside the
// program, and a diagram that reserves the wrong number of rows either overlaps
// the text below or leaves a gap.
const DiagramRows = 16

// hashSource keys a rendered diagram by its source.
func hashSource(s string) uint32 {
	var h uint32 = 2166136261
	for i := 0; i < len(s); i++ {
		h ^= uint32(s[i])
		h *= 16777619
	}
	return h
}
