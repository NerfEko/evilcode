package tui

import (
	"strings"
	"testing"
)

func TestSelfdevIsDisabled(t *testing.T) {
	m := newTestModel(t)
	m.runCommand("selfdev")

	if !strings.Contains(m.notice, "disabled") {
		t.Fatalf("notice = %q, want self-development disabled", m.notice)
	}
	for _, command := range VisibleCommands() {
		if command.Name == "selfdev" {
			t.Fatal("disabled selfdev command should not appear in the palette")
		}
	}
	if strings.Contains(SelfdevPrompt, "Load the") || strings.Contains(SelfdevPrompt, "pick the next") {
		t.Fatalf("disabled prompt still contains the old plan-first loop: %q", SelfdevPrompt)
	}
}
