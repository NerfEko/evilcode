package daemon

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"syscall"
	"time"
)

// EnsureRunning connects to the per-user daemon, starting a detached server
// when necessary. The caller owns the returned client.
func EnsureRunning(ctx context.Context) (*Client, error) {
	path := SocketPath()
	if client, err := DialPath(path); err == nil {
		return client, nil
	}
	return EnsureRunningPath(ctx, path)
}

// EnsureRunningPath is the testable form of EnsureRunning.
func EnsureRunningPath(ctx context.Context, path string) (*Client, error) {
	if client, err := DialPath(path); err == nil {
		return client, nil
	}

	exe, err := os.Executable()
	if err != nil {
		return nil, fmt.Errorf("find evilcode executable: %w", err)
	}
	cmd := exec.Command(exe, "serve", "-q", "-socket", path)
	cmd.Stdin = nil
	cmd.Stdout = io.Discard
	cmd.Stderr = io.Discard
	// The server must outlive the terminal process that started it. A new
	// session also keeps terminal-generated signals from reaching the daemon.
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("start evilcode server: %w", err)
	}
	wait := make(chan error, 1)
	go func() { wait <- cmd.Wait() }()

	deadline := time.NewTimer(5 * time.Second)
	defer deadline.Stop()
	ticker := time.NewTicker(25 * time.Millisecond)
	defer ticker.Stop()
	for {
		if client, err := DialPath(path); err == nil {
			return client, nil
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case err := <-wait:
			return nil, fmt.Errorf("evilcode server exited before listening: %w", err)
		case <-deadline.C:
			return nil, fmt.Errorf("timed out waiting for evilcode server at %s", path)
		case <-ticker.C:
		}
	}
}
