package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"

	"evilcode/internal/tools"
	"evilcode/internal/tuicmd"
)

// runUpdate follows origin, tests the fetched source, then replaces the
// resolved executable. It never touches a dirty checkout or a binary before
// the build and tests pass.
func runUpdate() error {
	root, err := gitOutput(".", "rev-parse", "--show-toplevel")
	if err != nil {
		return fmt.Errorf("update: not inside a git checkout: %w", err)
	}
	root = strings.TrimSpace(root)
	status, err := gitOutput(root, "status", "--porcelain")
	if err != nil {
		return fmt.Errorf("update: checking the working tree: %w", err)
	}
	if strings.TrimSpace(status) != "" {
		return fmt.Errorf("update refused: working tree is dirty:\n%s", strings.TrimSpace(status))
	}
	oldHead, err := gitOutput(root, "rev-parse", "HEAD")
	if err != nil {
		return fmt.Errorf("update: finding current revision: %w", err)
	}

	exe, mode, dir, err := updateTarget()
	if err != nil {
		return err
	}
	if err := gitRun(root, "fetch", "--prune", "origin"); err != nil {
		return fmt.Errorf("update: fetch origin failed: %w", err)
	}
	branch, err := gitOutput(root, "symbolic-ref", "--short", "HEAD")
	if err != nil {
		return fmt.Errorf("update refused: detached HEAD")
	}
	branch = strings.TrimSpace(branch)
	remote := "origin/" + branch
	if _, err := gitOutput(root, "rev-parse", "--verify", remote); err != nil {
		return fmt.Errorf("update: origin has no %s", remote)
	}
	counts, err := gitOutput(root, "rev-list", "--left-right", "--count", "HEAD..."+remote)
	if err != nil {
		return fmt.Errorf("update: comparing with %s: %w", remote, err)
	}
	ahead, behind, err := parseAheadBehind(counts)
	if err != nil {
		return fmt.Errorf("update: invalid revision comparison: %w", err)
	}
	if ahead > 0 {
		return fmt.Errorf("update refused: local branch is ahead of or diverged from %s", remote)
	}
	if behind == 0 {
		fmt.Printf("already up to date (%s)\n", tuicmd.Version)
		return nil
	}
	if err := gitRun(root, "merge", "--ff-only", remote); err != nil {
		return fmt.Errorf("update: fast-forward failed: %w", err)
	}
	oldHead = strings.TrimSpace(oldHead)
	rollback := func(failure error) error { return updateFailure(root, oldHead, failure) }

	newVersion, err := gitOutput(root, "describe", "--tags", "--always", "--dirty=false")
	if err != nil {
		newVersion = "unknown"
	}
	tmp := filepath.Join(dir, ".evilcode-update-"+strconv.Itoa(os.Getpid()))
	defer os.Remove(tmp)
	args := []string{"build", "-trimpath", "-ldflags=-X=evilcode/internal/tuicmd.Version=" + strings.TrimSpace(newVersion), "-o", tmp, "./"}
	if out, err := commandOutput(root, "go", args...); err != nil {
		return rollback(fmt.Errorf("update: build failed; binary unchanged:\n%s", updateTrimOutput(out)))
	}
	if out, err := commandOutput(root, "go", "test", "./..."); err != nil {
		return rollback(fmt.Errorf("update: tests failed; binary unchanged:\n%s", updateTrimOutput(out)))
	}
	if err := os.Chmod(tmp, mode.Perm()); err != nil {
		return rollback(fmt.Errorf("update: preserving executable mode: %w", err))
	}
	if err := os.Rename(tmp, exe); err != nil {
		return rollback(fmt.Errorf("update: installing %s: %w\nmanual: go build -trimpath -o %s .", exe, err, shellQuote(exe)))
	}
	fmt.Printf("updated %s: %s -> %s\n", exe, tuicmd.Version, strings.TrimSpace(newVersion))
	return nil
}

func updateTarget() (exe string, mode os.FileMode, dir string, err error) {
	exe, err = os.Executable()
	if err != nil {
		return "", 0, "", fmt.Errorf("update: finding executable: %w", err)
	}
	exe, err = filepath.EvalSymlinks(exe)
	if err != nil {
		return "", 0, "", fmt.Errorf("update: resolving executable: %w", err)
	}
	info, err := os.Stat(exe)
	if err != nil {
		return "", 0, "", fmt.Errorf("update: stating executable: %w", err)
	}
	dir = filepath.Dir(exe)
	probe, err := os.CreateTemp(dir, ".evilcode-update-probe-*")
	if err != nil {
		return "", 0, "", fmt.Errorf("update: install path is not writable; manual: go build -trimpath -o %s .", shellQuote(exe))
	}
	probeName := probe.Name()
	_ = probe.Close()
	_ = os.Remove(probeName)
	return exe, info.Mode(), dir, nil
}

func gitRun(dir string, args ...string) error {
	_, err := commandOutput(dir, "git", args...)
	return err
}

func gitOutput(dir string, args ...string) (string, error) {
	return commandOutput(dir, "git", args...)
}

func commandOutput(dir, name string, args ...string) (string, error) {
	cmd := exec.Command(name, args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		return tools.Truncate(string(out)), err
	}
	return tools.Truncate(string(out)), nil
}

func parseAheadBehind(s string) (ahead, behind int, err error) {
	parts := strings.Fields(s)
	if len(parts) != 2 {
		return 0, 0, fmt.Errorf("want two counts, got %q", s)
	}
	ahead, err = strconv.Atoi(parts[0])
	if err != nil {
		return 0, 0, err
	}
	behind, err = strconv.Atoi(parts[1])
	return ahead, behind, err
}

func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", "'\\''") + "'"
}

// updateFailure puts the clean checkout back where it started when the fetched
// revision cannot build or test. If anything else dirtied it during the run,
// leave that work alone and say so rather than resetting user files.
func updateFailure(root, oldHead string, failure error) error {
	status, err := gitOutput(root, "status", "--porcelain")
	if err != nil || strings.TrimSpace(status) != "" {
		return fmt.Errorf("%w (source was not rolled back because the checkout changed)", failure)
	}
	if err := gitRun(root, "reset", "--hard", oldHead); err != nil {
		return fmt.Errorf("%w (source rollback failed: %v)", failure, err)
	}
	return failure
}

func updateTrimOutput(out string) string {
	lines := strings.Split(strings.TrimSpace(out), "\n")
	if len(lines) > 20 {
		lines = append(lines[:20], fmt.Sprintf("… and %d more lines", len(lines)-20))
	}
	return strings.Join(lines, "\n")
}
