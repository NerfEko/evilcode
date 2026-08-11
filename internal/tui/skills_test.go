package tui

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"evilcode/internal/agent"
	"evilcode/internal/provider"
	"evilcode/internal/tools"
)

func TestSkillsCommandListsSourcesAndRefreshesPrompt(t *testing.T) {
	root := t.TempDir()
	first := filepath.Join(root, "first")
	if err := os.MkdirAll(first, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(first, "SKILL.md"), []byte(
		"---\ndescription: first skill\n---\nbody\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	set := tools.LoadSkills([]string{root})
	a := agent.New("s", provider.NewMock("mock", "chat"), "mock", nil,
		agent.NewConversation(agent.BuildSystemPrompt(agent.ProjectContext{}, nil, "")))
	t.Cleanup(a.Close)
	m := NewModel(a, HeaderState{SessionName: "s"}).WithSkills(set, agent.ProjectContext{})

	m.runCommandWithArg("skills", "")
	if len(m.blocks) != 1 || !strings.Contains(m.blocks[0].Text, "source: "+first) ||
		!strings.Contains(m.blocks[0].Text, "first skill") {
		t.Fatalf("/skills block = %+v", m.blocks)
	}

	second := filepath.Join(root, "second")
	if err := os.MkdirAll(second, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(second, "SKILL.md"), []byte(
		"---\ndescription: second skill\n---\nbody\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	m.runCommandWithArg("skills", "reload")
	if !strings.Contains(a.Conv.SystemPrompt(), "second: second skill") {
		t.Fatalf("reloaded system prompt = %q", a.Conv.SystemPrompt())
	}
}
