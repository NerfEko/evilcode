package theme

import "image/color"

// Literal is one ad-hoc rgb() color the UI emits directly rather than through a
// role. The spec quotes dozens of them — status ambers, picker golds, todo-card
// greens — and they stay as written, because they carry shades a 22-role
// vocabulary cannot express.
//
// They are enumerated here so the substitution pass can re-express them
// relative to whichever role they sit nearest. Without the registry a themed
// palette would move the roles and leave every literal behind, which is what
// makes a half-themed UI look broken rather than merely different.
type Literal struct {
	// Name identifies the literal for tests and diagnostics.
	Name string

	// Color is the value as written in the source.
	Color color.RGBA

	// Anchor is the role the literal is a variation of.
	Anchor Role
}

// Literals is the registry. Every ad-hoc color the renderer emits should be
// here; tests assert the two invariants that make the registry useful.
var Literals = []Literal{
	// Status line and notices.
	{"status_amber", RGB(255, 193, 7), RoleWarning},
	{"status_slow", RGB(255, 184, 108), RoleWarning},
	{"status_severe", RGB(255, 100, 100), RoleError},
	{"tool_hint", RGB(100, 100, 100), RoleDim},

	// Palette and picker.
	{"palette_gold", RGB(255, 213, 128), RoleWarning},
	{"palette_teal", RGB(128, 203, 196), RoleAsap},
	{"picker_hint", RGB(120, 120, 150), RoleDim},
	{"picker_provider", RGB(140, 180, 255), RoleInfo},
	{"picker_via", RGB(220, 190, 120), RoleWarning},
	{"picker_unavailable", RGB(180, 120, 120), RoleError},
	{"picker_limited", RGB(214, 184, 92), RoleWarning},
	{"picker_name", RGB(200, 200, 220), RoleAIText},
	{"picker_old", RGB(120, 120, 130), RoleDim},
	{"picker_favorite", RGB(255, 160, 210), RoleAccent},
	{"picker_recommended", RGB(255, 220, 120), RoleWarning},
	{"picker_caveat", RGB(210, 150, 110), RoleWarning},

	// Chrome shared by code blocks, diffs, and widgets.
	{"chrome_dim", RGB(100, 100, 100), RoleDim},
	{"widget_border", RGB(70, 70, 80), RoleBorder},
	{"meter_track", RGB(50, 50, 60), RoleBorder},

	// Meters.
	{"meter_low", RGB(255, 100, 100), RoleError},
	{"meter_mid", RGB(255, 200, 100), RoleWarning},
	{"meter_high", RGB(100, 200, 100), RoleSuccess},

	// Todo card (deliberately brighter than the global dim: the card sits on
	// the bare background with no border).
	{"todo_blocked", RGB(225, 165, 90), RoleWarning},
	{"todo_done", RGB(105, 190, 125), RoleSuccess},
	{"todo_cancelled", RGB(190, 105, 115), RoleError},
	{"todo_pending", RGB(135, 145, 160), RoleDim},
	{"todo_text_active", RGB(225, 232, 240), RoleAIText},
	{"todo_text_idle", RGB(195, 202, 212), RoleAIText},
	{"todo_group_hot", RGB(255, 210, 130), RoleWarning},
	{"todo_group_cool", RGB(170, 175, 205), RoleUser},
	{"todo_meta", RGB(140, 140, 150), RoleDim},
	{"todo_score_warn", RGB(220, 190, 100), RoleWarning},
	{"todo_score_bad", RGB(220, 120, 100), RoleError},

	// Scrollbar.
	{"scrollbar_focused", RGB(188, 208, 240), RoleInfo},
	{"scrollbar_unfocused", RGB(136, 148, 172), RoleDim},

	// Rainbow ramp endpoints and the decay target.
	{"rainbow_gray", RGB(80, 80, 80), RoleDim},
	{"tool_anim_from", RGB(80, 200, 220), RoleAsap},
	{"tool_anim_to", RGB(186, 139, 255), RoleUser},

	// Session picker.
	{"session_marked", RGB(140, 220, 160), RoleSuccess},
	{"session_unmarked", RGB(90, 90, 90), RoleDim},
	{"session_emoji", RGB(110, 210, 255), RoleInfo},
	{"session_batch", RGB(255, 140, 140), RoleError},
	{"session_saved", RGB(255, 180, 100), RoleWarning},

	// Composer and diffs. shell_green anchors to `ai` rather than `success`:
	// the two share a value in the default palette, but a shell-mode indicator
	// is a mode cue, not a success cue, and a palette that separates them
	// should move it with the mode color.
	{"shell_green", RGB(110, 214, 151), RoleAI},
	{"new_session_blue", RGB(120, 200, 255), RoleInfo},
	{"overscroll_countdown", RGB(150, 150, 165), RoleDim},

	// Plan card.
	{"plan_violet", RGB(158, 135, 255), RoleUser},

	// Model info widget.
	{"model_name", RGB(255, 150, 200), RoleAccent},
	{"git_branch", RGB(150, 170, 140), RoleSuccess},
}

// LiteralsByAnchor groups the registry by the role each literal varies from.
func LiteralsByAnchor() map[Role][]Literal {
	out := map[Role][]Literal{}
	for _, l := range Literals {
		out[l.Anchor] = append(out[l.Anchor], l)
	}
	return out
}
