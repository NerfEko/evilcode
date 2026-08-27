package tools

import (
	"strings"
	"testing"
)

// R2-16: a model-run shell command used to inherit the daemon's entire
// environment — provider API keys, harness secrets, unrelated build
// configuration — one `env` away from the model. It now gets an allowlist
// (process basics, locale, XDG user directories) plus whatever the user
// passed through by name.

func TestModelRunCommandsDoNotInheritTheDaemonEnvironment(t *testing.T) {
	t.Setenv("EVILCODE_TEST_SECRET", "hunter2")
	t.Setenv("OPENAI_API_KEY_TEST_LEAK", "sk-leak")
	t.Setenv("MY_TOOL_TOKEN", "tok")

	e := NewExec(t.TempDir())
	res, err := run(t, e.Tools(), "bash", map[string]any{
		"cmd": `printf '%s|%s|%s|%s' "${EVILCODE_TEST_SECRET:-unset}" "${OPENAI_API_KEY_TEST_LEAK:-unset}" "${MY_TOOL_TOKEN:-unset}" "${PATH:+set}"`,
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.Output != "unset|unset|unset|set" {
		t.Fatalf("model-run command saw environment it must not: %q", res.Output)
	}
}

// The allowlist keeps the names commands legitimately need.
func TestModelRunCommandsKeepTheBasics(t *testing.T) {
	t.Setenv("LC_ALL", "C.UTF-8")
	t.Setenv("XDG_CACHE_HOME", "/tmp/xdg-cache")
	e := NewExec(t.TempDir())
	res, err := run(t, e.Tools(), "bash", map[string]any{
		"cmd": `printf '%s|%s|%s' "${PATH:+set}" "${LC_ALL:-unset}" "${XDG_CACHE_HOME:-unset}"`,
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.Output != "set|C.UTF-8|/tmp/xdg-cache" {
		t.Fatalf("allowlisted variables missing: %q", res.Output)
	}
}

// The explicit pass-through: a user-configured name reaches the command, and
// only that name.
func TestEnvPassthroughForwardsOnlyNamedVariables(t *testing.T) {
	t.Setenv("MY_TOOL_TOKEN", "tok")
	t.Setenv("MY_OTHER_TOKEN", "other")

	e := NewExec(t.TempDir()).WithEnvPassthrough([]string{"MY_TOOL_TOKEN"})
	res, err := run(t, e.Tools(), "bash", map[string]any{
		"cmd": `printf '%s|%s' "${MY_TOOL_TOKEN:-unset}" "${MY_OTHER_TOKEN:-unset}"`,
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.Output != "tok|unset" {
		t.Fatalf("pass-through result = %q, want tok|unset", res.Output)
	}
}

func TestCommandEnvDropsScratchAndKeepsFallbackTmpdir(t *testing.T) {
	base := t.TempDir()
	t.Setenv("TMPDIR", "/custom/tmp")
	e := NewExec(base) // no scratch dir
	env := e.commandEnv()
	var sawTMP bool
	for _, entry := range env {
		if strings.HasPrefix(entry, "TMPDIR=") {
			if entry != "TMPDIR=/custom/tmp" {
				t.Fatalf("TMPDIR = %q, want the process value", entry)
			}
			sawTMP = true
		}
		if strings.HasPrefix(entry, "EVILCODE_") {
			t.Fatalf("harness variable leaked into the command environment: %q", entry)
		}
	}
	if !sawTMP {
		t.Fatal("TMPDIR disappeared entirely")
	}
}
