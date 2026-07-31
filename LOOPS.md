# LOOPS.md — flight recorder

Append-only. Never edit old entries. One entry per task from `plan.md`.

## 2026-07-30 P0.1 — repo bootstrap

Done: `git init` (branch `main`), `go mod init evilcode` (go 1.26.4), `.gitignore`
(binary, `probe/frames/`, `shots/`, `*.png` with a `testdata` exception).

Skeleton dirs deliberately not created empty — git does not track empty directories, so
packages are created as their first file lands. Not a spec deviation, just how git works.

Verified: `go build ./...` (no packages yet, trivially green).

## 2026-07-30 P0.2 — seed process files

Done: `LOOPS.md` (this file), `README.md` (§0.2.8 sections: what it is, build, quickstart,
what works today, config reference, keymap, probe rig), `DEVIATIONS.md`.

Verified: files exist, README describes only what actually ships.

Environment note for future loops: the build sandbox mounts `$GOPATH/pkg/mod` read-only
and blocks network, so `go` commands that touch the module cache must run unsandboxed.

## 2026-07-30 P0.3 — codex review skill

Not callable: `codex-companion.mjs setup --json` reports `ready: false`, CLI absent.
Installing it globally and authenticating an OpenAI account is the user's decision, so
the review step is inert rather than blocked on. Logged in DEVIATIONS.md with the exact
invocation to use once enabled. Codex verdict on all commits so far: n/a (CLI absent).

## 2026-07-30 P0.4 — internal/ansirender

Done: ANSI → cell grid → PNG.

- `parse.go`: SGR 0/1/2/3/7/22/23/27, 30–37, 90–97, 40–47, 100–107, 38;5;n / 48;5;n,
  38;2;r;g;b / 48;2;r;g;b, 39/49. 256-color resolution per spec (16 base, 6×6×6 cube
  `0 | 55+40x`, grayscale `8+10n`). Non-SGR escapes (CSI, OSC, DCS, charset) are
  consumed rather than leaking into the grid. Wide glyphs occupy two cells; combining
  marks ride the preceding cell.
- `render.go`: cell grid → image, cell box derived from the font's own advance and
  metrics. Default bg `rgb(18,18,24)` per spec. Backgrounds paint even where no ink
  lands, so bands and selections survive.
- `fallback.go` / `vocab.go`: font resolution, plus the §2.2/§2.3 glyph vocabulary
  used by the `probe fonts` diagnostic.

Verified: 40 test cases in `ansirender_test.go` covering truecolor, the 256-cube and
grayscale ramps, base/bright pairs, reverse video (including reversed defaults), bold,
faint, italic, 22/23/27/39/49 resets, colon-separated parameters, unknown-code
tolerance, non-SGR escape skipping, truncated escapes, `\r` / `\t` / trailing-blank
layout, wide-glyph cell occupancy, and PNG decode + dimensions. `go test ./...` green.

Three bugs found and fixed during the loop, all by tests rather than by reading:
uniseg's third return is a boundary bitmask (width lives in its high bits, not the
value); `ESC ( B`-style charset designations are three bytes, not two; skipped-over
cells must keep the default style rather than inheriting the live one.

Deviation logged: color emoji render as placeholder boxes (DEVIATIONS.md P0.4).

## 2026-07-30 P0.5 — probe rig

Done: `probe/probe.sh` (tmux driver: boot / keys / frame / png / kill) and the
`evilcode probe` subcommand (`render`, `text`, `fonts`, `hello`), plus `main.go`'s
stdlib subcommand switch.

`probe.sh` pins a short `TMUX_TMPDIR` (unix socket paths cap at 108 bytes) and boots
with a throwaway `HOME`, so a probe run can never scribble on real config or session
state. Capture waits for the pane to stop changing, so a frame is never sampled
mid-write.

Verified: booted, captured, rendered. PNG looked at — colors, bold, italic, the
user-prompt band, and box drawing all correct.

## 2026-07-30 P0.6 — golden harness

Done: `probe/probe_test.go` behind the `probe` build tag, `probe/scenarios/hello.txt`,
`probe/goldens/`, `probe/README.md` (28 lines).

The test drives `probe.sh` rather than reimplementing tmux control, so there is one
code path whether a human, an agent, or the test suite is driving.

Verified: `go build ./...` and `go test ./...` stay green untagged (the tag excludes
the package). `UPDATE_GOLDENS=1 go test -tags probe ./probe/...` writes goldens;
`go test -tags probe ./probe/...` passes against them. Drift detection was itself
checked by tampering a golden and confirming the test fails — an always-passing golden
test is worse than none.

## 2026-07-30 P0.7 — Phase 0 verification

Verified end to end: the hello-world bubbletea program boots under the probe rig, a
frame renders to PNG, the golden passes, and regeneration works. The `keys:0` → `keys:1`
delta between the two goldens proves input reaches the program and the frame repaints
rather than going stale.

PNGs looked at: `welcome.png` (rig smoke test) and a full §2.3 glyph-vocabulary sheet
at `-size 30`. The sheet's visible tofu boxes match `evilcode probe fonts` exactly —
34 glyphs, all but three being color emoji.

Phase 0 complete.

## 2026-07-30 P1.1 — provider layer

Done: `internal/provider` — interface plus three implementations.

- `provider.go`: own `Message`/`ToolCall`/`Req`/`Chunk` structs, mapped at the provider
  edge so the agent core never sees a vendor JSON shape. `Reasoning` is kept separate
  from `Content` so the TUI can GC traces independently (§9.7, §4.6).
- `ollama.go`: native `/api/chat` NDJSON, `/api/embed`, `/api/tags`. One client serves
  localhost and ollama.com — only BaseURL and the bearer token differ. Ollama issues no
  tool-call IDs, so the client synthesizes unique ones. `HTTPError` classifies retryable
  (429, 408, 5xx) from terminal (401, 404, 400), which the §12.6 breakers depend on.
- `openai.go`: SSE with indexed tool-call fragment accumulation — arguments arrive as
  string pieces that only parse once concatenated.
- `mock.go`: 9 deterministic scenarios (chat, thinking, tools, tools-buffered,
  tools-batch, plan, diff, error, long) selected by `EVILCODE_SCENARIO`.

Verified: 27 tests. Notable cases — tool calls arriving whole with no preceding text
(the Part V warning), synthesized IDs staying unique across chunks, the stream stopping
at `done:true` rather than reading into a following response, cancellation being honored
promptly, and the plan scenario chunking mid-fence so the streaming plan card (§12.1) is
actually exercised. A compile-time check asserts all three satisfy `Provider`.

## 2026-07-30 P1.2 — config

Done: `internal/config` — TOML load, env overrides, provider registry, `model@provider`
refs, per-model overrides, role chains, XDG paths.

Decoding straight into the defaults (rather than merging a parsed struct over them)
means an absent key keeps its default. This matters for booleans that default to true:
a merge from a zero value would silently clear `keybinding_hints`, `idle_animation`, and
`auto_poke` for anyone whose config file did not mention them. That deleted the whole
merge function too.

Verified: 20 tests, including the partial-config regression above, explicit `false`
still winning, providers replacing rather than merging, `SplitModelRef` taking the last
`@` (model names can contain one), and validation rejecting duplicate/unnamed/unknown-kind
providers before anything tries to use them.

## 2026-07-30 P1.3 — tools

Done: `internal/tools` — read/write/edit/glob/grep/bash, a plain slice rather than a
registry, with bounded-concurrency batch execution and DiffStat.

`Run` returns a `Result` struct rather than §17's bare `(string, error)`: the spec also
requires edit and write to "compute DiffStat and return the diff for §9.3 rendering",
which a string cannot carry. Output still goes to the model; the diff and stat are
display metadata it never pays for.

Diffs use `go-udiff`, already in the module graph via the Charm stack. §1.5's rule is
against *adding* a diff dependency, and this adds none.

Notable choices: paths resolve through `EvalSymlinks` before the containment check, so a
symlink cannot walk out of the workspace; `edit` refuses an ambiguous match and writes
nothing; the not-found message pushes toward re-reading, since a bare "not found" is
what starts edit-retry loops; `glob` skips `node_modules` and friends at the directory
level; ripgrep is shelled out to, never reimplemented, and its exit-1 "no matches" is
treated as an answer rather than a fault.

Verified: 33 tests. One real bug caught: `bash` with a timeout hung for the full command
duration because killing bash does not close the output pipes a grandchild still holds —
`sleep 10` under a 100ms timeout took the whole 10s. Fixed with `cmd.WaitDelay`.

Note: ripgrep is not installed on this machine, so the grep tool reports that and the
grep tests skip. The degradation path is what got exercised; the happy path needs `rg`.

## 2026-07-30 P1.4 — agent core

Done: `internal/agent` — events, the loop, safe-point interleave drain, retries, context
assembly, AGENTS.md/CLAUDE.md discovery, and the post-turn hook seam.

The loop is a plain function, not an actor system. Safe points are implemented as
specced: B (stream ended, no tool calls), C (urgent only, every remaining call stubbed
`[Skipped: user interrupted]` because the wire format requires tool_use/tool_result
adjacency), D (after all results, the default). Interrupts group by source so a system
nudge never merges into a user's sentence.

Retry policy distinguishes retryable from terminal via `provider.HTTPError`: a 401 is
deterministic and must not be re-sent, while a transport error is explicitly retryable.

Verified: 27 tests. Notable — partial output survives an interrupt as a real assistant
message; a failing tool still gets a result message with the error text in it; a 401 is
tried exactly once; `MaxSteps` stops a model that never stops calling tools; the
conversation cannot be mutated through a returned slice; the system prompt is asserted
to stay under ~700 tokens; and `TestAgentDoesNotImportBubbletea` enforces invariant 1 by
inspecting the package's own imports, since retrofitting it later would be a rewrite.

## 2026-07-30 P1.5 — sessions, names, prompt history

Done: `internal/core` (creature/modifier tables, §2.2) and `internal/session` (JSONL
store, resume, fork, crash detection, prompt history §6.1).

The name tables are guarded by the invariant-7 test: single codepoint, no ZWJ, no VS16,
no skin-tone modifier, and nothing from Unicode 13+. That last check failed against the
spec's own table, which contains two Unicode 13 glyphs — swapped, logged in
DEVIATIONS.md P1.5. Table extended from 24 to 40 creatures as §2.2 asks.

Crash detection is a clean-exit marker written by `Close`; a session file without one is
reported as crashed. A truncated final JSON line — what a `kill -9` leaves — is skipped
rather than failing the read, since recovering what survived is the point of resuming.

Prompt history is shared across sessions so Up-arrow recall reaches prior ones, caps at
1000 after dedupe, compacts at 2000 via write-to-temp-and-rename, and refuses prompts
over 10k chars. Search is free-form fuzzy (matches anywhere), deliberately looser than
the anchored slash-palette scorer, and rewards adjacency and word starts.

Verified: 21 session tests + 10 name tests.

## 2026-07-30 P1.6 — evilcode run

Done: `internal/runcmd` — headless one-shot. Text deltas go to stdout raw; tool rows,
notices and errors go to stderr, so `evilcode run -q` pipes cleanly. Exit codes: 0 ok,
1 error, 130 interrupted. Ctrl+C cancels the turn rather than killing the process, so
partial output and the session file both survive.

Verified by running it: the `tools` scenario reads a real file from this repo and
renders `✓ read internal/config/config.go · 2.9k tok`; the `tools-batch` scenario runs
three calls concurrently and renders the failing one with its full error. This is the
first proof of invariant 1 paying off — a second frontend over the same event stream,
with the agent core unchanged.

## 2026-07-30 P1.7–P1.13 — the TUI takes shape

Done: `internal/theme` (roles, dracula palette, procedural color) and `internal/tui`
(layout, scroll, transcript, markdown, highlighting, composer, status line, header,
welcome, and the bubbletea model), plus `internal/tuicmd` wiring and `evilcode tui`.

Structure first, as §3.2 asks: `Stack.Resolve` is the packed-vs-scrolling decision, and
everything sits on it. While content fits, the transcript takes its exact content height
so the conversation hugs the composer; on overflow it becomes a min-3 viewport. When even
the fixed rows do not fit, the transcript collapses rather than pushing the composer off
screen — losing history is recoverable, losing the input box is not.

Scroll feel is implemented as pure logic and tested without a terminal: wheel momentum
with velocity inferred from inter-notch timing, the tiered ease-out drain, tail-follow
catch-up, and the scrollbar hysteresis of §3.6 — which has a test that feeds the decision
back into itself to prove it reaches a fixed point instead of oscillating.

Verified visually, which is the step that cannot be skipped. PNGs looked at: the welcome
screen, a committed turn, and the diff scenario. Three real bugs found only by looking:

1. Spaces vanished from typed input. Bubble Tea v2's `Key.String()` spells space as
   `"space"`; the printable text is `Key.Text`.
2. `edit` refused a file inside its own workspace. This repo is reachable by two paths
   (`/home/...` and `/mnt/cachyos-home/...`), and a not-yet-existing path was compared
   unresolved against a symlink-resolved root. Now the deepest existing ancestor is
   resolved and the rest re-appended — which is the part an attacker could have pointed
   elsewhere anyway. Regression test added; the escape tests still pass.
3. The user-prompt band looked ragged in every PNG. `tmux capture-pane` strips trailing
   spaces, erasing background-only cells at a line's end — exactly what the band and the
   right fact stack depend on. Fixed with `-N` on the ANSI capture.

Diffs are chroma-highlighted then tinted per §9.3's `(syntax*70 + diff*30)/100`, and a
test asserts more than one foreground color survives inside an added line, so a future
"simplification" to flat red/green fails the build.

Goldens: `probe/scenarios/tui.txt` and `tui-diff.txt`. Two harness bugs fixed while
adding them — quoted scenario arguments were being split on spaces (tmux then
concatenated them without the spaces), and absolute repo paths leaked into goldens; both
path forms are now scrubbed to `<repo>`.

Verified: `go build ./... && go vet ./... && go test ./...` green; `go test -tags probe
./probe/...` green against 6 goldens.

## 2026-07-30 P1.14 — slash palette

Done: `internal/tui/palette.go` (ranking, fuzzy recolor, windowing) and
`commands.go` (the §13 registry, Phase 1 subset only — the palette never offers a
command that does nothing).

Ranking is two-bucket by design: a literal case-insensitive prefix outranks every fuzzy
match however good its score, so typing `/mod` always offers `/model` first. An empty
query keeps registry order rather than sorting, because the registry groups related
commands and length-sorting an unfiltered list is noise.

The highlight is a recolor, not an underline (§5.1): matched characters lift toward white
and go bold, unmatched ones dim, both staying in the row's own hue. A test asserts no
underline escape is emitted, so a future "improvement" to underlining fails the build.

Verified: 16 unit tests, plus `TestPaletteReservesNoLayoutHeight` in the probe suite,
which diffs the captured frame before and after opening the palette and requires every
row above the composer to be byte-identical. That is invariant 3 checked against real
frames rather than asserted in prose — the failure mode is a layout interaction, which
only exists once everything is composed.

Two bugs caught by looking at the PNG: the overlay was spliced after the left inset was
applied, so it sat one column left of everything else; and the palette rendered the full
list regardless of what was typed, because the query was mirrored into state that nothing
updated. The query is now derived from the input at render time, so the two cannot drift.

## 2026-07-30 P1.15 — model picker

Done: `internal/tui/picker.go` — the inline box of §5.3, plus the reusable rounded-box
helpers of §3.3 (`roundedBox`, `BoxTitled`).

The picker is the counterpart to the palette: it *does* reserve layout height, because it
is a surface you interact with rather than a hint floating over one. The key hints sit
outside the box, above it, since they describe what the box does rather than being part
of its content.

The style cascade is first-match-wins exactly as §5.3 lists it, and the selection keeps
itself centered rather than merely visible — a selection pinned to an edge gives no sense
of position in a long list.

Two bugs found by the tests: the filter underline was skipped on the selected row (the
selection highlight had replaced it, but the two carry different information — which row
is selected, and which characters matched — so the underline now applies on top); and a
title longer than its box pushed the right border off screen, so `BoxTitled` truncates.

Verified: 13 picker tests, including one that asserts every box row is the same cell
width, since a ragged right border is the classic box-drawing bug. PNG looked at.

## 2026-07-30 P1.16–P1.19 — composer input, scrolling, interrupt, welcome

Done: `internal/tui/input.go` (the readline `Editor`, newline paths, paste rules) plus
wiring for mouse wheel, the scrollbar, and `/terminal-setup`.

All three newline paths of §6.2 work. The trailing-backslash fallback is parity-based —
an odd number of trailing backslashes escapes the Enter, an even number does not, so `\`
continues a line and `\\` is a literal backslash that still submits. Verified live under
the probe: typing `line one \` then Enter inserts a newline, consumes the backslash, and
shows the one-shot `/terminal-setup` tip exactly once.

Paste never inspects the clipboard for images (§6.6): on Wayland a multi-MIME clipboard
is routinely misidentified, and a stray attachment is worse than a missing one. A bare
Enter within 150ms of a paste is swallowed rather than submitting. Pastes of five lines
or more collapse to a placeholder and are restored on send by replacing the *last*
occurrence — two pastes of the same size share a placeholder string, and replacing the
first each time would give both the same content.

The scrollbar exposed the feedback loop §3.6 exists for. It first rendered nothing
visible: rows already filled the width, so the bar was appended past the right edge. The
fix is what the spec says — the transcript wraps one column narrower when the bar shows,
and the *previous* frame's decision picks this frame's wrap width, so steady state wraps
once instead of oscillating.

Verified: 24 input tests; `go test ./...` green; 12 goldens green including a new
packed→scrolling scenario. PNGs looked at for the scrolled transcript and the newline
continuation.

Codex verdict on every commit so far: n/a (CLI absent, DEVIATIONS.md P0.3).

## 2026-07-30 P1.20 — Phase 1 verification

Verified against a real model, not the mock. The local ollama daemon proxies cloud
models (`remote_host: https://ollama.com`), so `deepseek-v4-flash:cloud@ollama-local`
exercises the cloud path through the native API without a separate key.

**Headless edit task.** Asked it to read a file and change a string. The transcript is
worth recording because it validates a design decision:

    ✓ read main.go · 20 tok
    ✗ edit main.go
      old string not found in main.go. Re-read the file — it may have changed, or the
      indentation may differ from what you expected
    ✓ bash cat -A main.go · 18 tok
    ✓ edit main.go · 5 tok (+1 -1)

The model's first edit guessed the indentation wrong. The error message — which §17
specifically asks to push toward re-reading rather than saying "not found" — drove it to
inspect the actual whitespace with `cat -A` and then edit correctly. That is the
edit-retry loop being broken by wording alone, before hash anchors (Phase 2) exist. The
file was genuinely modified and the process exited 0.

**Interactive TUI.** Same model, real streaming, real tool row, session auto-named
`Ghoul 💀`. Provider dots correctly show ollama-local ready and ollama-cloud
unconfigured. PNG looked at.

**Goldens**: 12 green. `go build ./... && go vet ./... && go test ./...` green.

Phase 1 complete; tagged `phase-1`.

Note for Phase 2: ripgrep is still not installed on this machine, so the `grep` tool
reports that and its happy path remains unexercised. Everything else is covered.

## 2026-07-30 P2.1–P2.4 — the todo discipline system

Done: `internal/todo` (model, gates, poke tree, deltas), the `todo` tool, the
`PokeHook` post-turn seam, `/plan` with its card renderer, and todo UI surfaces 1–3.

Histories are tool-owned and append-only, and a model-supplied history is discarded
outright — the trail is only evidence if the agent cannot author it. Each write
contributes at most one observation per score, and an unchanged repeat collapses, so a
stalled score cannot look like progress.

Low scores defer rather than nag, with exactly one exception: the *first* plan write
below 60 fires immediately, because a whole turn of wrong work cannot be undone at turn
end. One gate blocks rather than defers — completing a group needs end-to-end ownership
at 96 — and a rejected write leaves the stored list untouched.

**A real infinite loop, found by looking at the screen.** The first probe of the todo
card showed `Done.` sixty times and `Stopped after 60 tool rounds`. The incomplete-todos
branch resets the gate counter per spec (open todos mean the model is still iterating),
but that is only true *if the list moves*. A model that answers the nudge without
touching its todos loops until the step cap catches it — the exact failure §12.6 exists
to prevent, in the one branch that had no breaker. Added a progress fingerprint: three
nudges that change nothing disarm with an explanation. Sixty pokes became three.

**A second bug, also visual.** The delta rows vanished on the second probe run. The
delta was being passed to the UI through a side channel written on the tool goroutine and
read on the render goroutine — a race. It now rides the event as `Result.Display`, so it
cannot race, and `-race` is green.

**A probe-rig isolation failure with real consequences.** Chasing the above showed the
probe writing to the user's *real* `~/.local/share/evilcode/`. `probe.sh` pinned a
throwaway `HOME`, but `XDG_DATA_HOME` is an absolute path commonly exported by a login
shell and takes precedence over `HOME` in the XDG lookup — so the isolation was silently
defeated. The pane now pins all four XDG dirs under the fake home, and `reset_fixtures`
clears accumulated state so a probe run is repeatable. The stray real directory was
removed.

Verified: 44 todo tests, 20 plan/todo-card tests, 4 poke-hook tests in the agent package.
`go test -race` green. PNGs looked at for the plan card and the todo card — the
`75→100%` arrow renders, and the nested ```bash block sits inside the card's borders
rather than terminating it. 16 goldens green.

## 2026-07-31 P2.5–P2.9 — anchors, ask, git tools, role routing

Done: hash-anchored editing, the `ask` tool with its inline picker, the three read-only
git helpers, and `[roles]` routing with per-repo pinning.

**Anchored edits** (§17). `read` prefixes each line with a 4-hex content hash
(`a3f2|417| func main() {`) and records the file's mtime, size, and anchor map. `edit`
accepts `{anchor, op, lines}` patches alongside the exact-string form. Everything
resolves before anything mutates, so a partly invalid patch set changes nothing. Three
refusals, all loud: the file changed since it was read, the anchor is not in the version
read, or the anchor matches two identical lines. Fuzzy-matching any of those would
corrupt a file to save a retry, which is a bad trade.

**Role routing** (§16). A repo may pin `[roles]` and `default_model` via `.evilcode.toml`,
but the merge is deliberately narrow: a repo cannot add providers. Checking out a
repository must not be able to redirect the user's API keys somewhere new, and a test
asserts an injected `[[provider]]` block is ignored.

**ask** (§17) reuses the model picker's chrome rather than inventing a second visual
language for "choose one of these". A pending question owns the keyboard and the
composer becomes an answer box. Esc is an answer — the tool reports that nothing was
chosen rather than hanging. Headless omits the tool entirely rather than shipping one
that always fails.

Verified: 12 anchor tests, 5 role/repo-override tests, plus the existing suites.
PNG looked at: the ask picker mid-question, with the knight-rider bar running while the
tool blocks. Answered it under the probe and confirmed the choice reaches the model.
20 goldens green.

## 2026-07-31 P2.10–P2.14 — soft interrupts, history search, help overlay

Done: `docs/soft-interrupt.md`, the Ctrl+R reverse search, Ctrl+Up retrieval of staged
messages, Esc's poke-disarm semantics, and the full-screen help overlay.

`docs/soft-interrupt.md` writes down why an interleave is not a new API request: it is a
user message appended at a safe point so the next loop iteration carries it with the
cache prefix intact. Cancelling and re-requesting would be simpler and wrong in three
ways — it discards the turn, discards the cache, and treats every aside as a retraction.

Ctrl+R is readline's reverse-i-search including the part that makes it usable: the
selected match previews live into the composer, and cancelling restores the exact draft
from before. Selection clamps rather than wrapping — a history search that jumps to the
newest entry when you hold Up is disorienting.

Esc and Ctrl+C now differ where it matters. Esc means stop, so it disarms auto-poke;
Ctrl+C means skip this, so it does not. A harness that re-poked immediately after being
told to stop would be ignoring the instruction.

The help overlay's sections are hand-curated for readability, which risks drift, so the
uncovered remainder is computed and shown under "More commands". A test asserts every
visible command appears somewhere in the rendered overlay.

Two shared-code cleanups: the palette and the search now use one `spliceOverlay`, so
zero-height overlay placement has a single implementation; and box widths are measured in
cells rather than bytes, which a test caught — the footer's `·` separators are multibyte
and left the box ragged by three columns.

Verified: 18 new tests. 24 goldens green. PNG looked at for the help overlay and the
history search with its live composer preview.

## 2026-07-31 P2.15 — widget dock engine

Done: `internal/tui/dock.go` (placement, anchoring, hysteresis) and `widgets.go`
(Todos, ContextUsage, ModelInfo, GitStatus, BackgroundTasks, Tips, plus the meters and
the right fact stack).

The dock measures, per transcript row, how many trailing columns are blank, and places
boxes there. Widgets therefore cost zero layout height: they live where the text is not,
and a long line simply pushes them aside.

Anti-jitter is the whole design (invariant 4). A widget pins an *absolute transcript
line*, not a viewport row, so it scrolls with the content it was docked beside. When a
wide line slides under it, it hides in place rather than moving, and only re-homes after
the slot has been unusable for 120 consecutive frames — re-homing on the first bad frame
is what makes widgets skitter during streaming. New placements are chosen against a
look-ahead profile of the next 12 rows, so a fresh slot is not covered by the next line
to arrive.

Meters are coloured by what REMAINS rather than what is used: a bar that reddens as it
fills is a progress bar, and one that reddens as it empties is a warning. A test asserts
the direction, since the two are one comparison apart.

Verified: 17 dock/widget tests, including that a widget holds its row across ten frames,
hides rather than jumps when covered, returns to the same slot afterwards, re-homes only
past the threshold, and never overlaps another widget. One test bug found and fixed along
the way — the first scroll test held the rows array constant while scrolling, which is
not what scrolling does. PNG looked at: the Todos widget docked in the right margin.
25 goldens green.

## 2026-07-31 P2.16–P2.17 — centered mode, overscroll, typing lock

Done: Alt+C centered layout, the elastic pull-to-reveal facts line, Alt+S typing scroll
lock, and the right fact stack.

Overscroll only reveals when the gesture *began* at the bottom. Momentum that merely
arrives there is swallowed, so scrolling down through a long transcript does not flash
the facts line at the end of every scroll — that distinction is the whole difference
between "intentional" and "twitchy". The gesture gap is asserted by test to exceed the
redraw cadence, since a shorter one splits a single flick into two gestures and the
reveal never fires at all.

Centering is literal left padding rather than per-line centering, which keeps copy and
column math sane. Two ordering bugs found by looking at the frame: widgets were docked
before the padding was applied, so in centered mode they were pushed off the right edge;
and the left gutter was counted twice, because `ContentWidth` already includes it in the
pad it returns. Overlays now carry the same padding so they line up with the content
rather than the terminal.

Verified: 8 overscroll/centered tests. PNG looked at in both alignments.
