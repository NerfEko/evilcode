package tui

import "syscall"

// syscallExec replaces this process. Linux-only, like the rest of evilcode
// (plan.md §1): there is no portable exec in Go, and the alternative — spawn
// and exit — leaves a window where two copies are both reading the keyboard.
func syscallExec(path string, args, env []string) error {
	return syscall.Exec(path, args, env)
}
