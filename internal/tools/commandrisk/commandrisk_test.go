package commandrisk

import (
	"fmt"
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
		{"rm -rf /dev/sda", Catastrophic},
		{"rm -rf $HOME/.ssh", Catastrophic},
		{"rm -rf $HOME/projects", Confirm},
		{"rm -rf ./build", Low},
		{"rm -rf /workspace/repo", Confirm},
		{"rm -rf .git", Confirm},
		{"git clean -xfd", Confirm},
		{"find . -delete", Low},
		{"printf x | rm -rf ./build", Confirm},
		{"echo x > /etc/evilcode.conf", Catastrophic},
		{"echo x > ./build/log.txt", Safe},
		{"sudo rm -rf /", Catastrophic},
		{"bash -c 'rm -rf /'", Catastrophic},
		{"timeout 5 rm -rf /", Catastrophic},
		{"nice -n 10 rm -rf /", Catastrophic},
		{"env -u FOO rm -rf /", Catastrophic},
		{"eval 'rm -rf /'", Catastrophic},
		{"! rm -rf /", Catastrophic},
		{"find . -exec rm -rf / \\;", Catastrophic},
		{"sh -c '$UNTRUSTED_COMMAND'", Confirm},
		{"command -v rm", Safe},
		{"echo ready &&", Confirm},
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
