package tui

import (
	"strings"
	"testing"
)

// The picker surfaces the Ctrl+O default-setter right at the model the cursor
// is on, so the binding is discoverable without reading the one-line key
// summary above the box. The hint appears only for a selectable model that is
// not already the default.
func TestPickerDefaultHintShownForNonDefaultSelection(t *testing.T) {
	entries := []ModelEntry{
		{Name: "deepseek-v4-flash:0731", Provider: "ollama-cloud", Default: true},
		{Name: "glm-5.2:cloud", Provider: "ollama-cloud"},
	}
	r := testRenderer(120)

	// Cursor on the default model: no "set as default" hint.
	joined := strings.Join(plainLines(r.RenderPicker(PickerState{Entries: entries, Selected: 0})), "\n")
	if strings.Contains(joined, "Ctrl+O set as default") {
		t.Errorf("default-selected row should not offer the set-default hint:\n%s", joined)
	}

	// Cursor on a non-default model: the hint appears.
	joined = strings.Join(plainLines(r.RenderPicker(PickerState{Entries: entries, Selected: 1})), "\n")
	if !strings.Contains(joined, "Ctrl+O set as default") {
		t.Errorf("non-default selected row should show the set-default hint:\n%s", joined)
	}

	// An unavailable model never offers the hint — it cannot be set as default.
	unavail := []ModelEntry{{Name: "gone:1b", Provider: "ollama-local", Unavailable: true, Detail: "not pulled"}}
	joined = strings.Join(plainLines(r.RenderPicker(PickerState{Entries: unavail, Selected: 0})), "\n")
	if strings.Contains(joined, "Ctrl+O set as default") {
		t.Errorf("unavailable row should not offer the set-default hint:\n%s", joined)
	}
}