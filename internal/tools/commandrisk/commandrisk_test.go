package commandrisk

import (
	"fmt"
	"strings"
	"testing"
)

func testContext() Context {
	return Context{
		WorkspaceRoot: "/workspace/repo",
		WorkingDir:    "/workspace/repo/src",
		HomeDir:       "/home/tester",
		ConfigDir:     "/home/tester/.config/evilcode",
		DataDir:       "/home/tester/.local/share/evilcode",
	}
}

func TestAssessDestructiveTargets(t *testing.T) {
	cases := []struct {
		command string
		level   RiskLevel
	}{
		{"rm -rf /", Catastrophic},
		{"rm -rf ~", Catastrophic},
		{"rm -rf ~/", Catastrophic},
		{"rm -rf ${HOME}", Catastrophic},
		{"rm -rf ${HOME:-/}", Confirm},
		{"rm -rf $'/etc'", Confirm},
		{"rm -rf \"$HOME\"", Catastrophic},
		{"rm -rf /dev/sda", Catastrophic},
		{"rm -rf /{etc,var}", Catastrophic},
		{"rm -rf /{e,}tc", Catastrophic},
		{"rm -rf /{tmp,etc}", Catastrophic},
		{"rm -rf $HOME/.ssh", Catastrophic},
		{"rm -f $HOME/.config/evilcode/settings.toml", Confirm},
		{"rm -f /var/log/evilcode.log", Confirm},
		{"rm -rf $HOME/projects", Confirm},
		{"rm -rf ./build", Low},
		{"rm -rf ./build/*", Confirm},
		{"rm -rf ./{build,dist}", Low},
		{"rm -rf ./build/{1..100000}", Confirm},
		{"rm -rf /tmp/*", Confirm},
		{"rm -rf /workspace/repo", Confirm},
		{"rm -rf .git", Confirm},
		{"rm -rf /tmp/repo/.git", Confirm},
		{"git clean -xfd", Confirm},
		{"find . -delete", Low},
		{"printf x | rm -rf ./build", Confirm},
		{"printf x |& rm -rf ./build", Confirm},
		{"echo x > /etc/evilcode.conf", Catastrophic},
		{"echo x > /dev/sda", Catastrophic},
		{"echo x > /dev/null", Safe},
		{"echo x > ./build/log.txt", Safe},
		{"sudo rm -rf /", Catastrophic},
		{"busybox rm -rf /", Catastrophic},
		{"xargs -I {} sh -c 'rm -rf /'", Catastrophic},
		{"bash -c 'rm -rf /'", Catastrophic},
		{"sh -c \"$(printf 'rm -rf /')\"", Confirm},
		{"timeout 5 rm -rf /", Catastrophic},
		{"nice -n 10 rm -rf /", Catastrophic},
		{"nice 10 rm -rf /", Catastrophic},
		{"env -u FOO rm -rf /", Catastrophic},
		{"eval 'rm -rf /'", Catastrophic},
		{"eval rm -rf /", Catastrophic},
		{"! rm -rf /", Catastrophic},
		{"find . -exec rm -rf / \\;", Catastrophic},
		{"find . -delete -exec rm -rf / \\;", Catastrophic},
		{"sh -c '$UNTRUSTED_COMMAND'", Confirm},
		{"command -v rm", Safe},
		{"echo ready &&", Confirm},
		{";;;", Confirm},
		{"chmod 644 /home/tester/.config/evilcode/settings.toml", Safe},
		{"chmod -R 644 /home/tester/.config/evilcode", Confirm},
		{"find . -exec echo / \\;", Low},
		{"echo $(rm -rf /)", Catastrophic},
		{"rm -rf '/workspace/repo/build'", Low},
		{"rm -rf 'unterminated", Confirm},
	}

	for _, tc := range cases {
		t.Run(tc.command, func(t *testing.T) {
			assessment := Assess(tc.command, testContext())
			if assessment.Level != tc.level {
				t.Fatalf("Assess(%q) = %s, want %s (%s)", tc.command, assessment.Level, tc.level, assessment.Explanation())
			}
		})
	}
}

func TestAssessmentOrdinaryCommandsHasNoFalsePositives(t *testing.T) {
	commands := []string{
		"pwd", "ls -la", "git status --short", "git diff --stat", "go test ./internal/tools/commandrisk",
		"printf 'hello world\\n'", "echo ready", "mkdir -p ./build", "touch ./build/output.txt",
	}
	for i := 0; i < 100; i++ {
		commands = append(commands, fmt.Sprintf("printf 'line-%d\\n'", i))
	}
	for _, command := range commands {
		assessment := Assess(command, testContext())
		if assessment.Level > Low {
			t.Fatalf("ordinary command %q was elevated to %s: %s", command, assessment.Level, assessment.Explanation())
		}
	}
}

func TestGateRequiresSubstantiveJustification(t *testing.T) {
	assessment := Assess("rm -rf ../../outside", testContext())
	if assessment.Level != Confirm {
		t.Fatalf("expected confirm assessment, got %s", assessment.Level)
	}
	if verdict := Gate("rm -rf ../../outside", assessment, ""); verdict.Decision != DecisionReflect {
		t.Fatalf("empty justification decision = %s, want reflect", verdict.Decision)
	}
	if verdict := Gate("rm -rf ../../outside", assessment, "yes"); verdict.Decision != DecisionReflect {
		t.Fatalf("blind confirmation decision = %s, want reflect", verdict.Decision)
	}
	if verdict := Gate("rm -rf ../../outside", assessment, "This removes the generated build tree requested by the release task."); verdict.Decision != DecisionAllow {
		t.Fatalf("substantive justification decision = %s, want allow", verdict.Decision)
	}
	catastrophic := Assess("rm -rf /", testContext())
	if verdict := Gate("rm -rf /", catastrophic, "This is an extremely important reason that should never override the guard."); verdict.Decision != DecisionRefuse {
		t.Fatalf("catastrophic decision = %s, want refuse", verdict.Decision)
	}
}

func TestExplanationNamesExpandedProtectedTarget(t *testing.T) {
	assessment := Assess("rm -rf ~", testContext())
	if assessment.Level != Catastrophic || !strings.Contains(assessment.Explanation(), "/home/tester") {
		t.Fatalf("explanation = %q, want expanded home path", assessment.Explanation())
	}
}

func TestTokenizerHandlesSegmentsAndMalformedQuotes(t *testing.T) {
	segments := SplitSegments(Tokenize(`printf x | rm -rf ./build && echo done`))
	if len(segments) != 3 || len(segments[1]) == 0 || !segments[1][0].ReceivesPipe {
		t.Fatalf("unexpected segments: %#v", segments)
	}
	malformed := Tokenize(`echo 'not closed`)
	found := false
	for _, token := range malformed {
		found = found || token.Malformed
	}
	if !found {
		t.Fatal("unterminated quote was not marked malformed")
	}
}

func TestDangerousCommandCorpus(t *testing.T) {
	ctx := Context{WorkspaceRoot: "/home/u/proj", WorkingDir: "/home/u/proj", HomeDir: "/home/u"}
	catastrophic := []string{
		"rm -rf ~", "rm -rf $HOME", "rm -rf \"$HOME\"", "rm -rf ${HOME}",
		"rm -rf /home/u", "rm -fr ~/", "rm -rf /*", "rm -rf /etc",
		"rm --recursive --force /usr", "rm -rf ~/.ssh", "shred ~/.gnupg",
		"rm -rf ~/.aws", "echo safe && rm -rf ~", "find /home/u -delete",
		"rm -f /etc/passwd", "rm -rf /usr/bin/env", "rm /boot/vmlinuz",
		"shred /etc/shadow", "sudo env nice -n 5 rm -rf ~", "timeout 5 sudo rm -rf /",
		"find . -exec rm -rf / \\;",
	}
	for _, command := range catastrophic {
		if got := Assess(command, ctx).Level; got != Catastrophic {
			t.Errorf("Assess(%q) = %s, want catastrophic", command, got)
		}
	}

	immediate := []string{
		"rm -rf target", "rm -rf ./node_modules", "rm -f Cargo.lock",
		"rm -rf /home/u/proj/build", "rm -rf /tmp/scratch", "cargo clean",
		"ls -la", "git status", "echo hello > out.txt", "echo line >> /home/u/other/log.txt",
		"chmod 644 /home/u/other/file", "echo x > /dev/null",
	}
	for _, command := range immediate {
		if got := Assess(command, ctx).Level; got > Low {
			t.Errorf("Assess(%q) = %s, want safe/low", command, got)
		}
	}

	confirm := []string{
		"rm -rf /home/u/other-project", "rm -rf /srv/data", "shred /home/u/other/secrets.txt",
		"echo '' > /home/u/other/important.conf", "rm -rf $TARGET", "rm -rf $(cat list.txt)",
		"rm -rf", "rm -rf /var/log/app.log", "rm -f ~/.config/app/settings.toml",
		"sh -c \"$(printf 'rm -rf /')\"",
	}
	for _, command := range confirm {
		if got := Assess(command, ctx).Level; got != Confirm {
			t.Errorf("Assess(%q) = %s, want confirm", command, got)
		}
	}
}
