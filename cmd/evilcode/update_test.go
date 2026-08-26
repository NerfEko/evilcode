package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"evilcode/internal/config"
	"evilcode/internal/daemon"
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

// TestStopDaemonIfRunning verifies the update flow's daemon shutdown: it is a
// no-op when nothing is listening, and it gracefully stops a running daemon so
// the next `evilcode serve` picks up the freshly swapped binary.
func TestStopDaemonIfRunning(t *testing.T) {
	// No daemon at this path: a no-op, not an error or panic.
	stopDaemonIfRunningAt(filepath.Join(t.TempDir(), "no-such.sock"))

	// A 0700 temp dir keeps the owner-only socket reachable only by this user,
	// matching what CheckSocketPath requires; a short name stays under the unix
	// socket path cap.
	dir, err := os.MkdirTemp("", "evild")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	path := filepath.Join(dir, "s.sock")

	srv := daemon.NewServer(&config.Config{}, t.TempDir(), "")
	srv.Path = path
	if err := srv.Listen(); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	serveDone := make(chan error, 1)
	go func() { serveDone <- srv.Serve(ctx) }()
	t.Cleanup(srv.Close)

	stopDaemonIfRunningAt(path)

	// Serve returns only once Close() runs, which the MsgStop handler triggers.
	select {
	case <-serveDone:
	case <-time.After(3 * time.Second):
		t.Fatal("daemon did not stop after stopDaemonIfRunningAt")
	}
}
