# Deviations from plan.md

Append-only. Each entry: what the spec said, what was built instead, why.

## 2026-07-30 — P0.3 codex review step not active

**Spec** (§0.2 step 6, Phase 0 task 3): kick off a background codex review of every
commit; verify the skill is callable with a smoke review on the seed commit.

**Built instead**: nothing — the review step is inert. `codex-companion.mjs setup --json`
reports `{"ready": false, "codex": {"available": false, "detail": "not found"}}`. The CLI
is absent and installing it globally (`npm install -g @openai/codex`) plus authenticating
an OpenAI account is a user decision, not a build-agent one.

**Why it's OK to proceed**: the review is an advisory second opinion, not a correctness
gate. Every commit still passes `go build ./... && go vet ./... && go test ./...`, which
is the real gate. LOOPS.md entries carry `codex verdict: n/a (CLI absent)` until this is
turned on.

## 2026-07-30 — P0.4 ansirender draws color emoji as placeholder boxes

**Spec** (§14): render the frame grid "with an embedded monospace TTF (bold
variant for bold)".

**Built instead**: the embedded font is there (Go Mono, all four variants, via
`golang.org/x/image/font/gofont` — no TTF asset had to be vendored), but two
things were added on top:

1. A **system primary font** is preferred when installed (JetBrains Mono Nerd
   Font Mono). Go Mono covers only 15 of the 102 glyphs in plan.md §2.2/§2.3 —
   it has no rounded box corners (`╭ ╮ ╰ ╯`, used by every box in §3.3), no
   geometric shapes, no braille spinner, and no Nerd Font glyphs (§6.1, §8.9).
2. A **fallback chain** over system symbol fonts for anything the primary lacks.

Measured coverage of the §2.2/§2.3 glyph vocabulary:

| chain | drawable |
|---|---|
| Go Mono alone (as specced) | 15/102 |
| Go Mono + system symbol fonts | 59/102 |
| Nerd Font primary + system symbol fonts (shipped) | 68/102 |

**What is still missing**: 34 glyphs, all but `⊳ ↻ 📌` being color emoji. Color
emoji fonts (twemoji, Noto Color Emoji) keep their artwork in COLR/CBDT tables
that `x/image` cannot rasterize — their outlines have zero ink, so they render
no better than nothing. Installing a *monochrome* emoji font (Noto Emoji) fixes
this; the loader already searches for it and `EVILCODE_PROBE_FONTS` points at
one explicitly.

**Why it's OK**: emoji cell *width* is correct regardless (verified by test), so
layout checks are unaffected — only the glyph artwork is missing, and
`evilcode probe fonts` names exactly which glyphs are affected so a tofu box in a
PNG is never a mystery. Golden frames are plain text (§14), so none of this
affects test reproducibility.

## 2026-07-30 — P1.5 two §2.2 name-table emoji swapped

**Spec conflict, not a judgment call**: invariant 7 forbids Unicode 13+ codepoints
("single widely-supported codepoints only"), but the §2.2 tables contain two of them:

| entry | spec glyph | codepoint | assigned | shipped instead |
|---|---|---|---|---|
| creature `talon` | 🪶 | U+1FAB6 | Unicode 13.0 | 🦅 U+1F985 (9.0) |
| modifier `tomb` | 🪦 | U+1FAA6 | Unicode 13.0 | 🏛 U+1F3DB (7.0) |

The invariant wins because its stated reason — wide font support — is the actual goal,
and it is not theoretical: this repo's own `evilcode probe fonts` diagnostic reports
Unicode 13 glyphs as undrawable on font sets that handle the rest of the vocabulary
fine. `talon 🦅` also reads better than a feather did.

Names are unchanged; only the glyphs moved. The creature table was extended from the
spec's 24 to the requested ~40 using pre-Unicode-13 single codepoints throughout, and
`TestEmojiPredateUnicode13` now fails the build if a later one is added.

## 2026-07-30 — P0.3 codex review step not active (continued)

**To enable**: `npm install -g @openai/codex && codex login`, then per commit:
`node "$CLAUDE_PLUGIN_ROOT/scripts/codex-companion.mjs" review --json --base HEAD~1`
(plugin root `~/.claude/plugins/cache/openai-codex/codex/1.0.5`). Optionally
`/codex:setup --enable-review-gate` to require a fresh review before stop.
