package tui

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	tea "charm.land/bubbletea/v2"
)

// SelfdevPrompt opens a session on evilcode's own repository (plan.md §5, the
// Phase 5 graduation).
//
// It names the skill rather than restating it. The loop is long, it changes,
// and a copy of it embedded in a Go string is a copy that goes stale — the
// skill file is the one that gets edited.
const SelfdevPrompt = `You are working on evilcode itself — the program you are running inside.

Load the ` + "`selfdev`" + ` skill and follow it exactly. It encodes the loop:
pick the next unchecked task in plan.md, implement it, build and vet and test,
look at the rendered frame if it is visible in the TUI, update goldens, check
the task off, commit, and write the LOOPS.md entry.

Start by reading plan.md and telling me which task is next and why. Do not begin
implementing until you have said what you are about to do.`

// IsEvilcodeRepo reports whether a directory is evilcode's own source.
//
// Checked by content rather than by name: a clone called something else is
// still evilcode, and a directory called evilcode that is not the source would
// otherwise get a session claiming it could rebuild itself.
func IsEvilcodeRepo(dir string) bool {
	mod, err := os.ReadFile(filepath.Join(dir, "go.mod"))
	if err != nil {
		return false
	}
	if !strings.Contains(string(mod), "module evilcode") {
		return false
	}
	_, err = os.Stat(filepath.Join(dir, "plan.md"))
	return err == nil
}

// selfdevCommand implements `/selfdev`.
func (m *Model) selfdevCommand() tea.Cmd {
	if !IsEvilcodeRepo(m.cwd) {
		m.notice = "/selfdev only works inside evilcode's own repository " +
			"(no go.mod naming module evilcode here)"
		return nil
	}
	if m.processing {
		m.notice = "finish the current turn first"
		return nil
	}

	m.blocks = append(m.blocks, Block{Kind: BlockNotice, Text: strings.Join([]string{
		"🦇 Self-development mode.",
		"",
		"Working on " + m.cwd,
		"The `selfdev` skill has the loop. /rebuild to build and restart, " +
			"/reload to restart on the current binary.",
	}, "\n")})
	m.scroll.FollowBottom()
	m.submitHidden(SelfdevPrompt)
	return nil
}

// rebuildCommand implements `/rebuild`: build, test, and restart into the new
// binary, keeping the session.
//
// Tests run before the restart, not after. Restarting into a binary that does
// not pass its own tests is how a self-developing program locks itself out.
func (m *Model) rebuildCommand() tea.Cmd {
	if !IsEvilcodeRepo(m.cwd) {
		m.notice = "/rebuild only works inside evilcode's own repository"
		return nil
	}
	m.notice = "🔄 Building and testing..."

	return func() tea.Msg {
		build := exec.Command("go", "build", "-o", "evilcode", "./")
		build.Dir = m.cwd
		if out, err := build.CombinedOutput(); err != nil {
			return rebuildResult{err: fmt.Errorf("build failed:\n%s", trimOutput(out))}
		}

		test := exec.Command("go", "test", "./...")
		test.Dir = m.cwd
		if out, err := test.CombinedOutput(); err != nil {
			return rebuildResult{err: fmt.Errorf(
				"tests failed — not restarting:\n%s", trimOutput(out))}
		}
		return rebuildResult{ok: true}
	}
}

// rebuildResult carries a build's outcome back into the render loop.
type rebuildResult struct {
	ok  bool
	err error
}

// trimOutput keeps a command's failure readable in a transcript block.
func trimOutput(out []byte) string {
	lines := strings.Split(strings.TrimSpace(string(out)), "\n")
	const keep = 20
	if len(lines) > keep {
		lines = append(lines[:keep], fmt.Sprintf("… and %d more lines", len(lines)-keep))
	}
	return strings.Join(lines, "\n")
}

// reloadCommand implements `/reload`: re-exec the current binary, resuming this
// session.
//
// exec rather than spawn-and-exit: the terminal keeps one process, the session
// name carries over, and there is no window where two copies are both reading
// the keyboard.
func (m *Model) reloadCommand() tea.Cmd {
	if m.store == nil {
		m.notice = "no session to resume across a reload"
		return nil
	}
	return tea.Sequence(tea.Quit, func() tea.Msg {
		return reloadRequest{session: m.store.Name}
	})
}

// reloadRequest asks the caller to re-exec. It is returned rather than executed
// here so the TUI is fully torn down first — exec'ing from inside the render
// loop leaves the terminal in raw mode.
type reloadRequest struct{ session string }

// ReloadTarget is the session to resume after the TUI exits, or "".
func (m *Model) ReloadTarget() string { return m.reloadTo }

// Reexec replaces this process with a fresh one resuming the named session.
func Reexec(session string) error {
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	args := []string{exe}
	if session != "" {
		args = append(args, "-resume", session)
	}
	return syscallExec(exe, args, os.Environ())
}
