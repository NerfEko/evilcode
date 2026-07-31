package completions

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCompleteOffersSubcommandsFirst(t *testing.T) {
	got := Complete(nil)
	for _, want := range []string{"tui", "run", "serve", "attach"} {
		if !contains(got, want) {
			t.Errorf("%q missing from %v", want, got)
		}
	}
}

func TestCompleteStopsOfferingSubcommandsAfterOne(t *testing.T) {
	// `evilcode run <TAB>` suggesting `tui` proposes `evilcode run tui`, which
	// is not a thing.
	got := Complete([]string{"run"})
	if contains(got, "tui") {
		t.Errorf("a subcommand was offered after one was chosen: %v", got)
	}
	if !contains(got, "-remote") {
		t.Errorf("run's own flags are missing: %v", got)
	}
}

func TestCompleteKnowsProbesSecondLevel(t *testing.T) {
	got := Complete([]string{"probe"})
	for _, want := range []string{"render", "fonts", "hello"} {
		if !contains(got, want) {
			t.Errorf("%q missing from %v", want, got)
		}
	}
}

func TestCompleteKnowsTheShells(t *testing.T) {
	got := Complete([]string{"completions"})
	for _, want := range Shells {
		if !contains(got, want) {
			t.Errorf("%q missing from %v", want, got)
		}
	}
}

func TestCompleteAfterSocketDefersToTheShell(t *testing.T) {
	// A path is better completed by the shell's own file completion than by a
	// list this program invents.
	if got := Complete([]string{"serve", "-socket"}); len(got) != 0 {
		t.Errorf("offered %v for a path", got)
	}
}

func TestCompleteSessionsFromTheStore(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_DATA_HOME", dir)
	sessions := filepath.Join(dir, "evilcode", "sessions")
	if err := os.MkdirAll(sessions, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"bat", "crypt"} {
		if err := os.WriteFile(filepath.Join(sessions, name+".jsonl"), nil, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	got := Complete([]string{"tui", "-resume"})
	for _, want := range []string{"bat", "crypt"} {
		if !contains(got, want) {
			t.Errorf("%q missing from %v", want, got)
		}
	}
}

func TestCompleteNeverErrorsOnABrokenConfig(t *testing.T) {
	// A completion helper that errors leaves the user staring at a shell that
	// beeps, so a broken config completes nothing rather than complaining.
	dir := t.TempDir()
	path := filepath.Join(dir, "config.toml")
	if err := os.WriteFile(path, []byte("this is not = = toml"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("EVILCODE_CONFIG", path)

	// The call must return, whatever it returns.
	_ = Complete([]string{"run", "-m"})
}

func TestScriptsMentionTheirInstallLine(t *testing.T) {
	// A script a user cannot tell how to install is a script that never gets
	// installed.
	for _, shell := range Shells {
		var buf bytes.Buffer
		if err := WriteCompletions(&buf, shell); err != nil {
			t.Fatalf("%s: %v", shell, err)
		}
		out := buf.String()
		if !strings.Contains(out, "evilcode completions "+shell) {
			t.Errorf("%s script does not say how to install itself:\n%s", shell, out)
		}
		if !strings.Contains(out, "__complete") {
			t.Errorf("%s script never calls back for dynamic values:\n%s", shell, out)
		}
		// Errors from the helper must not reach the command line.
		if !strings.Contains(out, "2>/dev/null") {
			t.Errorf("%s script does not silence the helper's stderr:\n%s", shell, out)
		}
	}
}

func TestUnknownShellIsNamedBack(t *testing.T) {
	err := WriteCompletions(&bytes.Buffer{}, "csh")
	if err == nil {
		t.Fatal("an unknown shell was accepted")
	}
	if !strings.Contains(err.Error(), "csh") {
		t.Errorf("err = %v, want it to name the shell", err)
	}
}

func TestEverySubcommandInMainIsCompleted(t *testing.T) {
	// The two lists drift the first time a subcommand is added, and the failure
	// is silent: completion simply stops offering the new one.
	src, err := os.ReadFile(filepath.Join("..", "..", "main.go"))
	if err != nil {
		t.Fatal(err)
	}
	text := string(src)
	for _, sub := range Subcommands {
		if !strings.Contains(text, `case "`+sub+`"`) {
			t.Errorf("completions offer %q, which main.go does not handle", sub)
		}
	}
	// And the reverse: anything main.go handles and a person would type.
	for _, sub := range []string{"tui", "run", "serve", "attach", "probe", "dictate"} {
		if !contains(Subcommands, sub) {
			t.Errorf("main.go handles %q, which completions never offer", sub)
		}
	}
}

func contains(list []string, want string) bool {
	for _, s := range list {
		if s == want {
			return true
		}
	}
	return false
}
