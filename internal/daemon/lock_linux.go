package daemon

import (
	"fmt"
	"os"
	"syscall"
)

// lockSocketPath claims the socket for a daemon's whole lifetime, not just its
// bind.
//
// The lock file sits beside the socket and is never removed: an empty file is
// cheaper than the alternative, which is a second race over deleting it. The
// lock is advisory and process-scoped, released when the descriptor closes —
// including when a daemon dies any kind of death, so a dead daemon never
// wedges the next one.
//
// It is held for the daemon's entire life, not only across the bind, because
// the bind-time lock alone left a hole the recorded owls and banshees fell
// through: a daemon whose socket file is deleted from under it (Close removes
// the path by name, so a late-exiting old daemon deletes a successor's file)
// stays alive and unreachable, the next attach finds nothing to dial, spawns a
// serve, and the guarded bind happily binds the now-missing path — a second
// live daemon nobody can see. With the lock held for life, that second daemon
// fails the claim instead: one daemon per socket path, or none.
//
// The acquisition is nonblocking: a second daemon should refuse fast with a
// clear message, not hang behind the first.
func lockSocketPath(path string) (func(), error) {
	f, err := os.OpenFile(path+".lock", os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, fmt.Errorf("opening the socket lock: %w", err)
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		f.Close()
		return nil, fmt.Errorf("another daemon is already running on this socket: %w", err)
	}
	return func() {
		_ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
		f.Close()
	}, nil
}

// socketInode reports the inode of the socket file at path, or false when the
// path does not name a file. Used so a closing daemon only removes the path
// when it still points at the socket this daemon bound — never at a successor.
func socketInode(path string) (uint64, bool) {
	st, err := os.Stat(path)
	if err != nil {
		return 0, false
	}
	raw, ok := st.Sys().(*syscall.Stat_t)
	if !ok {
		return 0, false
	}
	return raw.Ino, true
}
