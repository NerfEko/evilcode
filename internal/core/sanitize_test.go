package core

import (
	"strings"
	"testing"
)

// H4.1: an OSC 52 payload in a repository file or a model's answer must not
// reach the terminal. It writes the user's clipboard.
func TestSanitizeStripsClipboardWrites(t *testing.T) {
	payload := "\x1b]52;c;aGVsbG8=\x07"
	got := SanitizeTerminal("before" + payload + "after")
	if strings.Contains(got, "52;c") || strings.Contains(got, "\x1b") {
		t.Errorf("an OSC 52 sequence survived: %q", got)
	}
	if got != "beforeafter" {
		t.Errorf("got %q, want the surrounding text untouched", got)
	}
}

func TestSanitizeKeepsLayoutAndDropsControl(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{"plain", "plain text", "plain text"},
		{"layout", "two\nlines\tapart", "two\nlines\tapart"},
		{"carriage return overwrites the line", "a\rb", "ab"},
		{"bell", "bell\a", "bell"},
		{"clear screen and home", "\x1b[2J\x1b[Hcleared", "cleared"},
		{"colour we did not choose", "\x1b[31mred\x1b[0m", "red"},
		{"window title", "\x1b]0;retitled\x07", ""},
		{"unterminated DCS", "\x1bPmalformed", ""},
		{"eight-bit CSI", "31mred", "31mred"},
		{"text survives", "emoji 🔥 and accents é", "emoji 🔥 and accents é"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := SanitizeTerminal(tc.in); got != tc.want {
				t.Errorf("SanitizeTerminal(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

// The common case is text with nothing to strip, and it must not be rebuilt.
func TestSanitizeReturnsCleanTextUnchanged(t *testing.T) {
	in := strings.Repeat("ordinary source code;\n", 100)
	if got := SanitizeTerminal(in); got != in {
		t.Error("clean text was altered")
	}
}
