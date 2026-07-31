package tui

import (
	"strings"
	"testing"
	"time"
)

func TestDefaultBindingsAreWellFormed(t *testing.T) {
	seenAction := map[Action]bool{}
	for _, b := range DefaultBindings {
		if b.Action == "" {
			t.Error("a binding has no action")
		}
		if seenAction[b.Action] {
			t.Errorf("duplicate binding for %s", b.Action)
		}
		seenAction[b.Action] = true

		if len(b.Keys) == 0 {
			t.Errorf("%s has no keys", b.Action)
		}
		if b.Desc == "" {
			t.Errorf("%s has no description; hotkey feedback needs one", b.Action)
		}
	}
}

func TestDefaultBindingsDoNotCollide(t *testing.T) {
	// A collision means one binding silently shadows another, which is the
	// kind of bug you only notice when a key mysteriously stops working.
	_, problems := NewKeymap(nil)
	for _, p := range problems {
		t.Errorf("default keymap problem: %s", p)
	}
}

func TestOverrideReplacesRatherThanAdds(t *testing.T) {
	// Rebinding is usually done to get a key back from evilcode; merging would
	// leave the old chord still captured.
	km, problems := NewKeymap(map[string]string{
		string(ActionScrollBookmark): "alt+k",
	})
	if len(problems) != 0 {
		t.Fatalf("unexpected problems: %v", problems)
	}
	if _, ok := km.Lookup("alt+k"); !ok {
		t.Error("the override key is not bound")
	}
	if _, ok := km.Lookup("ctrl+g"); ok {
		t.Error("the default key is still bound after an override")
	}
}

func TestOverrideAcceptsMultipleKeys(t *testing.T) {
	km, _ := NewKeymap(map[string]string{
		string(ActionPageUp): "alt+p, ctrl+b",
	})
	for _, k := range []string{"alt+p", "ctrl+b"} {
		if _, ok := km.Lookup(k); !ok {
			t.Errorf("%s is not bound", k)
		}
	}
}

func TestUnknownActionIsReported(t *testing.T) {
	// A rebind that does nothing is worse than one that says why.
	_, problems := NewKeymap(map[string]string{"nonsense_action": "ctrl+q"})
	if len(problems) == 0 {
		t.Fatal("an unknown action should be reported")
	}
	if !strings.Contains(problems[0], "nonsense_action") {
		t.Errorf("problem = %q, want it to name the action", problems[0])
	}
}

func TestCollisionIsReported(t *testing.T) {
	_, problems := NewKeymap(map[string]string{
		string(ActionScrollUp):   "ctrl+g",
		string(ActionScrollDown): "ctrl+g",
	})
	if len(problems) == 0 {
		t.Fatal("a collision should be reported")
	}
}

func TestNearestBindingSuggestsSomethingPlausible(t *testing.T) {
	km, _ := NewKeymap(nil)

	// Same base key, extra modifier: the near-miss should point at the real one.
	got, ok := km.NearestBinding("ctrl+shift+g")
	if !ok {
		t.Fatal("expected a suggestion for ctrl+shift+g")
	}
	if got.Action != ActionScrollBookmark {
		t.Errorf("suggested %s, want %s", got.Action, ActionScrollBookmark)
	}
}

func TestNearestBindingDeclinesWhenNothingIsClose(t *testing.T) {
	// A random suggestion is worse than none.
	km, _ := NewKeymap(nil)
	if _, ok := km.NearestBinding("ctrl+alt+shift+f19"); ok {
		t.Error("an unrelated chord should get no suggestion")
	}
}

func TestHotkeyUsageExplainsRareChordsThenStops(t *testing.T) {
	usage := LoadHotkeyUsage(t.TempDir())
	now := time.Now()

	explained := 0
	for i := 0; i < RareUseThreshold+3; i++ {
		if usage.Record("ctrl+g", now) {
			explained++
		}
	}
	if explained != RareUseThreshold {
		t.Errorf("explained %d times, want %d", explained, RareUseThreshold)
	}
}

func TestHotkeyHintReturnsAfterALongGap(t *testing.T) {
	// Muscle memory for a rarely-used chord decays; the reminder should come
	// back with it.
	usage := LoadHotkeyUsage(t.TempDir())
	now := time.Now()
	for i := 0; i < RareUseThreshold; i++ {
		usage.Record("alt+x", now)
	}
	if usage.Record("alt+x", now) {
		t.Fatal("a familiar chord should stay quiet")
	}
	if !usage.Record("alt+x", now.Add(ReminderAfter+time.Hour)) {
		t.Error("the hint should reappear after a long unused gap")
	}
}

func TestHotkeyUsagePersists(t *testing.T) {
	dir := t.TempDir()
	first := LoadHotkeyUsage(dir)
	now := time.Now()
	for i := 0; i < RareUseThreshold; i++ {
		first.Record("ctrl+r", now)
	}

	second := LoadHotkeyUsage(dir)
	if second.Record("ctrl+r", now) {
		t.Error("usage counts should survive a restart")
	}
}

func TestNearMissIsRateLimited(t *testing.T) {
	// Leaning on a key must not produce a wall of notices.
	usage := LoadHotkeyUsage(t.TempDir())
	now := time.Now()

	allowed := 0
	for i := 0; i < 10; i++ {
		// Space the presses past the gap so only the per-chord cap applies.
		if usage.AllowNearMiss("ctrl+shift+p", now.Add(time.Duration(i)*2*NearMissGap)) {
			allowed++
		}
	}
	if allowed != NearMissPerChord {
		t.Errorf("allowed %d hints, want the cap of %d", allowed, NearMissPerChord)
	}
}

func TestNearMissRespectsTheGap(t *testing.T) {
	usage := LoadHotkeyUsage(t.TempDir())
	now := time.Now()
	if !usage.AllowNearMiss("ctrl+shift+a", now) {
		t.Fatal("the first hint should be allowed")
	}
	if usage.AllowNearMiss("ctrl+shift+b", now.Add(NearMissGap/2)) {
		t.Error("a hint within the gap should be suppressed")
	}
}

func TestIsModifiedChord(t *testing.T) {
	// Telling someone that `q` is unbound would be noise; only deliberate
	// chords get near-miss feedback.
	for _, k := range []string{"ctrl+g", "alt+x", "ctrl+shift+j"} {
		if !IsModifiedChord(k) {
			t.Errorf("%q should count as a chord", k)
		}
	}
	for _, k := range []string{"a", "enter", "pgup", "up"} {
		if IsModifiedChord(k) {
			t.Errorf("%q should not count as a chord", k)
		}
	}
}

func TestPrettyKey(t *testing.T) {
	tests := map[string]string{
		"ctrl+g":      "Ctrl+G",
		"alt+shift+x": "Alt+Shift+X",
		"pgup":        "PgUp",
		"ctrl+up":     "Ctrl+Up",
		"enter":       "Enter",
	}
	for in, want := range tests {
		if got := PrettyKey(in); got != want {
			t.Errorf("PrettyKey(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestHotkeyHintRendering(t *testing.T) {
	r := testRenderer(80)
	got := plain(r.RenderHotkeyHint("ctrl+g", "toggle scroll bookmark"))
	for _, want := range []string{"⌨", "Ctrl+G", "→", "toggle scroll bookmark"} {
		if !strings.Contains(got, want) {
			t.Errorf("hint %q is missing %q", got, want)
		}
	}
}

func TestNearMissRendering(t *testing.T) {
	r := testRenderer(80)
	km, _ := NewKeymap(nil)
	nearest, found := km.NearestBinding("ctrl+shift+g")

	got := plain(r.RenderNearMiss("ctrl+shift+g", nearest, found))
	for _, want := range []string{"Ctrl+Shift+G", "isn't bound", "nearest"} {
		if !strings.Contains(got, want) {
			t.Errorf("near-miss %q is missing %q", got, want)
		}
	}
}

func TestThinkingModeCycles(t *testing.T) {
	// Three modes, and cycling must return to where it started.
	start := ThinkingCurrent
	seen := map[ThinkingMode]bool{}
	mode := start
	for i := 0; i < 3; i++ {
		seen[mode] = true
		mode = mode.Next()
	}
	if mode != start {
		t.Errorf("cycling three times gave %s, want %s", mode, start)
	}
	if len(seen) != 3 {
		t.Errorf("saw %d modes, want 3", len(seen))
	}
}

func TestCollapsedReasoningIsOneRow(t *testing.T) {
	r := testRenderer(80)
	b := Block{
		Kind:      BlockReasoning,
		Text:      "line one\nline two\nline three",
		Collapsed: true,
	}
	rows := plainLines(r.Lines(&b))
	if len(rows) != 1 {
		t.Fatalf("collapsed trace rendered %d rows, want 1", len(rows))
	}
	if !strings.Contains(rows[0], "▸ thought (3 lines)") {
		t.Errorf("row = %q", rows[0])
	}
}

func TestExpandedReasoningShowsTheTrace(t *testing.T) {
	r := testRenderer(80)
	b := Block{Kind: BlockReasoning, Text: "checking the fallback path"}
	joined := strings.Join(plainLines(r.Lines(&b)), "\n")
	if !strings.Contains(joined, "checking the fallback path") {
		t.Errorf("expanded trace = %q", joined)
	}
}
