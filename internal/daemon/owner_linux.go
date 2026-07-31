package daemon

import (
	"fmt"
	"os"
	"syscall"
)

// checkOwner refuses a directory belonging to anybody else.
//
// A directory that is mode 0700 but owned by another user is not protection,
// it is protection pointed the wrong way: they can do as they like inside it
// and this process cannot.
func checkOwner(dir string, info os.FileInfo) error {
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return nil
	}
	if uid := os.Getuid(); int(stat.Uid) != uid {
		return fmt.Errorf("%s is owned by uid %d, not %d", dir, stat.Uid, uid)
	}
	return nil
}
