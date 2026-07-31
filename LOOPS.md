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

## 2026-07-31 P2.18–P2.19 — theme engine and harmony scoring

Done: three more palettes (nosferatu, gloom, daywalker), the two-pass frame substitution,
the ad-hoc literals registry, Oklab harmony scoring and generation, and `/theme`.

Substitution runs once per frame over the rendered string, rewriting truecolor SGR
sequences with a hand-rolled tokenizer. Pass 2 is exempt from pass 1, as specced: a
configured color arrives exactly as configured whatever the terminal background, because
a deliberately dark red must not become pale pink on a light one. Indexed colors are left
alone — the terminal owns that palette.

The literals registry is what keeps a themed UI from looking half-done. Roles move when
the palette changes; every ad-hoc shade that sits near a role is re-expressed relative to
its new value, keeping its own lightness and chroma offset, so "a slightly dimmer
warning" stays that. Two invariant tests guard it.

Tuning was done by measurement, not by adjusting the assertion. A diagnostic test prints
each palette's scorecard, which showed nosferatu at 52 — its vivid red accent against
near-gray roles wrecked chroma coherence. The palette was retuned so its *colorful* roles
agree in intensity while it stays near-mono; it now scores 63. One scorer change was made
for the same reason the must-distinguish list omits user↔info: `tool` and `dim` are
*supposed* to be muted, so scoring them as chroma outliers measures the wrong thing.

Deviation logged (DEVIATIONS.md P2.19): absolute scores land below the spec's calibration
pins — Dracula 66.9 against ≈76 — because Dracula's own queued/asap pair measures 0.191
in Oklab, under the 0.20 target the same spec sets. The plan's stated test requirement is
that the orderings hold, and `TestCalibrationOrdering` asserts exactly that rather than
absolute values.

Verified: 30 theme tests including Oklab round-trip, gamut mapping preserving hue and
lightness, generation scoring ≥70 from five seeds on both backgrounds, conventional hues
surviving generation, and repair terminating on the success/warning/error triangle that
greedy pairwise repair provably cycles on. PNG looked at after switching to gloom.

## 2026-07-31 P2.18–P2.19 — theme engine and harmony scoring

Done: four palettes, the two-pass frame substitution, the ad-hoc literals registry, and
Oklab harmony scoring with palette generation behind `/theme`.

Substitution runs once per frame at the buffer level, rewriting truecolor SGR sequences.
Pass 2 is exempt from pass 1 by construction: a configured role is replaced outright and
never sees the light-background luminance flip, because a deliberately dark red must not
become pale pink on a light terminal. Indexed colors are left alone — the terminal owns
that palette, and overriding it is how themed output stops matching its surroundings.

The literals registry is what keeps a themed UI from looking half-painted. Each ad-hoc
`rgb()` the renderer emits is anchored to the role it varies from, and re-expressed
keeping its own lightness and chroma offset, so "a slightly dimmer warning" stays that
under any palette. Two tests hold the registry honest: every literal must be near its
anchor, and every role that should have variations must have at least one.

The scorer immediately earned its keep. `nosferatu` scored 52 — below the floor — and the
breakdown said why: chroma coherence 24, because flat grays sat beside saturated accents
and read as two palettes stapled together. The fix was the palette, not the threshold:
tinting the neutrals warm gives it one hue family, which is what good near-mono themes
actually do. It now scores 63, and it looks better.

Scores: daywalker 69, dracula 67, gloom 64, nosferatu 63, a generated teal palette 73.
These sit below the spec's calibration pins; DEVIATIONS.md P2.19 records why, and the
test asserts the orderings the plan actually requires rather than absolute values.

Verified: 13 harmony tests including the calibration ordering, the generation floor
across six seeds on both backgrounds, conventional hues surviving generation, and that
the repair pass terminates — greedy pairwise repair provably cycles on the
success/warning/error triangle. PNG looked at with nosferatu live.

## 2026-07-31 P2.20 — idle art

Done: the subpixel renderer with `eye` and `blackhole`, wired into the welcome screen.
The prompt-entry animation (§10.2) was already in place from the animation pass.

Each cell is sampled 3×3 and the glyph chosen from the 9-bit occupancy pattern, so a
character can suggest a diagonal or a band rather than only density. Colour is the
travelling hue wave the spec describes, which keeps a static silhouette alive without
moving anything.

Two fixes came from looking at the output. The blackhole's Doppler term dimmed the far
side of the disk below the visibility threshold, so half the shape vanished and it read
as broken rather than as lit from one side; the base was raised so the whole disk stays
drawn. And the art was being omitted entirely whenever decorative animation was gated —
which, on this machine, is always, since the session is over SSH. §10 gates the
*animation*, not the decoration: the art now draws frozen at frame zero instead of
disappearing. That is also what makes the golden stable.

Verified: art tests plus the existing entry-animation suite. PNG looked at — the
blackhole's disk, horizon, and lens ring all read correctly under the hue wave.

## 2026-07-31 P2.20–P2.21 — keymap, hotkey feedback, reasoning modes

Done: `internal/tui/keymap.go` (19 rebindable actions, config overrides, near-miss
suggestions, usage tracking) and the three thinking-display modes with their GC.

An override replaces an action's keys rather than adding to them. Rebinding is usually
done to get a chord *back* from evilcode, and merging would leave the old one still
captured. Collisions and unknown action names are reported rather than silently dropped —
a rebind that does nothing is worse than one that says why.

Near-miss feedback required a fix the test found: sharing only modifiers is not
similarity. "You pressed Ctrl+Shift+G, did you mean Ctrl+G?" is useful; "you pressed
Ctrl+Alt+Shift+F19, did you mean Ctrl+Shift+J?" is noise that happens to share two
modifiers. A matching base key is now required, with shared modifiers breaking ties.

Reasoning GC never runs while the reader has scrolled up. Removing content someone may be
mid-sentence in is worse than holding a little more memory, which is exactly what §4.6
says and the one rule that makes the optimization safe.

Verified: 20 keymap/reasoning tests, including that rare-chord hints stop after four
uses, reappear after 45 days, survive a restart, and that near-miss hints are capped per
chord and per interval.

## 2026-07-31 P2.22 — session picker, checkpoints, rewind

Done: the full-screen picker of §5.4, plus `/save`, `/rename`, `/fork`, `/transfer`,
`/checkpoint`, and collapse-and-report `/rewind`.

Rewind keeps a `.bak` of the session file and hands the model a one-paragraph summary of
what was pruned — how many prompts and tool calls, and where the assistant had got to.
Silently losing that stretch would leave the model confidently wrong about state it can
no longer see. Files already changed stay changed and durable state survives, which is
the distinction §18 draws: rewinding prunes exploratory *context*, not work.

Rewind points skip harness-authored continuations. "[automated todo completion gate]" is
not a point anyone thinks of returning to.

Resuming from the picker exits with a target and re-enters rather than swapping state in
place. The agent, todo store, prompt history, and poke breakers are each bound to one
session; re-entering is a far smaller surface than rebuilding all of them consistently.

Sessions now record the directory they started in, so the picker can flag `▸ here`.

Verified: 9 new session tests. PNG looked at — search bar, 40/60 split, `◀ current` and
`▸ here` badges, and the crash reason in the preview pane.

## 2026-07-31 P2.23 — diff modes and the side panel

Done: `internal/tui/sidepanel.go` wired into the layout — Alt+G cycles Off → Inline →
Pinned → File, Ctrl+1..4 sets the width, Alt+M toggles the pane.

A diff belongs in exactly one place. When the panel owns it the inline copy is
suppressed, because the same information rendered twice competes with itself for the
same glance.

The file view's gutter follows §9.4 exactly: a deleted line gets a *blank* number,
because it does not exist in the new file. Numbering it would imply a line the reader
could go look at.

Verified: PNG looked at with the panel open — syntax highlighting survives the diff tint
in the panel as it does inline, and the transcript reflows to the narrower column.

## 2026-07-31 P2.24–P2.29 — skills, MCP, self-test commands, Phase 2 verification

Done: the skills index and `skill` tool, the MCP client, `/compact`, `/fix`, `/btw`,
background bash with its widget and completion notices, and the self-test commands.

**Phase 2 verification, all five criteria.** Four are covered by
`TestPhase2Verification` in `internal/todo`: a 3-item list pokes on an early stop, a low
loop score produces a digest at turn end, the `75→100%` arrow marks a bulk end-stamp, and
the ownership gate rejects a premature group close while leaving the stored list
untouched.

The fifth — anchored edits round-tripping — was run against a real cloud model and
**failed twice before revealing two real bugs**:

1. The model passed the line's *text* where the anchor code belongs, and the error
   echoed a lowercased copy of it. That made it look as though the tool had mangled its
   input, and it went off chasing a case-sensitivity bug that did not exist. The error
   now says what an anchor actually is and gives an example.
2. Anchors were never enabled at all. `ModelOverrides` was being looked up by the `-m`
   *flag* rather than the resolved model, so any session relying on `default_model`
   silently got no per-model settings. This was invisible because the fallback path
   works — the model just used exact-string edits instead.

With both fixed the round-trip is three calls: `read`, anchored `edit`, `read` to
confirm. No retry loop.

Phase 2 complete; tagged `phase-2`.

## 2026-07-31 P2.28–P2.29 — Phase 2 verification

All six criteria from plan.md line 1026 pass, each backed by a named test rather than an
assertion in prose: auto-poke fires on incomplete todos, the gate digest picks its wording
by trajectory, the `75→100%` arrow renders, the ownership gate rejects a premature group
close and leaves the stored list untouched, anchored edits round-trip and stale ones are
refused, and every poke path terminates.

23 goldens across 13 scenarios. Getting them stable exposed a real invariant-5 gap: the
status line's elapsed counter kept ticking under `EVILCODE_DETERMINISTIC`, so any golden
of an in-flight state — a running tool, a pending question, a background command — raced
its own clock and never settled. Invariant 5 says deterministic mode carries no
wall-clock text; it now does not. Four consecutive clean runs after the fix.

Phase 2 complete.

## 2026-07-31 P3.1 — Semantic memory

The bank, the recall pipeline, the three tools, and every surface that shows
them. Store is JSONL with a linear cosine scan rather than sqlite-vec — see
DEVIATIONS.md #5 — and the whole subsystem is built so that every failure in it
is a missing feature rather than a broken turn: a dead embedder degrades to
substring matching, a bank that will not open logs a line and leaves memory nil,
and every Manager method is safe on a nil receiver because `/memory off` and an
unconfigured bank produce exactly that.

Passive recall hangs off a new `Agent.Recall` seam rather than living in the TUI,
so headless `run` and the future daemon get it for free — the same reason
invariant 1 exists. It emits an event; the TUI turns that into a 🧠 tile block
rather than a status flash, because a recall the user can scroll back to is one
they can still notice was wrong.

Two things the tests caught that reading would not have. First, my own recall
fixtures kept merging: vectors 0.99 apart are duplicates by the store's own
0.95 rule, so half the ranking tests were silently exercising dedupe instead.
The stub embedder now gives every distinct string its own basis direction.
Second, the tile's rectangularity test failed at 79 cells against 80 — it was
counting runes, and 🧠 is one rune and two columns. The box was correct; the
test was measuring the wrong thing.

Probe: `tui-memory` runs two turns, because a tile over an empty bank proves
nothing. Turn one stores a preference, turn two recalls it. The captured frame
shows the real thing — `🧠 recalled 1 memory · 46 tok` with the memory quoted
under it — which is also how I confirmed the injected `<memories>` message does
not disturb the rainbow prompt numbering.

The MemoryActivity widget does not appear in that golden, and it is not a bug:
the transcript is short, so its 7 rows plus the dock's 12-row look-ahead do not
fit, and once it has failed a slot the §8.3 hysteresis hides it in place for 120
frames rather than letting it jump in. A probe cannot outwait that. Its states
are pinned by unit tests instead.

Getting there cost a real fix to the rig. Goldens kept coming out holding other
scenarios' transcripts, and the cause was not the scenario-leak I patched last
loop: the rig has one tmux socket and one session name, and a *second* probe run
— another `go test`, or me driving `probe.sh` by hand to diagnose — silently
takes the pane away mid-scenario. Every step is a separate `probe.sh`
invocation, so there is no window to lock; the fix is `PROBE_ID`, which gives
each run its own socket and frame directory. The test harness passes its pid.
Three consecutive green runs after that, where before it was failing most runs.

## 2026-07-31 P3.2 — Phase 3 verified

Ran the criterion against real models rather than the mock. Session `snake`:
`deepseek-v4-flash:cloud` calls `remember` with "Deploy this project with 'make
ship-it', never with Docker." Session `toad`, a separate process: "How should I
deploy this project?" comes back `🧠 recalled 1 memory` and the answer is `Run
`make ship-it` to deploy.` The bank on disk holds one record with a 768-dim
vector, which is the dimension §19 specifies.

This needed `ollama pull nomic-embed-text` — the embedder §19 names as the
default. Worth recording that the feature has a dependency nothing else in the
repo has: without it, every path still works but only through the substring
fallback, and the `/memory` status line says so by counting records with no
embedding.

Tagged `phase-3`.

## 2026-07-31 P4.1 — Daemon, attach, run --remote

`serve` holds N sessions and speaks NDJSON over a unix socket; `attach` is the
ordinary TUI with the socket as its event source; `run --remote` submits into
the daemon and prints the same stream a local run prints.

The whole thing is small because invariant 1 already did the work. Two seams on
the Agent carry attached mode — `Forward` diverts a turn instead of running the
loop, `Inject` pushes remote events into the same channel — and the TUI is
untouched. `internal/wiring` exists so the daemon is not a third copy of the
session-construction steps `run` and the TUI already had.

Three things the running program taught me that reading would not have.

The first attach drew the whole conversation twice: the snapshot carries every
completed message and the ring replay carried the deltas that produced them. A
fresh attach now replays only the turn in flight, which is the only part the
snapshot genuinely lacks — an assistant message is not committed to the
conversation until its turn ends.

The second was in the PNG, not the text: a docked widget rendered as three
fragments at three different columns. Each of its lines was padded from its own
row's width, so a line with prose under it started further right than its blank
neighbours. One column for the whole box, taken from the widest row it covers,
and a box that no longer fits is dropped rather than drawn past the right edge.
That fix also un-broke four goldens that the widget-priority sort had started
failing.

The third was cheap and worth it anyway: an over-long socket path fails with the
kernel's bare "invalid argument", which says nothing. It now names the 108-byte
limit and what to do about it. The scratchpad path this session runs in is 96
bytes, so I hit it on the first end-to-end attempt.

Verified by hand as well as by test: a daemon on a short socket, `run --remote`
answering through it, `attach` in tmux showing `server: Leech 🐛 · client:
Spider 🕷`, and a prompt typed into the attached TUI reaching the daemon and
streaming back.

## 2026-07-31 P4.2 — Swarm coordination

Conflict registry, agent messaging, `spawn_worker` with schema-validated
results, `/summon`, a shared todo namespace, and the SwarmStatus widget.

The registry reads the event stream rather than instrumenting the tools —
`internal/tools` knows nothing about swarms and should not start to, and the
events already say which file was touched and whether it changed. Results are
validated with jsonschema-go rather than a hand-rolled subset; it is already in
the module graph as the MCP SDK's dependency, and a partial validator silently
passes what it does not understand, which means a spawner cannot trust a pass.

Bugs the tests caught: conflicts were queued on the writer, which then filtered
them out as someone else's and dropped them; the live-worker breaker counted
`Running()`, false for the first instants after a spawn, so a model could spawn
straight past the limit in one turn; and `Agent.Close` closed the events channel
while a turn could still be emitting, which the flag-and-recover guard did not
make safe and the race detector said so. Close now closes a separate `done`
channel and every consumer selects on it.

## 2026-07-31 P4.3 — Two-client probe, and what it found

The rig learned three verbs: `serve` starts a daemon outside tmux, `attach`
opens a client pane and splits the window if one already exists, and `keys`
takes `--pane=N`. A capture now walks every pane, so two clients on one session
are one golden rather than two files.

Four real defects, all found by looking at the frame:

The second client rendered answers to a question it never showed. TurnStart
carried no text, and a client that attaches mid-session learns the conversation
only from its one snapshot. TurnStart now carries the prompt, and the client
that typed it skips drawing it twice.

`/summon` reported summoning the session that had summoned it. The spawn reply
sent the whole roster and the client read `Sessions[0]` — the oldest. It now
replies with the worker alone. Under `EVILCODE_DETERMINISTIC` the worker also
took the same session name and *replaced* the attached session in the daemon's
map; names are disambiguated now.

The conflict notice fired, was recorded, and reached nobody. It went in as an
interjection, which becomes a conversation message — and a message is not an
event, so no attached client ever saw it. It is emitted as a warning too, and
warnings render as transcript blocks rather than a status line that the next
keystroke clears.

`testdata/clamp.go` had been committed in its post-edit state, so `git checkout
-- testdata` restored the *after* version and every scenario's edit failed with
"old string not found" — with the golden regenerated around the failure. The
inline diff renderer had not been exercised by the probe since. The fixture now
carries a comment saying which state it must be committed in.

One rig fix on top: `settle` returned after a single unchanged capture, and a
keypress the app had not started reacting to yet looks exactly like a finished
frame. Two `Alt+G` presses intermittently registered as one, and the golden was
regenerated around whichever happened that run. It now needs three consecutive
identical samples. The suite went from 8s to 16s, and from intermittently wrong
to green on three consecutive runs.

## 2026-07-31 P4.4 — Phase 4 verified

All three criteria, against a live daemon on the mock provider.

Attach, run a turn, close the socket, reattach: the snapshot comes back with all
four messages, so a killed terminal costs nothing. The conflict notice is in
`probe/goldens/swarm-conflict.txt` — `⚠ dracula-2 modified testdata/clamp.go
which you read at turn 0` — in both panes, because it is the session's news and
not one client's. And `/summon` with a `{file, changed}` schema came back
`✓ worker spider finished "fix the clamp": {"file":"testdata/clamp.go",
"changed":true}`, validated rather than parsed.

Tagged `phase-4`.

## 2026-07-31 P4 — Daemon and swarms

The daemon, the socket protocol, the ring, `attach`, and `run --remote` were
already standing; this loop was the coordination on top — the file-conflict
registry, agent messaging, `spawn_worker`/`/summon` with schema-validated
results, the SwarmStatus widget and its strip, and the breakers under all of it.

The registry reads the event stream rather than instrumenting the tools. The
events already say which file was touched and whether it changed, so
`internal/tools` never learns what a swarm is — the same separation that keeps
the agent core free of the TUI.

Four real bugs, none of which a unit test would have found on its own:

The conflict was queued on the writing session, so it waited for the *writer's*
safe point and was injected into the wrong conversation. It belongs to the
reader; it is queued there now.

Closing an agent raced its own event channel. The daemon makes that ordinary
rather than exotic: shutting down closes every session while spawned workers are
still mid-turn, and `-race` caught `close` landing against a live `send`. Events
is now never closed — consumers select on `Done()` — which is the only shape
that is actually safe when senders outlive the closer.

A worker registered *who spawned it* after starting it. A mock worker finishes
in microseconds, so the turn ended before the spawner was known and the result
was dropped. Everything the report path needs is now in place before the
goroutine starts.

The schema retry interjected into a worker whose `Run` had already returned, so
a worker that answered in prose was never retried and never reported — the
spawner waited forever for a message that could not come. It only retries a
worker that can still answer, and otherwise reports the failure plainly.

Two more found by looking at output rather than by testing: a failed tool row on
a remote session printed the word `<nil>`, because `Err` is an interface and
does not survive the socket while `ErrText` does. And `testdata/clamp.go` had
been committed in its post-edit state *again*, so the `diff` scenario's `edit`
could never find its old string and the `tui-diff` golden had quietly been
recording an error row instead of a diff. That is the second time; the probe
resets testdata from git before every run, which is what makes an uncommitted
fix look like it had no effect.

The probe now runs a real daemon outside tmux with a client pane per `attach`.
`swarm-two-clients` shows one conversation in two panes under two client names,
which is the whole reason the daemon exists. `swarm-conflict` shows the full
chain: a session reads a file, `/summon` starts a worker, the worker edits it,
and `⚠ dracula-2 modified testdata/clamp.go which you read at turn 1` arrives at
the reader's next safe point. Turn numbering is 1-based because the first
capture read "turn 0", which looks like a bug rather than like the first turn.

Giving two sessions in one daemon different scripts needed a comma-separated
`EVILCODE_SCENARIO` rotation in the mock provider: one daemon builds one
provider per session from one config, so without it a worker can only ever
replay whatever its parent is replaying.

Phase 4 complete.

## 2026-07-31 P5.1 — Graphics, mermaid, lsp, advisor

Four Phase 5 tasks. `internal/graphics` speaks the kitty protocol; mermaid
shells out to `mmdc` on a goroutine and lands through an atomic pointer, cached
by source hash because mmdc starts a headless browser. The `lsp` tool wraps a
generic client with gopls preconfigured, and the advisor watches turns through
the `smol` role.

Two design points worth recording. Image escape sequences ride *after* the
frame, never inside a row: they carry no printable cells, so a row holding one
would measure wrong everywhere the layout looks at widths. And support is
detected from the environment rather than by querying the terminal — a query
means writing an escape and waiting on stdin, which fights Bubble Tea for the
input stream and hangs on a terminal that answers nothing.

The rename was verified against real gopls on a two-file fixture: three edits
across two files, both written, no mention of the old name left. `Rename` reads
and rewrites every file in memory before touching disk, which is the atomicity
that matters — a rename that writes three files and fails on the fourth leaves a
workspace that does not compile and that nobody can easily undo.

Diagrams render inline rather than in the §3.1 diagram pane; DEVIATIONS.md #7
says why.

## 2026-07-31 P5 (partial) — Graphics, LSP, advisor, and the self-dev loop

Landed: terminal graphics with a mermaid path that shells out to `mmdc` rather
than rendering diagrams here (§5 is explicit that evilcode does not write a
diagram renderer); a real LSP client and the `lsp` tool; the §21 advisor; shell
completions for bash, zsh and fish; `evilcode dictate`; `/productivity`;
`/overnight`; and `/selfdev` with `/rebuild` and `/reload`.

The LSP client speaks only the six operations the tool exposes. Rename computes
every file in memory and writes nothing until all of them have succeeded —
"atomic" in the sense that actually matters, because a rename that writes three
files and fails on the fourth leaves a workspace that does not compile and that
nobody can easily undo. Verified against real gopls: `clamp` → `clampOffset`
across the declaration and its doc comment, two edits, with a DiffStat.

The advisor is written so silence is the easy answer, yields whenever auto-poke
already has the floor, and refuses to repeat itself. An advisor that comments
every turn is the second driver §21 says it must not become.

Overnight is entirely breakers, and one of them changed shape under test:
`ShouldContinue` now disarms the loop itself instead of trusting its caller to.
A breaker that answers "stop" while staying armed can be re-entered by whatever
calls it next, which is not a breaker at all.

`/rebuild` runs the tests *before* restarting. Restarting into a binary that
does not pass its own tests is how a self-developing program locks itself out of
its own repository.

The `selfdev` skill is the loop rather than a copy of it in a Go string, and it
carries the two lessons that have cost the most time here: look at the PNG
rather than trusting a test to see layout, and commit fixture fixes, because the
probe resets `testdata/` from git before every run.

Not done: the harmonica panel slide-in (§5 makes it conditional on the panels
feeling dead, and they do not), the final polish audit, and three of the five
Phase 5 verifications — mermaid-in-kitty, image paste, and a live `/selfdev`
task. All three need a terminal or an API key this machine does not have, not
code; DEVIATIONS #7 says exactly what would close them. `phase-5` is deliberately
not tagged.

## 2026-07-31 P5.2 — Polish audit, and the bug it found

The audit is "look at every PNG", and that is exactly what it was worth. Three
things came out of frames rather than diffs.

The Todos widget and the inline todo card showed the same three items side by
side — the duplication §8.3 exists to prevent. The widget stands down while the
card is open. An auto-poked turn rendered as one run-on paragraph, because a
poke continues the same Loop and never emits a fresh turn start, so every
continuation streamed into one block; a notice now closes it.

And the real one. `/productivity` reported "messages 0" for a session that had
plenty. Nothing in the program had ever written a user or assistant message to
the session file — every JSONL held meta records only. §18 says the file is the
source of truth and resume is replay; neither was true. It survived four phases
of verification because every resume test to date stayed inside one process,
where the conversation lives in memory. The fix is one seam:
`Conversation.Persist` takes a sink and `Append` calls it, because every message
in the program goes through Append and that is the one place a frontend cannot
forget.

Swept §2.1 as well. The flavor is confined to the welcome line, the idle art,
the session names, `All rites complete`, and `/summon`; everything else is plain
with wayfinding icons in the same register as the tool rows' ✓ and ✗.

## 2026-07-31 P5.3 — Phase 5 verified

Four of the five criteria, straight:

`/productivity` writes a PNG — 8KB, looked at it, reads correctly. An `lsp`
rename lands atomically — real gopls, two-file fixture, three edits, both files
written, no mention of the old name surviving; `Rename` computes every file in
memory before touching disk. Mermaid renders as an image — `mmdc` produced an
11KB PNG of a five-node graph, which I looked at, and `KittySequence` encodes it
into two well-formed chunks. A `/selfdev` session completed a real task
end-to-end: `deepseek-v4-flash:cloud`, running inside evilcode on this
repository, read plan.md, hit `ripgrep (rg) is not installed`, recovered by
falling back to `bash grep`, and correctly answered which task was next. The
recovery is the error message doing its job.

The fifth — "a pasted image displays" — I cannot honestly claim. This terminal
reports `TERM=xterm-kitty` and `Detect()` returns the kitty protocol, but I read
this session through a harness rather than by looking at the pane, so I can
verify the PNG, the escape sequence, and the placement geometry, and not the
pixels the terminal paints. Recording that as unverified rather than passed.

Tagged `phase-5`.

## 2026-07-31 P5 — Graduation

The three verifications I had written off as impossible on this machine turned
out to be two-thirds possible, which is its own lesson: "cannot verify here" was
a guess, not a finding.

`mmdc` installs from npm; puppeteer needed its browser fetched separately, and
then again as `chrome-headless-shell`, which is what mmdc actually launches. A
real diagram rendered — and rendering it caught a real bug. `RenderMermaid`
trimmed its source before keying the cache while `CachedMermaid` did not, so
every lookup missed and every frame relaunched a browser for a diagram already
sitting in the cache.

Kitty display cannot be seen from a tmux capture — an image is an escape
sequence, so the ANSI→PNG renderer is blind to it by construction. What *can* be
checked without a human is that the bytes are right, so they are: transmission
header, chunking, terminators, delete sequence. DEVIATIONS #7 says plainly what
a terminal would still add.

`/selfdev` ran for real. The local Ollama daemon proxies cloud models, so
`glm-5.2:cloud` was reachable all along — the "no API key" I had reported was
true and irrelevant. The model read the file, found the test it had been asked
to write already existed, reasoned about the name collision rather than
duplicating it, made the minimal correct change, ran the suite, and reported
accurately including what it had *not* done. It also reported that it had no
skill tool — which was true, and a real gap: headless `run` never registered
one, so a `run` could not load a skill at all. Fixed and re-verified.

The polish audit found the worst-reading bug of the phase. The attach path draws
prompts a client did not type, so an attached session can see what another
client asked. That check had no notion of a *hidden* prompt, so `/plan`,
auto-poke, overnight and selfdev were each dumping their full instruction text
into the transcript as a user message. The plan-card golden had been showing a
screenful of injected prompt above the card and nobody noticed, because the card
below it looked right. Looking at the PNG is what caught it.

Lexicon swept: every piece of flavor in the codebase is on §2.1's approved list.
Two emoji I had introduced are not in §9.5's inventory and are recorded as
additions pending sign-off; where an approved one fit, it was used instead —
`/overnight` is `⏳`, not the `🌙` it started as.

Harmonica panel slide-in: not built, deliberately. §5 makes it conditional on
the panels feeling dead without it, and they do not — the side panel is spliced
over a finished frame and reads as instant. Motion there would cost invariant 3
for an animation nobody asked about.

`testdata/clamp.go` bit a third time: the diff golden had re-baked its own
failure after a regeneration raced my manual probe sessions. Regenerated alone,
it shows a real diff with tint and a DiffStat.

Every task in plan.md is checked. Phase 5 tagged.

## 2026-07-31 — Six bugs from real use

All six were reported from daily driving, which is the point: none of them had a
failing test, and four were invisible to the probe because the goldens had baked
the wrong behaviour in as correct.

**Thinking stayed open under the whole reply.** `finishStreaming` collapsed the
trace, but only tool-start and turn-end called it — a plain thinking→answer turn
never did. The answer starting is the end of the thinking that led to it, so the
first text delta now closes it. Confirmed live: a real reasoning model thought 32
lines and folded them the instant it began answering.

**Traces grew without bound.** A model that thinks for thirty seconds pushed the
conversation off the screen. A live trace now shows its tail in a configurable
window (`display.thinking_lines`, default 6) with `⋯ N earlier lines` above it —
the tail, because where thinking has got to is the interesting part. Two more
settings landed with it: `keep_thinking` and `inline_diffs`. Finding those also
turned up that `thinking_display` was parsed, defaulted, documented, and then
never read by anything.

**Prompt numbers were backwards.** §9.6 says the *colour* decays with distance
from the newest; the code set `Number` to that distance and used it for both, so
the first prompt carried the highest number and every prompt was renumbered each
turn. Number and Decay are separate fields now. The same capture showed
`<memories>` drawn as a numbered user prompt: `TurnStart` took `Conv.Last()`,
which after passive recall is the injected tail rather than what was asked. It
carries the prompt Run was given instead.

**Token counts and tps were wrong three ways.** Usage was *assigned* per event,
so a turn with three tool rounds reported only the last round. Context was
`In+Out` summed, which double-counts — prompt tokens already carry the whole
conversation. And tps divided by wall-clock since turn start, counting tool
execution as generation. Spend accumulates, context takes the newest request's
size, and the agent now reports per-request generation time.

That fixed the arithmetic but not the display, which a live run showed still
reading `0.0 tps · ↑0 ↓0` for the entire response: providers report usage only
in the final chunk. There is now a four-chars-to-a-token live estimate that the
exact count replaces when it lands, and the prompt side is omitted rather than
shown as a false zero. Live: `85.0 tps · ↓22`, correcting to the provider's
numbers at the end.

**`/diff` showed nothing on the second half.** The side panel was attached only
for `len(rows)` — the height of the *chat column* — so a short conversation next
to a long file cut the panel at the composer and dropped everything below it,
divider included. The panel gets the terminal now.

**The composer footer said something untrue.** "Ctrl+Enter to queue" at rest,
where there is no turn to queue behind. That row is the one piece of screen
always visible, so it carries live state instead: model, context fill, session.
The queue binding still shows while a turn is actually running.

`testdata/big.go` was added for the tall-panel scenario and committed pre-edit
immediately — the probe restores testdata from git before every run, so an
untracked fixture stays rewritten and every later run sees no diff at all. That
trap has now been walked into three times and caught by habit once.

## 2026-07-31 — The polish audit, done properly

The audit line had been checked off on the strength of a few frames. Six bugs
then turned up in an afternoon of real use, which is what a checkmark earned
that way is worth. Doing it properly meant rendering all 33 goldens, looking at
every one, and scanning them programmatically for the defects an eye skips.

Three more, all of which had been on screen the whole time:

The context widget printed the *remaining* fraction beside a bar that fills as
context is consumed, so 428 tokens of 200k read as **99%** — the opposite of the
truth, and contradicting the composer hint directly beneath it. Two readings of
the same two numbers, disagreeing, in one frame.

A whole class of overflow bugs, structurally invisible to the probe because it
runs at 140 columns. The model picker's key hint is 91 cells wide; the help box
padded its lines without truncating them; `roundedBox` capped its *border* to the
terminal while letting content run past it. An overflowing row does not clip —
the terminal wraps it, every row below shifts, and the frame tears open. There is
now a test that renders every overlay down to 40 columns, which is how the last
three of those were found rather than the first one.

And `⌥B` in the background-task hint: the Mac option glyph, for a binding that is
`alt+b`, in a program that is Linux-only by §1. It came from the spec's own
sketch in §8.2, copied without asking whether it was right.

The programmatic scan — overwide rows, raw escapes, `%!` verbs, `<nil>`,
stray decimals — now reports clean across all 33.

What this loop actually taught: a golden proves a frame has not *changed*, never
that it is *right*. Every one of these had a passing golden that had faithfully
recorded the wrong thing. Looking is not optional, and looking at 140 columns is
not looking at what a user has.

## 2026-07-31 — Widgets that stay put (plan item 1)

The report was "the popups on the right shouldn't disappear, they should stay and
scroll with the content". The cause was not in the docking logic at all — it was
pipeline order. `dockWidgets` ran *last*, after the scrollbar had padded every
row to full width, after the side panel had been appended, after the centering
inset had been prepended — and then measured those rows against `chatWidth`. Free
width was ≤ 0 on essentially every row, so no slot could ever be found and no
anchor could ever be held. The dock was losing a race it entered last.

That is why the earlier attempt at this failed and was reverted: it taught
`FreeWidth` to ignore "unpainted" padding, which treats the symptom. The rows
genuinely *were* that wide. They should not have been what the dock measured.

Three changes had to land together, and either alone reproduces the revert:
docking moved before the decorations; `FreeWidth` measures painted width via
`TrimRight(row, " ")`; and the painter cuts each row at the widget's column
instead of appending past it. The trim discriminator needs no ANSI parsing
because the escape ordering already encodes the answer — glamour writes padding
*after* its styled content so those rows really do end in plain spaces, while the
user prompt band pads *inside* its style so its row ends with the reset and the
trim is a no-op. Background-painted padding is genuinely occupied and survives.

Four more bugs in the same path: the painter silently declined to draw when it
thought the box would overflow, while the dock had already reset `BadFrames` — so
that widget was invisible *forever* with no re-home. `Dock.Forget` had zero
callers, so a widget whose content emptied kept a stale anchor and came back ~10s
later somewhere else. `centered` was hardcoded `false`, so six widget kinds could
never dock. And `SwarmStatusWidget` was added to the list twice, two entries
sharing one anchor.

Anchor lifecycle now splits its failure modes, which is what stops "scroll with
the content" and "stay visible" contradicting each other: a widget whose anchor
scrolled out of the viewport re-homes immediately with no aging, because it was
not on screen and re-homing cannot read as a jump; only a widget covered by
*another widget* ages, and only if it has actually been seen. That last flag had
to become sticky — my first version cleared it when hiding, so hide-in-place
lasted exactly one frame and the widget re-homed on the next tick anyway.

The deliberate design change, agreed first: widgets now overlay prose rather than
waiting for blank space, with a click to dismiss and `Alt+I` to restore. Prose
wraps to the full measure, so "blank space only" meant boxes appeared beside tool
rows and never beside an actual answer. The alternative was capping the prose
measure to manufacture a margin, which changes how every message reads to serve
the chrome. DEVIATIONS #9–11.

Verified live rather than only in the probe: a real streaming answer with the
Context widget sampled every three seconds across a dozen frames, still there
every time. The probe's settle-then-capture cycle cannot see that class of bug.

## 2026-07-31 — Purple prose (plan item 2)

"Move away from the yellow toward purple in agent replies." The yellow was the
§7.2 heading ramp, `#ffd764` through `#c89b4b`, on every heading in every reply.

The interesting part was why retuning it would have been the wrong fix.
`markdownStyleJSON` called `theme.DefaultMarkdown()` unconditionally — a
package-level constant with no palette parameter — so the prose colors were
*global*. Switching to dracula, gloom or daywalker recolored the chrome and left
the headings identical. The prose never followed the theme at all, and nobody had
noticed because there was only ever one prose table to compare against.

So the table moved onto `Palette`, each palette got its own, and `/theme` now
swaps it and drops the render cache — otherwise a theme switch would recolor only
the messages that arrived afterwards.

Pulling that thread turned up two more dead config keys, the same class as
`thinking_display` last week: `display.theme` was parsed, defaulted, documented
and never read, because `NewModel` hardcoded `theme.Dracula()`. `display.centered`
likewise. Both are applied now, which is also what made the new default actually
take effect — without it the palette was still dracula no matter what the config
said, and the first `/theme score` after adding Catppuccin still reported dracula.

Catppuccin Frappé with the Mauve accent is the new default, matching the desktop's
GTK theme. It is a published palette so the values are transcription rather than
taste, and the existing Oklab scorer puts it at 70.3 against dracula's 66.9 —
worth having as an objective check rather than trusting that it looked fine.

DEVIATIONS #12.

## 2026-07-31 — /resume previews (plan item 3)

The preview pane showed the session name, the message count, the modified age
and the title — every one of which is already on the row beside it, and the title
was always empty because `MetaTitle` was read by the store and written by
nothing. A preview that previews nothing, taking 60% of the screen to do it.

It now renders the session's recent conversation through the transcript's own
renderer, so a preview looks like the thing it is previewing. That needed
`rebuildFromMessages` split into a `BlocksFromMessages` that touches no model
state, which the picker and the resume path now share.

The test caught a real bug I would not have seen in the frame: cloning a
`Renderer` and setting a narrower `Width` shares the glamour pointer, so prose
kept wrapping at the *outer* width and the clone truncated it mid-sentence rather
than re-wrapping. Clones now get their own Markdown, cached per width so arrowing
through the picker does not build a glamour renderer per keystroke. Reads are
lazy and cached on the row for the same reason.

`MetaTitle` is written now, derived per §5.4: the in-progress todo's group, then
the plan's stated intention, then the todo content, falling back to the first
prompt for a session with no list. Empty-`Preview` versus nil distinguishes
"loaded and empty" from "not loaded", or an empty session is re-read every frame.

Also fixed: the box returned a top border with no sides and no bottom when there
were no sessions at all.

## 2026-07-31 — Compaction (plan item 4)

The feature was auto-compaction. The bug underneath it was worse: `/compact`
never persisted. `Conversation.Compact` assigned the message slice directly,
bypassing `Append` and the session sink, so the summary was never written and the
pre-compaction messages stayed in the file. Resuming a compacted session replayed
the entire uncompacted history and threw the summary away. `/rewind` had done it
correctly for months via `session.Rewind`; compaction just never got the same
treatment.

So `session.Compact` now mirrors `Rewind` — backup, temp file, atomic rename —
keeping the meta history, because losing the model and cwd would make a compacted
session unresumable rather than merely shorter. The test that matters writes three
messages, compacts, and reads them back off disk through `Resume`.

Order matters inside `Compactor.Compact`: storage first, memory second. Dropping
the in-memory history while nothing reached disk would lose the session outright,
so a failed persist leaves the conversation exactly as it was. Memory then takes
what storage returned rather than guessing, or the two drift the first time the
format changes.

`Conversation.Compact` is deleted rather than fixed. A method that silently
desynchronises the file from memory should not survive the fix that found it.

The whole engine moved into `internal/agent`, because it was a `*tui.Model`
method — a daemon session, an overnight run and every spawned worker had no way
to compact at all. It takes a `Summarizer` func rather than a router so the
package still knows nothing about config (invariant 1).

Auto-compaction fires on an 85% threshold before dispatching rather than on the
provider's overflow error, which needs per-provider string matching and only
arrives once the tokens are spent — DEVIATIONS #13. Capped at three per session:
a summary that is itself over the threshold would otherwise compact forever
without ever sending a request, which presents as a hang rather than a loop.

Also fixed while here: `/compact` blocked the render loop with a synchronous
60-second call, so the "📦 Compacting…" notice it set was never painted — the
same function overwrote it before Bubbletea got control back. And `/context` was
registered in the command table with no case in the dispatch switch, so it
printed "not implemented yet". `m.ctxUsed` is cleared on compaction now, which
otherwise left the meter showing the pre-compaction size.

## 2026-07-31 — Vision (plan item 5)

The only one of the five outside the spec entirely: plan.md never mentions
vision. §6.6 specs the attachment *UX* — explicit Ctrl+V, file drops, `[image n]`
placeholders — without ever saying the images reach a model, and the helpers for
it (`IsImagePath`, `ImageExtensions`, `QuoteIfNeeded`) had been sitting in
`input.go` as dead code with only tests referencing them.

`Message.Images` holds raw bytes because the two wire formats disagree: Ollama
wants bare base64 with no MIME type, OpenAI wants a data URI inside content
parts. Putting encoded strings on the shared type would impose one provider's
format on the other. `oaiMessage.Content` had to become `any` for that, and every
text-only request still emits a bare string — switching everything to parts would
change the shape of every call to serve the rare one, and there is a test pinning
that.

MIME is sniffed from magic bytes rather than the file extension, because a
clipboard image has no name at all.

The capability gate is a per-model `vision = true` rather than a guess from the
name. A guess that says no to a capable model is invisible; one that says yes to
a text-only model fails deep in the provider with a message explaining nothing.

Attachments are never written to the session log, and that is deliberate: one
JSONL line per message against a 16 MB scanner buffer means a couple of images
exceed a line, and `Read` then silently truncates the entire replay from that
point with no error surfaced. Images ride one turn and are dropped. DEVIATIONS
#14.

Verified against a real vision model — `gemma4:31b-cloud` through evilcode's own
Ollama provider, reading the mermaid diagram the graphics work produced earlier
and describing the build loop it depicts. Both encodings are unit-tested, but the
one that mattered was watching a model actually see the picture.

## 2026-07-31 — Widgets, corrected

Three symptoms from real use: widgets showed up far too often, they did not
scroll cleanly, and they destroyed text they were not even covering. All three
were mine, from the previous loop.

**Destroying text was the worst and had nothing to do with widgets.**
`truncateCells` measured by iterating runes and calling `lipgloss.Width` on each
one — so every byte of an ANSI escape counted as a cell. A styled row carries a
~40-byte SGR prefix, so cutting it "at column 102" actually cut around column 60
and left a severed escape sequence behind. `1› explain the config` rendered as
`1› ex`. That bug was latent across the whole UI — the help box, the picker, every
notice — and only became destructive when the widget painter started cutting rows
for real. It is `ansi.Truncate` now, which also closes any style it cuts through.

**Showing up too often was the overlay experiment, and it was a bad idea.** The
reasoning had been that prose wraps to the full measure, so requiring a clear
margin means boxes rarely appear. True, but the conclusion was wrong: there is
always somewhere to put a box if you are willing to cover text, so widgets
appeared constantly and a box over a paragraph is harder to read past than a
missing box is to live without. Reverted to margin-only. The click-to-dismiss
affordance stays, since it costs nothing and answers a widget that is in the way.

**The scrolling was the thinking traces, exactly as reported.** An anchor is an
absolute content line, so anything that removes lines *above* a widget silently
changes what its anchor names — and a trace collapsing from nine lines to one the
instant the answer starts does precisely that, every turn. The dock now notices
the transcript getting *shorter* and re-homes rather than holding a line number
that has come to mean something else. Growth, the ordinary streaming case, still
holds its anchor, and there is a test for each direction.

The lesson worth keeping: I shipped the overlay change on an argument rather than
on use. It was agreed up front, which made it feel settled, but the first real
session with it was the first honest test and it failed immediately. A design
decision that can be checked by looking should be checked by looking before it
lands, not after.

## 2026-07-31 — Scroll slack: the view only moves one way

Suggested from use: when a thinking trace collapses, don't pull the conversation
back down to close the gap — leave the empty space and let new content fill it.

That is exactly right, and it is invariant 4 ("prefer stays put") applied to the
one place the transcript was still jumping. Concretely: with a tail-anchored
window, collapsing a nine-line trace to one moves every line *above* it down
eight rows on screen, right as the answer starts arriving — a jump in the
opposite direction from the one the reader is already tracking.

`Scroll` now carries slack: a shrink while following the tail is banked rather
than applied, the window is measured against content-plus-slack so the text holds
its position, and the gap renders as blank rows below. New content spends the
gap before scrolling resumes. A reader who has scrolled up is anchored to content
rather than to the bottom, so no slack is held for them — a gap there would just
be a hole in their view.

Two bugs of my own on the way in, both caught by running it rather than by
reading it. Slack extends the *window*, not the content, so an unclamped start
ran past the last line and panicked on the slice — the TUI died on the first
keystroke. And slack accumulates across turns, so uncapped it eventually scrolled
the conversation off the top entirely; it is capped at half the viewport, because
a gap larger than that is a blank screen rather than breathing room.

Verified live: the prompt row settles at 8 and stays at 8 through a full turn
including the collapse, where it used to drop.

**Follow-up, from use again:** the gap was still sitting there after a reply
finished. The cause was that slack *accumulated* — `+=` on every shrink — and a
single turn collapses several traces when the model thinks, calls a tool, and
thinks again. The sum outgrew anything a reply could fill, so the hole stayed.

The first fix I reached for was dropping the remainder at turn end, which was
wrong and was rejected as such: the gap exists so the answer can fill it, and
discarding it just moves the jump to a different moment. It holds the largest
single collapse now rather than their sum — one trace's worth, which is exactly
what the reply is expected to fill, and does. Live against deepseek: a 40-word
answer after a long think leaves no gap at all.

## 2026-07-31 H1.1 — The store kept writing to a file that no longer had a name

Both reviewers flagged it and both were right, which is rarer than it sounds.
`Compact` and `Rewind` rewrite the log the careful way — build the new content in
a temp file, `os.Rename` it over the old path — and every caller holds a
`*session.Store` whose `O_APPEND` descriptor is still on the pre-rename inode.
That inode now has zero links. Every message appended after an auto-compact,
`/compact` or `/rewind` went into it and ceased to exist the moment the process
closed the fd.

Reproduced before touching anything: create a session, write a message, compact,
append "now the retry gate", close, resume. The message is gone —

```
zz_bug_test.go:44: message appended after compaction is gone; resumed =
  [{user [conversation compacted]\n\nwe wired auth ...}]
zz_bug_test.go:87: message appended after rewind is gone; resumed = [{user first}]
```

Two paths, one mechanism, so one fix: `Store.Reopen()` closes the orphan and
opens the path again. It flushes the old writer first — whatever was still
buffered was written *before* the rewrite and belongs to what is now the `.bak`.

The call sites are what makes this recur, so they got the structural half of the
fix: `Store.Compact` and `Store.Rewind` methods that do the free function and
then reopen. All four callers (`wiring.go`, `tuicmd.go`, `run.go`,
`tui/sessioncmd.go`) now go through those, and the shape of the API no longer
lets someone rewrite a session out from under a store they are holding.

Verified: both reproductions pass, `go test ./...` green.

## 2026-07-31 H1.2 — A cancelled tool round left the calls unanswered

`runTools` checked `ctx.Err()` at the top of the result loop and returned
`context.Canceled` on the first iteration that saw it — before appending
anything. The assistant message with its `tool_calls` was already on the
conversation and already in the JSONL; the results never joined it.

Reproduced with a provider that answers once with a two-call round and a tool
that blocks until cancellation:

```
cancel_test.go:68: tool call "call_1" (blocker) has no result message
cancel_test.go:68: tool call "call_2" (blocker) has no result message
```

The cost is delayed, which is what makes it worth fixing rather than tolerating.
Nothing breaks at interrupt time — the turn ends, the UI looks right. The
malformed pair is durable, so the 400 arrives on the next request against a
strict endpoint, and again on every resume of that session afterwards.

The round now answers every call: an outcome with real output keeps it (a tool
that finished before the cancel did real work and the model should see it), and
everything else gets `stubSkipped`, the same stub safe point C already writes.
`context.Canceled` is returned after the appends rather than instead of them, so
`Loop` still ends the turn as interrupted.

The `_ = i` left over in the safe-point-C loop went with it.

Verified: reproduction passes, `go test ./...` green.

## 2026-07-31 H1.3 — The same unanswered call, arriving by the other door

H1.2 covered calls abandoned during a round. This is the round that never
started: the stream had finished delivering a `tool_call` when the interrupt
landed, `stream` returned the accumulated message with `context.Canceled`, and
`commitPartial` checked Content and Reasoning for emptiness and then appended
the whole thing — call included. Nothing downstream will ever answer it, because
`runTools` was never reached.

```
cancel_test.go:134: tool call "call_9" (blocker) has no result message
```

Fixed by dropping `ToolCalls` on the partial. Stubbing them like safe point C
was the other option in the plan and is worse here: a stub says a call was
attempted and skipped, and this one was never attempted. The text is what the
reader wanted kept; the calls were an intention, not an event.

The assertion helper is shared with H1.2's test, and it is the invariant rather
than the symptom: every `tool_call` in the conversation has a result message
with its ID. Both interrupt paths are now checked against it.

Verified: reproduction passes, partial text still kept, `go test ./...` green.

## 2026-07-31 H1.4 — Counting in the wrong units

`ApplyEdits` sliced Go strings with the protocol's `Character` offsets. Those are
UTF-16 code units — `é` is one unit and two bytes, `🔥` is two units and four —
so every edit on a line with non-ASCII text to its left landed at the wrong byte,
and an offset in the middle of a multi-byte sequence cut it in half. This is the
one finding in H1 that damages files rather than state, and it was single-source.

Three reproductions, all of them corrupting:

```
"x := \"héllo\";renamedd := 1"   want "x := \"héllo\"; renamed := 1"
"x := \"🔥\"renamedld := 1"      want "x := \"🔥\"; renamed := 1"
"vr := 1"                        want "v := 1"
```

The last one is the ugly case: the replaced range itself started at a valid byte
and ended inside `ä`, so the surviving text is not the identifier that was there
and not the one that was asked for.

`utf16ToByte` converts and validates. Bounds checking moved into it, which is
strictly better than the old `> len(line)` test — that one compared a unit count
against a byte count and would accept an offset past the line's end whenever the
line held non-ASCII. An offset landing between the halves of a surrogate pair is
an error rather than a guess, and because rename computes every file before
writing any, an error there stops the whole rename instead of half-applying it.

The outbound direction is wrong too and is not fixed here: `docPosition` sends
the model's 1-based column as a protocol character, so a symbol with non-ASCII
earlier on its line is looked up at the wrong position. It cannot corrupt a file
— the server just answers about something else — so it is logged as **H5.21**
rather than folded into this commit.

Verified: four reproductions pass, existing ASCII edit tests unchanged,
`go test ./...` green.

## 2026-07-31 H1.1/H1.2 — Two corrections from the codex reviews

Both reviews came back with the same shape of finding: the fix was right about
the mechanism and drew its boundary one step too narrow.

**H1.1 — the window is the whole rewrite, not the reopen.** Reopening after
`Compact`/`Rewind` fixes every append that comes *later*; an append that arrives
*during* the rewrite still lands on the doomed inode, and a rewrite takes as long
as the log is large.

The reproduction needed a detector, because ordering cannot see this: the lost
appends and the legitimately-compacted ones both sit before the survivors. The
backup is the detector. Compaction copies the log to `.bak` before replacing it,
so anything it erases is still recorded — a message in *neither* the new file nor
the backup was never erased, it was dropped. With 4,000 entries of history and an
appender running flat out:

```
"racer 11874" was appended successfully but is in neither the compacted log nor
its backup: it went to the orphaned inode (12151 of 12151 racers)
```

Twelve thousand racers, and the one that fell in the hole is named exactly.

Fixed by holding `s.mu` across the entire read-write-rename-reopen sequence
(`Store.rewrite`), so a concurrent append blocks and then lands on the new file
rather than the old one. The buffer is flushed before the rewrite reads, too, so
nothing in flight is dropped from the history it is about to summarize.

**H1.2 — empty output is not evidence of a cancel.** The first version stubbed
every outcome with empty output once the round was cancelled, which mislabels a
tool that succeeded with nothing to say and buries the error of one that failed
for its own reasons. Cancellation is now read off each outcome's error
(`errors.Is(out.Err, context.Canceled)`), which is where it actually lives; the
round is still reported as interrupted if the context is done.

Also logged from the reviews, not fixed here: the daemon closing a store while a
round is still stubbing (that is **H1.9**, already in the plan) and replay not
repairing histories that were already malformed (new: **H5.22**).

## 2026-07-31 H1.5 — The file was neither version for a quarter of a second

`write` and `edit` both ended in `os.WriteFile`, which truncates and then
writes. Between those two steps the file on disk is not the old contents and not
the new ones, and a crash, a short write or a full disk in that window leaves
the truncated remains with the original nowhere.

The window is not theoretical and does not need a crash to observe. A reader
loop running alongside eight write/rewrite cycles of a 200 KB file caught it
immediately:

```
a reader saw a partially written file: the file was empty — truncated, not yet rewritten
... the file held 49152 bytes, matching neither version
```

49,152 bytes is twelve pages: the write was in progress, and anything reading
that file — another tool call, a `go build`, the editor the user has open — sees
a file that never existed.

`writeAtomic` writes a same-directory temp file, syncs it, restores the
destination's mode and renames. Same directory because rename is only atomic
within a filesystem; sync before rename because otherwise the rename can be
durable while the contents are not, which is precisely the crash that leaves an
empty file. All three write sites in `fs.go` go through it.

The permission test was written to fail in the other direction — a temp file is
created 0600 and would have *narrowed* the mode had the chmod been forgotten,
where `os.WriteFile` on an existing file left it alone. It passed before the
change and passes after, which is the point of having written it first.

Verified: the reader observes only whole versions, modes survive both tools,
`go test ./...` green.

## 2026-07-31 H1.6 — Two edits, one file, one survivor

A batch runs eight-way concurrent and `edit` is read-modify-write, so two edits
to the same file in one batch both read the same original, both compute their
replacement against it, and the second write erases the first. Nothing reports
it: both calls return success, both diffs look right, and one of the two changes
is simply not in the file.

Reproduced on the first attempt with a 4,000-line file and two edits at opposite
ends of it:

```
attempt 0: "LAST" is missing — the other edit in the batch overwrote it from a stale read
```

A per-canonical-path mutex now spans the read through the write in both `edit`
and `write` — `write` because it reads the old contents for its diff and is the
same hazard with a shorter body. The lock has to cover the read, not just the
write: two writes that are individually atomic (H1.5) still lose one of two
edits if both computed against the same `before`, which is exactly why H1.5 was
not enough on its own and why the plan pairs them.

The map of mutexes is never pruned. It holds one per file edited in a session,
bounded by how much work a session does, and it is marked `ponytail:` rather
than swept.

Verified: five attempts, both edits land every time; `go test ./... -race`
green.

## 2026-07-31 H1.7 — The sink could not say no, and did not keep its place

Two faults in one seam, both about what `Conversation.Append` does after the
message is in memory.

The sink's signature had no error return, so `store.WriteMessage`'s failure was
discarded at all three wiring points. A full disk or a closed store leaves the
durable transcript behind the live conversation with nothing said, and the
session looks perfectly healthy until someone resumes it and finds it short.

The lock was also released before the sink ran, so two appends could reach
memory in one order and disk in the other. That one reproduces immediately —
200 concurrent appends, twice in a row:

```
disk order diverges from memory order at 5: disk "m006", memory "m019"
disk order diverges from memory order at 2: disk "m055", memory "m023"
```

Ordering is fixed by claiming a second lock, `sinkMu`, *while* the conversation
lock is held and releasing it after the write. The write still happens outside
the conversation lock — holding that across a disk write would stall every
reader, which is why it was written that way in the first place — but the order
is now decided while the memory order is being decided.

That ordering forced where the error lives. `persistErr` is guarded by `sinkMu`,
not by `mu`: `mu` is held while `sinkMu` is taken, so guarding it the other way
round inverts the lock order and deadlocks. The first version of this fix did
exactly that and was rewritten before it ran.

`PersistErr` reads and clears, and `endTurn` reports it — every turn ends there,
including the interrupted and errored ones, so it is the one place that cannot
be missed. One notice per turn rather than one per message.

README gained a paragraph on session durability and one on atomic writes, since
both are now behaviour a user can rely on rather than implementation detail.

Verified: both reproductions pass, `go test ./... -race` green.

## 2026-07-31 H1.8 — The safety net was optional

Both rewrites copy the log to `.bak` before replacing it, and both wrote it like
this:

```go
if data, err := os.ReadFile(path); err == nil {
	_ = os.WriteFile(path+".bak", data, 0o644)
}
```

Two discarded errors in three lines. The backup is what makes a mistaken rewind
or a bad compaction recoverable, and the one situation where it matters — the
filesystem is out of space, or read-only, or the path is occupied — is exactly
the situation where it silently was not written and the primary was destroyed
anyway.

Reproduced by putting a directory where the backup wants to go, for both paths:

```
compact: the log was replaced without a backup being written
         the log was modified even though the backup failed
rewind:  same, both
```

`backup` now reads, writes a temp file, syncs, closes and renames, and any
failure aborts the rewrite before the primary is touched. Temp-and-rename rather
than writing `.bak` directly, because a half-written backup would have destroyed
the previous good one on its way to failing.

Verified: both subtests pass — the rewrite refuses and the log is byte-identical
afterwards — and `go test ./... -race` is green.

## 2026-07-31 H1.9 — Closing a session outran the turn it was closing

`Session.close` cancelled the turn and closed the store in the next statement,
with nothing between them. A cancelled turn still writes: the partial answer,
the stubs H1.2 now appends for the tools it abandoned, the interrupt notice. All
of it went to a closed store.

The reproduction turned out to be blunter than the report suggested. Because the
turn runs on a goroutine, an immediate close can beat it to the *first* append:

```
close_test.go:41: the prompt never reached the session file
close_test.go:44: the turn's output never reached the session file; it holds 0 messages
```

Zero messages. Not a missing tail — the whole turn, prompt included.

`Session` now keeps a `turnDone` channel closed by the turn goroutine, and
`close` cancels, waits for it, and only then closes the store. The wait is
bounded at five seconds: a turn that will not unwind must not wedge shutdown,
and losing its tail in that case is exactly the old behaviour, reached only when
something is already stuck.

`cancel` and `turnDone` are read and written under `sess.mu` here, which is part
of what **H2.1** asks for; that task still owns the rest of the paths.

The final assertion is the invariant rather than the symptom: after close, the
session file holds everything the conversation holds. The store is shut, so
anything missing at that point is missing permanently. It also refuses to pass
vacuously — a turn that appended nothing fails the test rather than proving
nothing.

Verified: fails without the fix (disk empty, conversation holding the prompt),
passes with it across five runs; `go test ./... -race` green.

## 2026-07-31 H1.10 — A namespace named the files, not the plan

Every daemon session is built with `TodoNamespace: "swarm"`, and every one of
them called `todo.NewStore` on it. A namespace names a *set of files*, and two
stores over one set of files are two divergent copies: each loads its own
snapshot at open, never reloads, and writes the whole file back at the end of a
transaction. The same shape applies to the memory bank, opened per session over
one log.

```
omen  sees [wire the auth flow], want both sessions' items — the swarm plan is not shared
frost sees [add the retry gate], want both sessions' items
```

Two sessions, one plan, and each of them can see exactly half of it.

The fix is the one the plan names: the store is owned at server scope and handed
to every session by reference, exactly as the session registry already works.
`wiring.Options` gained `Todos` and `Bank`, and the build does not close what it
did not open — a worker finishing would otherwise close the swarm's memory bank
out from under its spawner. The bank is closed last in `Server.Close`, after
every session.

The reproduction had to be corrected once, and the correction is the interesting
part. The first version asserted that two independent writes both survive; they
do not, and should not, because a todo write *replaces* the list. That is what
makes the un-shared store dangerous rather than merely wrong: session two writes
its list computed from a stale copy, and session one's items are not merged away
but deleted. The test now reads the list, appends to what it finds, and checks
both sessions see both — which is the behaviour §20 describes.

Verified: fails before (each session seeing only its own half), passes after;
`go test ./... -race` green.

## 2026-07-31 H1.6/H1.7/H1.8 — Three corrections from the codex review

**The per-path lock was keyed on a spelling, not a file.** `lockPath` used the
path as `resolve` returned it, and `resolve` only resolves symlinks for the
confinement check — it hands back the unresolved path. Two calls naming one file
two ways (through a link, and as the link's target) took two different locks and
raced exactly as before. Now keyed on `resolveExisting`, the same resolution the
confinement check trusts.

**`Loop` could return without ending its turn.** The non-cancellation branch of
the `runTools` error returned straight out, skipping `endTurn` — so no TurnEnd
event, every listener waiting forever, and with H1.7 in place the persist error
unreported too. Unreachable today, since `runTools` only ever returns nil or
`context.Canceled`, but "unreachable" is a property of the current callee and
this is a loop that outlives its assumptions. It ends the turn as errored now.

**The backup was durable, its name was not.** `backup` synced the temp file and
checked the rename, but never synced the containing directory, so a crash
between the rename and the directory's own writeback could leave the `.bak`
entry gone while the primary had already been replaced — which is the single
outcome the function exists to prevent. It syncs the directory before returning.

Also reported and not acted on: `Append` holds `sinkMu` across an arbitrary
sink, so a sink that re-entered `Conversation` would deadlock against another
append. Every sink in the tree is `store.WriteMessage`, and a re-entrant one
would be a different bug; noted rather than defended against.

## 2026-07-31 H1.11 — A transaction that was four transactions

`Apply` mutated live state throughout and then wrote four files one after
another, returning on the first failure. Anything after that point never
reached disk, everything before it did, and memory kept serving the version that
was never fully written.

Reproduced by making the todo directory read-only after one successful write —
the same shape as a full disk, and unlike deleting the files it leaves on disk
exactly what a restart would replay:

```
the store serves 2 items, a restart replays 1: the failed write is live in
memory and absent from disk
```

Two halves to the fix.

On disk, every file is staged and synced *before* any of them is renamed. The
failure that actually happens — no space, no permission, a bad path — now
happens while the previous state is still the state on disk. Four renames are
not one atomic operation and this does not pretend otherwise; what changed is
what it takes to break it. Before: a full disk. After: the filesystem failing
between two metadata updates.

Staging also fixed something nobody reported. The temp path was `path + ".tmp"`,
shared by every store writing into that directory — the same collision H1.10
names in passing. `os.CreateTemp` gives each stage its own name.

In memory, `Apply` snapshots the four fields it touches and restores them if the
save fails. A clone threaded through the transaction would be cleaner, and would
mean rewriting every gate that reads what has been applied so far; the snapshot
buys the same guarantee — what the store serves is what a restart would replay —
for a tenth of the change.

Verified: memory and disk agree after the failed write, `go test ./... -race`
green.

## 2026-07-31 H1.12/H1.13 — The overnight loop counted twice and spent nothing

Both live in the same four lines of the turn-end handler, and the fix to one
moves the other, so they are one commit rather than two.

**H1.12** was two identical calls in a row:

```go
m.stepOvernight()
m.stepOvernight()
```

Every finished turn advanced the counter twice, so a 40-turn cap stopped at 20 —
and since both calls can pass `ShouldContinue`, each can submit a continuation,
which is two `agent.Run` goroutines on one agent. H2.3's single-flight guard is
the backstop that would have contained it; there is no such guard yet.

```
one turn advanced the counter by 2, want 1
```

The reproduction needed a real todo list to be worth anything. With `m.todos`
nil the first call halts the loop ("there is no todo list to work through") and
the second returns immediately on `!Active` — so the test passed against the
broken code, for a reason that had nothing to do with the bug. A test that
passes before the fix is not a reproduction, and this one was one edit away from
being filed as evidence.

**H1.13** was the budget breaker not working at all, for two independent
reasons. The tokens were read from the status line *after* it had been reset to
`StatusState{Phase: PhaseIdle}` in the statement above — always zero — and
`ShouldContinue` then *assigned* rather than added, so `Tokens` would have meant
"the most any single turn spent" even with a real number.

```
Tokens = 1000 after three 1000-token turns, want 3000
stopped for "reached the 40-turn cap" after spending 100001 of a 400000 budget
overnight recorded 0 tokens for a 1000-token turn
```

The last line is the whole feature: a 1000-token turn recorded as zero. Invariant
6 says every auto-continuation path has a working breaker; this one had a
decorative one, and the turn cap was silently doing all the work — at half its
stated value, thanks to H1.12.

The turn's cost is now read before the reset and passed in, and accumulates.

Verified: all four reproductions fail before and pass after; `go test ./...
-race` green.

## 2026-07-31 H1.14 — Verifying phase H1

Each item in the phase's verification list, and what actually stands behind it:

- **compact → append → resume round-trips** — `TestAppendAfterCompactSurvivesResume`
  and its rewind twin, plus `TestAppendsRacingCompactAreNotLost`, which is the
  stronger one: it catches the loss through the backup rather than through
  ordering.
- **a cancelled turn's transcript replays against a strict endpoint** — added
  here. The existing tests checked the *conversation*; this one checks what was
  handed to the persistence sink, which is what a resume rebuilds from. The
  in-memory check would pass on a build that persisted nothing at all.
- **an LSP rename on a non-ASCII file is byte-exact** — `TestApplyEditsUsesUTF16Offsets`
  covers the edit application, which is where the corruption was. The `Rename`
  round trip through a live server is not tested here: it needs gopls, and the
  server's answer is the input to the part that was broken, not the broken part.
- **concurrent edits to one file in one batch both land** —
  `TestConcurrentEditsToOneFileBothLand`, five attempts, and it failed on the
  first attempt before the fix.
- **overnight stops at its budget** — `TestOvernightBudgetBreakerActuallyFires`,
  which spends a quarter of the budget per turn and asserts on *which* breaker
  stopped it. Asserting only that it stopped would have passed against the
  broken code, where the turn cap did all the work.

One more finding folded in from the codex review of H1.9: a turn starting while
`close` was waiting replaced the channel `close` was waiting on, so the store
could shut under a live turn. Sessions now refuse new turns once closing has
begun.

`go build ./... && go vet ./... && go test ./... -race` green. Tagged `harden-1`.

Four of the fourteen tasks came back from review needing a second pass, all of
them the same shape: the fix was right and its boundary was one step too narrow.
Reopening after the rewrite but not during it. Reading cancellation off the
round instead of off each call. Locking a path spelling instead of a file.
Waiting for a turn without preventing the next one. Worth carrying into H2,
where the boundaries are all concurrency and there is no reason to think that
gets easier.

# Phase H2 — Concurrency

## 2026-07-31 H2.1 — One field, four writers, no lock

`sess.cancel` was assigned in `Input` and in the worker spawn, read in
`Interrupt` and in `close`, with no lock holding any of it together. Two
attached clients could each overwrite the other's, so an interrupt cancelled a
turn that had already ended while the live one carried on unstoppable.

The reproduction is the race detector. Eight concurrent `Input`s, eight
concurrent `Interrupt`s and four spawns against one session:

```
WARNING: DATA RACE
Read at 0x00c000188888 by goroutine 34:
Previous write at 0x00c000188888 by goroutine 31:
```

Every path now goes through `beginTurn` or `cancelTurn`, both under `sess.mu`,
and `beginTurn` cancels whatever it replaces rather than dropping it on the
floor. The worker spawn used the field directly and now shares the same door,
which also gives workers the `closing` refusal and the `turnDone` tracking that
`close` waits on — neither of which it had.

The test is not in this commit. What it exercises after this fix are races
*inside* the agent — `a.seq++` in `newEvent`, the `pendingImages` swap in `Run`
— because nothing yet stops two `Run`s on one agent. Those are H2.3, H2.8 and
H2.9, and the test lands with the one that finally makes it pass. The evidence
for this task is that no daemon field appears in the race report any more; what
is left is a different bug that the same test happens to find.

## 2026-07-31 H2.2 — Checking idle and becoming busy were two moments

`Input` asked `a.Running()` and then started a turn. `Running` is set by the
turn's own goroutine, so between the check and the goroutine actually reaching
that line the session still reports idle — long enough for a second client to
check, see idle, and launch a second `Run` against one conversation.

Reproduced deterministically by racing the reservation itself rather than the
agent: sixteen goroutines released at once, all sixteen claiming they had
started a turn.

```
16 of 16 racers each started a turn on one session, want 1
```

Sixteen of sixteen, not a rare interleaving.

`beginTurn` now takes the reservation under the same lock that holds `cancel`,
before any goroutine exists, and `endTurn` releases it when the run returns.
Input's loser no longer drops the text: it becomes an interjection into the turn
that is running, which is what the code always intended and what the check-then-
launch shape only approximated.

`Running()` stays what it is — a fact about the agent, useful for the UI. The
reservation is a fact about the session, and the difference between those two is
the bug.

Verified: sixteen racers, one winner, across three runs with -race; the session
accepts a turn again after `endTurn`.

## 2026-07-31 H2.3/H2.9 — The backstop that would have caught H1.12

Nothing at the agent rejected a second turn. Two loops share one conversation,
one tool set and one event sequence, and what comes out is a single transcript
interleaved from two turns. H1.12 was producing exactly that in the overnight
loop, and it was fixed at the call site because there was nothing underneath it.

`Loop` now takes the running flag atomically and returns `ErrBusy` if it is
already set. Callers are still expected to check first — the daemon reserves
under its own lock (H2.2), the TUI checks `m.processing` — and this is what
catches the ones that check and then race.

H2.9 rides along because it is the same lock. `Run` swapped `pendingImages`
without holding `a.mu` while `Attach` writes it under the lock. Safe only
because the TUI happened to attach before starting the run goroutine, which is
a fact about today's caller rather than about the code.

The evidence is the race report from H2.1's test, which was held back for
exactly this. Sixteen concurrent inputs against one session, before:

```
Read at 0x00c000188888 by goroutine 36:
  evilcode/internal/agent.(*Agent).Run() agent.go:285      ← pendingImages
  evilcode/internal/agent.(*Agent).newEvent() events.go:120 ← a.seq++
```

After: zero races across three runs, and the test lands here rather than with
the commit whose name it carries.

`a.seq++` (H2.8) stopped appearing because there is only one turn now, but that
is not the same as being fixed: the daemon's `deliverConflicts` calls `Notice`
from the pump goroutine while the turn emits from its own. That path is still
open and stays its own task.

Verified: a second `Run` against a held-open turn returns `ErrBusy`; the daemon
race test is clean; `go test ./... -race` green.

## 2026-07-31 H2.8 — Two events, one sequence number

`a.seq++` on a plain int. The turn emits from its goroutine and the daemon's
conflict delivery calls `Notice` from the pump, so the increment is a read and a
write from two goroutines with nothing between them. Two events taking the same
sequence is a reattaching client silently missing one, since the sequence is
precisely how it works out what it missed.

Eight goroutines, fifty events each:

```
seq_test.go:43: sequence 6 was handed out twice
race detected during execution of test
```

Now an `atomic.Int64`. A mutex would have done as well and this is one counter
with no invariant to hold alongside it.

**A regression from H2.3, caught by the codex review of the phase before it.**
The review pointed out that `flushPending` can start a queued turn immediately
before `stepOvernight` starts a continuation — two turns from the TUI in one
event. Before H2.3 both ran, badly. After H2.3 the second gets `ErrBusy`, and
both TUI submit paths discarded the error from `Run`, so the user's queued text
would have been silently dropped instead.

`submit` now interjects on `ErrBusy`, which is what the daemon's `Input` does
with its loser. The hidden path still drops it: a rejected overnight
continuation is not lost work, because the running turn's end steps the loop
again.

Worth writing down as a pattern rather than an incident. Adding a guard turns a
silent wrong behaviour into a silent refusal, and a caller that ignored the
error was fine while the error could not happen. Every guard added in this phase
needs its callers re-read for exactly this.

Verified: distinct sequence numbers across three -race runs; `go test ./...
-race` green.

## 2026-07-31 H2.4 — Both breakers were advisory

`SpawnFor` read the live worker count, read the per-session spawn count, and
then spawned. Three operations, nothing reserved between them: concurrent spawns
all read the same numbers, all passed, and all started.

```
16 workers are live, past the 4 limit
the session spawned 36 workers, past the 12 limit
```

Four times the global cap and three times the per-session one — not a rare
interleaving but the ordinary outcome, because every racer sees the state as it
was before any of them acted.

`swarmState.reserve` now takes both decisions and both increments under one
lock, before anything is built, and `release` rolls back a spawn that failed to
start. The live count moved from a scan of the session map to a counter, because
a scan cannot see the workers other goroutines are in the middle of starting —
which is the entire window being exploited. `markFinished` returns the
reservation exactly once, outside the session lock.

`Spawn`, the direct path, reserves too, with no spawner to charge the
per-session half to. A worker started that way is as live as any other, and a
counter blind to it admits one worker too many.

`liveWorkers()` stays a scan: it answers "what is actually running" for the UI
and the existing tests, which is a different question from "may another start".

The holder in the test is doing real work — the mock finishes in microseconds,
so without keeping workers unfinished the live cap is never under pressure and
the test passes against the broken code.

Verified: both caps hold under 16 and 36 concurrent spawns; `go test ./...
-race` green.

## 2026-07-31 H2.12 — Picking a free name is not claiming it

`Create` listed the existing sessions, picked a name none of them held, and then
opened it — without `O_EXCL`. Two creators list at the same time, both see the
same name free, and both append to one log. Two conversations interleaved in one
file, each store believing it owns it. The daemon spawning workers makes that
ordinary rather than exotic.

Sixteen concurrent creators, twice in a row:

```
two sessions were created as "wisp" — they share one log
two sessions were created as "owl" — they share one log
```

`CreateNamed` claims one name with `O_CREATE|O_EXCL`, and `Create` retries on
collision with the loser marking the name taken. Bounded at 64 attempts,
because a run of collisions means the creature table is full rather than that
another draw would help.

Deterministic mode keeps the old behaviour deliberately: goldens depend on every
run producing `dracula`, so an existing file is reopened rather than refused.
That is the one place two stores over one log is the intended outcome.

Done ahead of H2.5, which needs it: a worker's name cannot be allocated before
its resources if allocating a name is not an atomic act in the first place.

Verified: sixteen concurrent creators, sixteen distinct sessions, five runs;
`go test ./... -race` green.

## 2026-07-31 H2.5 — The worker was renamed but not moved

Name collision was resolved *after* the store and the agent were built, and only
in the daemon's map. The renamed worker kept the session log it had been built
with, and its swarm tools kept the identity they had been bound with — so it
wrote into another session's file and sent messages as another session.

The reproduction needed `EVILCODE_DETERMINISTIC=1`, where every session is
created under the same name and the collision is the normal case rather than a
rarity:

```
the worker is called "dracula-2" but writes to the session log of "dracula"
the worker's log is .../sessions/dracula.jsonl, which is not its own
the worker and the session it collided with share one log
```

`claimName` now settles both halves before anything is built: the daemon map
decides what the name means to clients, `session.CreateNamed` decides what it
means on disk, and a `reserved` set covers the gap between claiming a name and
inserting the finished session, so a concurrent spawn cannot settle on the same
one. `wiring.Build` takes the store rather than creating one, and the swarm
tools are bound after the name is final.

This is why H2.12 came first. Settling a name before building is only worth
anything if claiming a name is atomic; otherwise the daemon's map agrees while
the filesystem does not.

Verified: a suffixed worker owns its own log and identity, and the session it
collided with keeps its own; `go test ./... -race` green.
