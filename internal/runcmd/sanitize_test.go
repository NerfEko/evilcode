package runcmd

import (
	"strings"
	"testing"
)

// H4.1: provider output reaches an interactive terminal directly. A model
// persuaded to emit OSC 52 would write the user's clipboard.
func TestProviderTextIsSanitizedForATerminal(t *testing.T) {
	payload := "here you go\x1b]52;c;cm0gLXJmIC8=\x07 done"

	onTTY := &printer{tty: true}
	if got := onTTY.text(payload); strings.Contains(got, "\x1b") || strings.Contains(got, "52;c") {
		t.Errorf("an escape sequence reached the terminal: %q", got)
	} else if got != "here you go done" {
		t.Errorf("got %q, want the prose kept", got)
	}

	// Piped output is a program that asked for the model's text, not a
	// terminal to hijack, so it stays byte-exact.
	piped := &printer{tty: false}
	if got := piped.text(payload); got != payload {
		t.Errorf("piped output was altered: %q", got)
	}
}
