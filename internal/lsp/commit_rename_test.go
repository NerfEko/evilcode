package lsp

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// H5.17: the write phase used to overwrite each file in place, sequentially,
// so a failure partway through left some files renamed and others not —
// despite Rename's doc comment claiming the whole operation was atomic.
// commitRename stages every replacement first and only renames once every
// stage succeeds, so a mid-way failure leaves nothing touched.

func noopForget(string) {}

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestCommitRenameAppliesEveryFile(t *testing.T) {
	dir := t.TempDir()
	a := filepath.Join(dir, "a.go")
	b := filepath.Join(dir, "b.go")
	writeFile(t, a, "package a\nfunc old() {}\n")
	writeFile(t, b, "package a\nfunc old() {}\n")

	res := &RenameResult{
		Before: map[string]string{a: "package a\nfunc old() {}\n", b: "package a\nfunc old() {}\n"},
		After:  map[string]string{a: "package a\nfunc renamed() {}\n", b: "package a\nfunc renamed() {}\n"},
	}
	if err := commitRename(res, noopForget); err != nil {
		t.Fatal(err)
	}
	for _, f := range []string{a, b} {
		got, _ := os.ReadFile(f)
		if !strings.Contains(string(got), "renamed") {
			t.Errorf("%s = %q, want the renamed body", f, got)
		}
	}
}

func TestCommitRenameLeavesEverythingUntouchedWhenAStagingFailsMidway(t *testing.T) {
	// The bug this reproduces: the old write phase wrote each file directly,
	// in map-iteration order, so whichever files it reached before hitting one
	// it could not write were already overwritten by the time the failure
	// surfaced. Repeated 20 times because Go's map iteration order is
	// randomized, and the old code only corrupted a.go when it happened to be
	// visited before b.go.
	for i := 0; i < 20; i++ {
		dir := t.TempDir()
		a := filepath.Join(dir, "a.go")
		writeFile(t, a, "package a\nfunc old() {}\n")

		// b.go is a directory, not a file: staging it (which reads it back to
		// verify, then tries to create a temp file beside it) fails reliably,
		// regardless of which file commitRename reaches first.
		b := filepath.Join(dir, "b.go")
		if err := os.MkdirAll(b, 0o755); err != nil {
			t.Fatal(err)
		}

		res := &RenameResult{
			Before: map[string]string{a: "package a\nfunc old() {}\n", b: "package a\nfunc old() {}\n"},
			After:  map[string]string{a: "package a\nfunc renamed() {}\n", b: "package a\nfunc renamed() {}\n"},
		}
		if err := commitRename(res, noopForget); err == nil {
			t.Fatalf("run %d: expected staging b.go (a directory) to fail", i)
		}
		gotA, _ := os.ReadFile(a)
		if string(gotA) != "package a\nfunc old() {}\n" {
			t.Fatalf("run %d: a.go = %q, must be untouched when b.go's staging failed", i, gotA)
		}
	}
}

func TestCommitRenameRefusesASourceChangedSincePhaseOne(t *testing.T) {
	dir := t.TempDir()
	a := filepath.Join(dir, "a.go")
	writeFile(t, a, "package a\nfunc concurrentlyEdited() {}\n")

	res := &RenameResult{
		Before: map[string]string{a: "package a\nfunc old() {}\n"}, // stale: not what's on disk
		After:  map[string]string{a: "package a\nfunc renamed() {}\n"},
	}
	if err := commitRename(res, noopForget); err == nil {
		t.Fatal("expected a source changed since phase one to be refused")
	}
	got, _ := os.ReadFile(a)
	if !strings.Contains(string(got), "concurrentlyEdited") {
		t.Errorf("a.go = %q, must keep the concurrent edit rather than being overwritten", got)
	}
}
