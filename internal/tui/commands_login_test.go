package tui

import (
	"strings"
	"testing"
)

// The /login help must advertise the provider argument and the deepseek
// example so the feature is discoverable from the palette and /help login.
func TestLoginHelpMentionsProviderArg(t *testing.T) {
	cmd, ok := FindCommand("login")
	if !ok {
		t.Fatal("login command not registered")
	}
	if !strings.Contains(cmd.Help, "provider") {
		t.Errorf("Help = %q, want it to mention \"provider\"", cmd.Help)
	}
	for _, want := range []string{"[provider]", "/login deepseek", "/login status"} {
		if !strings.Contains(cmd.Long, want) {
			t.Errorf("Long missing %q:\n%s", want, cmd.Long)
		}
	}
}

// The login command stays in a help section so /help surfaces it.
func TestLoginCoveredByHelpSection(t *testing.T) {
	covered := false
	for _, sec := range HelpSections {
		for _, n := range sec.Names {
			if n == "login" {
				covered = true
			}
		}
	}
	if !covered {
		t.Error("login is not listed in any HelpSection")
	}
}
