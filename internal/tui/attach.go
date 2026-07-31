package tui

import (
	"fmt"
	"os"
	"os/exec"
	"strings"

	tea "charm.land/bubbletea/v2"

	"evilcode/internal/provider"
)

// Attachment is an image staged for the next message (plan.md §6.6).
//
// The bytes are held rather than the path: a clipboard image has no path at all,
// and a file can change between attaching and sending. They are dropped after
// the turn, and deliberately never written to the session log — one JSONL line
// per message against a 16 MB scanner limit means a couple of images would
// silently truncate the entire replay from that point on.
type Attachment struct {
	Placeholder string
	MIME        string
	Bytes       []byte

	// Source is the file it came from, or "" for a clipboard paste. Shown in
	// the notice so an attachment is identifiable before it is sent.
	Source string
}

// MaxAttachments bounds one message. Vision models degrade badly past a handful
// of images, and the cost is per-image on every provider.
const MaxAttachments = 4

// attachImage stages bytes and returns the placeholder to insert.
func (m *Model) attachImage(data []byte, source string) (string, error) {
	if len(m.attachments) >= MaxAttachments {
		return "", fmt.Errorf("already carrying %d images, which is the limit", MaxAttachments)
	}
	if len(data) == 0 {
		return "", fmt.Errorf("the image is empty")
	}
	if len(data) > MaxImageBytes {
		return "", fmt.Errorf("the image is %s, over the %s limit",
			humanBytes(len(data)), humanBytes(MaxImageBytes))
	}

	mime := provider.DetectImageMIME(data)
	placeholder := fmt.Sprintf("[image %d]", len(m.attachments)+1)
	m.attachments = append(m.attachments, Attachment{
		Placeholder: placeholder, MIME: mime, Bytes: data, Source: source,
	})
	return placeholder, nil
}

// TakeAttachments returns the staged images and clears them, so an attachment
// travels with exactly one message.
func (m *Model) TakeAttachments() [][]byte {
	if len(m.attachments) == 0 {
		return nil
	}
	out := make([][]byte, 0, len(m.attachments))
	for _, a := range m.attachments {
		out = append(out, a.Bytes)
	}
	m.attachments = nil
	return out
}

// ClipboardImageCommands are tried in order to read an image off the clipboard.
//
// Explicit, never from bracketed paste: a Wayland clipboard advertises several
// MIME types at once and is routinely misidentified, and a stray image
// attachment is worse than a missing one (plan.md §6.6, and the gotchas ledger).
var ClipboardImageCommands = [][]string{
	{"wl-paste", "--type", "image/png"},
	{"xclip", "-selection", "clipboard", "-t", "image/png", "-o"},
}

// readClipboardImage returns image bytes from the clipboard, if there are any.
func readClipboardImage() ([]byte, error) {
	var tried []string
	for _, argv := range ClipboardImageCommands {
		if _, err := exec.LookPath(argv[0]); err != nil {
			continue
		}
		tried = append(tried, argv[0])
		out, err := exec.Command(argv[0], argv[1:]...).Output()
		if err == nil && len(out) > 0 {
			return out, nil
		}
	}
	if len(tried) == 0 {
		return nil, fmt.Errorf("no clipboard tool found — install wl-clipboard or xclip")
	}
	return nil, fmt.Errorf("no image on the clipboard")
}

// pasteImage implements Ctrl+V / Alt+V.
func (m *Model) pasteImage() tea.Cmd {
	data, err := readClipboardImage()
	if err != nil {
		m.notice = err.Error()
		return nil
	}
	placeholder, err := m.attachImage(data, "")
	if err != nil {
		m.notice = err.Error()
		return nil
	}
	m.editor.Insert(placeholder)
	m.notice = fmt.Sprintf("Pasted %s (%s)",
		provider.DetectImageMIME(data), humanBytes(len(data)))
	return nil
}

// DropPaths handles a file drop: images attach, everything else is inserted as a
// path (quoted when it contains whitespace, so it survives being pasted into a
// shell command).
func (m *Model) DropPaths(paths []string) {
	var images, files int
	for _, path := range paths {
		path = strings.Trim(strings.TrimSpace(path), `"'`)
		if path == "" {
			continue
		}
		if !IsImagePath(path) {
			m.editor.Insert(QuoteIfNeeded(path) + " ")
			files++
			continue
		}
		data, err := os.ReadFile(path)
		if err != nil {
			m.notice = "could not read " + path + ": " + err.Error()
			continue
		}
		placeholder, err := m.attachImage(data, path)
		if err != nil {
			m.notice = err.Error()
			continue
		}
		m.editor.Insert(placeholder + " ")
		images++
	}
	if n := dropNotice(images, files); n != "" {
		m.notice = n
	}
}

// dropNotice describes what a drop attached, per §6.6.
func dropNotice(images, files int) string {
	switch {
	case images == 0 && files == 0:
		return ""
	case files == 0:
		return fmt.Sprintf("Dropped %s", plural(images, "image"))
	case images == 0:
		return fmt.Sprintf("Dropped %s", plural(files, "file"))
	default:
		return fmt.Sprintf("Dropped %s and %s",
			plural(images, "image"), plural(files, "file"))
	}
}

// WithVision declares whether the active model accepts image attachments.
func (m *Model) WithVision(ok bool) *Model {
	m.vision = ok
	return m
}
