package tui

import (
	"strings"
	"testing"
	"time"

	"evilcode/internal/agent"
	"evilcode/internal/theme"
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
