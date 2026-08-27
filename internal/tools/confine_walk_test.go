package tools

import (
	"os"
	"path/filepath"
	"testing"
)

// R2-08: on kernels without openat2 the confined open used to fall back to the
// ordinary pathname open — silently dropping the guarantee the callers
// advertise — and mkdirAllConfined's resolve → MkdirAll → verify shape could
// create directories outside the workspace before the verify detected it. The
// open now fails closed (unless the user opted into weak confinement) and
// directory creation is a descriptor walk with no-follow on every component.

func TestMkdirAllBeneathCreatesNestedDirectories(t *testing.T) {
	root := t.TempDir()
	if err := mkdirAllBeneath(root, filepath.Join(root, "a", "b", "c")); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(filepath.Join(root, "a", "b", "c"))
	if err != nil || !info.IsDir() {
		t.Fatalf("nested directories were not created: %v", err)
	}

	// An existing directory is a no-op, and the workspace root itself is too.
	if err := mkdirAllBeneath(root, filepath.Join(root, "a", "b", "c")); err != nil {
		t.Fatalf("re-creating existing directories failed: %v", err)
	}
	if err := mkdirAllBeneath(root, root); err != nil {
		t.Fatalf("the workspace root itself failed: %v", err)
	}
}

func TestMkdirAllBeneathRefusesASymlinkComponent(t *testing.T) {
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, "real"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(root, "real"), filepath.Join(root, "join")); err != nil {
		t.Fatal(err)
	}

	// The walk is asked for a path THROUGH the symlink. It must refuse rather
	// than follow it — no directory is created on the far side either.
	err := mkdirAllBeneath(root, filepath.Join(root, "join", "sub"))
	if err == nil {
		t.Fatal("a symlinked component was followed during directory creation")
	}
	if _, err := os.Stat(filepath.Join(root, "real", "sub")); err == nil {
		t.Fatal("directories were created through the symlinked component")
	}
}

func TestMkdirAllBeneathRefusesEscape(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	err := mkdirAllBeneath(root, filepath.Join(outside, "escaped"))
	if err == nil {
		t.Fatal("a path outside the workspace was created")
	}
	if _, err := os.Stat(filepath.Join(outside, "escaped")); err == nil {
		t.Fatal("the outside directory now exists")
	}
}

func TestConfinedWriteStillCreatesParentsThroughTheWalk(t *testing.T) {
	root := t.TempDir()
	f := NewFS(root).WithConfine(true)

	if out := f.Tools().RunOne(t.Context(), Call{
		Name: "write", Args: []byte(`{"path":"deep/nested/file.txt","content":"hello"}`),
	}); out.Err != nil {
		t.Fatal(out.Err)
	}
	data, err := os.ReadFile(filepath.Join(root, "deep", "nested", "file.txt"))
	if err != nil || string(data) != "hello" {
		t.Fatalf("confined write through created parents failed: %q %v", data, err)
	}
}

// ErrWeakConfinementUnavailable is only reachable on a kernel without openat2;
// on this kernel strong confinement works, so a confined read of the
// workspace's own files still succeeds with weak mode off or on.
func TestWeakConfineIsInertWhereOpenat2Exists(t *testing.T) {
	if !openat2Supported() {
		t.Skip("this kernel has no openat2; the fail-closed path is the live one here")
	}
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "f.txt"), []byte("ok"), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, weak := range []bool{false, true} {
		f := NewFS(root).WithConfine(true).WithWeakConfine(weak)
		if _, err := f.openConfined(filepath.Join(root, "f.txt")); err != nil {
			t.Fatalf("weak=%v: confined open failed: %v", weak, err)
		}
	}
}

func TestOpenBeneathRefusesOutsidePathsRegardlessOfWeak(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	for _, weak := range []bool{false, true} {
		if _, err := openBeneath(root, filepath.Join(outside, "x"), os.O_RDONLY, 0, weak); err == nil {
			t.Fatalf("weak=%v: a path outside the workspace was opened", weak)
		}
	}
}
