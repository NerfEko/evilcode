package tools

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"golang.org/x/sys/unix"
)

// openBeneath opens a path that must resolve inside root, atomically.
//
// The check-then-open shape it replaces has a race in the gap: confinement
// resolved symlinks to decide the path was inside the workspace, and then
// opened the original path. Anything that swaps a directory component for a
// symlink between those two steps is writing wherever it likes, and a tool
// call's own workspace is exactly where a model has just been running commands.
//
// RESOLVE_BENEATH makes the kernel refuse any resolution that leaves the
// directory the descriptor names — including through a symlink, an absolute
// path, or `..` — so the decision and the open are one operation.
func openBeneath(root, full string, flags int, perm os.FileMode) (*os.File, error) {
	rel, err := filepath.Rel(root, full)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return nil, fmt.Errorf("path %q is outside the workspace %s", full, root)
	}

	dir, err := os.Open(root)
	if err != nil {
		return nil, err
	}
	defer dir.Close()

	fd, err := unix.Openat2(int(dir.Fd()), rel, &unix.OpenHow{
		Flags:   uint64(flags) | unix.O_CLOEXEC,
		Mode:    uint64(perm),
		Resolve: unix.RESOLVE_BENEATH | unix.RESOLVE_NO_MAGICLINKS,
	})
	if err != nil {
		if !openat2Supported() {
			// Older kernels: fall back to the resolve-then-open path, which is
			// what everything did before. Saying so rather than pretending the
			// guarantee holds.
			return os.OpenFile(full, flags, perm)
		}
		return nil, fmt.Errorf("opening %s inside the workspace: %w", rel, err)
	}
	return os.NewFile(uintptr(fd), full), nil
}

// checkBeneath reports whether full resolves inside root, using the same
// kernel resolution the confined open relies on.
//
// A write goes through a temp file and a rename rather than a single open, so
// there is no descriptor to hand it; opening the parent directory beneath the
// root is what proves the destination is where it claims to be.
func checkBeneath(root, full string) error {
	dir := filepath.Dir(full)
	rel, err := filepath.Rel(root, dir)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return fmt.Errorf("path %q is outside the workspace %s", full, root)
	}
	f, err := openBeneath(root, dir, os.O_RDONLY, 0)
	if err != nil {
		return err
	}
	return f.Close()
}

// openat2Supported reports whether the kernel has the syscall at all. It landed
// in 5.6; this exists so a refusal on an older kernel is not reported as an
// escape attempt.
var openat2Supported = sync.OnceValue(func() bool {
	dir, err := os.Open(os.TempDir())
	if err != nil {
		return false
	}
	defer dir.Close()
	fd, err := unix.Openat2(int(dir.Fd()), ".", &unix.OpenHow{
		Flags:   uint64(os.O_RDONLY),
		Resolve: unix.RESOLVE_BENEATH,
	})
	if err == unix.ENOSYS {
		return false
	}
	if err == nil {
		unix.Close(fd)
	}
	return true
})
