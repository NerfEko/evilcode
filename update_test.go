package main

import (
	"strings"
	"testing"
)

func TestShellQuote(t *testing.T) {
	if got := shellQuote("/tmp/a'b"); got != "'/tmp/a'\\''b'" {
		t.Errorf("shellQuote = %q", got)
	}
}

func TestValidateReleaseURL(t *testing.T) {
	if err := validateReleaseURL("https://git.evileko.dev/evileko/evilcode/releases/download/v1/evilcode-linux-amd64"); err != nil {
		t.Fatalf("canonical release URL rejected: %v", err)
	}
	for _, rawURL := range []string{
		"http://git.evileko.dev/asset",
		"https://attacker.example/asset",
		"https://git.evileko.dev.attacker.example/asset",
		"https://user:pass@git.evileko.dev/asset",
		"https://git.evileko.dev:443/asset",
		"%gh&%ij",
	} {
		t.Run(strings.ReplaceAll(rawURL, "/", "_"), func(t *testing.T) {
			if err := validateReleaseURL(rawURL); err == nil {
				t.Errorf("unsafe update URL accepted: %q", rawURL)
			}
		})
	}
}

func TestNewGetReturnsMalformedURLError(t *testing.T) {
	if req, err := newGet("%"); err == nil || req != nil {
		t.Fatalf("newGet malformed URL = (%v, %v), want an error", req, err)
	}
}
