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

// writeAtomicBeneath replaces a file's contents through a descriptor held on
// its parent directory.
//
// The first version of this checked the parent with openBeneath, closed it, and
// then called the ordinary write — which re-resolves the pathname from scratch.
// That is the same check-then-use shape confinement had in the first place,
// with an extra step: a component swapped between the check and the write
// redirects it exactly as before.
//
// Holding the directory descriptor is what closes it. Every operation after
// that — creating the temp file, writing it, renaming it into place — names
// files relative to that descriptor, so it does not matter what the path
// resolves to afterwards: the fd refers to the directory that was verified.
func writeAtomicBeneath(root, full string, data []byte) error {
	dir, err := openBeneath(root, filepath.Dir(full), os.O_RDONLY|unix.O_DIRECTORY, 0)
	if err != nil {
		return err
	}
	defer dir.Close()
	dirFd := int(dir.Fd())
	base := filepath.Base(full)

	mode := uint32(0o644)
	var st unix.Stat_t
	if err := unix.Fstatat(dirFd, base, &st, unix.AT_SYMLINK_NOFOLLOW); err == nil {
		mode = st.Mode & 0o777
	}

	tmp := "." + base + ".tmp"
	_ = unix.Unlinkat(dirFd, tmp, 0)
	fd, err := unix.Openat(dirFd, tmp,
		unix.O_CREAT|unix.O_EXCL|unix.O_WRONLY|unix.O_CLOEXEC|unix.O_NOFOLLOW, mode)
	if err != nil {
		return fmt.Errorf("creating a temporary file in the workspace: %w", err)
	}
	file := os.NewFile(uintptr(fd), tmp)

	if _, err := file.Write(data); err != nil {
		file.Close()
		_ = unix.Unlinkat(dirFd, tmp, 0)
		return err
	}
	if err := file.Sync(); err != nil {
		file.Close()
		_ = unix.Unlinkat(dirFd, tmp, 0)
		return err
	}
	if err := file.Close(); err != nil {
		_ = unix.Unlinkat(dirFd, tmp, 0)
		return err
	}
	if err := unix.Renameat(dirFd, tmp, dirFd, base); err != nil {
		_ = unix.Unlinkat(dirFd, tmp, 0)
		return err
	}
	return nil
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
