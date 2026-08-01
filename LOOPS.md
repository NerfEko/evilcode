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

## 2026-07-31 H2.3 — Refusing after committing is not refusing

The codex review of H2.3 found the guard in the wrong place. `Loop` held it, but
`Run` appends the user message, runs recall and consumes the attached images
*before* calling `Loop` — so a caller told "busy" had already had its prompt
committed to the conversation and the session file. The TUI's new `ErrBusy`
handler then interjected the same text, duplicating it.

The reservation moved to the top of `Run`, before any mutation; `Loop` keeps its
own for the callers that use it directly, and both share `beginRun`/`endRun`.

The same review found the worker schema retry vanishing: it calls `Loop` the
moment it sees `EventTurnEnd`, and `running` was cleared by a deferred call that
had not run yet, so the retry was refused and silently dropped. `endTurn` now
releases the reservation *before* emitting the event, so a listener acting on
TurnEnd finds an agent that is ready.

That is the second time in this phase the same pattern has cost something: a new
guard converts a silent wrong behaviour into a silent refusal, and every caller
that ignored the error was correct only while the error was impossible.

Verified: a refused turn appends nothing and its prompt is nowhere in the
conversation; `go test ./... -race` green.

## 2026-07-31 H2.6 — Finished, except for the part still running

The spawn goroutine called `markFinished()` as soon as `Run` returned. But
`reportWorkerResult` returns `false` to mean exactly *not finished*, and then
drives a second `Loop` for the schema retry. So a worker under retry was counted
finished: its slot under `MaxLiveWorkers` was handed to the next spawn while it
was still spending tokens, and the retry could overlap the tail of the original
run.

Finishing now belongs to the turn end, which `observe` already owns. The spawn
goroutine marks only a `Run` that *failed* — no turn end was emitted, so nothing
else will ever finish it, which is the case the original comment was worried
about and the only one it was right about.

**On the reproduction, honestly.** I could not make this fail deterministically,
and two attempts are worth recording because both were wrong in instructive
ways.

The first waited for `retried` to be set and then checked `finished()`. It
passed against the broken code: by the time the test observed the flag, the
whole retry had already completed. A window microseconds wide is not observable
by polling for it.

The second subscribed to the worker's event stream and checked at the retry's
own `TurnStart`. It failed against the *fixed* code — which looked like a
finding until I read it properly. The subscription channel is buffered 256 deep,
so receiving a `TurnStart` says the event happened at some point, not that it is
happening now. Against an instant mock the retry had finished before the test
read its start. A test that reports the past as the present is not a slow test,
it is a wrong one.

What is tested is the rule the fix rests on: arming a retry reports `false` and
leaves the worker unfinished, and the worker finishes when the retry's turn
ends. The window itself is closed by construction — the spawn goroutine no
longer has an opinion about finishing a worker whose turn ended — rather than by
timing, which is why I am content to leave it unreproduced and say so here
rather than quietly.

Verified: `go test ./... -race` green.

## 2026-07-31 H2.7 — The loser closed the winner's session

`Open` built the session outside the map lock and only then checked whether
another goroutine had won. That check was there and it worked — the duplicate
object was discarded. What nobody noticed is what discarding it *does*: closing
a session closes its store, and closing a store appends a clean-exit marker. To
the same file the winner is still writing to.

The first version of this test asserted on object identity and passed, which is
the finding stated too literally. Asserting on the log instead:

```
the live session's log holds 7 clean-exit marker(s) written by discarded
duplicates; crash detection now reads it as cleanly closed
```

Seven of eight racers each stamped "this session exited cleanly" into a
conversation that had not started yet. That is H5.10's crash detection reading a
crash as a clean exit, arriving from a completely different direction.

`Open` now holds a per-name channel while it builds. Concurrent openers wait on
it and then loop round to find either the published session or the failure,
rather than building their own copy to throw away. A fresh session — empty name
— skips it, having no name to collide on.

Verified: eight concurrent opens produce one session and a log with no
lifecycle markers in it, five runs with -race.

## 2026-07-31 H2.3/H2.12 — Two corrections from the codex review

**A finished turn could free the next turn's reservation.** This one I would not
have found by reading, and it undoes the guard entirely.

A turn is released twice. `endTurn` releases it before emitting the event, so a
listener acting on TurnEnd finds a ready agent — that was the previous
correction. The deferred release in `Run` then fires when `loop` returns,
covering the paths `endTurn` does not reach. Between those two moments the next
turn can start, and the deferred release does not know that: turn A's `defer`
clears turn B's reservation, and turn C walks in beside B.

So the guard held right up until a turn ended, which is the only moment anything
tries to start another one.

Fixed with a generation. `beginRun` stamps the reservation, `endRun` releases
only if the stamp still matches, and `releaseRun` — what `endTurn` calls — bumps
it so the run's own deferred release becomes a no-op.

**`CreateNamed` returned a store alongside an error.** If the start-marker write
failed, the caller took the error path and the descriptor and the claimed name
stayed held for the life of the process. It closes the file and returns nil now.

Also reported, not acted on:

- `Server.Close` does not `markFinished` its workers, so the live counter is
  left non-zero. The server is being torn down and the counter dies with it.
- Deterministic mode drops `O_EXCL` by design, so two processes both running
  with `EVILCODE_DETERMINISTIC=1` can append to one log. That is what the flag
  is for — goldens need the same session name every run — and it is documented
  as such rather than treated as a bug here.

## 2026-07-31 H2.10 — One repository's settings became everyone's

`wiring.Build` applied repo overrides by calling `LoadRepoOverrides` on the
config it was handed. In the daemon that config is one object shared by every
session, so a session built in a repo that pins a model pinned it for every
other session too — including ones in unrelated directories — and two builds
running together wrote to it at once.

```
the shared config's default model became "pinned-by-the-repo" after building a
session in a repo that pins "pinned-by-the-repo"; every other session now uses it too
```

`Config.Clone` copies what overrides write: the slices, because `Models` is
appended to, and the maps. The role pointers are shared deliberately —
`LoadRepoOverrides` replaces them rather than writing through them, so copying
them would be copying something nothing mutates.

Two tests, because half a fix here is invisible: the shared config must be
unchanged, *and* the built session must actually get the repo's pin. A clone
that is never applied passes the first test perfectly.

Verified: fails before (the shared config takes the repo's model), passes after,
`go test ./... -race` green.

## 2026-07-31 H2.11 — Twelve gopls for one Go file

`Manager.For` dropped the lock across `Start` and never rechecked. Concurrent
callers each launched a server; the last one into the map won, and the rest kept
running — a process, a pipe pair and two goroutines apiece — until the program
exited.

The first version of the test passed against the broken code, and the reason is
worth keeping. Its fake launcher returned instantly, so the unguarded window was
nanoseconds wide and the scheduler happened to serialize twelve goroutines
through it. A real `gopls` takes seconds to start and index; the window is that
whole time. Twenty milliseconds of sleep in the fake is not making the test
easier to pass, it is making the fake resemble the thing it stands in for:

```
12 servers were started for one language; 11 of them leak
callers were handed different clients for one language
```

A stub that is instant when the real thing is slow does not test the code, it
tests the scheduler.

`For` now holds a per-language channel across the launch. Late callers wait on
it and then loop round to find the client or the failure the launcher recorded,
which also means a failed start is shared rather than re-attempted twelve times.

Verified: one server for twelve concurrent callers, all handed the same client,
three runs with -race.

## 2026-07-31 H2.13 — The cap bounded the work, not the goroutines

`RunBatch` started a goroutine for every call in the list and *then* let them
queue on the semaphore. The cap did what it was written to do — eight tools run
at once — but the list comes from the model, and its length is not a fact about
the machine.

```
a 5000-call batch put 5001 goroutines in flight; the cap is 8
```

Getting that number took two attempts. The first test sampled goroutine counts
while a fast tool ran and caught 33, which is a failure that reads like noise.
Holding every tool open until the count is taken is what turns it into the
actual figure, and 5001 is a different kind of claim than 33.

Now a fixed pool of `min(MaxConcurrent, len(calls))` workers pulling from a
channel, so the goroutine count is the cap rather than the request. The batch
itself is capped at 64, with everything past it answered by an error explaining
the limit rather than dropped — an unanswered `tool_use` is the transcript H1.2
exists to prevent, and it would be careless to reintroduce it here while
defending against a different problem.

Cancellation answers every call too, for the same reason: the dispatch loop
fills in the tail it never sent rather than leaving blanks.

Verified: goroutines in flight stay at the cap for a 5,000-call batch, calls
past 64 come back refused, a cancelled batch answers all 32; `go test ./...
-race` green.

## 2026-07-31 H2.15 — The second question ate the first

`PendingAsk` was a single slot. A second question overwrote the first, and the
first tool call was left blocked on a `Reply` channel nobody held any more —
until the user noticed the turn had stopped and interrupted it.

The comment above the type explained why a slot was enough: "the tool batch
already bounds how many can be in flight". A batch runs its calls
*concurrently*, so two asks in one round is the ordinary case rather than the
exotic one. The reasoning was not just wrong, it named the exact mechanism that
made it wrong.

```
the second question displaced the first on screen
```

Now one on screen and the rest queued — answering shows the next. `Remove`
resolves one specific question wherever it sits, which is what a cancelled tool
call needs: its own call may be the one waiting behind another, and answering
"whatever is on screen" with nil would strand the wrong one. `Cancel` releases
everything, for the end of a turn.

Verified: two questions in one round are both answered in order, removing a
queued one leaves the visible one alone, cancelling releases all three; `go test
./... -race` green.

## 2026-07-31 H2.14 — Three shells, one working directory

`bash` carries its working directory between calls, which is the point: a `cd`
should still hold on the next command, the way it does for a person. It does
that by reading the directory at the start of a call and writing it back at the
end. Run in parallel, three calls all start where the *previous round* left off,
and the last to finish decides where the next round begins.

```
call 1 failed: exit status 1
bash: line 1: cd: two: No such file or directory
```

Three calls that each step down one directory, and the second one cannot find a
directory that is plainly there — because it started somewhere it did not
expect. When the paths happen to resolve, there is no error at all: the command
runs in the wrong directory and reports success.

Foreground execution is serialized on its own lock, taken *before* the timeout
starts so a queued command does not spend its budget waiting for the shell.
Background commands are untouched — they carry no directory state and detaching
is what they are for.

One lock for the whole shell rather than anything cleverer. A tool that carries
a working directory is a single conversation with a single shell, and pretending
it is several is the bug.

Verified: three parallel `cd` calls each land where they meant to, three runs;
`go test ./... -race` green.

## 2026-07-31 H2.16/H2.17 — Three commands that did not wait

All three rewrite or replace the conversation while a turn may still be writing
to it, and all three are in the TUI, so they go together.

**`/compact` and `/rewind`** did not look at `m.processing` at all. A turn in
flight keeps appending across `Conv.Reset`, and its messages land *after* the
rewrite — attached to a conversation that has been truncated or summarized out
from under them. Both now refuse first, before any of their other checks, which
is also what makes the refusal testable: the reproduction hit "compaction is not
configured" and "no session to rewind" before ever reaching the question.

**`/plan`** cancelled the running turn and submitted its own in the next
statement, without waiting for the cancelled one to end. That was a race before
this phase. It is worse *because of* this phase: H2.3 refuses the second turn,
and `submitHidden` discards the error, so `/plan` during a turn would have
silently done nothing at all. The prompt is queued now and starts on the
matching `TurnEnd`.

That is the third time in H2 that adding a guard turned a race into a silent
no-op somewhere else. The pattern is worth stating plainly: a guard does not
fix a caller, it reclassifies the caller's bug. Anything that ignored an error
because the error was impossible needs re-reading the moment it becomes
possible.

Verified: both commands refuse mid-turn and change nothing; a `/plan` issued
during a turn is queued and starts when that turn ends; `go test ./... -race`
green.

## 2026-07-31 H2.18 — A lost render is permanent

Concurrent mermaid renders shared one `atomic.Pointer`. A second result
overwrote a first that had not been drained yet, and the lost one is not merely
late: `renderDiagrams` marks a source with `""` before starting it, so that
sentinel — "already started, do not start again" — stays in the map forever. The
diagram never appears and can never be retried for the life of the session.

With the queue shortened to one slot, which is what the pointer effectively was:

```
the render of "graph B" was lost; its source is still marked started, so it can
never be retried
the render of "graph C" was lost; ...
```

A buffered channel now, drained one per frame by the render loop. If it ever
does fill, the overflow path *unmarks* the source instead of dropping it
silently, so the next repaint starts it again — the sentinel that made this
permanent is the thing that has to be cleaned up on any path that loses a
result.

The map and the queue are both touched by render goroutines and by the render
loop, so both moved under a mutex.

Verified: three concurrent renders all reach the transcript; `go test ./...
-race` green.

## 2026-07-31 H2.19 — Verifying phase H2

The phase's three named checks:

- **Two attached clients interrupting each other cancel their own turns.**
  Reserved on two sessions, interrupted one, asserted the other's context is
  untouched. The interesting half is the negative: before H2.1 the field was
  written from four paths unsynchronized, so "whose turn did that cancel" had no
  reliable answer at all.
- **A swarm at the worker cap stays at the cap under concurrent `/summon`.**
  Twenty concurrent spawns against a cap of four, checked against both the
  admission counter and a scan of live sessions — the two disagreeing would be
  its own bug.
- **`-race` is green across `./...`, daemon tests included.** It is.

Nineteen tasks, and the phase divides cleanly into two kinds. Most were
check-then-act: reading a count, a name, a flag, and acting on the answer after
it could have changed. `beginTurn`, `reserve`, `claimName`, `opening`,
`starting` are all the same edit — decide and commit under one lock.

The other kind is the one I did not expect to be a theme: **a fix that
reclassifies someone else's bug.** Adding `ErrBusy` turned a silent double-run
into a silent refusal in three separate callers, and each needed its own repair
— `submit` interjecting, `/plan` queueing, the worker retry no longer racing its
own release. The guard was right every time; what was wrong was every caller
that ignored an error because the error had been impossible.

Two tasks left the phase without a reproduction I would defend: H2.6's ordering
window, which an instant mock cannot expose, and the deterministic-mode session
name, which is intended behaviour. Both are written up where they happened
rather than smoothed over.

Tagged `harden-2`.

# Phase H3 — Resource exhaustion and leaks

## 2026-07-31 H3.1 — The producer kept talking to nobody

Both stream producers emitted a parse-error chunk and then `continue`d reading.
The consumer returns on the first chunk carrying an error, so the *next* send
has no receiver: the goroutine blocks there for the rest of the turn, holding
the response body and the connection open, while the retry it triggered opens
another.

The `resp.Error` branch two lines below already returns. The parse branch was
the one that did not, in both providers, identically.

```
openai: the producer kept sending after the terminal error
ollama: the producer kept sending after the terminal error
```

The test consumes the way the agent does — stop at the first error — and then
checks the channel is closed. If the producer is still trying to send, the
channel stays open and the read blocks, which is the leak made observable rather
than inferred.

Both branches now send and return.

Verified: both providers close their stream after a malformed line, three runs;
`go test ./... -race` green.

## 2026-07-31 H3.2 — A limit that was only ever documentation

`FS.MaxReadBytes` is declared, documented as capping a single read, and
initialized in `NewFS`. Nothing reads it. `read` did an unbounded `os.ReadFile`
and then split the whole thing into lines, so the peak cost of reading a
multi-gigabyte file was the file itself, twice, and truncation applied only
after all of it was resident.

```
an 80000-byte file was read whole against a 4096-byte cap
```

A file past the cap with no `offset`/`limit` is refused, with an error that says
how big it is and what to do instead. A file past the cap *with* a window
streams just that window through a scanner — refusing outright would make `read`
useless on exactly the files where paging matters, which is not a limit, it is a
missing feature.

Both reviewers flagged this pattern — a declared limit that is never enforced —
as worth grepping for as a class. `MaxReadBytes` was one; `MaxResultBytes` is
enforced; command output is H3.3, next.

**A flake of my own, found by the full run rather than by the targeted one.**
H2.13's dispatch loop broke out on cancellation without filling the calls it had
not sent, so a cancelled batch could return zero-valued outcomes in the middle
of the slice — the unanswered `tool_use` of H1.2, arriving through the fix for a
different problem. The sweep now runs after `wg.Wait()` over everything, rather
than inside the loop that was racing.

Verified: an oversized read is refused with guidance, a paged read of the same
file works, twenty runs of the batch tests are clean; `go test ./... -race`
green.

## 2026-07-31 H3.3/H3.4 — What a command may hold, and what it may leave behind

Same file, same two lines of setup, so they went together.

**Unbounded output.** Both paths accumulated stdout and stderr in a
`bytes.Buffer` and truncated at the end. The truncation was real — the model
never saw more than `MaxResultBytes` — so the bug is invisible in the result and
lives entirely in what it cost to get there.

Allocation is how that shows from outside:

```
a 64 MB command allocated 189.1 MB; the output buffer is unbounded
a 64 MB background command allocated 189.1 MB; its output is unbounded
```

189 MB to deliver 50 KB, and a background command holds it for up to thirty
minutes. A ring writer keeps the last megabyte and says so when it dropped
anything. The tail rather than the head, because that is where a failure
explains itself.

**Orphaned grandchildren.** Cancelling kills the process that was started, not
its children, and a shell command is almost always a parent. A grandchild
sleeping past the timeout wrote into the workspace after the tool call had
already returned an error:

```
a grandchild survived the timeout and wrote to the workspace after the tool call
had returned
```

Commands run in their own process group now, and cancellation signals the group
— the negative pid — rather than the leader. The existing `WaitDelay` comment
had noticed the symptom ("killing bash does not close the output pipes if a
grandchild still holds them open") and worked around the pipe half without
following it to the process half.

Verified: both allocation checks under 32 MB, the grandchild's marker file never
appears; `go test ./... -race` green.

## 2026-07-31 H3.5 — The attachment took the session with it

Raw image bytes went into the transcript record. Four attachments at the 4 MiB
limit — which the TUI allows — exceed the reader's 16 MiB record cap, and the
failure is not what it looks like:

```
bufio.Scanner: token too long
```

Not "the images were dropped". The *read* fails at that line, so every message
after it is unreachable and the session cannot be resumed at all. The
attachments are the smallest part of what is lost.

Attachments now live beside the log in `<session>.blobs/`, content-addressed by
SHA-256, and the record keeps references. Same image twice costs one file.
Written through a temp file and synced, because a truncated image handed to a
vision model on resume is a worse outcome than a missing one.

A missing blob is skipped rather than fatal on read. Refusing to resume because
an attachment was cleaned up would trade a small loss for a total one, which is
the same mistake in the other direction.

**Not done, and worth saying:** nothing removes blobs when a session is
compacted, rewound or deleted, so they accumulate. That is disk in a directory
the user owns rather than a session that will not open, and it is a cleanup task
rather than a correctness one.

Verified: four 4 MiB attachments round-trip through resume, the message after
them survives, the bytes are on disk beside the log; `go test ./... -race`
green.

## 2026-07-31 H3.6/H3.7 — Two ways to leave a session open

**Switching sessions recursed.** `Run` called `Run` from inside itself, so the
outer frame's defers — the session store, the MCP client, the LSP manager, the
agent, the memory bank — did not run until the final unwind. Twenty switches
meant twenty live sets of every MCP server process and every language server,
all idle and none reachable.

The `/reload` path next to it already does this correctly: it re-execs, which
cannot accumulate frames. The picker path was written to look similar and is
not.

Now a loop. `runOnce` returns the session to switch to and `runSessions` calls
it again — after the previous frame has unwound. The test asserts exactly that
ordering, since the leak *is* the ordering:

```
enter 0 -m some-model / leave 0 / enter 1 -resume wisp -m some-model / leave 1 / ...
```

Testing it needed the loop extracted from the TTY work, which is also the only
reason this is testable at all: the thing that leaked is not observable from
outside a terminal, but "did frame N finish before frame N+1 started" is.

**Resuming twice.** Both entry points called `session.Resume` a second time
purely to get the messages, throwing away the `*Store` it returns — a leaked
descriptor and a redundant full-file parse per resume, on the path where the
file is at its largest. The messages from the first call were in scope the whole
time.

Verified: the switch loop tears down in order and an error stops it; one
`session.Resume` per path now; `go test ./... -race` green.

## 2026-07-31 H3.8/H3.9 — One connection, forty relays

**Every attach started a relay and stopped none.** The goroutine is blocked
reading a subscription channel the connection has already unsubscribed from, so
it cannot even receive — it just sits there until the whole connection closes.

```
40 re-attaches on one connection left 40 extra goroutines; each attach starts a
relay and none of them stop
```

Forty attaches, forty goroutines. Not approximately: exactly one per attach,
which is what makes it a leak rather than a load pattern.

The cancel is per *subscription* rather than per connection, which is the whole
point — the connection outlives the attachment, so anything keyed to the
connection cannot end an attachment.

**A writer failure left everything else running.** An encode error returned from
the writer goroutine and nothing else noticed: the reader kept accepting frames,
the relay kept forwarding events, and every producer kept queueing into a
connection nobody was draining, until the queues wedged. The writer now drops
the connection, which fails the reader's next `Scan` and unwinds the rest
normally. Guarded by a `sync.Once`, since the deferred teardown closes the same
connection.

Verified: forty re-attaches leave no goroutines behind, ten opened-and-closed
connections return to baseline; `go test ./... -race` green.

## 2026-07-31 H3.10–H3.13 — Four ways to hand control to something else

**The LSP reader trusted the header.** `Content-Length` is a claim, and the
allocation happened before a byte of the body arrived:

```
error = "EOF", want it to name the size limit
refusing the frame still allocated 68719479304 bytes
```

68 GB from a header with nothing behind it. Capped at 64 MiB, with negative and
absent lengths refused separately — the old code folded all three into `length
<= 0`, so "no Content-Length" and "Content-Length: -1" produced the same message
for very different problems.

**mmdc ran with no context and no timeout.** It starts a headless browser; one
that hangs held a subprocess and its waiting goroutine for the life of the
session. Bounded at sixty seconds, in its own process group, so the browser dies
with the request rather than outliving it — the same shape as H3.4, in a
different file.

**The clipboard read shelled out from inside Update.** Every frame waits on the
update loop, so a clipboard tool waiting on a selection owner that never answers
freezes the interface with no way to type past it. Now a `tea.Cmd` with a
five-second bound. The dropped-image path had the reverse of a bound: it read
the whole file and *then* checked the 4 MiB limit, so refusing a two-gigabyte
file cost two gigabytes. Stat first, then read at most the limit plus one — the
stat can be stale, so the read needs its own ceiling regardless.

**Two network calls in the update loop.** Opening the model picker fetched the
list with a five-second timeout; the session picker embedded the query on every
keystroke that found nothing — which is precisely the keystroke someone is still
typing through. Both are commands now. The search carries its query back and is
discarded if the filter has moved on, because a result for a question the user
has already stopped asking is worse than no result.

Verified: the picker opens without blocking and fills in after, pasting returns
immediately, a stale search result is ignored, an oversized frame is refused
without allocating; `go test ./... -race` green.

## 2026-07-31 H3.14 — Verifying phase H3

The phase's four named checks:

- **A malformed SSE frame mid-stream leaves no goroutine behind.** Twenty-five
  streams, each abandoned at its first error. Against the old code:

  ```
  25 malformed streams left 28 goroutines behind, each holding a connection
  ```

  Against the fix, none. Counting rather than `goleak`, because the number is
  the finding — 28 for 25 streams says one per stream plus noise, which is a
  leak; a leak detector would have said "yes" without saying that.

- **`read` on a 2 GB file refuses instead of dying.** A sparse file, and the
  refusal allocates under a megabyte — the size check happens before the bytes
  are touched, which is what makes it a refusal rather than a slower death.

- **Twenty session switches leave one MCP server set.** Covered by H3.6's
  ordering test rather than by counting processes: the leak was frames not
  unwinding, and "did frame N finish before N+1 began" is the same claim without
  needing a terminal and twenty real MCP servers.

- **A `bash` timeout leaves no orphan grandchildren.** H3.4's test watches for a
  marker file a grandchild writes two seconds after its parent was killed.

Phase H3's findings were nearly all one shape: **a limit that existed only as
prose.** `MaxReadBytes` declared and never referenced. Output truncated at the
end, so the cap was real for the model and fictional for memory. `Content-Length`
believed. A timeout on the tool call but not on the browser it starts. Both
reviewers flagged this as a class worth grepping for rather than fixing three
times, and they were right — it was six.

The other recurring shape was **work happening on the wrong side of a
boundary**: two network calls and a subprocess inside the update loop, a
recursion where a loop belonged. Those do not fail, they hang, which is why the
suite was green through all of them.

Tagged `harden-3`.

# Phase H4 — Boundaries

## 2026-07-31 H4.1 — The terminal is not a display, it is an interpreter

Repository content and provider output both reach the terminal, and neither was
stripped of the sequences that make a terminal do things rather than show them.
A file in a cloned repo, rendered — not executed, not opened, just *displayed* —
could carry OSC 52 and write the user's clipboard. A model can be talked into
emitting the same.

The fixture is what a hostile file looks like: a clipboard write, a screen
clear, a title change, wrapped in plausible Go. Rendered through the transcript,
the frame came out carrying all of it.

`core.SanitizeTerminal` drops C0, DEL and C1, keeps newline and tab, and
consumes an escape sequence to its terminator rather than dropping the
introducer and leaving the payload as visible text. Applied at two choke points:
`renderTokens`, where highlighted repository content is styled, and the
transcript renderer, where every block's text, tool name, target, intent and
diff pass through. Before styling, deliberately — sanitizing the finished frame
would strip the escapes evilcode itself puts there.

Headless output is sanitized only when stdout is a terminal. Piped output stays
byte-exact: the consumer there is a program that asked for the model's text, not
a terminal to hijack, and mangling it would break `evilcode run | jq`.

## 2026-07-31 — Corrections from the codex review of phase H3

This review earned its keep. Twelve findings, most of them mine, and one is a
regression I introduced that would have quietly damaged existing sessions.

**Legacy sessions with attachments lost whole turns.** H3.5 moved images out of
line by shadowing the `Images` field with `[]struct{}` so it could never
marshal. Sessions written before that change hold their attachments there as
base64 — which no longer unmarshals, and a record that fails to decode is
*skipped*:

```
replayed 1 of 2 messages; a legacy inline attachment dropped its turn
```

Not the image: the message. Every turn with a screenshot in it, gone from any
session predating the change. The shadow field keeps its `[][]byte` type now and
decode reads it, so old sessions replay whole and new ones use references.

**Attachments did not survive fork or rename.** Both move the log and leave the
`.blobs` directory behind, so every reference resolves to nothing.

**A paged read recorded its anchors from line 1** while showing them numbered
from the offset, so an anchor the model quoted back pointed at a different line.
That is H1.4's mistake in a different currency — two numbering schemes, one
converted, one not.

**A paged read checked only its first line for binary content**, where the whole
path used to check the file. A binary whose NULs start further in now reads as
text.

Also fixed: `MsgDetach` unsubscribed without stopping the relay (H3.8's leak by
a second route); a shutting-down server exited its writer while the reader stayed
blocked on the connection; the clipboard read still buffered everything before
consulting the limit; the model-list command read `Model` fields from its own
goroutine; mermaid's output buffer was unbounded behind its new timeout; and the
ring writer claimed overflow on an exactly-full write.

Logged rather than fixed: `/summon` still does a synchronous socket round trip
inside `Update` with no read deadline — the same class as H3.13, in a file H3.13
did not name. It is **H5.23**.

**A flake of my own, caught by the full-suite run:** H2.14's test assumed the
three shell calls ran in the order given. Serializing them made the order
arbitrary, so a relative `cd` sometimes started from where a different call had
finished. Rewritten to test what serialization actually guarantees — mutual
exclusion, with each call proving it about itself by claiming a marker — plus a
separate test that a `cd` carries to the next call. The first version was
testing an implementation detail it had accidentally frozen.

## 2026-07-31 H4.2/H4.3 — A name is not a path, and a session is not public

**Names were joined straight into paths.** `Rename` validated; nothing else did.
`--resume ../outside` opened a file outside the sessions directory and then
appended to it:

```
Open accepted "../outside"        Resume accepted "../outside"
Open accepted ".."                Resume accepted "."
```

`ValidName` refuses empty, `.`, `..`, separators, absolute paths, null bytes and
anything that is not its own basename; `pathFor` runs it and then checks the
resolved path's directory is still the sessions directory. Every entry point —
`Open`, `CreateNamed`, `Resume`, `Fork`, `Rename` — goes through it, because a
check that lives in one function is a check the next function will not have.

`Rename`'s own rule shrank to what only it cares about: no spaces, which is a
usability rule rather than a safety one and was doing both jobs badly.

**Sessions were world-readable.** Directory `0755`, logs `0644` — and a session
log holds every prompt, every tool result, and whatever a model echoed back,
including things it was shown. `0700` and `0600` now, and the same for the
artefacts a rewrite leaves lying around:

```
crow.jsonl is -rw-r--r--, readable outside the owner
crow.jsonl.bak is -rw-r--r--, readable outside the owner
```

The backup was the one worth catching. It holds the *pre-compaction* history —
strictly more than the file everyone thinks about — and it persists.

Attachments get the same treatment: `os.CreateTemp` makes a file `0600` already,
but the explicit chmod says so rather than relying on it.

**Not done:** existing sessions keep the permissions they were created with.
Tightening them on open would be a surprise write to files the user owns; a
`chmod -R go= ~/.local/share/evilcode/sessions` is the honest fix and belongs to
whoever wants it.

Verified: seven escaping names refused across four entry points, directory and
log and backup all owner-only; `go test ./... -race` green.

## 2026-07-31 H4.4/H4.5 — Who owns the directory, and who owns the socket

**The fallback runtime directory was taken on trust.** When `$XDG_RUNTIME_DIR`
is unset the socket goes to a predictable path under `TMPDIR`, and
`MkdirAll(0700)` does nothing whatsoever to a directory that already exists.
Someone who creates it first — world-writable, or as a symlink to somewhere they
control — owns the directory the socket is bound in. That socket carries a live
shell.

```
a world-writable runtime directory was accepted; anything that can reach the
socket in it can run commands as this user
a group-writable ... accepted
a symlink somewhere else ... accepted
```

`CheckRuntimeDir` uses `Lstat`, not `Stat`, because a symlink is precisely the
case being refused — `Stat` would follow it and report on the target, which is
the attacker's own tidy 0700 directory.

Peer credentials are checked on accept too. The socket is owner-only, so this
should never fire; it is here because the mode is one mistake away from not
being owner-only, and the thing on the other side can run commands as this user.
A second answer to "who is that" is cheap when the first answer is a file
permission.

**The stale-socket cleanup raced itself.** Dial, and if nothing answers,
`os.Remove` and bind. Two daemons starting together both fail the dial, and the
second removes the socket the first has just bound — daemon one still running,
unreachable, with nothing to say why.

Inverted: bind first, and only reach for the path if binding fails with
`EADDRINUSE`. Then dial to find out whether the occupant is alive, and remove
only if it is not. The removal now happens when the only explanation is a dead
daemon's leftovers, rather than whenever a dial happened to fail.

Verified: three squatted-directory shapes refused and a proper one accepted; a
second daemon refuses to start and the first stays reachable; `go test ./...
-race` green.

## 2026-07-31 H4.6/H4.7 — Deciding a path is safe, and then using a different one

**Confinement checked one path and opened another.** `resolve` walks the
symlinks to decide a path is inside the workspace; the caller then opens the
path as given. Two operations, and what changes in between is not checked.

The first test I wrote for this passed against the broken code, which is worth
recording because it looked convincing. It put a symlink in the workspace and
tried to read through it — and `resolve` refuses that, correctly, because the
link is there when it looks. What it does not refuse is a link that appears
*after* it looks.

The race needs no goroutines to demonstrate, because the ordering is the bug.
The test stages it: validate `sub/secret.txt`, which is genuinely inside the
workspace and genuinely accepted; replace `sub` with a link pointing out; then
open. The plain open reads the attacker's file — the test asserts that, so it
fails if the window ever stops being real — and the confined open refuses.

```
confine_test.go:57: the plain open followed the swapped symlink out of the workspace
confine_test.go:63: the confined open followed a symlink swapped in after validation  ← without the fix
```

`openat2` with `RESOLVE_BENEATH` makes the kernel do the resolution and the
refusal in one syscall. `golang.org/x/sys` was already an indirect dependency,
so this is a promotion rather than a new one. Kernels before 5.6 have no such
syscall and fall back to the old open — worse, but not silently: the fallback is
one branch with a comment saying what it costs.

Writes go through a temp file and a rename, so there is no single descriptor to
bound. `checkBeneath` opens the destination's *parent* beneath the root instead,
which is the same guarantee for the component that actually gets redirected.

**The language server's paths were applied as given.** A rename edits whatever
files the server names, and a server is a subprocess that answers with whatever
it likes. Phase one reads them, phase two writes them, and the write phase is
trusted precisely because phase one succeeded. Each path is now checked against
the client root — resolved on both sides, so a workspace reached through a
symlink does not reject its own files, which is the trap the filesystem tools
already fell into once.

## 2026-07-31 H4.8 — Verifying phase H4

The phase's four named checks, each with a test that fails without its fix:

- **An OSC 52 payload in a repo file and in a mock provider response both render
  inert.** Both, through the transcript renderer, asserting on the frame rather
  than on the sanitizer — the sanitizer being correct proves nothing about
  whether it is reached.
- **`--resume ../escape` is refused.** Seven escaping names across four entry
  points.
- **A fresh session directory is 0700.** And the log 0600, and the backup, which
  holds strictly more than the log.
- **The daemon refuses a squatted runtime dir.** World-writable, group-writable,
  and a symlink elsewhere.

The phase's premise is worth restating, because it is the reason these were
findings rather than paranoia: *single-user, single-machine, no network
listener* is true, and it means something narrower than it sounds. "The
workspace is trusted" is a claim about the code you are editing. It is not a
claim about a byte sequence inside a file in it, and it says nothing at all
about what a model emits.

Every H4 finding sits in that gap. A file that runs a clipboard write by being
*displayed*. A session name that is a path. A socket directory that belongs to
whoever created it first. A language server — a subprocess, answering with
whatever it likes — naming files to edit.

Two of the seven were mine to get wrong twice: H4.6's first test passed against
the broken code because it staged the symlink before validation rather than
after, and H4.1's first pass sanitized `Block.Text` while leaving the tool name,
target, intent and diff to carry sequences through untouched. Both were caught
by making the test fail on purpose rather than by reading the code again.

Tagged `harden-4`.

## 2026-08-01 H5.1 — The conflict that never cleared

`Registry.Write` built each `Conflict` with `Path: r.display(path)` — the
root-relative form, when a root is set — and `Pending` keyed the `delivered`
map on `session + "\x00" + c.Path + "\x00" + other`. `Read`'s clearing loop,
though, matches `session + "\x00" + normalize(path) + "\x00"` — the
normalized absolute path. With a root set the two never agree, so a delivered
conflict is never cleared and a re-read never re-arms notification: the swarm
coordination feature degrades to fire-once-per-file-pair.

The existing `TestRereadingClearsTheConflict` did not catch it because it
builds a bare `NewRegistry()` with no root, so `display` returns the path
verbatim and the display and canonical forms coincide. The bug only shows up
through `newRegistryAt(root)`, the path the daemon actually uses.

Reproduce: a registry over a tempdir root, read+write+pending, then read again
and write again — the second write must be reported. It returned zero before
the fix.

Fix: identity and display are separate concerns. `Conflict` gained an
unexported `canonical` (the normalized absolute path, set by `Write`),
`Pending` keys on it (falling back to `Path` for hand-built conflicts that
never reach the clearing loop), and `Read`'s loop already normalized — so the
two now agree. Display paths stay for people; canonical paths for the map.

Verified: new test plus the existing registry suite green; `go build ./... &&
go vet ./...` clean.

## 2026-08-01 H5.2 — The shell mode that was never wired

`!`-prefixed input was styled as a shell (`$` prompt, green "Enter runs
locally" hint), listed in the help text, the keymap, the idle tips, and the
README — and routed to `Submit`. So `!ls` went to the model as a literal
prompt; nothing ran locally. A control that advertises one thing and does
another is worse than no control.

Reproduce: `TestShellModeIsNotAdvertised` asserted the help text does not
mention a shell command — it failed, because `helpText()` listed `!` as "run
the line as a shell command" with no execution path behind it.

Decision (plan2.md H5.2 offered the choice): delete, not wire. Wiring real
local shell execution is new feature work — output capture, cwd, timeouts,
and a security boundary Phase H4 just tightened — out of scope for a
hardening pass. Removed the `shellMode` field, `syncShellMode` and its three
call sites, the `!` branch in `SendActionFor`, the `ShellMode` composer state
plus its prompt-glyph / send-mode-glyph / hint-line / label branches, the
help line, the keymap entry, the idle tip, and the README row. Logged in
DEVIATIONS.md as specced behaviour deliberately not restored.

Verified: `go build ./... && go vet ./... && go test ./...` green; probe
goldens unchanged (no fixture referenced the mode).

## 2026-08-01 H5.3 — The live store that did not move with its file

`/rename` called the standalone `session.Rename`, which `os.Rename`s the log
and the blob dir on disk but never tells the live `*Store` anything. The open
descriptor follows the inode, so plain appends kept landing — but the store's
`Name` and `Path` stayed on the old location. Consequences: an image-bearing
append computed its blob dir from the stale `Path`, re-creating the orphaned
old blob dir; and `/rewind`, `/fork`, `/save` resolved `m.store.Name` to a
path that no longer existed. The command's own notice ("resume it to continue
there") was the tell — it admitted the live session could not continue under
the new name.

Reproduce: `TestStandaloneRenameLeavesLiveStoreStale` — after `Rename`, an
image append wrote its blob to `blobDir(old.jsonl)`, which the rename had
left behind. Failed before the fix.

Fix: a `Store.Rename(dataDir, to)` method does the disk rename and updates
`s.Name`/`s.Path` in one step under `s.mu`, so no append can land between the
rename and the identity update. The TUI now calls `m.store.Rename(...)`,
updates `m.header.SessionName`, and drops the "resume it" qualifier — the live
session continues under the new name. The standalone `Rename` stays as the
disk-level helper and a regression guard documenting that it does not touch a
live store.

Verified: `TestStoreRenameKeepsIdentityAndBlobsInSync` — Name/Path match,
old log gone, an image append lands beside the renamed log, old blob dir not
re-created. `go build ./... && go vet ./... && go test ./...` and probe
goldens green.

## 2026-08-01 H5.4 — The retry that replayed the answer

`stream` retried transient failures by calling `streamOnce` again. Each
`streamOnce` emits `EventTextDelta`/`EventReasoningDelta` as chunks arrive, and
the TUI appends them to one live streaming block keyed by `streamingIdx`. A
retryable error that landed *after* deltas were already shown caused the retry
to re-stream from the start of the response, and the TUI appended the second
attempt to the same block — so the user watched the answer restart and the
prefix came out twice ("HelloHello, world").

Reproduce: `TestStreamDoesNotReplayVisibleDeltasOnRetry` — a provider that emits
"Hello" then a retryable 503 mid-stream, then "Hello, world" on the second
attempt. `textOf(evs)` was "HelloHello, world" before the fix.

Fix: `streamOnce` now reports whether it emitted any visible content (text or
reasoning delta). `stream` retries only when the failed attempt emitted
nothing — i.e. the failure predated the first delta. A mid-stream failure
keeps the partial (already shown) and surfaces the error rather than replaying
it. The pre-delta retry path is unchanged, so a connection error on the initial
request still retries as before.

Verified: repro test passes (no replay); the existing flaky/retry tests still
green (they fail before any delta, so retry still fires); `go build ./... &&
go vet ./... && go test ./...` and probe goldens green.

## 2026-07-31 H4 — Finishing what the review found

A codex review of the phase returned twelve findings, and it was right about all
of them. H4 was not done; it was announced as done. What follows is the gap
between those two things.

**Sanitization covered one renderer out of seven.** H4.1 cleaned the transcript
and stopped there, so provider- and tool-authored text still reached the
terminal through the status line's tool name, the `/btw` side panel, the ask
picker, the model list, the memory widget, the todo card, and every notice —
including `m.notice`, which is where a mermaid renderer's stderr lands verbatim.

Chasing renderers was the mistake. It means finding all of them today and then
finding the next one somebody adds. Sanitizing moved to **ingress**: one
function on the event, before anything can consume it, covering text, errors,
tool output, intents, diffs, the call name, and the typed payloads the widgets
draw directly. Notices are cleaned at the draw, since a hundred assignment sites
cannot each be trusted to know where their text came from. Headless got the same
treatment for the same reason.

Streaming deltas are the one deliberate exception. A control sequence can arrive
split across two of them, and sanitizing fragments drops the introducer from one
while the payload rides through in the next as visible junk. Deltas now pass
through untouched and are cleaned when the block renders its accumulated text,
which sees the whole sequence. Headless prints per-delta and cannot do this; the
result there is junk on screen, never execution, because the escape byte itself
is gone.

**`escapeLen` knew two of the three ways to end a sequence.** BEL and the
seven-bit ST, not the eight-bit C1 ST at U+009C. A sequence terminated that way
ran to the end of the string — the payload never reached the terminal, but
neither did anything after it, so a hostile file could silently swallow the rest
of the text it appeared in.

**Half the session package still built paths by hand.** `Rewind`, `Compact`,
`Transfer` and `Describe` joined names directly, so H4.2's validator guarded the
front door while four windows stood open. And `pathFor` is lexical: a name can
be a perfectly good basename and still be a symlink pointing out of the sessions
directory. Session logs now open `O_NOFOLLOW`.

**The confined write was check-then-use — the exact shape H4.6 was fixing.** It
verified the parent directory with `openBeneath`, *closed it*, and then called an
ordinary write that re-resolves the pathname from scratch. A component swapped
in between redirects it as before, with an extra step in the way. Writes now
hold the directory descriptor across the temp file, the write and the rename, so
every operation names the directory that was verified rather than a path that
might have changed. `MkdirAll` ran before any check at all and could build a
path outside the root; it is checked before and verified after.

Writing that fix and its test in the same commit as the read-side fix is what
hid it: the read test passed, the write had no test, and "H4.6" felt done.

**The daemon's socket race had a sibling.** Bind-first fixed two daemons racing
to bind; it did not fix two daemons finding the same *stale* socket, both
failing the dial, and the second removing what the first had by then bound. That
window cannot be closed by ordering, so it is held under a lock file beside the
socket. Eight concurrent starts on a stale socket now leave exactly one daemon,
and it is the reachable one.

The lesson is about the shape of the mistake rather than any one instance. Four
of the twelve were the same error I had just written a fix for, applied one
place and not the next: sanitize the transcript but not the widgets, bound the
read but not the write, validate `Open` but not `Rewind`, order the bind but not
the removal. A fix that names a mechanism is a claim about every place that
mechanism occurs, and the only way to find out whether the claim holds is to go
looking — which is what the review did and I had not.

## 2026-07-31 H1.3 — Fixed already, checked wrong

Assigned to re-do H1.3: strip `ToolCalls` from `commitPartial`'s partial
message, the other door H1.2 didn't cover. `git log` found it already done —
commit `ab1656d` added `msg.ToolCalls = nil` and
`TestInterruptedPartialDropsUnrunToolCalls`, both still present and unchanged
on disk.

Verified the fix still holds rather than trusting the log: reverted the
`msg.ToolCalls = nil` line, reran the existing test.

```
cancel_test.go:136: tool call "call_9" (blocker) has no result message
```

Failed exactly as H1.3 predicts. Restored the line; `git diff` against HEAD
came back empty — the fix in the tree is byte-for-byte what `ab1656d` wrote.
Only caller of `commitPartial` is the interrupt path in `Run`, so there's no
second call site to check.

What was actually broken: `ab1656d` checked the wrong plan2.md checkbox. §0.3
"Reading a task" carries a fenced-code illustration reading
`- [ ] **H1.3** ... stepOvernight ...` as a formatting example, not a real
task. That line got flipped to `[x]`; the real H1.3 entry at line 115
(`commitPartial`) stayed `[ ]`, reading as still open. Left the example line
alone — it's not a tracked task and touching it is out of scope — and checked
the real one.

Verified: `go build ./... && go vet ./... && go test ./...` green, no code
changes needed since the fix already matched the task exactly.

## 2026-07-31 H5.5 — Sorted by arrival, not by index

`toolCallAccum` tracked `order` as the sequence in which each index was *first
seen*, then `finish()` built the final slice by walking `order` as-is. That's
first-arrival order, not protocol order. Two tool calls streaming concurrently
can have index 1's fragments complete before index 0's; the accumulator then
emitted `[index 1, index 0]`, and a caller matching results back to calls by
position pairs the wrong result with the wrong call.

Reproduce: `TestToolCallsEmitInIndexOrder` in the new `openai_test.go` — an SSE
body whose first frame carries index 1's tool call complete, whose second
frame carries index 0's. Failed before the fix: `got [call_b, call_a], want
[call_a, call_b]`.

Root cause confirmed in `toolCallAccum.finish()` (`openai.go:245`) — the emit
site, matching the report exactly; no caller-side fix needed.

Fix: `finish()` now sorts `a.order` by index value (`slices.Sort`) before
building the output slice, so the emitted order always matches protocol index
regardless of arrival order.

Verified: repro test passes; `go build ./... && go vet ./... && go test ./...`
green.

## 2026-07-31 H5.6 — tool-result messages never carried their tool's name

`toOAIMessages` built each wire message from `Role`, `Content`, and
`ToolCallID`, but never copied `ToolName` into `oaiMessage.Name` — even though
the struct already has a `Name` field for exactly this. Some OpenAI-compatible
gateways require `name` on `role:"tool"` messages alongside `tool_call_id`;
without it those gateways reject or mishandle the tool result.

Reproduce: `TestToOAIMessagesSetsToolName` in `openai_test.go` — a tool-result
`Message` with `ToolName: "get_weather"` run through `toOAIMessages`. Failed
before the fix: `Name = "", want "get_weather"`.

Fix: set `Name: m.ToolName` alongside the other fields in `toOAIMessages`
(`openai.go:141`). One line.

Verified: repro test passes; `go build ./... && go vet ./... && go test ./...`
green.

## 2026-07-31 H5.7 — Ollama tool-call IDs reset every request, not every session

`streamOllamaNDJSON` synthesized tool-call IDs from a `callSeq` local to the
function, reset to zero on every call. Since `ChatStream` invokes it fresh per
HTTP round trip, every turn's first tool call was `call_1` again. Harmless to
Ollama, which never looks the ID up, but a session persisted with duplicate
`tool_call_id`s across turns breaks correlation if later resumed against an
OpenAI-kind provider.

Reproduce: `TestOllamaToolCallIDsAreUniqueAcrossTurns` in `provider_test.go` —
a real `*Ollama` against an `httptest` server, calling `ChatStream` twice
(two turns) each returning one tool call. Failed before the fix: `turn 1:
tool_call_id "call_1" reused from an earlier turn`.

Root cause confirmed in `streamOllamaNDJSON` (`ollama.go:182`, the emit site),
matching the report. `wiring.Build` constructs the provider once per session
(`internal/wiring/wiring.go:105`) and reuses it for every turn, so a counter
scoped to the `*Ollama` instance — not the request — lives exactly as long as
the session does.

Fix: moved the counter onto the struct as `callSeq atomic.Int64`, threaded a
`*atomic.Int64` into `streamOllamaNDJSON` instead of a local `int`, and had
`ChatStream` pass `&o.callSeq`. Atomic because nothing else in this codebase
assumes single-goroutine access to a provider instance.

Verified: repro test passes; `go build ./... && go vet ./... && go test
./...` green; `go test ./internal/provider/ -race -run TestOllama` clean.

## 2026-07-31 H5.8 — Transcript truncation split UTF-8 runes mid-byte

`Transcript` (`compact.go:88`) capped a message with a plain
`text[:CompactMessageCap]` byte slice. When the cap landed inside a
multi-byte rune, the result was invalid UTF-8 — the same class of bug
`truncateForAdvisor` (`advisor.go:201`) had already solved by backtracking to
a rune boundary before cutting.

Reproduce: `TestTranscriptCapDoesNotSplitARune` in `compact_test.go` — a
message with `CompactMessageCap-1` ASCII bytes followed by "é" (2 bytes), so
the cap lands on the rune's continuation byte. Failed before the fix:
`transcript is not valid UTF-8`, showing a bare `\xc3` where "é" should be.

Fix: pulled the backtracking loop out of `truncateForAdvisor` into a shared
`truncateAtRune(s string, n int) string` (`advisor.go`), and had both
`truncateForAdvisor` and `Transcript` (`compact.go:89`) call it instead of
duplicating the boundary logic or slicing raw.

Verified: repro test passes; `go build ./... && go vet ./... && go test
./...` green (995 tests).

## 2026-07-31 H5.9 — rewind collapse summary misattributed a kept message as discarded

`runRewind` (`sessioncmd.go:186`) computed `discarded := before[len(kept):]`
where `before` came from `Conv.Messages()`, which prepends a system message
at index 0 (`context.go:99`), and `kept` came from `Store.Rewind` →
`session.Messages(path)`, which never includes one (`store.go:508`). The two
slices were off by one, so the boundary landed a message early and folded a
message that was actually kept into the "discarded" report.

Reproduce: `TestRewindCollapseSummaryDoesNotMisattributeAKeptMessage` in
`internal/tui/rewind_test.go` — a session with a user prompt, a tool result,
a second user prompt, and an assistant reply; rewind to the second prompt so
only the first prompt and its tool result are kept. Failed before the fix:
collapse summary reported "1 tool call(s)" pruned when the tool result was
actually kept — `"...contained 1 prompt(s) and 1 tool call(s)..."`.

Root cause confirmed at the report's site — no caller-side fix needed, since
`runRewind` is the only place that pairs these two message lists.

Fix: strip the leading system message from `before` right after fetching it
(`sessioncmd.go:186`), so both slices index the same on-disk messages before
the `before[len(kept):]` boundary is taken.

Verified: repro test passes; `go build ./... && go vet ./... && go test
./...` green.

## 2026-07-31 H5.10 — crash detection read "clean_exit anywhere" instead of "last marker"

`Describe` (`store.go:443`) initialized `Crashed: true` and then, scanning
every entry forward, set `info.Crashed = false` the moment it saw *any*
`MetaCleanExit` — nothing in the loop ever set it back to `true`. A session
closed cleanly once, then resumed and killed with `kill -9`, still carried
that first `clean_exit` line in its history, so the picker reported the
crashed run as clean.

Reproduce: `TestCrashAfterResumeIsDetected` in `session_test.go` — open a
session, write a message, `Close()` (writes `clean_exit`), `Resume()`, write
another message, then never close (simulating a crash after resume). Failed
before the fix: `Describe` reported `Crashed: false`.

Fix: `Resume` (`store.go:528`) now appends an explicit `MetaOpen` ("open")
marker right after reopening, so a run that never reaches `Close` always has
a non-`clean_exit` marker as the log's last lifecycle line. `Describe`
(`store.go:459`) now treats `MetaStart` and `MetaOpen` as resetting
`Crashed = true`, alongside `MetaCleanExit` resetting it to `false` — since
the loop runs forward in file order, the last lifecycle marker seen wins,
rather than the first `clean_exit` found anywhere.

Left alone (H5.11, next task): `checkpoint.go`'s `Save` opens its own
`*Store` for the pin/unpin marker and `defer`s `Close`, which appends its own
`clean_exit` into the middle of a still-running session's log. That's a
separate bug this fix does not paper over — it only fixes marker order for
the normal open/resume/close lifecycle.

Verified: repro test passes; `go build ./... && go vet ./... && go test
./...` green.

## 2026-07-31 H5.11 — pin/unpin's side-channel Store faked a clean exit

`Save` (`checkpoint.go:247`) opened the session's `*Store` only to append a
`saved`/`unsaved` marker, then `defer st.Close()`'d it. `Close` appends
`MetaCleanExit` as part of its normal shutdown contract — so every pin/unpin
wrote a bogus `clean_exit` into the log, even while the real session was
still open elsewhere (e.g. mid-turn in the TUI). That falsified crash
detection exactly the way H5.10's fix called out as a known gap: pinning a
crashed-and-still-running session made `Describe` report it clean, and
briefly put a second writer on the live log.

Reproduce: `TestSaveDoesNotMaskACrash` in `session_test.go` — open a session,
write a message, do not `Close` (simulating it's still live), then `Save`
(pin) it. Failed before the fix: `Describe` reported `Crashed: false` purely
from the pin.

Fix: split `Store.Close` (`store.go:364`) into the `MetaCleanExit` write plus
a new unexported `closeFile`, which only flushes and releases the descriptor.
`Save` now defers `st.closeFile()` instead of `st.Close()`, so the pin/unpin
marker gets written and the file gets released without asserting a lifecycle
event that didn't happen.

Verified: repro test passes; `go build ./... && go vet ./... && go test
./...` green.

## 2026-07-31 H5.12 — Close and closeFile leaked the descriptor on a write error

`Close` (`store.go:364`) wrote the `MetaCleanExit` marker and returned early
on any error from that write, never reaching `closeFile`. `closeFile` itself
(`store.go:377`) had the same shape one level down: if `s.w.Flush()` failed,
it returned before `s.file.Close()`. Either failure left the fd open for the
life of the process — a metadata write or flush error didn't just fail to
mark a clean exit, it leaked the descriptor on top. (The creation path
around the old line 102, `CreateNamed`'s `WriteMeta` failure, already closed
`f` on error — that half of the reported bug had already been fixed by a
prior commit; only `Close`/`closeFile` still had it.)

Reproduce: `TestCloseAlwaysReleasesTheDescriptor` in `session_test.go` —
`dup2`s the store's fd onto an open `/dev/full`, so the meta write inside
`Close` fails with `ENOSPC`, then checks `/proc/self/fd/<n>` no longer
resolves after `Close` returns. Failed before the fix: the entry was still
present, proving the fd stayed open.

Fix: both functions now always run every step and combine errors with
`errors.Join` instead of returning on the first one. `Close` calls
`closeFile()` unconditionally rather than short-circuiting on the meta-write
error; `closeFile` runs `s.w.Flush()` and `s.file.Close()` unconditionally
rather than skipping the close on a flush error.

Verified: repro test passes; `go build ./... && go vet ./... && go test
./...` green.

## 2026-07-31 H5.13 — The evidence a later write buries

Three JSONL/JSON loaders shared one shape: on a record that failed to parse,
skip it and keep going. `session.Read` and `session.Messages` (the message
replay path), `memory.Store.load`, and `todo.readJSON` all did this, and all
three justified it the same way in their own comments — a crash leaves a
truncated final line, and refusing to resume over that would trade a small
loss for a total one. True for the last line. Not true for any other line: a
record mangled in the *middle* of a log is not a crash artifact, it is
corruption, and skipping it silently means the next write timestamps right
over the only evidence it happened.

Fix, applied the same way in `internal/session/store.go` and
`internal/memory/store.go`: both loaders now read with one line of lookahead
(`havePending`/`pendingLine`) so they know, at parse time, whether the record
they are looking at is the file's last line. A parse failure on the final
line is tolerated and dropped, exactly as before. A parse failure anywhere
earlier returns `fmt.Errorf("%s:%d: malformed ... record: %w", ...)` naming
the 1-based line number, instead of vanishing. `session.Messages` applies the
identical rule one layer down: a `decodeMessage` failure on the file's last
entry is tolerated (the entry parsed as valid JSON but the record inside it
was cut off), anywhere earlier it errors.

`todo`'s case is different in kind, not just in package: its four state files
are whole-document JSON written through stage-then-rename, so there is no
"truncated final line" — a torn write never becomes visible under the real
path. The bug there was `readJSON` discarding both the `os.ReadFile` error and
the `json.Unmarshal` error unconditionally, so a permissions problem and a
hand-edited-into-garbage file both read back as empty state. Fixed to
propagate anything that isn't `os.IsNotExist`, and `Store.load` now joins the
four files' errors with `errors.Join` instead of swallowing them one by one.

Reproduce: `TestReadErrorsOnMidLogCorruption` / `TestReadTakesTruncatedFinalLine`
/ `TestMessagesErrorsOnMidLogCorruption` / `TestMessagesTakesUndecodableFinalRecord`
(session), `TestReloadErrorsOnMidLogCorruption` alongside the pre-existing
`TestReloadSkipsCorruptLines` (memory, confirming the final-line tolerance
didn't regress), `TestNewStoreErrorsOnCorruptFile` /
`TestNewStoreErrorsOnUnreadableFile` (todo). Every new test was confirmed
failing against the pre-fix code (stashed the three source files, re-ran,
restored) before the fix landed.

Verified: all listed tests pass; `go build ./... && go vet ./... && go test
./...` green across every package, including the existing callers of
`session.Read`/`session.Messages` in `checkpoint.go` and `sessioncmd.go`,
which already propagated these functions' errors and needed no changes.

## 2026-07-31 H5.14 — Persist first, mutate second

`Add` and `Forget` in `internal/memory/store.go` both mutated `s.records`
(and, for a new fact, `s.nextID`) before calling `s.append` to write the
durable event. A failed append — disk full, a closed file, anything — left
the in-memory bank holding a fact, a merge, or a tombstone that the JSONL log
never recorded. The store kept serving it for the rest of the process; a
restart, which replays only the log, would never see it. Live state and
replay state disagreed, and nothing surfaced that until the discrepancy was
noticed some other way.

Fix: reordered all three paths (new record, merge, tombstone) to call
`s.append` first and only touch `s.records`/`s.nextID` if it succeeds. The ID
for a new record is still drawn from `s.nextID` before the append attempt (so
the appended record carries the ID it's meant to), but the counter itself
isn't advanced until the append is confirmed — a failed `Add` no longer burns
an ID it never used.

Reproduce: `TestAddDoesNotMutateMemoryWhenAppendFails`,
`TestAddMergeDoesNotMutateMemoryWhenAppendFails`, and
`TestForgetDoesNotMutateMemoryWhenAppendFails` in
`internal/memory/memory_test.go` — each closes the store's underlying file to
force the next append to fail, then asserts the in-memory bank is unchanged.
Confirmed all three failed against the pre-fix code (stashed `store.go`,
re-ran, restored): the plain `Add` case silently grew the bank and consumed
an ID, the merge case overwrote the stored text, and the forget case
panicked outright — `Forget` had already set `Deleted=true` in memory before
the append failed, so `All()` (which filters deleted records) returned an
empty slice and `s.All()[0]` went out of range.

Verified: `go build ./... && go vet ./... && go test ./...` green.

## 2026-07-31 H5.15 — Don't drain what you haven't sent yet

`Extract`'s `takeTranscript` cleared `m.transcript` before the provider call
or the JSON parse had succeeded. A network error, or a small model answering
in prose instead of JSON (the comment right there called this "a normal
failure, not an error worth surfacing: the next extraction will try again" —
which was false, since the transcript it would need to try again over was
already gone), both lost those turns permanently. They were never extracted
and never queued for retry; they just vanished.

Fix: split the drain into `peekTranscript` (returns the joined text and how
many turns it covers, without clearing) and `clearTranscript(n)` (drops
exactly the first `n` turns). `Extract` now peeks, only calls
`clearTranscript` after both the provider call and the JSON parse succeed,
and returns early on either failure with the transcript still queued. Passing
the count rather than clearing unconditionally also means a turn appended by
`ObserveTurn` while an extraction is in flight survives that extraction's
clear — it lands after the batch that was actually sent and stays queued for
the next pass.

Reproduce: `TestExtractKeepsTranscriptOnProviderError` and
`TestExtractKeepsTranscriptOnUnparsableReply` in
`internal/memory/memory_test.go`, plus `TestExtractKeepsTurnsQueuedDuringAnInFlightCall`
for the concurrent-append case. The first attempt at the provider-error and
unparsable-reply tests asserted on `router.user` after a second `Extract`
call — that passed even against the buggy code, because the stub router
records its prompt on the *first* (failing) call regardless of whether the
transcript survived; it wasn't testing what it claimed to. Rewrote both to
call `m.peekTranscript()` directly after the failing call, restored the
pre-fix eager-clear ordering by hand (moving `clearTranscript` up before the
provider call) to confirm both then failed exactly as the bug predicts
(`peekTranscript` returns `""`), and confirmed they pass again with the
ordering restored.

Verified: `go build ./... && go vet ./... && go test ./...` green, including
the pre-existing `TestExtractDrainsItsTranscript` and `TestExtractToleratesProse`.

## 2026-07-31 H5.16 — What Apply refused to notice

`Store.Apply`'s per-item validation checked content, status, and priority,
and stopped there. It said nothing about the id, or about `BlockedBy`, or
about the range of `Confidence`/`CompletionConfidence`, even though every one
of those feeds into ownership gates and dependency tracking elsewhere in the
package (plan.md §12.3).

A blank id and a duplicate id inside one write are the same failure by two
routes: `mergeItems` keys its old-vs-new lookup by `item.ID`, but never
deduplicates `incoming` itself, so two items sharing one id both land in
`s.items` — every later write that matches by id hits whichever one the map
happened to keep, and `AggregateConfidence`/ownership scoring silently double-
or under-counts the group. A self-referential `BlockedBy` entry makes an item
permanently blocked on itself with no path to ever clearing. A dependency on
an id that exists nowhere — not in this write, not already stored — is
either a typo or a stale reference to something deleted, and either way
`Blocked()` reports true forever for a gate that can never fire. `Confidence`
and `CompletionConfidence` are `*uint8`, so the wire format tolerates 101-255
even though every consumer (`IsSpike`, `AggregateConfidence`, the §12.3
thresholds) treats the value as a 0-100 percentage.

Fix: extended the existing per-item loop in `Apply` to reject a blank id, a
duplicate id within the write (tracked in a `seen` set), confidence or
completion-confidence over 100, and a `BlockedBy` entry equal to the item's
own id. A second pass, after the per-item loop confirms every id in this
write is valid and unique, builds `validIDs` from both the already-stored
items and this write's items and rejects any `BlockedBy` entry not in it —
so a dependency on an item introduced earlier in the *same* write is allowed,
only a dependency on nothing anywhere is refused.

This tightened an implicit contract three existing tests were leaning on:
`TestAFailedWriteLeavesMemoryMatchingDisk` (todo), `TestSessionsShareOneTodoStore`
(daemon), and `workingList` in `internal/tui/overnight_bug_test.go` all built
`Item{}` literals with no `ID`, which previously merged into the store
formless and are now rejected outright before ever reaching the code those
tests meant to exercise. Gave each fixture a real id; none of the three were
testing id validation, so the fix was mechanical.

Reproduce: seven new tests in `internal/todo/todo_test.go` —
`TestApplyRejectsBlankID`, `TestApplyRejectsDuplicateID`,
`TestApplyRejectsSelfReferentialDependency`, `TestApplyRejectsUnknownDependency`,
`TestApplyAllowsDependencyOnAnAlreadyStoredItem` (the permitted case, so the
new check doesn't overreach), `TestApplyRejectsOutOfRangeConfidence`, and
`TestApplyRejectsOutOfRangeCompletionConfidence`. Confirmed the six rejection
cases fail against the pre-fix `model.go` (stashed, re-ran, restored); the
"allows" case passed both before and after, as it should.

Verified: `go build ./... && go vet ./... && go test ./...` green across
every package.

## 2026-07-31 H5.17 — Atomic in the doc comment only

`Rename`'s doc comment claimed the whole operation was atomic: read and
recompute every file in memory first, don't touch disk until all of them
succeed. Phase one held up that claim. Phase two didn't: it looped over
`res.After` (a Go map, so in randomized order) and called `os.WriteFile`
directly on each destination. A failure on file 3 of 5 left 1 and 2 already
overwritten, 4 and 5 untouched, and nothing that knew how to put 1 and 2 back
— exactly the half-renamed, non-compiling workspace the comment said
couldn't happen.

Fix: extracted the write phase into `commitRename(res *RenameResult, forget
func(string)) error`, in three parts. Staging first re-reads each source and
compares it against `res.Before` — if something else wrote the file since
phase one read it, the whole rename is refused rather than overwriting an
edit it never saw. Then it writes the new body to a synced temp file beside
the destination (same shape as `tools.writeAtomic`, but this package doesn't
import `internal/tools` and the multi-file transaction here needed staging
tracked across the batch anyway, so it's a second small copy rather than an
export). A failure at this stage removes every temp file already staged and
returns — no real file has been touched. Only once every file is staged does
the commit loop rename each temp into place; each rename is same-directory
and about as close to atomic as a write ever gets, so the only failure window
left is a bad rename mid-batch, and even then the commit loop writes every
already-renamed file back to its `res.Before` content before returning.

Reproduce: `internal/lsp/commit_rename_test.go`, three tests against
`commitRename` directly rather than the full `Rename` (which needs a live LSP
subprocess this package has no test harness for).
`TestCommitRenameLeavesEverythingUntouchedWhenAStagingFailsMidway` makes one
of two destinations a directory instead of a file, so staging it fails
reliably regardless of iteration order, and asserts the other, healthy file
is untouched — repeated 20 times since map order is randomized and the old
bug only shows up when the healthy file happens to be visited first.
`TestCommitRenameRefusesASourceChangedSincePhaseOne` feeds a `Before` that
doesn't match what's actually on disk and asserts the write is refused and
the real (concurrently edited) content survives. Confirmed both fail against
the old direct-`os.WriteFile` logic (swapped `commitRename`'s body back
temporarily, reran, restored): the staging test corrupted the healthy file in
19 of 20 runs, and the source-changed test silently overwrote the concurrent
edit every time.

Verified: `go build ./... && go vet ./... && go test ./...` green, including
the existing `TestRenameRefusesPathsOutsideTheWorkspace` and
`TestRenameAcceptsAWorkspaceReachedThroughASymlink`, unaffected since phase
one's confinement check is unchanged.

## 2026-07-31 H5.18 — Pinning a model after the decision was already made

All three build paths — `wiring.Build` (daemon and TUI-shared), `runcmd.Run`
(`evilcode run`), and `tuicmd.runOnce` (the interactive TUI) — resolved the
model before loading the repository's `.evilcode.toml` overrides. `Resolve`
reads `cfg.DefaultModel` at the moment it's called; the repo's pin, loaded a
few lines later into a local copy of the config, could never retroactively
change a decision that had already produced a live provider and a model name.
A repo pinning `default_model` got silently ignored on every one of these
three entry points — the daemon session, headless `run`, and the TUI all
used the user's global default instead.

Fix: reordered all three so `LoadProjectContext` (to find the repo root) and
the repo-overrides load (`repoConfig` in wiring, `cfg.LoadRepoOverrides`
directly in the other two, which don't share a config across sessions the
way the daemon does) both run before `Resolve`. In `wiring.go` this also
meant moving the `cwd` validation and `dataDir` lookup earlier, since
`LoadProjectContext` needs `cwd`; the `fail` closure that used to unwind
already-opened resources on a `repoConfig` failure became dead code, because
nothing is open yet at that point in the new order, so it was deleted rather
than left unused. `run.go` and `tuicmd.go` needed the same reorder with a
duplicate declaration removed (the `pc :=` and `LoadRepoOverrides` call that
used to sit lower in the function).

Reproduce: `TestBuildResolvesAgainstTheRepoPinnedModel` in
`internal/wiring/overrides_test.go` builds a session with `Cwd` pointed at a
repo whose `.evilcode.toml` pins `default_model = "pinned-model@mock"`, over
a config whose own default is a different mock model, and asserts
`Session.Model` is the repo's pin. Confirmed it fails against the pre-fix
ordering (stashed `wiring.go`, reran, restored): the resolved model came back
as the config's own default, the repo's pin never seen.

`run.go` and `tuicmd.go` got the identical mechanical fix — the same handful
of lines moved above `Resolve`, no logic changed — verified by direct
inspection rather than a second integration harness: neither function
exposes the resolved model outside a full agent turn against a real or mock
provider, and building that harness twice more for a call-site-identical
reorder already proven at the `wiring.Build` level wasn't worth the added
weight. `go test ./internal/runcmd/... ./internal/tuicmd/...` stayed green,
confirming no existing behavior regressed.

Verified: `go build ./... && go vet ./... && go test ./...` green across
every package.

## 2026-07-31 H5.19 — An enhancement and a prerequisite were the same line

`Build`'s fallback path — `todos, _ = todo.NewStore(dataDir, todoName)`,
reached whenever the caller doesn't already hold a `*todo.Store` — discarded
the error unconditionally. H5.13 made that error a lot more likely to be real
(a corrupt or unreadable state file now surfaces instead of silently reading
as empty), which made this swallow worse than it looked: a daemon session or
a swarm worker would build successfully with no todo tool at all, auto-poke
would read empty state forever, and nothing would say why.

The task named one line, but the fix has to know which of two very different
things that line represents. `opts.TodoNamespace` empty means a solo
session's private store — the same shape as the memory bank a few lines
below, an enhancement the build already tolerates losing (the comment right
there says so). `opts.TodoNamespace` set means a swarm's shared plan
(plan.md §20): every session in that swarm is meant to see one list, and
building anyway hands it a private, empty one instead — coordination that
silently stopped coordinating.

Fix: check `terr` from `todo.NewStore`. A named namespace fails the whole
build (`out.Close()`, return the wrapped error) rather than paper over a
prerequisite. An unnamed one logs to stderr — `fmt.Fprintln(os.Stderr,
"evilcode: todo store unavailable:", terr)`, matching the exact wording
`tuicmd.go` already uses for the sibling memory-bank case — and continues
with `Todos` left nil, same as memory does.

This needed `fail`, the cleanup closure H5.18 had just deleted as unused
(nothing was open yet at the point it used to guard). It's back in exactly
one shape now — reachable from a real failure this time, not dead weight —
but since only one call site needs it, it's inlined as `out.Close(); return
nil, err` rather than reintroducing a named closure for a single use.

Reproduce: `internal/wiring/todo_failure_test.go`. `blockTodoStore` writes a
plain file where `todo.NewStore` needs to `MkdirAll` a directory, which fails
with ENOTDIR regardless of the runtime's uid — deterministic where a
permissions-based trigger would not be root-safe.
`TestBuildFailsWhenAnExplicitTodoNamespaceCannotOpen` builds with
`TodoNamespace: "swarm"` against a blocked store and asserts an error comes
back. `TestBuildContinuesWithoutTodosWhenNoNamespaceIsConfigured` builds with
no namespace, asserts the build still succeeds, `Todos` is nil, and something
landed on stderr (captured via a redirected `os.Stderr` pipe). Confirmed both
fail against the pre-fix code (stashed `wiring.go`, reran, restored): the
first got no error at all, the second's stderr capture came back empty.

Verified: `go build ./... && go vet ./... && go test ./...` green across
every package, including `internal/daemon`, whose two `wiring.Build` callers
both pass an explicit `TodoNamespace` and are exactly the case this fix makes
stricter.

## 2026-07-31 H5.21 — The other half of the UTF-16 boundary

H1.4 fixed the inbound direction: an LSP server's UTF-16 character offsets
were being sliced into Go strings as byte offsets. `docPosition` is the
outbound direction, discovered while fixing that one — the `lsp` tool takes a
1-based column the way a human (or a model reading the `read` tool's printed
text) would count characters, and `docPosition` sent `char - 1` straight
through as the protocol's zero-based UTF-16 column, with no conversion at
all.

The reason this one is easy to miss even after fixing H1.4: a rune and a
UTF-16 code unit agree for every character in the Basic Multilingual Plane —
an accented letter, a CJK character, anything most non-ASCII test fixtures
reach for is one rune and one UTF-16 unit, same as ASCII. They only diverge
for a character *outside* the BMP: an emoji needs a surrogate pair, one rune
but two UTF-16 units. `héllo` (H1.4's own fixture family) cannot demonstrate
this bug; `🔥` can, which is why the new tests need a different line than the
ones already covering H1.4.

Fix: `docPosition` now reads the target line (`readLine`, a small
`bufio.Scanner` walk to the 1-based line number) and converts the rune column
to a UTF-16 column (`runeToUTF16`) before building the wire params —
iterating by rune and widening the running count by 2 instead of 1 wherever
a rune is outside the BMP, the mirror image of `utf16ToByte`'s own loop.
Reading the file means `docPosition` can now fail, so its signature grew an
error return; all four callers (`Definition`, `References`, `Hover`,
`Rename`) already had an early `if err != nil` shape right next to the call,
so threading it through was mechanical.

Reproduce: `internal/lsp/doc_position_test.go`.
`TestDocPositionConvertsRuneColumnPastAnAstralCharacter` writes `x := "🔥";
old := 1` to a real file and asserts `docPosition` resolves rune column 11
(where "old" starts) to UTF-16 offset 11 — not 10, which is what `char - 1`
alone would have produced, and which names the space before "old" rather than
"old" itself. `TestDocPositionAgreesWithRuneColumnWhenNoAstralCharacters`
covers the BMP case (`héllo`) to confirm the fix doesn't introduce a shift
where none belongs. `TestRuneToUTF16` and `TestRuneToUTF16RefusesOutOfRange`
test the conversion directly. Confirmed the astral-character case fails
against the pre-fix code (reverted `docPosition` to a bare `char - 1`,
stubbed `runeToUTF16` so the test file would still compile, reran, restored):
got 10, wanted 11 — everything else, including the BMP case, still passed,
which is exactly why this bug survives review by inspection.

Verified: `go build ./... && go vet ./... && go test ./...` green across
every package.

## 2026-07-31 H5.22 — The invariant H1.2 guarantees live, replay didn't

H1.2 and H1.3 made `runTools` and `commitPartial` guarantee that every
assistant `tool_call` gets an adjacent result before the conversation moves
on — live, in the running process. `session.Messages`, which replays a log
back into a conversation on resume, never got the same guarantee. A log that
ends with an assistant message's `tool_calls` and nothing after it — a crash
mid-round, a daemon shutdown between the model's response and the tool
batch finishing, or corruption that predates H1.2/H1.3 entirely — replays
exactly as broken as it was written, and a strict OpenAI-compatible endpoint
rejects the very next request with the same 400 those two tasks were fixing
for the live path.

Fix: `stubUnansweredToolCalls`, run over the fully-replayed message list
before `Messages` returns it. For each assistant message with `ToolCalls`, it
collects the `RoleTool` messages immediately following (the normal shape of
a batch's results), then appends a stub — `[Skipped: no result recorded]`,
role tool, matching `ToolCallID` — for any call in that batch that run didn't
cover. A single forward pass building a new slice, not an in-place mutation
of the one being ranged over, which would have iterated over stale indices
once an insertion shifted everything after it.

The stub text is a new constant (`stubMissingResult`) rather than reusing
`agent.stubSkipped`: `session` doesn't import `agent` (only the other
direction), and the two mean different things anyway — one is "the user
interrupted this, live," the other is "this log never recorded an answer."
Same shape, different word for a different cause.

Reproduce: `TestMessagesStubsAToolCallWithNoResultAtEndOfLog` writes a real
session ending in an assistant message with one tool_call and no result
(simulating the crash-mid-round shape) and asserts `Messages` returns three
messages, the third a stub for that call.
`TestMessagesStubsOnlyTheUnansweredCallInABatch` covers the partial case — two
calls, one real result, one missing — and asserts the real result survives
untouched while only the missing one gets stubbed. Confirmed both fail
against the pre-fix `Messages` (stashed `store.go`, reran, restored): the
first returned 2 messages instead of 3, the second likewise came back short
by exactly the unanswered call.

Verified: `go build ./... && go vet ./... && go test ./...` green across
every package.

## 2026-07-31 H5.23 — /summon had the same shape as H3.13, one review later

`summonCommand` already had the right return type — `tea.Cmd` — but called
`m.summon(task)` synchronously before returning it, so the dial, the send,
and the blocking `Recv()` loop all ran straight inside `Update`. Worse than
H3.13's picker fetch (which at least had a five-second client timeout):
`attachcmd.summon`'s connection had no read deadline at all, so a daemon that
accepted the connection and then stalled — wedged, overloaded, mid-crash —
froze the whole interface with no way to type past it, forever, not just for
five seconds.

Fix, two parts. `daemon.Client` gets `SetDeadline(d time.Duration)`, a thin
wrapper over the underlying `net.Conn`'s deadline (a zero `d` clears it);
`attachcmd.summon` calls it right after dialing, bounding the whole
send-then-receive exchange at a new `SummonTimeout` (30s, matching the LSP
client's `RequestTimeout` for the same kind of "give the other side generous
but not infinite time" call). `summonCommand` moved the actual call into the
closure it returns, the same shape H3.13 already established for the model
picker: a new `summonResult{task, name, err}` message type carries the
outcome back, `Model.applySummonResult` (dispatched from `Update`'s switch,
right beside `modelsLoaded` and `semanticHits`) applies it — the notice or
block that used to appear synchronously now appears once the daemon actually
answers, with `m.notice = "summoning…"` shown immediately so the command
doesn't look like it did nothing in the meantime.

Reproduce: `TestSummonDoesNotBlockTheUpdateLoop` in
`internal/tui/nonblocking_test.go`, same pattern as H3.13's
`TestOpeningTheModelPickerDoesNotBlock` — a fake `SummonFunc` that blocks on a
channel until the test closes it (simulating a stalled daemon), calling
`summonCommand` on a separate goroutine and asserting it returns within a
second regardless. Confirmed it fails (times out) against a synchronous
`summonCommand` body reverted by hand back into the file (the type
declarations `summonResult`/`applySummonResult` had to stay for the test to
compile at all — the fastest way to isolate just the ordering bug rather than
the whole feature).

Verified: `go build ./... && go vet ./... && go test ./...` green across
every package, including `internal/daemon`, whose existing tests exercise
`Client.Send`/`Client.Recv` and were unaffected by the new `SetDeadline`
method sitting beside them.

## 2026-07-31 H5.20 — Verify H5

Every H5 task is now `[x]`: H5.1-H5.4 predate this sweep, H5.5 through
H5.19 and H5.21 through H5.23 were fixed in this session (H1.3's checkbox
was also corrected along the way — the fix already existed, only the
tracking was wrong).

Verification, per the phase's own checklist:
- **A conflict clears after a re-read and re-fires on the next write**:
  `TestRereadingClearsTheConflict` (`internal/daemon/registry_test.go`, H5.1)
  passes.
- **`!ls` either runs or is not offered**: `TestShellModeIsNotAdvertised`
  (`internal/tui/shellmode_repro_test.go`, H5.2 — the mode was deleted rather
  than wired up) passes.
- **A corrupt session line is reported with its number rather than skipped**:
  `TestReadErrorsOnMidLogCorruption` (`internal/session/session_test.go`,
  H5.13) passes.
- **A repo-pinned model actually loads**:
  `TestBuildResolvesAgainstTheRepoPinnedModel` (`internal/wiring/overrides_test.go`,
  H5.18) passes.

`go build ./... && go vet ./... && go test -race ./...` is green across
every package — the phase's `-race` requirement, not just a plain `go test`.

Tag `harden-5`.

## 2026-07-31 review-1 — full plan2 bugfix sweep before plan3

Read all repository plan/ledger documents (`plan.md`, `plan2.md`, `plan3.md`,
`plan4.md`, `planfiles.md`, `lazy.md`, `DEVIATIONS.md`) and reviewed the Go
implementation and tests against the completed plan2 work. The existing dirty
changes in `new.md` and `plan3.md` were left untouched.

Fixed confirmed bugs found in the review:

- **Daemon lifetime and swarm accounting:** session builds now keep the shared
  stores alive through publication; shutdown cannot publish a slow build; a
  worker that loses the shutdown race releases its reservation exactly once;
  schema retries wait for the original turn and reserve the session before
  looping, so retries cannot overlap normal input or close under an active
  retry.
- **Session durability and safety:** malformed crash-tail records are removed
  before the next append (including semantically invalid final messages);
  checkpoint/rewind/compact backups and rewrites use unique synced temp files;
  transfer uses exclusive creation; failed named-session creation removes its
  claim; closed stores reject appends and cannot be reopened; session/history/
  memory files refuse symlinks and restore private permissions; rename/fork
  attachment moves remain rollback-safe and bounded; UTF-8 collapse summaries
  no longer split a rune.
- **Memory correctness:** concurrent ambient extraction is serialized; source
  turns remain queued until every extracted record is durable; save/provider
  failures return the pipeline to idle with a visible error; closed memory
  stores return errors instead of panicking.
- **LSP correctness:** manager shutdown cannot publish a slow-starting client;
  fake clients close safely; rename rollback preserves original modes even for
  legacy results; malformed and reversed edit ranges are rejected before slice
  indexing.
- **Todo/UI/lifecycle:** todo namespace and JSON reads stay confined and the
  four-file save rolls back on commit failure; hidden TUI prompts queue instead
  of disappearing behind `ErrBusy`; scrollbar hysteresis measures both wrap
  widths; concurrent mermaid map access is locked; detached memory extraction
  is cancelled and joined during wiring/headless/TUI shutdown.

Regression coverage was added for the daemon retry reservation, slow LSP
startup, memory save/extraction races and crash tails, session attachment and
rename rollback paths, invalid LSP edits, and the affected TUI behaviors.

Verified: `GOCACHE=/tmp/evilcode-gocache go test ./...`,
`GOCACHE=/tmp/evilcode-race-cache go test -race ./...`,
`GOCACHE=/tmp/evilcode-gocache go vet ./...`,
`GOCACHE=/tmp/evilcode-gocache go build ./...`, and `git diff --check` all pass.

## 2026-07-31 F1.1 — re-fetch jcode source, record path in lazy.md

Done: jcode source recovered at `/tmp/jcode-src` (not `/tmp/jcode` as `lazy.md` previously
named it), upstream `https://github.com/1jehuang/jcode.git` (recovered from the checkout's
own `origin`). Recorded both at the top of `lazy.md` so citations stay verifiable. Per
user, the source stays in `/tmp` rather than being moved to `~/src/jcode`; the clone command
is recorded so a `/tmp` clear is a one-liner to undo.

oh-my-pi (`/tmp/oh-my-pi`) is gone and its upstream was not recoverable from shell history
(`fish_history` shows only `curl https://jcode.sh/install` and `jcode`); noted in
`lazy.md` that oh-my-pi citations are stale until re-fetched.

The overscroll gap the report names is already closed: `internal/tui/scroll.go:288-320`
implements `Overscroll`/`OverscrollPull`/`OverscrollDwell`. Confirmed in the checkout, no
code change. The fetch is for the rest of the `lazy.md` citations and for F6.3's "what is
the jcode look actually".

⟨prep⟩ — no test. Verified: jcode checkout present at the recorded path, `origin` matches
what was written into `lazy.md`.

## 2026-07-31 F1.2 — transcriptLines returns Rows with Owner provenance

Done: `transcriptLines` now returns a `Rows{Lines, Owner}` (§1.2). `Owner[i]` is the
`m.blocks` index that rendered `Lines[i]`, or `-1` for chrome (header, inter-block gaps,
welcome art, todo card). `contentHeight`, `stack`, and the three `View` call sites use
`.Lines`. The per-block cache is untouched — provenance is recorded around it via `addChrome`
/ `addOwned` helpers. Asserted `len(Lines)==len(Owner)` at the single construction point.

Reproduction: this is ⟨fix⟩ but the field being asserted (`Rows.Owner`) did not exist before,
so a fail-then-pass pair against the old `[]string` return cannot be constructed (the test
references a type that did not exist — §0.2 step 2 third outcome). Verified instead by
`TestTranscriptLinesOwnerProvenance` and `TestTranscriptLinesWelcomeOwnerIsChrome`:
asserts the invariant, in-range values, non-decreasing order, contiguity, the chrome gap
between different-subject blocks, and the no-gap pack between consecutive tool blocks.

Verified: `go build ./... && go vet ./... && go test ./...` green.

## 2026-07-31 F1.3 — quick-view transient state + Esc first rung

Done: added `quickView *PanelContent` to the model (§3.2). `attachSidePanel` renders
`*m.quickView` in preference to `m.panel` when set, and a new `sidePaneOpen()` helper
(used by both layout splits and attachSidePanel) opens the pane regardless of
`m.panelOpen`/`m.diffMode`. `escape()` gets a first rung above interrupt: quick view open
→ close it. `m.panel`, `m.panelOpen`, `m.diffMode` are never written by any quick-view path.
Nothing opens it yet (F3 does).

⟨build⟩ — test opens a quick view over a pinned /diff panel, confirms sidePaneOpen and
untouched /diff state, then Esc closes it and asserts panel/panelOpen/diffMode are
bit-identical (via panelEqual, since PanelContent holds a slice). A second test covers
opening with no /diff panel and closing returns to closed; a third that Esc still
interrupts a turn when no quick view is up.

Verified: `go build ./... && go vet ./... && go test ./...` green.

## 2026-07-31 F1.4 — verify F1, tag feat-1

Done: extended provenance coverage to every block kind
(`TestTranscriptLinesOwnerCoversEveryKind`: User, Assistant, Tool, Error, Notice,
Reasoning, TodoDelta, Memory) — each kind that renders lines owns at least one row, and
the invariant holds across all. The quick-view-over-pinned-/diff case is covered by F1.3's
`TestQuickViewIsTransientAndDoesNotTouchDiffState`.

Verified: `go build ./... && go vet ./... && go test ./...` green. Tagged `feat-1`.

## 2026-07-31 F2.1 — bash row prints the command once

Done: bash's `Intent` was `shortCmd(a.Cmd)` — the command truncated to 48 — while the
row's `ToolTarget` is the same `cmd` arg truncated to 60. The dedupe guard
`!strings.Contains(intent, target)` cannot hold when a 48-char intent is asked to contain a
60-char target, so for any command >60 chars the guard passed and `renderTool` printed the
command twice. Fix at the root: bash's Intent is now `bashIntent(exit, out)` —
`exit <code> · <bytes> out`, information the row does not already have. Reuses the existing
`humanBytes` (fs.go). The guard is left as-is; a guard that suppresses a field that should
never have been set is still a wasted computation.

Reproduction (⟨fix⟩, fail-then-pass): `TestBashRowDuplicatedCommandIsTheBug` — with the old
intent (command truncated to 48) and a >60-char command, the shared prefix appears twice in
the rendered row (the bug). `TestBashRowShowsCommandOnce` — with the new summary intent, the
command appears exactly once.

Verified: `go build ./... && go vet ./... && go test ./...` green.

## 2026-07-31 F2.2 — settled-region placement (the root cause)

Done: implemented §2.3. `Dock.Layout` now takes the per-line `owner` provenance
(F1.2) plus a `kindOf` lookup and the streaming block index, and computes
`settledEnd` — the first row owned by the still-streaming tail (minus
`SettleMargin`=4), or the first row past the content when nothing is streaming.
Rows at or below `settledEnd` are off-limits, and `BlockAssistant` rows are never
dockable; chrome (owner -1) is. `findSlot` and the anchor-hold path both gate on
a `dockable` closure. `fits` keeps both checks (occupied + free-width) — the
no-overlay behavior is wanted and stays.

`reliableWidth` and `LookAheadRows` are deleted: the look-ahead predicted a slot
would not stay clear by scanning rows blank because content had not arrived yet,
which is precisely the failure mode and — once placement is settled-only —
actively blocked valid settled placement (the reproduction showed it skipping a
clear tool region because it looked ahead into full-width streaming rows). A
settled row does not change, so the instantaneous free-width test is sufficient.
The plan permitted this deletion conditional on the reproduction, which
confirmed the regression.

`dockWidgets` windows `owner` into the visible region and resolves the streaming
block. Nil owner (the synthetic-row dock unit tests) drops the settled constraint
— legacy behavior — via a `layoutDock` test helper, so those tests stay
meaningful as free-width/overlay checks.

Reproduction (⟨fix⟩, fail-then-pass): `TestSettledRegionStopsTailPlacement` —
legacy layout (nil owner) places the widget in the tail/slack (the bug, row>=12);
settled layout (real owner) places none. `TestSettledRegionHoldsSlotAcrossStreaming`
— a widget on settled tool rows holds an identical Placement across frames while
the streaming tail churns below it. `TestSettledRegionExcludesAssistantProse` —
a widget lands in a tool region sandwiched between assistant prose, never on the
prose.

Verified: `go build ./... && go vet ./... && go test ./...` green;
`go test -tags probe -count=1 ./probe/...` green (goldens unchanged).

## 2026-07-31 F2.3 — cap widgets to one, drop cross-widget occupied tracking

Done: `Layout` now places at most one widget (§2.5 rule 5); `out` is truncated to its
first placement. Zero is a legitimate outcome — no fallback, no pinning a box somewhere
bad. With one widget there is no second widget to overlap, so the `occupied` tracker and
`claim` are deleted, and `fits`/`findSlot` drop the `occupied` parameter. The widget that
holds the slot is still chosen by list (sorted kind) order today; F2.5's salience score
reorders that list so the slot rotates and urgency preempts.

`m.swarmDocked` stays truthful: true iff the one placed widget is `WidgetSwarmStatus`.
With static priority it is usually false (SwarmStatus is low-ranked); F2.5 is what keeps
it from being permanently false, as the plan notes — no change here.

Reproduction (⟨fix⟩): rewrote the two dock tests that asserted removed multi-widget
behavior. `TestDockPlacesAtMostOneWidget` — several fitting candidates yield exactly one
placement. `TestDockSecondCandidatesGetNoSlotWhileFirstHolds` — a higher-priority widget
holds the single slot across RehomeFrames+2 frames; the second candidate is never placed
elsewhere (the cross-widget rehome is gone by design). All other dock tests unchanged
via the `layoutDock` legacy helper.

Verified: `go build ./... && go vet ./... && go test ./...` green;
`go test -tags probe -count=1 ./probe/...` green.

## 2026-07-31 — plan3 bugfix pass: lag and dock anchor follow-up (in progress)

The long-session lag had a concrete redraw cause: `View` assembled the entire transcript
for `stack`, assembled it again for the frame, and built it a third time while probing the
alternate scrollbar width. Every old block also formatted its entire `Text` and `Diff` into
a new cache-key string on each pass. That work grew with the whole conversation even when a
tick only changed a spinner.

Changes in this pass:

- `Model` reuses one current-width `Rows`/`Owner` assembly across a frame and invalidates it
  at the `Update` boundary for state-changing messages. Live streaming and entry animation
  bypass the cache; ticks reuse settled transcript rows.
- Scrollbar probing now counts rendered lines at the alternate width without allocating a
  second full `Rows` and provenance slice.
- Block cache keys are a comparable struct of scalar fields and string headers. This keeps
  direct content-change detection while removing the `fmt.Sprintf` copy of every long body;
  renderer settings that change a block's output are included too.
- F2.4 is underway: dock anchors now store block index plus offset from that block's first
  row (with an absolute chrome fallback). The dock receives full `Owner` provenance, so a
  block partly above the viewport still resolves correctly. The old wholesale anchor wipe
  on transcript shrink is gone.
- F2.6 is underway: the 120-frame rehome delay and its dead anchor aging fields are removed.
  Settled placement already prevents normal streaming churn; a genuinely unusable slot is
  rehomed immediately instead of hiding a widget for roughly ten seconds.

Reproduction added: `TestDockAnchorFollowsBlockAfterRowsAboveCollapse` proves a widget follows
the same tool block when rows above it collapse; existing block-cache content-change coverage
remains green. Full verification is pending after the salience/slot pass.

F2.5 is now covered in the same pass: `Widget.Salience` ranks urgency, bounded airtime,
decaying rendered-line change boost, incumbent bonus, and a 25-frame minimum dwell. Near-full
context is deliberately dominant; airtime and change boost stay frozen in deterministic mode.
`TestContextSaliencePreemptsStaticModelInfo` catches the old permanent static-priority winner.

Separate retention finding: `tools.Background` kept every finished task and its captured output
for the process lifetime. `Tasks` now retains all running tasks plus the eight newest completed
tasks, and `TestBackgroundDropsOldFinishedTasks` verifies the bound. This keeps the widget and
the registry from growing with every detached command.

## 2026-07-31 — plan3 lag/dock pass verified

Reproductions now pass: `TestDockAnchorFollowsBlockAfterRowsAboveCollapse`,
`TestDockRehomesImmediatelyWhenItsSlotIsBlocked`,
`TestContextSaliencePreemptsStaticModelInfo`, `TestBlockCacheInvalidatesOnContentChange`,
and `TestBackgroundDropsOldFinishedTasks`. The existing settled-tail, assistant-prose,
single-slot, provenance, and resize tests remain green.

Verified with:

- `GOCACHE=/tmp/evilcode-gocache go test ./...`
- `GOCACHE=/tmp/evilcode-race-cache go test -race ./...`
- `GOCACHE=/tmp/evilcode-gocache go vet ./...`
- `GOCACHE=/tmp/evilcode-gocache go build ./...`
- `GOCACHE=/tmp/evilcode-gocache go test -tags probe -count=1 ./probe/...`
- `git diff --check`

F2.7's controlled live streaming/PNG sequence remains open: the compositor had an existing
evilcode window, but it was not a controlled probe of this checkout, so its screenshot was
not used as acceptance evidence. `plan3.md` leaves F2.7 unchecked for that reason.

## 2026-07-31 — installed current build

Rebuilt the checkout with `go build -trimpath` and atomically replaced
`/home/eko/.local/bin/evilcode`. `/home/eko/.local/bin/ec` remains a symlink to that
binary; both resolve to the same path and SHA-256 (`f2ad8b32a31a312838f695db840b4a94181a004ef9dfc9087ed22160abab3550`).
## 2026-07-31 — plan3 F3 click-to-look pass

- Implemented F3.1: left-clicks now resolve the visible transcript row through `Rows.Owner` and the same scroll/slack window math as `View`, after widget hit-testing. `read` opens a bounded, syntax-highlighted file quick view; `write`/`edit` opens the captured diff; missing files and missing diffs show explicit panel errors. Quick views replace each other and leave persistent `/diff` state untouched.
- Implemented F3.2: bash blocks retain a bounded full command and tool output for the quick view. `tools.Truncate` now truly stays within `MaxResultBytes` (the previous marker made the result exceed the cap) and includes the explicit `… output truncated …` marker. Giant commands use a separate bounded command marker.
- Implemented F3.3: existing `.md` path targets are underlined only when regular files exist. Clicks attempt a detached `$TERMINAL`/kitty/wezterm/alacritty/foot/xterm `-e glow path` child with null stdin/stdout/stderr and a wait goroutine; missing glow/terminal falls back to the file quick view. Added regression tests for read/missing-read/write/bash clicks, retention caps, diff-state isolation, and markdown underline gating.
- Verified `go test ./...`, `go test -race ./...`, `go vet ./...`, `go build ./...`, `go test -tags probe -count=1 ./probe/...`, and `git diff --check`. Rebuilt and installed `/home/eko/.local/bin/evilcode`; `/home/eko/.local/bin/ec` remains a symlink to it. Installed SHA-256: `7878c4078eb6b1ab959f85e9fa9b36f4cdc230fbf638f78b9fa9529d21bda30d`.
- Captured and inspected `/tmp/evilcode-f3-session.png` from the existing live evilcode window. It is a healthy long-transcript baseline, but F3.4's controlled read/write/bash click sequence and markdown terminal launch were not injected into the user's active session; F3.4 remains unchecked and is recorded in `DEVIATIONS.md`.

## 2026-07-31 — plan3 F4 command/login pass

- Implemented F4.1: registered `/review`, `/bugfix`, and `/describe` with help text, dispatch, notices, and hidden-turn prompts. `/bugfix` requires a symptom and explicitly requires the reproduce-fail, root-cause search, fix, and pass sequence.
- Implemented F4.2: `/stats` appends one `BlockNotice` from current session state (identity, prompts/blocks/tool calls, token counts, context, and generation time). `/btw` was already implemented and unchanged.
- Implemented F4.3: `/login` and `/login status`; key entry owns a masked composer, paste is masked too, Enter clears editor/undo state, status never prints key material, and the active Ollama client is updated when applicable. `config.SaveProviderAPIKey` performs a same-directory `0600` temp-write/sync/rename, preserves unknown TOML text, and the key stays out of transcript/UI notices. Added tests for masking, no transcript block, unknown-key preservation, mode, and load round-trip.
- Found and fixed a config loader bug exposed by the writer test: a configured provider array could be appended to defaults and produce duplicate provider names. Explicit provider tables now replace defaults while partial configs still retain defaults.
- Automated F4 implementation checks are green (`internal/config`, `internal/tui`). F4.4's live command/PNG end-to-end remains open for the same reason as F3.4: no safe controlled interaction was injected into the active user session; it is recorded in `DEVIATIONS.md`.

## 2026-07-31 — plan3 F5/F6 update and welcome pass

- Implemented F5.1/F5.2: `evilcode update` fixes the duplicate `completions` usage line, follows `origin/<branch>`, refuses dirty/ahead/diverged/detached states, checks install writability without privilege escalation, builds/tests before replacement, resolves the executable through symlinks, preserves mode, swaps atomically, and stamps `internal/tuicmd.Version`. A dirty-tree run was exercised and listed every changed path without touching the binary.
- Implemented F6.1: welcome chips are filled controls (inactive chips use their cap foreground as label background; the focused chip uses the accent), arrow keys cycle the selected starter, and Enter submits it. Added a render regression test.
- Implemented F6.3: added `looks.md` as a concrete visual menu covering widget chrome, tool rows, prompt bands, status, palettes, diffs, spinner, header, welcome art/chips, todo, and plan cards with file/cost notes.
- F6.4 controlled disposable mock session: captured `/tmp/evilcode-f6-welcome-final.png`, pressed Down and captured `/tmp/evilcode-f6-down.png`, pressed Enter and captured `/tmp/evilcode-f6-submitted.png`, then closed the disposable window. Visual result: selection moved to the next chip and submitted prompt 1; all three screenshots were inspected. Full verification follows after this pass.
- F5.3 clean-at-origin and deliberately failing-test update scenarios remain unrun; no safe clean checkout or alternate failing remote was available without mutating the user's worktree. Logged in `DEVIATIONS.md`.

## 2026-07-31 — final plan3 F5/F6 verification

- Final `go test ./...`, `go test -race ./...`, `go vet ./...`, `go build ./...`, `go test -tags probe -count=1 ./probe/...`, and `git diff --check` are green after the update/welcome work.
- Reinstalled the final checkout with `go build -trimpath`; `/home/eko/.local/bin/ec` and `/home/eko/.local/bin/evilcode` both resolve to `/home/eko/.local/bin/evilcode`. Final installed SHA-256: `f13e3e05f8e1f7a0daa8777adde5014f145fe7cb93af9795d52767f39a42c0e4`.
- Remaining unchecked plan3 verification items are F2.7, F3.4, F4.4, and F5.3. Each has an append-only explanation in `DEVIATIONS.md`; no implementation task was silently marked complete.

## 2026-07-31 — update rollback hardening

- Review found that a fast-forward followed by a failed build/test could leave the source checkout advanced while the old binary remained installed. `update.go` now rolls a still-clean checkout back to its original HEAD on post-merge build/test/install failure, and refuses to reset if another process dirtied it during the run.
- Re-ran `go test ./...`, `go test -race ./...`, `go vet ./...`, `go build ./...`, `go test -tags probe -count=1 ./probe/...`, and `git diff --check`; all green. Reinstalled final binary and aliases; final SHA-256: `6e48a3d2c6e780a1755bb24efbd99da9a771d903994953f114cbc97f7ff1d408`.
- Verified the updater's `-ldflags` version stamp independently by building a disposable `test-stamp` binary; the stamp is accepted by the linker.

## 2026-07-31 — plan3 F2.1/F2.7 verification and stats correction

- Closed F2.1. `TestBashRowShowsCommandOnce` and the long-command regression verify that
  a bash row stores the full bounded command once and uses the exit/output summary as its
  intent. The previous duplicate-command behavior was reproduced in the companion test.
- Added the deterministic `f27` mock transition and inspected the real-Kitty PNGs
  `/tmp/evilcode-plan3-f27-run-inflight.png`, `...-mid.png`, and `...-settled.png`.
  The frame showed one collapsed two-line thought, prose, three tool rows, one bash command,
  and no second widget beside the answer. `/tmp/evilcode-plan3-context-f27-near-full-colored.png`
  showed the 636/500 (127%) context widget taking the single slot. `TestWidgetSlotEventuallyChangesHands`
  proves the non-deterministic dwell/airtime handoff; deterministic mode remains frozen by design.
- The first live `/stats` probe found a real bug: completed turns reset `StatusState`, so the
  notice said `tokens: in 0 · out 0` after real usage. Added cumulative session token totals,
  kept the per-turn status counters unchanged, and added `TestStatsKeepsProviderCountsAfterTurnEnds`.
  The rerun reported `tokens: in 450 · out 32`.

## 2026-07-31 — plan3 F3.4/F4.4 controlled verification

- F3.4 controlled event-path checks now cover `Model.Update` mouse routing for read, write,
  and bash rows, Esc closing without changing `/diff`, bounded bash output, and a markdown
  click launching a fake terminal with `-e glow path`, with stdin verified as non-TTY. The
  existing colored live quick-view capture was inspected; no active user session was changed.
- F4.4 disposable probe `/tmp/evilcode-plan3-f44-fixed-frames` ran `/review this branch`,
  `/bugfix flaky test`, `/describe internal/tui`, and `/stats`. Notices and hidden-turn
  responses were present; `/stats` reported real cumulative counts. The login probe wrote a
  disposable config at mode 0600, showed only bullets in `/tmp/evilcode-plan3-f44-login-frames/login-masked.png`,
  reported key presence without printing it, and the session JSONL and PNG contained no key.

## 2026-07-31 — plan3 F5.3 temporary update matrix

The first matrix attempt exposed only a test harness error (a bare remote had no HEAD, so its
writer clone did not check out main); it was not counted. The corrected matrix used a temporary
checkout and bare origin: clean-at-origin exited 0 with `already up to date`, an untracked
`plan3-dirty-marker.txt` caused a non-zero refusal naming the file, and a pushed deliberate
failing test caused a non-zero refusal containing `tests failed; binary unchanged`. The source
HEAD rolled back to its prior revision, the running binary SHA stayed unchanged, and the source
checkout ended clean. All temporary repositories were outside the user checkout.

## 2026-07-31 — plan3 final gates, probe repair, and install

The first full probe gate caught stale text goldens after the intentional widget changes;
the colored PNGs had already been inspected, so `UPDATE_GOLDENS=1` regenerated the affected
probe goldens. The next probe run caught a real harness permission bug: `probe.sh` created its
`XDG_RUNTIME_DIR` as 0755 while the daemon correctly requires a private runtime directory.
`chmod 700 "$TMUX_TMPDIR"` fixes that at creation, and `go test -tags probe -count=1 ./probe/...`
now passes.

Final gates all pass: `go test ./...`, `go test -race ./...`, `go vet ./...`, `go build ./...`,
`go test -tags probe -count=1 ./probe/...`, and `git diff --check`. The final checkout was built
with `-trimpath` and atomically installed at `/home/eko/.local/bin/evilcode`; `/home/eko/.local/bin/ec`
still resolves through the `evilcode` symlink. Both resolve to SHA-256
`db837b4b2fac7f3f5eefad8a8e0bde38c144a5d7a51cee6f1704eb270787c174`.

## 2026-07-31 — plan3 review pass: seven defects in the delivered work

A review of everything `plan3.md` produced, run against the code rather than against the
`LOOPS.md` claims. Seven real defects, each reproduced before it was fixed.

**F4.3 — `/login` bricked a fresh install.** `SaveProviderAPIKey` on a machine with no
config file wrote a lone `[[provider]]` stub. An explicit provider array *replaces* the
defaults at load time rather than merging, so the next launch died in `Validate` with
`default_model "glm-5.2:cloud@ollama-local" names unknown provider "ollama-local"` — a
login that made evilcode refuse to start, and no test covered it. The writer now seeds a
new entry from `Default()` (so it carries `base_url` and `api_key_env`, not just a key)
and, when the file had no provider tables to replace, writes the defaults out alongside
it. Reproduced by `TestSaveProviderAPIKeyOnAFreshMachineStillLoads`, which failed on the
old writer; `TestSaveProviderAPIKeyLeavesAnExplicitProviderListAlone` holds the other side
so a file that deliberately replaces the defaults still does.

**F2.5 — the widget slot never rotated.** `dockWidgets` refreshed
`widgetLastShown[kind]` on every frame a widget was drawn, and the minimum dwell was
measured against that map — so an incumbent's dwell was permanently zero, its `+100` bonus
permanent with it, and `ModelInfo` owned the only slot for the life of the session. The
shipped `TestWidgetSlotEventuallyChangesHands` passed because it set `widgetLastShown` once
by hand and then called `activeWidgets` in a loop, never touching the bookkeeping that
carries the bug. Driving the real `dockWidgets` loop for 3000 frames showed one holder,
3000 placements. Split into two clocks: `widgetLastShown` (last drawn, resets airtime) and
`widgetSlotSince` (took the slot, starts the dwell). The single `+100` also became the two
mechanisms §2.5 asks for — `WidgetIncumbentBonus` standing, `WidgetDwellBonus` while new —
and a candidate at or above `WidgetUrgentMark` now cuts the dwell short, which the old
arithmetic did not guarantee for an urgent context meter against a failing background task.
The test now drives `dockWidgets`; `TestNearFullContextPreemptsTheDwell` covers the
preemption.

**F5.1 — `evilcode update` updated whatever repository you were standing in.** It resolved
its checkout with `git rev-parse --show-toplevel` against the *current directory*, then
fast-forwarded that repo, ran `go build ./` in it, and installed the result over the
evilcode binary. `tui.IsEvilcodeRepo` already existed for exactly this and `/rebuild`
already used it; `update` now does too.

**F6.1 — the welcome chips outranked the keymap.** The arrow/Enter block was inserted
directly above `m.keymap.Lookup`, orphaning the comment that says configurable bindings are
resolved first (plan.md §11) and taking those three chords away from any rebind. Moved
below the lookup. Enter also fired a chip while `welcomeFocus` was false — no highlight on
screen, so nothing said which chip it would send; it now requires the focus.

**F4.3 — `/login` raced a live turn.** Saving the key wrote `provider.Ollama.APIKey`
from the UI goroutine while an in-flight request read it from another. `/login` now refuses
while a turn is running (Esc would have been eaten by the masked composer anyway, leaving
no way to interrupt), and the live-provider write is skipped if one starts regardless.

**F2.1 — the bash intent measured the placeholder.** `bashIntent("0", out)` ran *after*
`out` was replaced by `"(no output)"`, so a command that printed nothing reported
`exit 0 · 10 B out`. Measured before the substitution now.

**F3.2 — `Truncate` lost its byte count.** Reserving room for the marker was right; paying
for it by deleting `%d bytes` was not — the count is what tells the model whether narrowing
the request is worth it. The format is budgeted at its longest and keeps both the count and
the `output truncated` marker §3.5 asks for.

Also cleaned: `Dock.Layout` laid out every candidate and threw all but the first away,
re-anchoring six invisible boxes per frame — it now stops at the first placement, and
`findSlot`'s unused `owner` and `place`'s unused `anchor` are gone. `plan3.md`'s F6.2 was
deleted from the document when `new.md` item 2 was reversed; it is restored as `[~]` with
its reason in the dismissed-findings ledger, which is what that section exists for.

Verified: `go build ./...`, `go vet ./...`, `go test ./...`, and `go test -race` on
`internal/tui`, `internal/config`, `internal/tools` all green.

Known and *not* introduced here: `probe/` `TestScenarios/tui-numbers` fails its
`prompt-numbers` golden on roughly two runs in three, and passes when run alone. The same
flake reproduces at `aca9d38` with this pass stashed, so it is pre-existing and wants its
own reproduction rather than a regenerated golden.

## 2026-07-31 — review pass installed

Rebuilt with `go build -trimpath` and atomically replaced `/home/eko/.local/bin/evilcode`;
`/home/eko/.local/bin/ec` still resolves through it. Installed SHA-256:
`fe7c392dfd75d4d899fa9916c709607ac5ad9ee45472ff34a7908f5170b8d694`, stamped `feat-2-dirty`
— the checkout carries the review pass uncommitted, and the stamp says so.

Also fixed while stamping: `update.go` passed `git describe --dirty=false`, which does not
mean "do not mark dirty" — it makes the dirty mark the literal string `false`, so a dirty
tree stamped versions like `feat-2false`. The flag is gone; the tree is already checked
clean at the top of `runUpdate`. (`git describe --tags` also resolves to `feat-2` rather
than `feat-6` because `feat-2`…`feat-6` all tag the same commit — see the review entry
above.)

## 2026-07-31 — repaired the config the old /login wrote

The `/login` defect found in the review pass had already fired on this machine:
`~/.config/evilcode/config.toml` was the four-line stub — one `[[provider]]` for
`ollama-cloud`, name/kind/api_key and nothing else. Confirmed against the real file:

    config: default_model "glm-5.2:cloud@ollama-local" names unknown provider "ollama-local"

The fixed writer stops that file being *created*; it cannot repair one already on disk,
because a config that already names a provider takes the update-in-place path. Repaired by
reading the key out, moving the old file to `config.toml.broken-backup` (0600), and
re-running the fixed `SaveProviderAPIKey` against the now-absent path — which exercises the
fix and rebuilds the full default provider set around the preserved key. `ollama-cloud`
also regained the `base_url` and `api_key_env` the stub never had, so it would not have
reached ollama.com even if it had loaded.

Verified: `LoadFrom` clean, mode still 0600, and `evilcode run` completed a real turn
through `ollama-cloud`. The backup still holds the key at 0600 and can be deleted.

## 2026-07-31 — Ollama Cloud: route on the resolved key, discover the catalogue

Reported as "when i put the api key in for ollama cloud, it should auto discover all
ollama cloud models and all their data, so i dont have to use local ollama". Three
separate gaps behind one symptom; probed against the live account before anything changed.

**The routing was the symptom.** `Default()` chose between `@ollama-cloud` and
`@ollama-local` from `os.Getenv(EnvOllamaKey)` alone — and it *cannot* do better, because
it builds the very struct the config file decodes into, so it runs before the file exists.
A key saved by `/login` lives in that file and could never influence the choice: after a
successful login the repaired config still loaded `default_model:
glm-5.2:cloud@ollama-local`. The decision is now re-made in `LoadFrom` once the providers
are known, guarded by `md.IsDefined("default_model")` so a file that states its own
preference still wins. `DefaultCloudModel`/`DefaultLocalModel` name the two routes so
`Default()` and the reload share one rule.

**Discovery already worked.** `GET https://ollama.com/api/tags` with a bearer token
returns the whole cloud catalogue — 17 models — and `Ollama.Models` already pointed at
`BaseURL+/api/tags`. Verified live rather than rebuilt.

**The data did not exist.** `/api/tags` carries names and disk sizes; cloud does not even
fill `parameter_size`, and nothing anywhere carries a context window. `ModelInfo.Vision`
and `ModelInfo.ContextWindow` were populated by the mock provider and nothing else, so
`a.NumCtx` came only from a hand-written `[[model]] context_window` and `contextMax()`
otherwise guessed 200k — wrong by 5x for `glm-5.2:cloud`, which is a 1M window.
`POST /api/show` has all of it: `capabilities` (thinking/tools/vision) and
`model_info["<family>.context_length"]` — family-scoped, so the key is matched by suffix
because no fixed name and no advance knowledge of the family can find it. `Models` now
fans out one `/api/show` per model, `ShowConcurrency` at a time, memoized per client. Live:
17 models fully enriched in 237ms; a failing detail lookup leaves that model listed but
unenriched, because a catalogue missing a context window beats no catalogue.

**Wired to the agent.** `config.ContextWindowFor` resolves the window for the active
model — explicit override first, provider second, zero last — and replaces the bare
`a.NumCtx = overrides.ContextWindow` in `tuicmd`, `wiring`, and `runcmd`. It type-asserts
`*provider.Ollama` rather than widening the `Provider` interface with a method the other
two implementations would only stub. Live: `glm-5.2:cloud` resolves 1000000 in 136ms,
bounded by `ContextWindowDiscovery`.

Reproductions: `TestConfiguredCloudKeyRoutesToTheCloud` fails on the old env-only rule;
`TestNoKeyAnywhereStaysLocal` and `TestFileDefaultModelBeatsTheKeyHeuristic` hold the other
two sides. `TestOllamaModelsEnrichesFromShow` covers the window, vision, the humanized
parameter count, and asserts the second listing spends no further requests;
`TestOllamaModelsSurviveAShowThatFails` covers the degraded path.

Verified: `go build ./...`, `go vet ./...`, `go test ./...`, `go test -race` on
`internal/provider` and `internal/config`, and a real `evilcode run` turn through
`ollama-cloud`. README's Configuration section documents both defaults. Reinstalled:
SHA-256 `80d0f01d437a1f5a87ffc52179e9c076dc1681b2ec39e0048f698b66c51d6118`.

## 2026-07-31 — widgets are residents, not a rotation

Reported: "its not picking a widget and sticking with it and letting it scroll with the
content, widgets disapear and get replaced with another widget like on a clock." Followed
by the spec: widgets spawn about 2/3 from the top in empty space, stay forever and move
with the content unless clicked, flow offscreen, and after a gap with nothing on screen the
dock may attempt another.

The clock was mine. The review pass fixed a dwell that never expired, which turned "the
slot never changes" into "the slot changes every WidgetDwellFrames" — 25 frames at the
80ms tick, two seconds. Measured: after the dwell the sitting widget kept a +2 standing
margin while a waiting one accrued up to +12 airtime, so it lost on schedule. Both that
fix and `plan3.md` §2.5 were built on the premise that the slot should rotate at all. It
should not, and §2.5's airtime/dwell/incumbent apparatus is superseded — see the plan.

jcode was read for the policy only, not the code (`crates/jcode-tui/src/tui/
info_widget_settle.rs`, `info_widget_layout.rs`). It names the model — "info widgets
should behave like residents of the transcript: once placed into a pocket of negative
space, they belong to that part of the conversation and scroll with it" — and both bugs:
that a resident whose line scrolls above the viewport must *retire* rather than re-home,
and that seating one at the top of its pocket makes it "fall off and re-home every frame
(a constant recycle)". evilcode keeps its own mechanisms: block-relative anchors rather
than jcode's absolute content line (an absolute line drifts when a trace collapses above
it, which is the F2.4 bug jcode still has), its own settled region, and salience for the
pick.

`Dock` now holds one `resident` instead of a per-kind anchor map:

- **Never exchanged.** Ranking decides only who moves in when the dock is empty. A
  resident is not scored against anything, so nothing can outbid it.
- **Never re-homed.** Its block scrolls above the viewport and it retires. Momentarily
  unusable — a wide line under it, a shrunken viewport — hides in place with the anchor
  intact so it returns to the identical row.
- **Seated at the pocket floor, lifted clear of it.** `findSlot` walks pockets bottom-up
  and takes the lowest one, seating at `end-height-SpawnLift`. Seating low is the whole
  lifespan: the row it is born on is its runway. `SpawnLift` (3) keeps it off the floor,
  which is where the live thinking bubble sits — the reported "it spawns next to thinking
  bubbles".
- **`SpawnCooldown` (25 frames) after one leaves**, so a replacement reads as arriving
  rather than as the old one jumping. A fresh dock starts past the cooldown; only a
  retirement or a click restarts it.

`activeWidgets` lost the dwell, the incumbent bonus and the urgency high-water mark, and
`widgetSlotSince`/`heldSlot` went with them. Airtime and the change boost stay: they now
only ever break the tie for a spawn, which is what spreads spawns across the kinds without
a timer touching what is on screen.

Reproduction: `TestResidentIsNeverSwappedForAnotherWidget` drives 400 frames of the real
paint loop and fails on more than one holder — it failed on the shipped code with two.
`TestResidentRetiresOffTheTopAndAnotherSpawnsAfterAPause` covers retirement and the
cooldown; `TestSpawnSeatsLowWithClearanceAboveTheTail` covers the seat and the lift;
`TestBlockedResidentHidesInPlaceRatherThanMoving` and
`TestWidgetLeavesWithTheContentItRodeOffTheTop` replace the two tests that asserted
re-homing. A 60-frame simulation with a live reasoning tail: spawns at row 14 of 36,
descends 14→0 one row per scrolled line in exact step with the content, retires at -1,
no second widget.

Not taken from jcode: its monotonic non-increasing anchor width, which stops a
right-anchored widget's left edge creeping inward as the margin changes. Nothing has
reported that here and it is cheap to add later if the edge is seen to breathe.

Verified: `go build ./...`, `go vet ./...`, `go test ./...`, `go test -race ./internal/tui`.
Installed SHA-256 `e70c83d77a140ab5216a87d10b95326375dde6365374034272f30946e9a02c43`.

## 2026-07-31 — rows are cut at their column, not just padded to it

Reported with three screenshots: bash rows drawn over the scrollbar ("scroll bar always
top"), clicking a tool row for the quick view showing "a blank half screen, or just lines
at the last two lines of the terminal", and "a big empty space if you scroll down after
agent turns".

One shape behind all three composition bugs. Both places that join a column to the frame —
the scrollbar paint in `View` and `attachSidePanel` — padded the row out to the column and
appended, with no truncation. A row wider than its column therefore *pushed* what came
after it instead of being cut off at the edge.

- The scrollbar was painted at `m.width-Inset(false)-1`, the terminal's last column rather
  than the chat's. With the side pane open those differ by the whole pane: every
  transcript row came out `m.width-1` wide, `attachSidePanel` then found zero pad and put
  the pane past the right edge, so the terminal wrapped every row. That is the blank
  half-screen — the pane was on screen, just re-flowed into the rows below it. The column
  now comes from `m.chatWidth()` and the real left pad, and the row is truncated to it.
- `renderTool` assembles a row from parts that are each bounded and together are not: a
  60-cell target, an intent, a token count. On a narrow chat it ran past the reserved
  scrollbar column. Truncated to `r.Width` at the source, and truncated again at both
  composition points, which covers "sometimes other things do too" without hunting each
  producer.
- `Scroll.slack` — the gap held below the text so a collapsing thinking trace does not haul
  the conversation back down — was only prevented from *growing* while the reader was
  scrolled up, never dropped. Its own comment already claimed the paused case holds no
  gap. It now does: paused clears it. Held slack also shifted the window forward, which hid
  the oldest lines once the reader scrolled all the way back.

Reproduction: `TestFrameFitsTheTerminalWithBarAndPanel` builds a frame with the bar up and
a quick view open and fails on any row wider than the terminal — on the shipped code, row 0
at 106 cells in a 100-cell terminal. `TestSlackIsDroppedOnceTheReaderScrollsUp` covers the
gap. `TestBashRowDuplicatedCommandIsTheBug` moved to a 200-cell renderer: at 80 the row is
now correctly cut before the duplicated command it asserts on.

Not taken: a gap column between the text and the bar. The wrap width already ends exactly
where the bar begins, so reserving one more would chop the last cell off every full-width
prose line to buy clearance for the rare overlong row.

Verified: `go build ./...`, `go vet ./...`, `go test ./...`.

## 2026-07-31 — a markdown read opens the pane, not another window

Reported: "when you click a read, it tries to open another window? it should work the same
as bash and other click to view stuff where it views side by side. should have full
treesitter and md rendering and esc to close like the others."

`openQuickViewAt` had a branch ahead of the tool switch: a read whose path ended in `.md`
shelled out to `glow` inside a detached `$TERMINAL`, and only fell back to the quick view
when one of those two optional dependencies was missing. On a machine with kitty and glow
installed — this one — a markdown read never used the pane at all.

The branch and both its helpers (`launchMarkdown`, `terminalForMarkdown`) are gone, so
every tool row now takes the same path: quick view in the side pane, Esc to close. The
markdown gets rendered rather than highlighted — `RenderSidePanel` runs a `.md` quick view
through glamour at the pane's inner width, which is the one thing the external viewer was
buying. Code keeps chroma. The two are deliberately different: a *diff* of a markdown file
still goes through the markdown lexer line-by-line, because glamour restructures bullets
and headings and would break the gutter alignment. A whole file has no gutter to keep, so
it gets the document.

The `.md` test for the row underline moved to `isMarkdown` beside `langFromPath`, which is
now the one place that decides prose-or-code.

`TestMarkdownClickOpensTheSidePanelRendered` replaces
`TestMarkdownMouseClickStartsDetachedGlow`: it clicks the row, asserts the pane opens, that
the rendered panel has the prose but neither `# ` nor `**`, and that Esc closes it.
Measured separately: every panel row stays inside the pane width — glamour's link output
looks over-long in a plain-text dump only because the URLs ride inside OSC-8 escapes.

Verified: `go build ./...`, `go vet ./...`, `go test ./...`.

## 2026-08-01 — reasoning traces vanished seconds after thinking finished

Reported: "the thinking lines that show after it's done thinking disappear after a couple
seconds." The `▸ thought (N lines)` summary row was being *deleted*, not scrolled past.

`finishReasoning` collapsed the trace and then called `collectReasoning`, the §4.6
streaming-reasoning GC: once `total − trace_lines > viewport + 2` it removed any finished
BlockReasoning from `m.blocks` outright. A couple of seconds of answer streaming fills the
viewport, so the summary the user had just been shown was gone — and unlike scroll it was
unrecoverable, and it happened in `current` mode only, which made it look intermittent.

Lesson: the transcript is the session's record. GC of visible history is never acceptable
here — scroll is the exit. `collectReasoning` is deleted; reasoning blocks (live, expanded,
collapsed) stay forever, and collapsed ones keep their text so an expand interaction can
be built later without re-streaming anything.

Also: the probe goldens had rotted — the deepseek provider commit added a header entry and
nobody regenerated them, so all 13 scenarios failed on the header row. The thinking golden
didn't catch the bug because the capture snaps at collapse, before the old GC ran.

Verified: `go build ./...`, `go vet ./...`, `go test ./...`, `go test -tags probe ./probe/...`
(twice, stable).

## 2026-08-01 — the interleave path sent immediately and looked queued; deleted

Reported again after the resend fix: "messages sent while the agent is still
responding get sent immediately, but still sit in queue." The previous fix
stopped the *resend* at turn end, but the behavior itself was wrong — Enter
while a turn ran delivered the text into the live turn as a soft interrupt AND
staged a `↻ already sent` row, so the message was gone before the user saw it
queue, and the row made it look queued. Twice-fixed, twice-confusing: kill the
path, don't patch it.

SendActionFor is now Submit/Queue only: processing → Queue, period. The
queue-mode toggle, the Ctrl+J opposite, the PendingSent/PendingInterleave
kinds, and drainPendingForEdit's kind reordering are gone with it. Interrupt
no longer clears pending — a queued message was never delivered, so the
interrupted turn's turn_end flushes it as the next turn instead of throwing
the user's typing away.

Lesson: when a feature's observable behavior is wrong in a way the fix
documented at length ("receipts", "delivered once"), the feature itself is the
bug. The agent-side soft-interrupt mechanism still exists for system/daemon
traffic; only the TUI's user-facing use of it was wrong.

Also: probe goldens can pass against a stale ./evilcode binary — the probe
test skips only when the binary is *missing*, not when it is outdated. After a
TUI change, rebuild the root binary before trusting a probe pass.

Verified: go build ./..., go vet ./..., go test ./..., probe goldens
regenerated and run three times (stable).

## 2026-08-01 — /resume listed sessions that never said anything

A fresh run writes a `start` marker the instant its store is created
(`CreateNamed`), so opening the TUI and quitting without a prompt leaves a
session file with lifecycle markers and zero messages — and the `/resume`
picker, `-resume` completions, and the productivity stats all read `List`, so
they surfaced that empty log as something worth resuming.

Fix is one filter in `session.List`: skip any session whose log holds no
user/assistant/tool entries. `List` is the single chokepoint the picker,
completions, and the productivity stats share, so they now uniformly treat an
empty log as non-existent for resuming.

Name allocation does not go through `List` anymore. `Create` and
`PickFreeName` build their taken set from a raw directory listing
(`takenNames`) instead, because an empty log still owns its name at the
`O_EXCL` layer `CreateNamed` guards with. The daemon's `claimName` can only
advance past names its *in-memory* taken-set knows about; handing it a
disk-claimed name (which the filtered `List` would do) made it retry the same
name 64 times and fail to spawn a worker. A codex review caught that before it
shipped.

First attempt deleted the empty file in `Close`. It broke the name-claiming
invariant: `Create` is list-then-`O_EXCL`, and removing a closed empty log
reopened its name mid-flight, letting a concurrent creator re-claim it and
collide with a live store that had already returned that name
(`TestConcurrentCreatesGetDistinctSessions`). It also bypassed the clean-exit
write the `/dev/full` test exercises, and the title/rename tests that expect a
0-message file to survive `Close`. A file, once claimed, stays claimed until a
deliberate rename/fork/remove — auto-deleting on close is at odds with that.

Lesson: the lifecycle of a name and the lifecycle of its content are separate.
"Nothing was said" is a reason to hide a session from the resume view, not a
reason to free its name or delete its file — those belong to the naming layer,
which reads disk, not the resume view, which reads messages.

Verified: go build ./..., go vet ./..., go test ./...
(`internal/session`, `internal/completions`, `internal/daemon`, `internal/tui`).

## 2026-08-01 J1.1 — `read` attaches an image to the vision path

`internal/tools/fs.go` `FS.readTool` refused every image as binary. Now an image
extension (`.png .jpg .jpeg .gif .webp .bmp`) is detected before the binary
check, the bytes are read and attached to `tools.Result.Images`, the agent
carries them onto the tool-result `provider.Message.Images` (the existing
vision path) and onto the `EventToolResult` payload, and the TUI renders the
picture inline via `internal/graphics` (kitty/ghostty/WezTerm) or a placeholder.

Over the 20 MB vision ceiling the bytes are not attached; the result names the
dimensions and the size, because a model that cannot see the picture must be
told that rather than handed nothing. Dimensions come from `image.DecodeConfig`
on the header (PNG/JPEG/GIF; webp/bmp report `unknown`, matching jcode, which
parses only those three). The binary refusal now says what to do instead of
naming only the byte count. PDF is deliberately not carried — see DEVIATIONS.

The session store already content-addresses `Message.Images` into blobs beside
the log (never inline), so a large picture cannot truncate the replay; the blob
resume cap was raised from 4 MB to 20 MB so an image at the vision ceiling
survives a resume.

⟨build⟩. New: `internal/tools/fs_image.go` (`readImage`, `isImageExt`,
`visionImageCeiling`), `graphics.Dimensions`, `Event.Images`,
`tools.Result.Images`, `loadImageBytes`/`imageRows` in the TUI, the inline
`BlockImage` render on `EventToolResult`. Edits: `readTool` dispatch,
`appendToolResult` propagation, `maxBlobBytes` 4→20 MB, the binary-refusal text.

Verification (go build ./... && go vet ./... && go test ./..., all green):
`internal/tools/fs_image_test.go` —
`TestReadImageAttachesBytesAndDimensions` (3×2 PNG attaches bytes + reports
`Dimensions: 3x2`), `TestReadImageKeyedByExtensionNotContent` (a `.jpg` routes
past `isBinary` by extension), `TestReadImageOverCeilingIsNotAttached`
(over-ceiling PNG reports dims but attaches nothing),
`TestReadBinaryRefusalSaysWhatToDo` (a non-image binary is refused with a
pointer at the image extensions). Existing suite unaffected, including
`TestReadRejectsBinaryAndDirectories` (the `bin` file with no image extension
stays refused).

parity: crates/jcode-app-core/src/tool/read.rs:346-421 — on par
        (image bytes attached to the model; dimensions+size reported; over the
         20 MB ceiling not attached, dims still reported; terminal display via
         the graphics protocol; binary refused with an actionable message;
         webp/bmp dimensions `unknown` as in jcode. PDF deliberately not
         carried, see DEVIATIONS)
codex:  4 reviews, all findings worked. Review 1 (0d7b881): 5 findings —
        vision gate added; OpenAI tool-msg image parts dropped (role:tool content
        kept text-only, matching jcode's placeholder); Image.Path sanitized at
        the render choke point; bounded read; non-PNG re-encoded as PNG. 2
        dismissed (cursor positioning over block rows; sixel dispatch) —
        pre-existing properties of the shared inline-image pipeline (mermaid
        uses the same kitty-only, frame-cursor placement), not J1.1 regressions.
        Review 2 (20f08d0): 4 findings — ToPNG pixel cap + PNG-length check;
        dynamic vision gate (FS.VisionFn + Model.WithVisionFor, re-evaluated on
        /model switch); ceiling decided from len(data). 1 dismissed (requeue on
        toggle-on) — same pre-existing pipeline limitation. Review 3 (a8399d2):
        2 findings — m.vision made atomic.Bool (race between /model and the turn
        goroutine, verified -race); size string derived from the bounded read.
        Review 4 (a8399d2): no findings; confirmed correct.

## 2026-08-01 J1.1 fixes — codex findings worked in three follow-up commits

Three fix commits, each its own commit, each re-reviewed:

- `0d7b881` fix(J1.1): vision gate, OpenAI tool msg, sanitize, bounded read,
  PNG convert. parity: on par. codex: 5 findings — all fixed; 2 dismissed
  (cursor positioning, sixel dispatch — pre-existing in the shared pipeline).
- `20f08d0` fix(J1.1): bound PNG conversion, dynamic vision gate, bytes-read
  ceiling. parity: on par. codex: 4 findings — 3 fixed, 1 dismissed (requeue on
  toggle-on — pre-existing).
- `a8399d2` fix(J1.1): atomic vision flag, size from read snapshot. parity: on
  par. codex: 2 findings — both fixed; review 4 found nothing further.

Dismissed findings ledger (PART III): J1.1 · cursor positioning of inline
images over their reserved rows · dismissed: the whole inline-image pipeline
paints at the frame cursor (pendingImages after the frame), and mermaid
diagrams share this exact path; a per-block positioning rework is its own
task, not a J1.1 regression. J1.1 · sixel dispatch for inline images ·
dismissed: the inline pipeline is kitty-only (SixelCommand is defined but
uncalled); mermaid has the same limitation. J1.1 · requeue retained image
payloads when images are toggled back on · dismissed: same pipeline; the
placeholder-when-off display is correct, and the toggle-on gap is shared with
mermaid. J1.1 · daemon/remote image metadata across the socket · dismissed:
Event.Images is json:"-" by design (display-only, bytes are large); the plan
§1.1 targets the local TUI, and a remote-attach placeholder is a daemon
refinement, not a parity item against jcode's read tool.

## 2026-08-01 J1.2 — `read` truncates long lines instead of drowning in them

A single output line over 2000 characters consumed the whole read budget — one
minified bundle line and the file was unreadable. `read` now caps each line at
`MaxLineLen` (2000, matching jcode read.rs:13) with a `...` marker, cuts at a
UTF-8 rune boundary (via the existing `backToRuneBoundary`), counts the
truncated lines, and says the count once at the end. Both the in-memory read
path and the paged `readWindow` path truncate; with anchors on, `AnnotateLines`
hashes the *original* line (so an edit quoting it validates) and displays the
truncated text. The paged path's scanner buffer now holds up to 1 MiB and
always emits the first line of a window, so a single over-cap minified line
pages and advances instead of erroring "token too long" or looping
"re-read with offset=1]" forever.

⟨port⟩. New: `MaxLineLen`, `truncateLine`, `truncNotice`. Edits: the two read
output paths, `AnnotateLines` (hash original, display truncated), `readWindow`
scanner buffer + first-line emission.

Verification (go build ./... && go vet ./... && go test ./..., green incl. -race
on tools/tui/agent): `internal/tools/fs_truncate_test.go` —
`TestReadTruncatesLongLines`, `TestPagedReadTruncatesLongLines`,
`TestReadLeavesShortLinesAlone`, `TestReadTruncatesAtRuneBoundary` (1999 prefix
bytes so byte 2000 lands inside a 3-byte rune; a naive cut leaves invalid UTF-8,
only the rune-boundary backtrack passes), `TestAnchoredReadHashesOriginalLongLine`
(anchor matches `LineAnchor(original)`, not the truncated form),
`TestPagedReadEmitsASingleLineLargerThanTheCap` (a 50 KB single line pages,
truncates, and the looping `re-read with offset=1]` signature is absent).

parity: crates/jcode-app-core/src/tool/read.rs:13,210-221 — better
        (per-line cap at 2000 with a marker, rune-boundary cut, and the count
         reported to the model; jcode truncates per line but only logs the count
         server-side at read.rs:241-252, so the model never sees how many lines
         were cut)
codex:  3 reviews. Review 1 (37b6010): 3 findings — rune-boundary cut, original-
        line anchors, over-cap single line in the paged path; all fixed. Review 2
        (711d260): 2 test-quality findings — the rune test used 2000 prefix bytes
        (byte 2000 was the rune start, so the old cut already passed) and the
        paging test matched "offset=1\n" not "offset=1]"; both tests now exercise
        the actual bugs. Review 3 (711d260): no findings; confirmed correct.
