package main

import "testing"

func TestParseAheadBehind(t *testing.T) {
	for _, test := range []struct {
		input         string
		ahead, behind int
	}{
		{"0 0", 0, 0},
		{"2 5\n", 2, 5},
	} {
		ahead, behind, err := parseAheadBehind(test.input)
		if err != nil || ahead != test.ahead || behind != test.behind {
			t.Errorf("parseAheadBehind(%q) = %d, %d, %v; want %d, %d", test.input, ahead, behind, err, test.ahead, test.behind)
		}
	}
	if _, _, err := parseAheadBehind("diverged"); err == nil {
		t.Error("malformed revision counts should fail")
	}
}

func TestShellQuote(t *testing.T) {
	if got := shellQuote("/tmp/a'b"); got != "'/tmp/a'\\''b'" {
		t.Errorf("shellQuote = %q", got)
	}
}
