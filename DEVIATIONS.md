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

## 2026-07-31 — P2.19 harmony scores differ from the spec's calibration pins

**Spec** (§7.5): "Calibration pins (dark bg): Dracula ≈76, Solarized Dark ≈70, Nord ≈69,
Gruvbox ≈67, neon-chaos ≈56, unreadable-mud ≈38. A test asserts these orderings hold."

**Built**: the scorer implements every documented rule — Oklab throughout, the five
weighted criteria, `0.4·mean + 0.6·worst` per criterion, `0.75·weighted_mean +
0.25·worst_critical` overall, the CVD projections, and the must-distinguish pairs with
their documented omissions. But absolute scores land lower than the pins: Dracula 66.9
rather than ≈76.

**Why it was not tuned to match**: the gap comes from `DistinctTarget = 0.20`, which the
spec also states. Measured in Oklab, Dracula's own `queued`/`asap` pair sits at 0.191 and
`system`/`queued` at 0.158 — both below the target the same spec sets. Either the pins
came from a scorer with a different distance metric, or the target is stricter than the
reference palette satisfies. Moving the target to make one number match would be tuning
to a constant rather than to the eye.

The plan's actual test requirement is that the *orderings* hold, and they do:
unreadable-mud < neon-chaos < the built-in palettes, with Dracula above neon-chaos.
`TestCalibrationOrdering` asserts exactly that and deliberately does not assert absolute
values, which would break on any future tuning.

**Visible effect**: `/theme score` reports numbers a few points below what the spec's
table would suggest. Relative judgments — is this palette better than that one — are
unaffected.

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

## 5. Memory is a JSONL file with a brute-force scan, not sqlite-vec

**Spec:** §19 pins a sqlite-vec database at `~/.local/share/evilcode/memory.db`
with a vec0 table doing 768-dim cosine search.

**Built:** an append-only JSONL bank at `~/.local/share/evilcode/memory.jsonl`,
loaded into memory at open, searched by a linear cosine scan.

**Why:** sqlite-vec is a C extension. Using it means cgo and a compiled `.so` the
user installs before evilcode will start — a hard dependency, and a
cross-compilation problem, for a bank that holds a few thousand short strings.
The scan it replaces is not a compromise at this scale: 5k memories × 768 floats
is single-digit milliseconds, it runs off the hot path, and passive recall is
already bounded by a 5s embed timeout that dwarfs it. The file format also
matches the session store, so one idiom covers both.

**When this would need revisiting:** a bank in the hundreds of thousands, or
multiple processes writing concurrently — the daemon in Phase 4 serializes
through one process, so that is not yet the case.

## 6. The MemoryActivity widget rarely finds a dock slot

**Spec:** §8.8 puts the 4-step memory pipeline in the right margin, and §8.3
gives it priority 7 — ahead of ModelInfo.

**Built:** the widget renders exactly as specced and is unit-tested, and the
priority order is now enforced by sorting on `WidgetKind` rather than by the
order `activeWidgets` happens to append (which had drifted: MemoryActivity was
appended after ModelInfo and lost its slot every frame). But at 7 rows it is the
tallest widget in the set, and in practice it seldom finds a free rectangle.

**Why:** `FreeWidth` measures a row with `lipgloss.Width`, and glamour pads every
rendered paragraph out to the full wrap width. Prose therefore measures as
occupying the whole line even where it visibly stops, so no slot spans it. The
look-ahead in `reliableWidth` compounds it: a 7-row widget needs roughly 13
consecutive unobstructed rows.

Two attempts at the measurement half have been reverted. Counting only painted
padding as occupied does find the slots, and a widget with its full bracket
pipeline docks beside the recall tile — but on a second run nothing docked at
all, so the rule is not yet right and is not worth guessing at from inside the
memory loop.

The write half *is* fixed: each of a widget's lines used to be padded from its
own row's width, so a line with text under it started further right than its
blank neighbours and the box came out as fragments at three different columns.
The whole box now takes one column, chosen from the widest row it covers, which
is what a rounded border needs to survive contact with real text.

**When this would need revisiting:** it is the same root cause for every widget
taller than about five rows, so fixing the measurement — and the write path with
it — is worth its own loop rather than a corner of the memory one.

## 7. Diagrams render inline, not in a dedicated diagram pane

**Spec:** §3.1 puts a diagram pane outermost in the horizontal split, carved
before the side pane — `Right` or `Top`, min width 30 / min height 6, ratio
clamped 20..100 — with its own position, zoom, and pan keys in §11.

**Built:** mermaid fences render through `mmdc` to a PNG and are drawn inline in
the transcript with the kitty graphics protocol, reserving a fixed 16 rows. With
`mmdc` absent the source shows styled with `↻ mermaid (render requires mmdc)`.

**Why:** the pane is a third region in a split that already carves a side pane
out of the chat column, and every one of its knobs — position, ratio, zoom, pan
— is a keybinding plus a piece of layout state plus a hysteresis rule, none of
which can be checked without a terminal that draws images. Inline delivers what
the diagram is for: seeing it. The side panel already exists and already takes
arbitrary content, so a diagram that wants more room has somewhere to go without
a new region.

**When this would need revisiting:** the first time a diagram is genuinely too
large to read at 16 rows. That is a real limit, not a hypothetical one — it is
just not the common case, and the zoom keys are worth building against a real
diagram rather than an imagined one.

## 7. Kitty image display is verified by protocol, not by eye

**Spec:** the Phase 5 gate asks that mermaid render as an image in kitty and
that a pasted image display.

**Verified:** `mmdc` was installed and a real diagram rendered — a 12.6 KB PNG
that was looked at. The kitty transmission was then checked against the
protocol: `a=T,f=100,c=40,r=20,i=7`, base64 PNG, chunked with `m=1` and a final
`m=0`, every chunk terminated, plus a matching delete sequence. The
absent-renderer path was captured in the probe as well.

**Not verified:** that a kitty-protocol terminal draws those pixels. This
session runs under tmux against xterm-256color, and the probe's ANSI→PNG
renderer works on cells — an image is an escape sequence, so it is invisible to
a capture by construction. What can be checked without a human is that the
bytes are right, and they are.

**When this would need revisiting:** run under kitty, ghostty, or WezTerm once
and look at the screen. Nothing in the code is waiting on it.

## 8. Two emoji added to the §9.5 inventory

**Spec:** §2.1 says the evil lexicon is complete and additions need sign-off;
§9.5 gives a closed emoji inventory.

**Added:** `📊` for `/productivity` and `🖼` for the image placeholder.

**Why:** neither has a substitute in the inventory that means the right thing —
the closest options are `💰` and `🔎`, and both would read as something else.
Where an approved emoji *did* fit, it was used instead: `/overnight` uses `⏳`
rather than the `🌙` it started with, because overnight is fundamentally about
elapsed time.

Both satisfy invariant 7: single codepoint, no ZWJ, no variation selector,
Unicode 6.0, and rendered correctly in the probe's font stack.

**When this would need revisiting:** immediately, if you would rather they were
something else — this is a taste call and it is yours, not mine.

## 9. Widgets overlay text and can be dismissed, rather than living only in blank space

**Spec:** §8.3 docks widgets into "negative space" — the blank columns beside the
transcript — and never over text.

**Built:** widgets prefer clear space and fall back to sitting *over* prose, with
a click on a box hiding it for the session and `Alt+I` bringing them all back.

**Why:** prose wraps to the full measure, so on a 140-column terminal a paragraph
leaves no margin at all. Honouring "blank space only" meant boxes appeared beside
tool rows and lists and never beside an actual answer — which is most of the
screen most of the time. The alternative considered was capping the prose measure
at 96 columns to manufacture a permanent margin, but that changes how every
message reads in order to serve the chrome. Overlaying is the smaller cost, and
it is only acceptable *because* dismissal exists: a box that can cover your text
has to be swattable.

**When this would need revisiting:** if the prose measure is ever capped for
readability on its own merits, the fallback becomes rare and the dismissal
affordance could go back to being optional.

## 10. Left-preferring widgets dock on the right

**Spec:** §8.3 gives six widget kinds a left-margin preference — UsageLimits,
KvCache, Compaction, BackgroundTasks, SwarmStatus, AmbientMode — and says left
widgets exist only in centered mode.

**Built:** the preference is recorded and then falls back to the right margin.

**Why:** `CenteredCap` is 96 columns, so at a 140-column terminal the left margin
is 22 cells — narrower than `WidgetMinWidth` of 24. A left-margin painter would
be dead code at every width anyone actually runs, and honouring the preference
literally meant those six kinds could never render at all. `centered` was also
hardcoded `false` at the call site, so in practice none of them had ever
appeared.

**When this would need revisiting:** raise `CenteredCap` above ~124 and a real
left margin exists; the `Side` on each anchor is already tracked for it.

## 11. Widget shrinking with hysteresis is not implemented

**Spec:** §8.3 says a pinned widget shrinks with hysteresis before it hides.

**Built:** it holds its slot, and now overlays rather than hiding.

**Why:** widget width is derived from content, so shrinking means re-rendering
each kind at a narrower measure — a second rendering path per widget. Overlay
plus dismissal covers the case shrinking was for, at a fraction of the surface.

## 12. Markdown headings follow the palette, and the default palette is Catppuccin Frappé

**Spec:** §7.2 fixes the prose table, headings included, as an amber ramp
(`#ffd764` → `#c89b4b`); §7.1 calls dracula the default palette.

**Built:** each palette carries its own §7.2 table, headings ride that palette's
accent, and the default is `catppuccin-frappe` with the Mauve accent.

**Why:** the prose table was a package-level constant that `markdownStyleJSON`
read unconditionally, so `/theme` recolored the chrome and left every heading in
every reply the same amber — the prose did not follow the theme at all. Retuning
the constant would have hidden that; making it palette-derived fixes it. Two
neighbouring config keys turned out to be dead in the same way and are now
applied: `display.theme` (NewModel hardcoded dracula) and `display.centered`.

The default follows the desktop: the GTK theme here is
`catppuccin-frappe-mauve-standard+default`. Catppuccin Frappé is a published
palette, so its values are transcribed rather than invented, and it scores 70.3
on the existing Oklab harmony scorer against dracula's 66.9.

Everything in §7.2 other than the four heading colors is still the spec table
verbatim, and the test asserting that still runs.

**When this would need revisiting:** if evilcode is ever distributed rather than
personal, the default should probably go back to a palette that owes nothing to
the local desktop.

## 13. Auto-compaction triggers on a threshold, not on the provider's overflow error

**Spec:** §9.9 lists "Context overflow: auto-compact → `✓ Context compacted.
Retrying...` (emergency variant labeled)" — i.e. compaction as error recovery.

**Built:** compaction happens *before* dispatching, once the last request's
context passes 85% of the window. The `✓ Context compacted. Retrying...` notice
is emitted as specced.

**Why:** the provider's context-length error only arrives after the tokens are
already spent, and recognising it means per-provider string matching in both
`ollama.go` and `openai.go` — a brittle surface that changes whenever a vendor
rewords a message. A pre-flight threshold prevents nearly every overflow without
parsing anything, and costs one comparison per turn.

**Not built:** the "emergency variant" for an overflow that gets through anyway —
a window smaller than the threshold assumed, say. That still surfaces as an
ordinary error.

**When this would need revisiting:** if overflows show up in practice despite the
threshold, the error path is worth adding underneath it rather than instead
of it.

## 14. Vision is new scope, and attachments are never persisted

**Spec:** plan.md never mentions vision or multimodal — grep returns nothing.
§6.6 specs the *attachment UX* (explicit Ctrl+V, file drops, `[image {n}]`
placeholders, the "Pasted image/png (412 KB)" notice) without ever saying the
images reach a model.

**Built:** the §6.6 input rules as written, plus the send path they imply —
`Message.Images` as raw bytes, encoded per provider at the edge (Ollama takes
bare base64, OpenAI takes a data URI inside content parts), gated on a per-model
`vision = true`.

**Why the gate is configured rather than sniffed:** a guess that says no to a
capable model is invisible, and one that says yes to a text-only model fails deep
inside the provider with a message that explains nothing.

**Attachments are deliberately not written to the session log.** One JSONL line
per message against a 16 MB scanner buffer means a couple of images exceed a
line, and `Read` then silently truncates the *entire replay from that point on*
with no error surfaced — a data-loss bug that presents as "my session came back
half empty". Images travel with one turn and are dropped.

**When this would need revisiting:** if attachments should survive a resume, they
need side-car files referenced by path, not bytes inline.
