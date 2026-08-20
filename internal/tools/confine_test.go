package tools

import (
	"os"
	"path/filepath"
	"testing"
)

// H4.6: confinement resolves symlinks to validate a path and then opens the
// original path. Those are two operations, and what changes in between is not
// checked.
//
// The race needs no goroutines to demonstrate — the ordering is the bug, so the
// test performs it in order: validate a path that is genuinely inside the
// workspace, swap a component for a symlink pointing out, then open. That is
// exactly the window, staged rather than raced.
func TestAPathSwappedAfterValidationCannotEscape(t *testing.T) {
	base := t.TempDir()
	root := filepath.Join(base, "workspace")
	outside := filepath.Join(base, "outside")
	if err := os.MkdirAll(filepath.Join(root, "sub"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(outside, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(outside, "secret.txt"), []byte("not yours\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "sub", "secret.txt"), []byte("fine\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	f := NewFS(root).WithConfine(true)

	// Step one: validation. `sub/secret.txt` is inside the workspace and is
	// accepted, which is correct at this instant.
	full, err := f.resolve("sub/secret.txt")
	if err != nil {
		t.Fatalf("a path inside the workspace was refused: %v", err)
	}

	// Step two: the swap. `sub` becomes a link to somewhere else. Nothing
	// re-checks, because the check has already happened.
	if err := os.RemoveAll(filepath.Join(root, "sub")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(root, "sub")); err != nil {
		t.Fatal(err)
	}

	// Step three: the open. This is what the old code did with the validated
	// path, and it reads the attacker's file.
	if data, err := os.ReadFile(full); err != nil || string(data) != "not yours\n" {
		t.Fatalf("the staged swap did not reproduce the escape (err=%v)", err)
	}
	t.Log("the plain open followed the swapped symlink out of the workspace")

	// And this is the fix: the same open, bounded by the kernel, refuses.
	file, err := f.openConfined(full)
	if err == nil {
		file.Close()
		t.Error("the confined open followed a symlink swapped in after validation")
	}
}

// A file genuinely inside the workspace still opens.
func TestAConfinedReadStillReadsItsOwnFiles(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "inside.txt"), []byte("fine\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	f := NewFS(root).WithConfine(true)

	res, err := run(t, f.Tools(), "read", map[string]any{"path": "inside.txt"})
	if err != nil {
		t.Fatalf("a file inside the workspace was refused: %v", err)
	}
	if res.Output == "" {
		t.Error("the read produced nothing")
	}
}

// With confinement off — the default — nothing is bounded, which is documented
// behaviour rather than an oversight.
func TestAnUnconfinedReadStillReachesOutside(t *testing.T) {
	base := t.TempDir()
	root := filepath.Join(base, "workspace")
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatal(err)
	}
	outside := filepath.Join(base, "elsewhere.txt")
	if err := os.WriteFile(outside, []byte("readable\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	f := NewFS(root)
	if _, err := run(t, f.Tools(), "read", map[string]any{"path": outside}); err != nil {
		t.Errorf("an unconfined session could not read outside its root: %v", err)
	}
}

// The write path had the same check-then-use shape the read path was fixed for:
// it verified the parent directory, closed it, and then called an ordinary
// write that re-resolves the pathname from scratch. Holding the directory
// descriptor across the temp file and the rename is what closes it.
func TestAConfinedWriteCannotBeRedirectedAfterItsCheck(t *testing.T) {
	base := t.TempDir()
	root := filepath.Join(base, "workspace")
	outside := filepath.Join(base, "outside")
	if err := os.MkdirAll(filepath.Join(root, "sub"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(outside, 0o755); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(outside, "victim.txt")
	if err := os.WriteFile(target, []byte("original\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	f := NewFS(root).WithConfine(true)
	full, err := f.resolve("sub/victim.txt")
	if err != nil {
		t.Fatal(err)
	}

	// The swap, after the path has been judged safe.
	if err := os.RemoveAll(filepath.Join(root, "sub")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(root, "sub")); err != nil {
		t.Fatal(err)
	}

	if err := f.writeConfined(full, []byte("overwritten\n")); err == nil {
		t.Error("a confined write followed a symlink swapped in after its check")
	}

	data, err := os.ReadFile(target)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "original\n" {
		t.Errorf("the file outside the workspace was rewritten: %q", data)
	}
}

func TestConfinedWriteDoesNotDeleteAPreexistingTempNamedFile(t *testing.T) {
	root := t.TempDir()
	target := filepath.Join(root, "target.txt")
	oldTemp := filepath.Join(root, ".target.txt.tmp")
	if err := os.WriteFile(target, []byte("old\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(oldTemp, []byte("belongs to the user\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	f := NewFS(root).WithConfine(true)
	if err := f.writeConfined(target, []byte("new\n")); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(oldTemp)
	if err != nil {
		t.Fatalf("the writer deleted a preexisting .tmp file: %v", err)
	}
	if string(got) != "belongs to the user\n" {
		t.Errorf("preexisting .tmp file was changed: %q", got)
	}
}
