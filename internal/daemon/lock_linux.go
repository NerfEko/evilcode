package daemon

import (
	"fmt"
	"os"
	"syscall"
)

// lockSocketPath serializes daemons racing to bind the same socket.
//
// The lock file sits beside the socket and is never removed: an empty file is
// cheaper than the alternative, which is a second race over deleting it. The
// lock is advisory and process-scoped, released when the descriptor closes, so
// a daemon that dies mid-bind does not wedge the next one.
func lockSocketPath(path string) (func(), error) {
	f, err := os.OpenFile(path+".lock", os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, fmt.Errorf("opening the socket lock: %w", err)
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX); err != nil {
		f.Close()
		return nil, fmt.Errorf("locking the socket path: %w", err)
	}
	return func() {
		_ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
		f.Close()
	}, nil
}
