package tui

import (
	"strings"
	"testing"
	"time"

	"evilcode/internal/agent"
	"evilcode/internal/theme"
	"evilcode/internal/tools"
)

func TestOrchestrateCommandRoundTripsLocally(t *testing.T) {
	m := newTestModel(t).WithOrchestrate(agent.NewOrchestrateHook(true))
	m.orchestrateCommand("status")
	if m.orchestrator {
		t.Fatal("orchestrator mode armed itself")
	}
	m.orchestrateCommand("on")
	if !m.orchestrator || !m.orchestrate.Active() {
		t.Fatal("/orchestrate on did not arm the flag and the hook")
	}
	m.orchestrateCommand("bogus")
	if !strings.Contains(m.notice, "usage") {
		t.Fatalf("bad arg notice = %q, want usage", m.notice)
	}
	m.orchestrateCommand("off")
	if m.orchestrator || m.orchestrate.Active() {
		t.Fatal("/orchestrate off did not disarm the flag and the hook")
	}
}

func TestOrchestratorGatesTodoAndPoke(t *testing.T) {
	m := newTestModel(t)
	m.agent.SetTools(tools.Set{{Name: "todo"}, {Name: "read"}})
	poke := agent.NewPokeHook(nil, true)
	m.WithTodos(nil, poke).WithOrchestrate(agent.NewOrchestrateHook(true))
	m.editor.Text = "/"

	m.orchestrateCommand("on")
	if !m.agent.ToolBlocked("todo") {
		t.Fatal("todo tool remained available in orchestrator mode")
	}
	if poke.Enabled() {
		t.Fatal("auto-poke remained enabled in orchestrator mode")
	}
	for _, suggestion := range m.paletteSuggestions() {
		if suggestion.Name == "todo" || suggestion.Name == "todos" || suggestion.Name == "poke" {
			t.Fatalf("gated command was advertised: %q", suggestion.Name)
		}
	}
	help := strings.Join(plainLines(m.renderer.RenderHelpFor(0, 100, 200, true)), "\n")
	if strings.Contains(help, "/todos") || strings.Contains(help, "/poke") {
		t.Fatalf("gated command leaked into help: %s", help)
	}

	m.orchestrateCommand("off")
	if m.agent.ToolBlocked("todo") {
		t.Fatal("todo tool stayed blocked after orchestrator mode ended")
	}
	if !poke.Enabled() {
		t.Fatal("auto-poke was not restored after orchestrator mode ended")
	}
}

func TestOrchestrateCommandForwardsWhenAttached(t *testing.T) {
	m := newTestModel(t)
	var gotKind, gotArg string
	m.remoteCommand = func(kind, arg, secret string) error {
		gotKind, gotArg = kind, arg
		return nil
	}
	m.orchestrateCommand("on")
	if gotKind != "orchestrate" || gotArg != "on" {
		t.Fatalf("forwarded (%q, %q), want (orchestrate, on)", gotKind, gotArg)
	}
	if !m.orchestrator {
		t.Fatal("attached /orchestrate on did not arm the local tint")
	}
	m.orchestrateCommand("off")
	if m.orchestrator {
		t.Fatal("attached /orchestrate off did not drop the local tint")
	}
}

func TestKeywordSubmitArmsOrchestratorTint(t *testing.T) {
	m := newTestModel(t).WithOrchestrate(agent.NewOrchestrateHook(true))
	m.armOrchestratorFromKeyword("please orchestrate the refactor")
	if !m.orchestrator {
		t.Fatal("keyword did not arm the tint")
	}
	if m.swarm != nil {
		t.Fatal("test model should not have swarm state")
	}
	m2 := newTestModel(t)
	m2.WithOrchestrate(agent.NewOrchestrateHook(true))
	m2.armOrchestratorFromKeyword("just refactor it")
	if m2.orchestrator {
		t.Fatal("plain prompt armed orchestrator mode")
	}
}

func TestComposerTintMarksOrchestratorMode(t *testing.T) {
	r := NewRenderer(theme.Dracula(), 60)
	plain := strings.Join(plainLines(r.RenderComposer(ComposerState{Text: "hi", PromptNumber: 1})), "\n")
	glow := strings.Join(plainLines(r.RenderComposer(ComposerState{Text: "hi", PromptNumber: 1, Orchestrator: true})), "\n")
	if plain != glow {
		t.Fatal("tint leaked into plain text")
	}
	rawPlain := r.RenderComposer(ComposerState{Text: "hi", PromptNumber: 1})
	rawGlow := r.RenderComposer(ComposerState{Text: "hi", PromptNumber: 1, Orchestrator: true, Elapsed: 10 * time.Second})
	if strings.Join(rawPlain, "\n") == strings.Join(rawGlow, "\n") {
		t.Fatal("orchestrator tint did not change the composer render")
	}
}

func TestAgentsRendersWorkerModelAndTokens(t *testing.T) {
	m := newTestModel(t)
	swarm := &SwarmState{}
	swarm.Publish([]SwarmAgent{
		{Name: "bat", Task: "wiring auth", Worker: true, Running: true, Since: 42 * time.Second, Model: "small@mock", Tokens: 1500},
		{Name: "ed", Since: 90 * time.Second},
	})
	m.swarm = swarm
	m.agentsCommand()
	if len(m.blocks) == 0 {
		t.Fatal("agents produced no output")
	}
	text := m.blocks[len(m.blocks)-1].Text
	if !strings.Contains(text, "small@mock") || !strings.Contains(text, "tok") {
		t.Fatalf("agents output = %q, want model and tokens", text)
	}
	for _, line := range strings.Split(text, "\n") {
		if strings.HasPrefix(line, "ed ") && strings.Contains(line, "tok") {
			t.Fatalf("cost leaked onto the plain session row: %q", line)
		}
	}
}
func TestSwarmRosterRainbowKeepsNamesLegible(t *testing.T) {
	r := NewRenderer(theme.Dracula(), 60)
	s := &SwarmState{}
	s.Publish([]SwarmAgent{
		{Name: "bat", Task: "wiring auth", Worker: true, Running: true, Since: 42 * time.Second},
		{Name: "raven", Task: "survey", Worker: true, Since: 90 * time.Second},
	})
	flat := strings.Join(plainLines(r.SwarmStatusWidget(s, 0).Lines), "\n")
	s.Orchestrator = true
	rainbow := strings.Join(plainLines(r.SwarmStatusWidget(s, 0).Lines), "\n")
	if flat != rainbow {
		t.Fatalf("rainbow changed plain text:\n%s\nvs\n%s", flat, rainbow)
	}
	for _, name := range []string{"bat", "raven"} {
		if !strings.Contains(rainbow, name) {
			t.Errorf("roster lost %q in rainbow mode", name)
		}
	}
}
