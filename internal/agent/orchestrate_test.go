package agent

import (
	"context"
	"strings"
	"testing"

	"evilcode/internal/provider"
)

func TestSystemPromptCarriesFanOutBullet(t *testing.T) {
	prompt := BuildSystemPrompt(ProjectContext{}, nil, "")
	for _, want := range []string{
		"To fan out",
		"spawn_worker",
		"never spawn for what one read answers",
	} {
		if !strings.Contains(prompt, want) {
			t.Errorf("system prompt is missing fan-out guidance %q", want)
		}
	}
}

func TestHasOrchestrateKeyword(t *testing.T) {
	fires := []string{
		"orchestrate",
		"please orchestrate the refactor",
		"run orchestrate.",
		"orchestrate, then verify",
		"the orchestrate keyword arms the mode",
		"lets orchestrate\nthe migration",
	}
	for _, text := range fires {
		if !HasOrchestrateKeyword(text) {
			t.Errorf("HasOrchestrateKeyword(%q) = false, want true", text)
		}
	}
	silent := []string{
		"",
		"Orchestrate this",
		"ORCHESTRATE",
		"orchestrated the deploy",
		"reorchestrate everything",
		"orchestrates",
		`"orchestrate" the thing`,
		"'orchestrate' now",
		"`orchestrate` the migration",
		"see a/orchestrate/b for details",
		"/orchestrate the flags",
		"open orchestrate.md",
		"just refactor it",
	}
	for _, text := range silent {
		if HasOrchestrateKeyword(text) {
			t.Errorf("HasOrchestrateKeyword(%q) = true, want false", text)
		}
	}
}

func TestOrchestrateHookInjectsContractOnce(t *testing.T) {
	ag := New("test", nil, "mock", nil, NewConversation("system"))
	hook := NewOrchestrateHook(true)
	ag.Conv.Append(provider.Message{Role: provider.RoleUser, Content: "please orchestrate the refactor"})
	appended, err := hook.PostTurn(context.Background(), ag)
	if err != nil {
		t.Fatal(err)
	}
	if !appended {
		t.Fatal("hook did not fire on the keyword message")
	}
	if !hook.Active() {
		t.Fatal("hook is not armed after firing")
	}
	last, _ := ag.Conv.Last()
	if last.Role != provider.RoleSystem || !strings.Contains(last.Content, "spawn_worker") {
		t.Fatalf("contract not injected as a system message: %+v", last)
	}
	if appended, _ := hook.PostTurn(context.Background(), ag); appended {
		t.Fatal("hook fired twice for one keyword message")
	}
}

func TestOrchestrateHookDisabledIgnoresKeyword(t *testing.T) {
	ag := New("test", nil, "mock", nil, NewConversation("system"))
	hook := NewOrchestrateHook(false)
	ag.Conv.Append(provider.Message{Role: provider.RoleUser, Content: "orchestrate this"})
	if appended, _ := hook.PostTurn(context.Background(), ag); appended {
		t.Fatal("disabled hook injected the contract")
	}
	if hook.Active() {
		t.Fatal("disabled hook armed")
	}
}

func TestOrchestrateHookArmDisarmRoundTrip(t *testing.T) {
	ag := New("test", nil, "mock", nil, NewConversation("system"))
	hook := NewOrchestrateHook(true)
	hook.Arm()
	if !hook.Active() {
		t.Fatal("Arm did not arm")
	}
	if appended, _ := hook.PostTurn(context.Background(), ag); !appended {
		t.Fatal("explicit Arm did not queue the contract")
	}
	hook.Disarm()
	if hook.Active() {
		t.Fatal("Disarm did not disarm")
	}
	// The old keyword message must not re-arm: only a new one does.
	ag.Conv.Append(provider.Message{Role: provider.RoleUser, Content: "orchestrate again"})
	if appended, _ := hook.PostTurn(context.Background(), ag); !appended {
		t.Fatal("a new keyword message after Disarm did not re-arm")
	}
}
