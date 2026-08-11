package main

import "testing"

func TestShellQuote(t *testing.T) {
	if got := shellQuote("/tmp/a'b"); got != "'/tmp/a'\\''b'" {
		t.Errorf("shellQuote = %q", got)
	}
}