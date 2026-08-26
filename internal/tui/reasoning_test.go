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
	if got != provider.ReasoningEffortLow || m.reasoningEffort != provider.ReasoningEffortLow {
		t.Fatalf("cycled effort = %q/%q, want low (the level after none)", got, m.reasoningEffort)
	}
	if m.header.ReasoningEffort != provider.ReasoningEffortLow {
		t.Errorf("header effort = %q, want low", m.header.ReasoningEffort)
	}
	if !m.setEffort(provider.ReasoningEffortMax) || got != provider.ReasoningEffortMax {
		t.Errorf("explicit max effort = %q", got)
	}
	if m.setEffort(provider.ReasoningEffortMedium) {
		t.Error("medium should be rejected by DeepSeek's advertised levels")
	}
	if !strings.Contains(m.notice, "available: none, low, high, max") {
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

func TestReasoningMenuShowsOnlyGLM53DocumentedLevels(t *testing.T) {
	// GLM-5.3-Flash always reasons with low/high/max; the menu must not offer
	// none or medium, which the model does not accept.
	a := agent.New("s", provider.NewOllama("ollama-local", "", ""),
		"glm-5.3-flash", nil, agent.NewConversation(""))
	m := NewModel(a, HeaderState{Provider: "ollama-local", Model: "glm-5.3-flash"})

	want := provider.GLM53ReasoningEfforts()
	if !slices.Equal(m.reasoningLevels, want) {
		t.Fatalf("glm-5.3-flash menu levels = %v, want %v", m.reasoningLevels, want)
	}
	sel := ModelEntry{Name: "glm-5.3-flash", Provider: "ollama-local"}
	if !m.openReasoningPicker(sel) {
		t.Fatal("expected the reasoning picker to open")
	}
	var shown []provider.ReasoningEffort
	for _, level := range m.reasoningPicker.levels {
		shown = append(shown, level)
	}
	if !slices.Equal(shown, want) {
		t.Errorf("reasoning picker shows %v, want %v", shown, want)
	}
	if got := m.reasoningPicker.levels[m.reasoningPicker.selected]; got != provider.ReasoningEffortHigh {
		t.Errorf("picker default = %q, want high", got)
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

// TestPickerShowsReasoningMenuAfterModelSelection drives the model picker the
// way keypresses do: the first Enter opens a reasoning-level menu instead of
// applying the model, and the second Enter accepts the highlighted level. The
// menu highlights the model's last used effort, or high when none is remembered.
func TestPickerShowsReasoningMenuAfterModelSelection(t *testing.T) {
	a := agent.New("s", provider.NewOpenAI("openai", "http://example.invalid", ""),
		"gpt-5.6-luna", nil, agent.NewConversation(""))
	t.Cleanup(a.Close)

	var savedRef string
	var savedEffort provider.ReasoningEffort
	m := NewModel(a, HeaderState{Provider: "openai", Model: "gpt-5.6-luna"}).
		WithPersistentModelState(
			"",
			map[string]string{"gpt-5.6-luna@openai": "max"},
			nil,
			func(ref string, effort provider.ReasoningEffort) error {
				savedRef, savedEffort = ref, effort
				return nil
			},
		)
	m.pickerOpen = true
	m.picker.Entries = []ModelEntry{
		{Name: "gpt-5.6-luna", Provider: "openai", Current: true,
			ReasoningEfforts: provider.OpenAIGPT56ReasoningEfforts()},
		{Name: "gpt-5.6-terra", Provider: "openai",
			ReasoningEfforts: provider.OpenAIGPT56ReasoningEfforts()},
	}
	m.picker.Selected = 1

	// First Enter: the model is not applied yet; the reasoning menu opens.
	if _, _ = m.handlePickerKey("enter"); m.header.Model != "gpt-5.6-luna" {
		t.Fatalf("first Enter applied the model early: header.Model = %q", m.header.Model)
	}
	if !m.reasoningPickerOpen {
		t.Fatal("first Enter should open the reasoning-level menu")
	}
	if m.pickerOpen {
		t.Fatal("the model picker should close when the reasoning menu opens")
	}
	// Terra has no remembered effort, so the highlight defaults to high.
	if got := m.reasoningPicker.levels[m.reasoningPicker.selected]; got != provider.ReasoningEffortHigh {
		t.Errorf("default highlight = %q, want high", got)
	}

	// Second Enter: the highlighted level is applied with the model.
	if _, _ = m.handleReasoningPickerKey("enter"); m.header.Model != "gpt-5.6-terra" {
		t.Fatalf("second Enter should apply the model: header.Model = %q", m.header.Model)
	}
	if m.reasoningEffort != provider.ReasoningEffortHigh {
		t.Errorf("applied effort = %q, want high", m.reasoningEffort)
	}
	if m.reasoningPickerOpen {
		t.Error("the reasoning menu should close after confirming")
	}
	if savedRef != "gpt-5.6-terra@openai" || savedEffort != provider.ReasoningEffortHigh {
		t.Errorf("saved effort = %q/%q, want gpt-5.6-terra@openai/high", savedRef, savedEffort)
	}
}

// TestPickerReasoningMenuHighlightsLastUsedLevel checks that a model with a
// remembered effort highlights that level rather than the high default.
func TestPickerReasoningMenuHighlightsLastUsedLevel(t *testing.T) {
	a := agent.New("s", provider.NewOpenAI("openai", "http://example.invalid", ""),
		"gpt-5.6-luna", nil, agent.NewConversation(""))
	t.Cleanup(a.Close)

	m := NewModel(a, HeaderState{Provider: "openai", Model: "gpt-5.6-luna"}).
		WithPersistentModelState("", map[string]string{
			"gpt-5.6-luna@openai": "max",
		}, nil, nil)
	m.pickerOpen = true
	m.picker.Entries = []ModelEntry{
		{Name: "gpt-5.6-luna", Provider: "openai", Current: true,
			ReasoningEfforts: provider.OpenAIGPT56ReasoningEfforts()},
	}
	m.picker.Selected = 0

	if _, _ = m.handlePickerKey("enter"); !m.reasoningPickerOpen {
		t.Fatal("first Enter should open the reasoning-level menu")
	}
	if got := m.reasoningPicker.levels[m.reasoningPicker.selected]; got != provider.ReasoningEffortMax {
		t.Errorf("highlight = %q, want max (the remembered level)", got)
	}
}

// TestPickerSkipsReasoningMenuForNonReasoningModel checks that a model with no
// reasoning levels is applied directly, with no second menu.
func TestPickerSkipsReasoningMenuForNonReasoningModel(t *testing.T) {
	a := agent.New("s", provider.NewMock("mock", "chat"), "mock-large", nil,
		agent.NewConversation("system"))
	t.Cleanup(a.Close)
	m := NewModel(a, HeaderState{Model: "mock-large", Provider: "mock"})
	m.pickerOpen = true
	m.picker.Entries = []ModelEntry{
		{Name: "mock-large", Provider: "mock", Current: true},
		{Name: "mock-small", Provider: "mock"},
	}
	m.picker.Selected = 1

	if _, _ = m.handlePickerKey("enter"); m.header.Model != "mock-small" {
		t.Fatalf("header.Model = %q, want mock-small applied directly", m.header.Model)
	}
	if m.reasoningPickerOpen {
		t.Error("a non-reasoning model should not open the reasoning menu")
	}
}

// TestPickerReasoningMenuEscapeCancels checks that Esc closes the reasoning
// menu without applying the model.
func TestPickerReasoningMenuEscapeCancels(t *testing.T) {
	a := agent.New("s", provider.NewOpenAI("openai", "http://example.invalid", ""),
		"gpt-5.6-luna", nil, agent.NewConversation(""))
	t.Cleanup(a.Close)
	m := NewModel(a, HeaderState{Provider: "openai", Model: "gpt-5.6-luna"})
	m.pickerOpen = true
	m.picker.Entries = []ModelEntry{
		{Name: "gpt-5.6-luna", Provider: "openai", Current: true,
			ReasoningEfforts: provider.OpenAIGPT56ReasoningEfforts()},
		{Name: "gpt-5.6-terra", Provider: "openai",
			ReasoningEfforts: provider.OpenAIGPT56ReasoningEfforts()},
	}
	m.picker.Selected = 1

	if _, _ = m.handlePickerKey("enter"); !m.reasoningPickerOpen {
		t.Fatal("first Enter should open the reasoning-level menu")
	}
	if _, _ = m.handleReasoningPickerKey("esc"); m.reasoningPickerOpen {
		t.Error("Esc should close the reasoning menu")
	}
	if m.header.Model != "gpt-5.6-luna" {
		t.Errorf("Esc should not apply the model: header.Model = %q", m.header.Model)
	}
}
