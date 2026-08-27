package tui

import (
	"strings"
	"testing"
)

func TestShellModeIsNotAdvertised(t *testing.T) {
	// `!` shell mode was listed in the help and styled in the composer but had
	// no execution path: `!ls` was routed to Submit and sent to the model as a
	// literal prompt. Advertising "Enter runs locally" for something that
	// actually talks to the model is a lie, so the mode is gone rather than
	// half-built (plan2.md H5.2).
	if strings.Contains(helpText(), "shell command") {
		t.Error("help text advertises a shell mode with no execution path")
	}
}
