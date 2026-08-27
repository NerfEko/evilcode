package tools

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
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
func openBeneath(root, full string, flags int, perm os.FileMode, weak bool) (*os.File, error) {
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
			// Older kernels: the kernel cannot enforce the boundary, so the
			// choice is explicit — fail closed, or fall back to the
			// resolve-then-open path only when the user asked for weak
			// confinement, which says the check is advisory (R2-08).
			if weak {
				return os.OpenFile(full, flags, perm)
			}
			return nil, fmt.Errorf(
				"%w: strong workspace confinement needs Linux 5.6+ (openat2); "+
					"set features.confine_weak to accept resolve-then-open on this kernel",
				ErrWeakConfinementUnavailable)
		}
		return nil, fmt.Errorf("opening %s inside the workspace: %w", rel, err)
	}
	return os.NewFile(uintptr(fd), full), nil
}

// ErrWeakConfinementUnavailable marks the fail-closed refusal on a kernel
// without openat2. It is distinct from a path escape so the caller can say
// "your kernel cannot enforce this" rather than "the model tried to escape".
var ErrWeakConfinementUnavailable = errors.New("strong confinement is unavailable on this kernel")

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
func writeAtomicBeneath(root, full string, data []byte, weak bool) error {
	dir, err := openBeneath(root, filepath.Dir(full), os.O_RDONLY|unix.O_DIRECTORY, 0, weak)
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

	var tmp string
	fd := -1
	for range 16 {
		var suffix [8]byte
		if _, err := rand.Read(suffix[:]); err != nil {
			return fmt.Errorf("creating a temporary filename in the workspace: %w", err)
		}
		tmp = "." + base + "." + hex.EncodeToString(suffix[:]) + ".tmp"
		fd, err = unix.Openat(dirFd, tmp,
			unix.O_CREAT|unix.O_EXCL|unix.O_WRONLY|unix.O_CLOEXEC|unix.O_NOFOLLOW, mode)
		if err == nil {
			break
		}
		if err != unix.EEXIST {
			return fmt.Errorf("creating a temporary file in the workspace: %w", err)
		}
	}
	if fd < 0 {
		return fmt.Errorf("creating a temporary file in the workspace: too many name collisions")
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

// mkdirAllBeneath creates dir's components below root with mkdirat, walking
// from a descriptor on the verified root. Every component is opened
// O_NOFOLLOW, so a directory swapped for a symlink mid-walk is refused instead
// of followed — the check and the creation are one walk over pinned
// descriptors, which is the guarantee the resolve → MkdirAll → verify shape in
// mkdirAllConfined could only approximate (R2-08).
func mkdirAllBeneath(root, dir string) error {
	rel, err := filepath.Rel(root, dir)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return fmt.Errorf("path %q is outside the workspace %s", dir, root)
	}
	if rel == "." {
		return nil
	}

	parent, err := os.Open(root)
	if err != nil {
		return err
	}
	defer parent.Close()

	for _, comp := range strings.Split(filepath.ToSlash(rel), "/") {
		if comp == "" || comp == "." {
			continue
		}
		fd, err := unix.Openat(int(parent.Fd()), comp,
			unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
		if err == unix.ENOENT {
			if err := unix.Mkdirat(int(parent.Fd()), comp, 0o755); err != nil {
				return fmt.Errorf("creating %s inside the workspace: %w", comp, err)
			}
			fd, err = unix.Openat(int(parent.Fd()), comp,
				unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
		}
		if err != nil {
			// ELOOP here means a path component was swapped for a symlink
			// between the resolve and now; ENOTDIR means something non-
			// directory occupies the name. Either way the walk stops rather
			// than following it.
			return fmt.Errorf("walking %s inside the workspace: %w", comp, err)
		}
		parent.Close()
		parent = os.NewFile(uintptr(fd), comp)
	}
	return nil
}
