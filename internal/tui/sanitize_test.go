package tui

import (
	"strings"
	"testing"
)

// H4.1: repository content reaches the terminal through the transcript. A file
// in a cloned repo carrying OSC 52 writes the user's clipboard the moment it is
// displayed — no tool call, no confirmation, just rendering it.
func TestRepositoryContentCannotDriveTheTerminal(t *testing.T) {
	// What a hostile fixture file looks like: a clipboard write, a screen
	// clear, and a title change, wrapped in ordinary-looking source.
	fixture := "package main\n" +
		"// \x1b]52;c;cm0gLXJmIH4=\x07\n" +
		"func main() {\x1b[2J\x1b[H}\n" +
		"// \x1b]0;pwned\x07\n"

	m := newTestModel(t)
	m.blocks = []Block{{Kind: BlockAssistant, Text: "```go\n" + fixture + "```"}}

	frame := frameString(m)
	assertInert(t, frame)
}

// And so must provider output, which arrives on the same path.
func TestProviderOutputCannotDriveTheTerminal(t *testing.T) {
	m := newTestModel(t)
	m.blocks = []Block{
		{Kind: BlockAssistant, Text: "Sure.\x1b]52;c;cm0gLXJmIH4=\x07 All done."},
		{Kind: BlockTool, ToolName: "read\x1b[2J"},
		{Kind: BlockError, Text: "failed\x1b]0;retitled\x07"},
	}

	frame := frameString(m)
	assertInert(t, frame)
}

// frameString renders every block the way the transcript does and returns what
// would reach the terminal.
func frameString(m *Model) string {
	var b strings.Builder
	for i := range m.blocks {
		for _, line := range m.renderer.Lines(&m.blocks[i]) {
			b.WriteString(line)
			b.WriteByte(10)
		}
	}
	return b.String()
}

// assertInert checks that no escape sequence in the frame is one evilcode did
// not put there. Styling is expected; anything addressing the terminal itself
// is not.
func assertInert(t *testing.T, frame string) {
	t.Helper()

	for _, forbidden := range []struct {
		seq  string
		what string
	}{
		{"\x1b]52", "OSC 52 — writes the user's clipboard"},
		{"\x1b]0;", "OSC 0 — retitles the window"},
		{"\x1b[2J", "CSI 2J — clears the screen"},
		{"\x1b[H", "CSI H — homes the cursor"},
	} {
		if strings.Contains(frame, forbidden.seq) {
			t.Errorf("the rendered frame carries %s", forbidden.what)
		}
	}

	// The payload must not survive as text either: a clipboard write printed
	// as visible characters is a smaller problem, but it is still content the
	// renderer was told to drop.
	if strings.Contains(frame, "cm0gLXJmIH4=") {
		t.Error("the OSC 52 payload is still in the frame")
	}
}
