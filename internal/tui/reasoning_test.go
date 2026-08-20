package tui

import (
	"slices"
	"strings"
	"testing"

	"evilcode/internal/agent"
	"evilcode/internal/provider"
)

func TestReasoningEffortCyclesAdvertisedModelLevels(t *testing.T) {
	var got provider.ReasoningEffort
	m := NewModel(nil, HeaderState{Provider: "deepseek", Model: "deepseek-reasoner"}).
		WithReasoningEfforts(provider.DeepSeekReasoningEfforts()).
		WithReasoningEffort(provider.ReasoningEffortNone, func(effort provider.ReasoningEffort) error {
			got = effort
			return nil
		})

	m.cycleReasoningEffort()
	if got != provider.ReasoningEffortHigh || m.reasoningEffort != provider.ReasoningEffortHigh {
		t.Fatalf("cycled effort = %q/%q, want high", got, m.reasoningEffort)
	}
	if m.header.ReasoningEffort != provider.ReasoningEffortHigh {
		t.Errorf("header effort = %q, want high", m.header.ReasoningEffort)
	}
	if !m.setEffort(provider.ReasoningEffortMax) || got != provider.ReasoningEffortMax {
		t.Errorf("explicit max effort = %q", got)
	}
	if m.setEffort(provider.ReasoningEffortMedium) {
		t.Error("medium should be rejected by DeepSeek's advertised levels")
	}
	if !strings.Contains(m.notice, "available: none, high, max") {
		t.Errorf("unsupported-level notice = %q", m.notice)
	}
}

func TestReasoningEffortUsesProviderModelFallback(t *testing.T) {
	a := agent.New("s", provider.NewOpenAI("openai", "http://example.invalid", ""),
		"gpt-5.2", nil, agent.NewConversation(""))
	m := NewModel(a, HeaderState{Provider: "openai", Model: "gpt-5.2"})

	if len(m.reasoningLevels) == 0 || !hasReasoningEffort(m.reasoningLevels, provider.ReasoningEffortXHigh) {
		t.Fatalf("gpt-5.2 levels = %v, want the OpenAI fallback levels", m.reasoningLevels)
	}
	if m.header.ReasoningEffort != provider.DefaultReasoningEffort {
		t.Errorf("initial header effort = %q, want %q", m.header.ReasoningEffort, provider.DefaultReasoningEffort)
	}
	if !m.setEffort(provider.ReasoningEffortHigh) {
		t.Fatal("high should be accepted for gpt-5.2")
	}
	if got := a.ReasoningEffort(); got != provider.ReasoningEffortHigh {
		t.Errorf("agent effort = %q, want high", got)
	}
}

func TestReasoningEffortRecognizesOllamaGLMModels(t *testing.T) {
	a := agent.New("s", provider.NewOllama("ollama-local", "", ""),
		"glm-5.2:cloud", nil, agent.NewConversation(""))
	m := NewModel(a, HeaderState{Provider: "ollama-local", Model: "glm-5.2:cloud"})

	if got, want := m.reasoningLevels, provider.OllamaReasoningEfforts(); !slices.Equal(got, want) {
		t.Fatalf("Ollama GLM levels = %v, want %v", got, want)
	}
	if m.header.ReasoningEffort != provider.ReasoningEffortMedium {
		t.Errorf("Ollama GLM header effort = %q, want medium", m.header.ReasoningEffort)
	}
}

func TestReasoningEffortRestoresPerModelAndPersistsChanges(t *testing.T) {
	a := agent.New("s", provider.NewOpenAI("openai", "http://example.invalid", ""),
		"gpt-5.6-luna", nil, agent.NewConversation(""))
	t.Cleanup(a.Close)

	var savedModel string
	var savedRef string
	var savedEffort provider.ReasoningEffort
	m := NewModel(a, HeaderState{Provider: "openai", Model: "gpt-5.6-luna"}).
		WithPersistentModelState(
			"",
			map[string]string{
				"gpt-5.6-luna@openai":  "max",
				"gpt-5.6-terra@openai": "high",
			},
			func(ref string) error {
				savedModel = ref
				return nil
			},
			func(ref string, effort provider.ReasoningEffort) error {
				savedRef, savedEffort = ref, effort
				return nil
			},
		)

	// Switching away restores Terra's remembered value and writes the global
	// last-model preference.
	m.applyModel(ModelEntry{
		Name:             "gpt-5.6-terra",
		Provider:         "openai",
		ReasoningEfforts: provider.OpenAIGPT56ReasoningEfforts(),
	})
	if m.reasoningEffort != provider.ReasoningEffortHigh {
		t.Errorf("Terra effort = %q, want high", m.reasoningEffort)
	}
	if savedModel != "gpt-5.6-terra@openai" {
		t.Errorf("saved model = %q, want gpt-5.6-terra@openai", savedModel)
	}
	if !m.setEffort(provider.ReasoningEffortMax) {
		t.Fatal("changing Terra's effort should succeed")
	}
	if savedRef != "gpt-5.6-terra@openai" || savedEffort != provider.ReasoningEffortMax {
		t.Errorf("saved Terra effort = %q/%q, want gpt-5.6-terra@openai/max", savedRef, savedEffort)
	}

	// And switching back restores Luna's independent value rather than Terra's.
	m.applyModel(ModelEntry{
		Name:             "gpt-5.6-luna",
		Provider:         "openai",
		ReasoningEfforts: provider.OpenAIGPT56ReasoningEfforts(),
	})
	if m.reasoningEffort != provider.ReasoningEffortMax {
		t.Errorf("Luna effort = %q, want max", m.reasoningEffort)
	}
}

func TestWithPersistentModelStateRestoresAnUnspecifiedInitialHeader(t *testing.T) {
	a := agent.New("s", provider.NewOpenAI("openai", "http://example.invalid", ""),
		"gpt-5.6-luna", nil, agent.NewConversation(""))
	t.Cleanup(a.Close)
	m := NewModel(a, HeaderState{Provider: "openai", Model: "gpt-5.6-luna"}).
		WithPersistentModelState("", map[string]string{
			"gpt-5.6-luna@openai": "max",
		}, nil, nil)

	if m.reasoningEffort != provider.ReasoningEffortMax {
		t.Errorf("initial effort = %q, want max", m.reasoningEffort)
	}
	if m.header.ReasoningEffort != provider.ReasoningEffortMax {
		t.Errorf("initial header effort = %q, want max", m.header.ReasoningEffort)
	}
}
