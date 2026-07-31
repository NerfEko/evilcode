package tools

import (
	"context"
	"os/exec"
	"syscall"
)

// setProcessGroup puts a command in a process group of its own.
//
// Cancelling a context kills the process it started, not that process's
// children. A shell command is almost always a parent: `( sleep 2; write ) &`,
// a build that forks compilers, a script that starts a server. Those
// grandchildren outlived the timeout and kept working in the workspace after
// the tool call had returned an error to the model.
func setProcessGroup(cmd *exec.Cmd) {
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	cmd.SysProcAttr.Setpgid = true
}

// runGroup runs a command and kills its whole process group if the context
// ends first.
//
// The negative pid is the group. Signalling the leader alone is what
// CommandContext already does, and is what leaves the descendants behind.
func runGroup(ctx context.Context, cmd *exec.Cmd) error {
	if err := cmd.Start(); err != nil {
		return err
	}

	done := make(chan struct{})
	go func() {
		select {
		case <-ctx.Done():
			if cmd.Process != nil {
				_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
			}
		case <-done:
		}
	}()

	err := cmd.Wait()
	close(done)
	return err
}
