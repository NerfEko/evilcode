package daemon

import (
	"fmt"
	"net"
	"os"
	"syscall"
)

// checkPeer refuses a connection from another user.
//
// The socket is already owner-only, so this should never fire. It is here
// because the socket's mode is one mistake away from not being owner-only — a
// squatted runtime directory, an umask surprise, a future refactor — and the
// thing on the other side of this check can run commands as this user. A cheap
// second answer to "who is that" is worth having when the first answer is a
// file permission.
func checkPeer(conn net.Conn) error {
	unixConn, ok := conn.(*net.UnixConn)
	if !ok {
		return nil
	}
	raw, err := unixConn.SyscallConn()
	if err != nil {
		return err
	}
	var cred *syscall.Ucred
	var credErr error
	if err := raw.Control(func(fd uintptr) {
		cred, credErr = syscall.GetsockoptUcred(int(fd), syscall.SOL_SOCKET, syscall.SO_PEERCRED)
	}); err != nil {
		return err
	}
	if credErr != nil {
		return credErr
	}
	if uid := os.Getuid(); int(cred.Uid) != uid {
		return fmt.Errorf("connection from uid %d, refused", cred.Uid)
	}
	return nil
}
