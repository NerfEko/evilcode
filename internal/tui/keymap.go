package tui

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"charm.land/lipgloss/v2"

	"evilcode/internal/theme"
)

// Action names a rebindable command (plan.md §11). Actions are strings rather
// than an enum so a config file names them directly and an unknown one can be
// reported instead of silently ignored.
type Action string

const (
	ActionScrollUp        Action = "scroll_up"
	ActionScrollDown      Action = "scroll_down"
	ActionPageUp          Action = "page_up"
	ActionPageDown        Action = "page_down"
	ActionPrevPrompt      Action = "prev_prompt"
	ActionNextPrompt      Action = "next_prompt"
	ActionScrollBookmark  Action = "scroll_bookmark"
	ActionCenteredToggle  Action = "centered_toggle"
	ActionInfoWidgets     Action = "info_widget_toggle"
	ActionTodoCard        Action = "todo_card_toggle"
	ActionDiffMode        Action = "diff_mode_cycle"
	ActionImages          Action = "images_toggle"
	ActionSidePanel       Action = "side_panel_toggle"
	ActionTypingLock      Action = "typing_scroll_lock"
	ActionAutoPoke        Action = "auto_poke_toggle"
	ActionHistorySearch   Action = "history_search"
	ActionRetrievePending Action = "retrieve_pending"
	ActionThinkingDisplay Action = "thinking_display_cycle"
	ActionReasoningEffort Action = "reasoning_effort_cycle"
	ActionBackgroundTask  Action = "background_task"
	ActionSelectionMode   Action = "selection_mode"
)

// Binding pairs an action with the keys that trigger it and a description for
// the help overlay and hotkey feedback.
type Binding struct {
	Action Action
	Keys   []string
	Desc   string
}

// DefaultBindings is the §11 keymap. Keys are Bubble Tea key strings.
var DefaultBindings = []Binding{
	{ActionScrollUp, []string{"ctrl+shift+k"}, "scroll up one line"},
	{ActionScrollDown, []string{"ctrl+shift+j"}, "scroll down one line"},
	{ActionPageUp, []string{"pgup", "alt+u"}, "page up"},
	{ActionPageDown, []string{"pgdown", "alt+d"}, "page down"},
	{ActionPrevPrompt, []string{"ctrl+k", "ctrl+["}, "jump to the previous prompt"},
	{ActionNextPrompt, []string{"ctrl+j", "ctrl+]"}, "jump to the next prompt"},
	{ActionScrollBookmark, []string{"ctrl+g"}, "toggle scroll bookmark"},
	{ActionCenteredToggle, []string{"alt+c"}, "centered ↔ left aligned"},
	{ActionInfoWidgets, []string{"alt+i"}, "toggle info widgets"},
	{ActionTodoCard, []string{"alt+x"}, "toggle the todo card"},
	{ActionDiffMode, []string{"alt+g"}, "cycle diff display mode"},
	{ActionImages, []string{"alt+shift+i"}, "show images inline or as placeholders"},
	{ActionSidePanel, []string{"alt+m"}, "toggle the side panel"},
	{ActionTypingLock, []string{"alt+s"}, "typing scroll lock"},
	{ActionAutoPoke, []string{"ctrl+p"}, "toggle auto-poke"},
	{ActionHistorySearch, []string{"ctrl+r"}, "search prompt history"},
	{ActionRetrievePending, []string{"ctrl+up", "alt+up"}, "retrieve staged messages"},
	{ActionThinkingDisplay, []string{"alt+t"}, "cycle thinking display"},
	{ActionReasoningEffort, []string{"alt+r"}, "cycle reasoning effort"},
	{ActionBackgroundTask, []string{"alt+b"}, "send the running tool to the background"},
	{ActionSelectionMode, []string{"alt+o"}, "toggle mouse text selection (highlight & copy)"},
}

// Keymap resolves a key press to an action.
type Keymap struct {
	byKey  map[string]Binding
	byName map[Action]Binding
}

// NewKeymap builds the default keymap, applying config overrides.
//
// An override replaces an action's keys outright rather than adding to them:
// rebinding is usually done to get a key *back* from evilcode, and merging
// would leave the old one still captured.
func NewKeymap(overrides map[string]string) (*Keymap, []string) {
	km := &Keymap{
		byKey:  map[string]Binding{},
		byName: map[Action]Binding{},
	}
	for _, b := range DefaultBindings {
		km.byName[b.Action] = b
	}

	var problems []string
	for name, keys := range overrides {
		action := Action(name)
		b, ok := km.byName[action]
		if !ok {
			problems = append(problems, "unknown keybinding action: "+name)
			continue
		}
		b.Keys = splitKeys(keys)
		km.byName[action] = b
	}

	// Build the reverse index last so overrides are reflected, and report
	// collisions rather than letting one binding silently shadow another.
	claimed := map[string]Action{}
	var actions []Action
	for a := range km.byName {
		actions = append(actions, a)
	}
	sort.Slice(actions, func(i, j int) bool { return actions[i] < actions[j] })

	for _, a := range actions {
		b := km.byName[a]
		for _, k := range b.Keys {
			if prev, taken := claimed[k]; taken {
				problems = append(problems, "key "+k+" is bound to both "+
					string(prev)+" and "+string(a))
				continue
			}
			claimed[k] = a
			km.byKey[k] = b
		}
	}
	return km, problems
}

func splitKeys(s string) []string {
	var out []string
	for _, part := range strings.Split(s, ",") {
		if p := strings.TrimSpace(part); p != "" {
			out = append(out, p)
		}
	}
	return out
}

// Lookup resolves a key to its binding.
func (k *Keymap) Lookup(key string) (Binding, bool) {
	b, ok := k.byKey[key]
	return b, ok
}

// Keys returns the keys bound to an action.
func (k *Keymap) Keys(a Action) []string {
	return k.byName[a].Keys
}

// Describe returns an action's description.
func (k *Keymap) Describe(a Action) string { return k.byName[a].Desc }

// Bindings returns every binding, sorted by action.
func (k *Keymap) Bindings() []Binding {
	out := make([]Binding, 0, len(k.byName))
	for _, b := range k.byName {
		out = append(out, b)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Action < out[j].Action })
	return out
}

// Hotkey feedback tuning (plan.md §6.8).
const (
	// RareUseThreshold is how many times a chord must have been used before
	// evilcode stops explaining it.
	RareUseThreshold = 4

	// ReminderAfter is how long an unused binding waits before it is explained
	// once more. Muscle memory decays; the hint should come back with it.
	ReminderAfter = 45 * 24 * time.Hour

	// NearMissGap and NearMissPerChord rate-limit the "not bound" hint, so
	// leaning on a key does not produce a wall of notices.
	NearMissGap      = 1200 * time.Millisecond
	NearMissPerChord = 3
)

// usageRecord is one chord's history.
type usageRecord struct {
	Count    int       `json:"count"`
	LastUsed time.Time `json:"last_used"`
}

// HotkeyUsage tracks how often chords are used, so rarely-used ones can explain
// themselves and familiar ones can stay quiet.
type HotkeyUsage struct {
	path string

	mu      sync.Mutex
	records map[string]*usageRecord

	// nearMiss rate-limiting state.
	lastNearMiss time.Time
	nearMissSeen map[string]int
}

// LoadHotkeyUsage reads the usage file. A missing or corrupt file is not an
// error: the worst case is a few extra hints.
func LoadHotkeyUsage(dataDir string) *HotkeyUsage {
	h := &HotkeyUsage{
		path:         filepath.Join(dataDir, "hotkey_usage.json"),
		records:      map[string]*usageRecord{},
		nearMissSeen: map[string]int{},
	}
	if data, err := os.ReadFile(h.path); err == nil {
		_ = json.Unmarshal(data, &h.records)
	}
	return h
}

// Record counts a use and reports whether the chord should explain itself.
func (h *HotkeyUsage) Record(key string, now time.Time) bool {
	h.mu.Lock()
	defer h.mu.Unlock()

	rec, ok := h.records[key]
	if !ok {
		rec = &usageRecord{}
		h.records[key] = rec
	}

	// The hint reappears once after a long gap, because muscle memory for a
	// rarely-used chord decays and the reminder is welcome again.
	stale := !rec.LastUsed.IsZero() && now.Sub(rec.LastUsed) > ReminderAfter
	explain := rec.Count < RareUseThreshold || stale
	if stale {
		rec.Count = 0
	}

	rec.Count++
	rec.LastUsed = now
	h.save()
	return explain
}

// AllowNearMiss reports whether an unbound chord should produce a hint,
// applying the rate limits.
func (h *HotkeyUsage) AllowNearMiss(chord string, now time.Time) bool {
	h.mu.Lock()
	defer h.mu.Unlock()

	if now.Sub(h.lastNearMiss) < NearMissGap {
		return false
	}
	if h.nearMissSeen[chord] >= NearMissPerChord {
		return false
	}
	h.nearMissSeen[chord]++
	h.lastNearMiss = now
	return true
}

func (h *HotkeyUsage) save() {
	data, err := json.Marshal(h.records)
	if err != nil {
		return
	}
	_ = os.MkdirAll(filepath.Dir(h.path), 0o755)
	_ = os.WriteFile(h.path, data, 0o644)
}

// NearestBinding finds the bound chord most similar to an unbound one, so an
// unhandled press suggests what the user probably meant instead of vanishing.
func (k *Keymap) NearestBinding(chord string) (Binding, bool) {
	best, bestScore := Binding{}, 0
	for key, b := range k.byKey {
		if s := chordSimilarity(chord, key); s > bestScore {
			best, bestScore = b, s
		}
	}
	// Require real overlap; a random suggestion is worse than none.
	return best, bestScore >= 2
}

// chordSimilarity scores how close two chords are.
//
// A matching base key is required, not merely weighted: "you pressed
// Ctrl+Shift+G, did you mean Ctrl+G?" is useful, while "you pressed
// Ctrl+Alt+Shift+F19, did you mean Ctrl+Shift+J?" is noise that happens to
// share modifiers. Shared modifiers then break ties among the candidates that
// do match.
func chordSimilarity(a, b string) int {
	aParts := strings.Split(a, "+")
	bParts := strings.Split(b, "+")
	if len(aParts) == 0 || len(bParts) == 0 {
		return 0
	}
	if aParts[len(aParts)-1] != bParts[len(bParts)-1] {
		return 0
	}

	score := 2
	inB := map[string]bool{}
	for _, p := range bParts[:len(bParts)-1] {
		inB[p] = true
	}
	for _, p := range aParts[:len(aParts)-1] {
		if inB[p] {
			score++
		}
	}
	return score
}

// RenderHotkeyHint draws the `⌨ Ctrl+G → toggle scroll bookmark` feedback line.
func (r *Renderer) RenderHotkeyHint(key, desc string) string {
	dim := r.style(theme.RoleDim)
	accent := lipgloss.NewStyle().
		Foreground(lipgloss.Color(r.Palette.Hex(theme.RoleInfo)))
	return dim.Render("⌨ ") + accent.Render(PrettyKey(key)) + dim.Render(" → "+desc)
}

// RenderNearMiss draws the `⌨ Ctrl+Shift+P isn't bound · nearest: …` line.
//
// Silently swallowing an unhandled modified chord is the behavior this
// replaces: the user pressed something deliberate and deserves to know it did
// nothing (plan.md §6.8).
func (r *Renderer) RenderNearMiss(chord string, nearest Binding, found bool) string {
	dim := r.style(theme.RoleDim)
	warn := lipgloss.NewStyle().
		Foreground(lipgloss.Color(r.Palette.Hex(theme.RoleWarning)))

	line := dim.Render("⌨ ") + warn.Render(PrettyKey(chord)) + dim.Render(" isn't bound")
	if found && len(nearest.Keys) > 0 {
		line += dim.Render(" · nearest: ") +
			r.style(theme.RoleInfo).Render(PrettyKey(nearest.Keys[0])) +
			dim.Render(" → "+nearest.Desc)
	}
	return line
}

// PrettyKey renders a Bubble Tea key string the way a person writes it.
func PrettyKey(key string) string {
	parts := strings.Split(key, "+")
	for i, p := range parts {
		switch p {
		case "ctrl":
			parts[i] = "Ctrl"
		case "alt":
			parts[i] = "Alt"
		case "shift":
			parts[i] = "Shift"
		case "pgup":
			parts[i] = "PgUp"
		case "pgdown":
			parts[i] = "PgDn"
		case "up", "down", "left", "right", "enter", "esc", "tab", "space":
			parts[i] = strings.ToUpper(p[:1]) + p[1:]
		default:
			if len(p) == 1 {
				parts[i] = strings.ToUpper(p)
			}
		}
	}
	return strings.Join(parts, "+")
}

// IsModifiedChord reports whether a key press carries a modifier, which is what
// distinguishes a deliberate chord from ordinary typing. Near-miss feedback
// only fires for these — telling someone that `q` is unbound would be noise.
func IsModifiedChord(key string) bool {
	return strings.Contains(key, "ctrl+") ||
		strings.Contains(key, "alt+") ||
		strings.Contains(key, "shift+")
}
