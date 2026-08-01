# evilcode — the plan that makes it feel like evilcode

`plan.md` built evilcode. `plan2.md` made it survive being used. This document makes it
*yours*: the features that are missing, the two places the transcript actively lies to
you, and the visual identity that stops it reading as a jcode reskin. It is the working
spec for that pass and is checked off here, exactly as `plan.md` and `plan2.md` were.

Everything below comes from one source: `new.md`, twelve items written by the person who
uses this tool every day, at `1d487ee` (2026-07-31). That is a different kind of source
than either predecessor. `plan.md` built from a spec that was known-good. `plan2.md` fixed
bugs from two models that had read the code and might be wrong about it. This plan
implements a **user report**: the symptoms are certain — he watched them happen — and the
causes, the scope, and the shape of the fix are usually unstated — and where they are
stated, they are worth more than a reading of the code, because they come from watching it.
Item 5 says "widgets flash in and out". The follow-up says *why*: they spawn at the bottom
of the screen, where text is still streaming and thinking bubbles are still scrolling.
That is the root cause, and §2.2 is written around it.

So this plan carries the diagnosis the report does not. Every task below names the file,
the symbol, and the mechanism, because that work has been done once here rather than
twelve times at the keyboard.

---

# PART 0 — Process: how the building agent works

## 0.1 What is different from `plan.md` and `plan2.md`

`plan2.md` §0.2 required **reproduce-then-fix**: write the smallest test that fails
because of the bug, watch it fail, fix, watch it pass. That discipline earned its place
and it does not go away — but only about a third of this plan is bug-shaped. The rest is
new behavior, and you cannot write a failing test for a `/login` command that does not
exist yet in any meaningful sense: the test fails because the code is absent, which
teaches nothing.

So tasks here are tagged with which loop they run:

- **⟨fix⟩** — the reported behavior is wrong today. Run the `plan2.md` loop: reproduce
  first, fail-then-pass, no exceptions. F2 is entirely this. So are F1.2, F6.1.
- **⟨build⟩** — the behavior does not exist. Run the `plan.md` loop: implement against
  the spec in PART I, then the one runnable check that fails if the logic breaks.
- **⟨prep⟩** — neither. A fetch, a document, a decision. No test, but still a commit and
  still a `LOOPS.md` entry.

The one rule that survives unchanged from both predecessors: **a golden only proves a
frame has not changed, never that it is right.** Every task in F6 changes what the screen
looks like on purpose. Regenerate a drifted golden only after looking at the new PNG and
deciding it is better.

## 0.2 The loop (repeat until this plan is complete)

1. Pick the next unchecked `[ ]` task in the current phase.
2. **⟨fix⟩ only — reproduce.** Write the smallest test that fails *because of this bug*,
   and watch it fail. Cite the failure in the `LOOPS.md` entry. Three outcomes:
   - It fails → you understand the bug. Continue.
   - It passes → the symptom is real but your theory of it is wrong. Do not fix what you
     could not break; go back and find the actual mechanism. Unlike `plan2.md`, `[~]` is
     **not** available here as an escape: a model's guess can be wrong, but the user
     watched this happen. Keep looking.
   - You cannot construct the test → say so in `LOOPS.md`, fix it anyway, and verify with
     a PNG instead. Name which.
3. **Find the root, not the report.** Every item in `new.md` describes a screen, not a
   function. Grep every caller before editing. Three of the twelve items turn out to
   share one missing mechanism (F1.2) — build it once.
4. Implement. Reuse what is here before writing new: the side panel, the dock, the block
   cache, and the `submitHidden` prompt path all already exist and all get reused below.
5. `go build ./... && go vet ./... && go test ./...` green.
6. **⟨fix⟩ only** — run the reproduction from step 2 again. It must now pass. A fix
   without a fail-then-pass pair is not done.
7. TUI-visible change → boot the probe rig, drive the scenario, render to PNG, **look at
   the image**. Compare against the § of PART I the task cites. Existing goldens that now
   differ: confirm the new frame is *right* before regenerating.
8. Mark `[x]`. Commit — one task per commit, always green.
9. Kick off a **background codex review of the commit** (`codex:rescue`). Don't block.
   Fold findings into the next iteration. Log dismissed findings with a reason.
10. Append one entry to **`LOOPS.md`** (append-only, never edit old entries):
    `## <date> F<n>.<m>` — what was done, the reproduction and how it failed for ⟨fix⟩,
    verification (test names / PNG filenames), codex verdict when known, deviations.
11. Behavior changed → `README.md` in the same commit. Specced behavior deliberately not
    built → `DEVIATIONS.md`.

## 0.3 Reading a task

```
- [x] **F2.1** `internal/tui/app.go:558` `applyEvent` — description — fix.  ⟨fix⟩ ⟨new.md#10⟩
```

- **ID** — `F<phase>.<n>`. `plan.md` used `P`, `plan2.md` used `H`; `F` is this plan, so a
  `LOOPS.md` heading or a commit subject says which spec it came from without looking.
  Cite it in the commit subject and the `LOOPS.md` heading.
- **`file:line` `symbol`** — line numbers are as-of `1d487ee` and drift the moment you
  start editing; the symbol does not. Trust the symbol.
- **⟨fix⟩ / ⟨build⟩ / ⟨prep⟩** — which loop from §0.1 this task runs.
- **⟨new.md#N⟩** — which of the twelve items it serves. Several items span phases; the
  cross-reference table in PART III is the map back.

## 0.4 Ordering

Phases are ordered by what each one unblocks, then by how often the user hits it:

| phase | what it is | items |
|---|---|---|
| the mechanism three items need, built once | F1 | 5, 6, 9, 10 |
| the transcript stops lying | F2 | 5, 10 |
| click to look | F3 | 8, 9, 10 |
| the commands that are missing | F4 | 3, 4, 12 |
| it updates itself | F5 | 7 |
| it stops looking like jcode | F6 | 1, 2, 11 |

F1 first because it is the only real dependency in the plan: items 5, 9, and 10 all need
the transcript to know which block a screen row belongs to, and it currently does not.
Building that three times, three ways, is how a codebase gets the way `dock.go` already
is. F6 last because it is the only phase whose "right" is a matter of taste, and taste
is cheaper to exercise once everything else is stable.

Within a phase, order is yours. Adjacent tasks in one file are worth batching into one
investigate-fix cycle — but still one commit each.

---

# PART I — What is being built

`plan2.md` skipped this part: the repo was the spec, and the tasks were corrections to it.
This plan adds behavior that has no precedent in the repo, so the behavior is specified
here and the phases below implement it. Where a task and this part disagree, this part
wins — or you log a deviation.

## §1 Row provenance

### §1.1 The problem

`internal/tui/app.go:2025` `transcriptLines` renders every block and returns a flat
`[]string`. Once it returns, **nothing on screen knows where it came from**. The dock
(`app.go:2571` `dockWidgets`) measures those rows with `FreeWidth` and places boxes into
whatever columns are blank; it cannot tell a paragraph of the model's prose from the
blank right-hand side of a tool row. The mouse handler (`app.go:461`) can hit-test
widgets, because `Placement` records their geometry — and can hit-test nothing else,
because nothing else has geometry.

Three of the twelve items are downstream of exactly this:

- **item 5** — widgets must never dock next to model text, and may dock next to tool
  calls and thinking blocks. That is a per-row question about block kind.
- **item 9** — clicking a read or write tool row opens a quick view. That is a per-row
  question about which block.
- **item 10** — same, for a bash row.

### §1.2 The mechanism

`transcriptLines` gains a parallel array. For every line it emits, it records the index
of the `Block` that produced it, or `-1` for lines no block owns (the header, the gaps
`needsGapAfter` inserts, the welcome art, the todo card).

```go
// Rows is a rendered transcript plus the provenance of every line.
type Rows struct {
    Lines []string

    // Owner[i] is the index into Model.blocks of the block that rendered
    // Lines[i], or -1 for chrome: the header, the inter-block gaps, the
    // welcome art, the pinned todo card.
    Owner []int
}
```

Invariants, all cheap to assert and all worth asserting:

- `len(Lines) == len(Owner)` — a single `assert`-style check in the one place `Rows` is
  built is enough; a mismatch here would misplace every widget and every click.
- `Owner` is non-decreasing where it is not `-1`. Blocks render in order.
- A block that renders zero lines contributes zero entries. It does not get a `-1`.

`contentHeight()` becomes `len(rows.Lines)` and nothing else changes about scrolling.
The existing per-block render cache (`Block.cache`, `Block.cacheWidth`, `Block.cacheKey`)
is untouched: provenance is recorded around the cache, not inside it, so a cache hit
still costs nothing.

### §1.3 What consumes it

| consumer | question it asks | §  |
|---|---|---|
| `Dock.Layout` | is this row settled, and may a widget sit on it? | §2 |
| mouse click | which block did I click? | §3 |
| `.md` link detection | which tool row holds this path? | §4 |

Nothing else. This is not a general-purpose layout system and must not grow into one.

## §2 Widget placement policy

### §2.1 What the report says

> the widgets sometimes appear and get covered by some text then disappear, widgets should
> not be flashing in and out, they should appear on top, but never appear next to model
> text output, they can appear next to tool calls or thinking blocks but they should only
> scroll with the main output, they shouldnt scroll inside thinking blocks and they
> shouldnt disappear.

The follow-up names the cause, which is worth more than the symptom:

> its because widgets try to fill space on the left side that is empty, but widgets spawn
> at the bottom of the screen where text is still generating and thinking bubbles are
> scrolling, they need to generate a little higher up so they appear next to stuff that
> isnt going to change. the functionality that tries to stop them from overlaying text is
> actually pretty cool. and it doesnt need to take every oportunity it can to spawn a
> widget, there should only be max 1 widget on screen, and there doesnt nessesarily have
> to be any.

Stated as rules the dock must obey:

1. **Only on settled rows.** A widget may only be placed where the content has finished
   changing. This is the root cause, and §2.2 is the whole of it.
2. **Never next to model text.** A widget may not occupy a row owned by a `BlockAssistant`.
3. **May sit next to tool calls and finished thinking.** `BlockTool`, a completed
   `BlockReasoning`, and the chrome kinds are dockable. A *live* reasoning trace is
   excluded by rule 1 rather than by a rule of its own — which is how "may appear next to
   thinking blocks" and "must not scroll inside thinking blocks" stop contradicting each
   other.
4. **Never overlays text.** Kept exactly as it is: `fits`' free-width check stays.
5. **At most one on screen, and zero is fine.** No fallback placement, no pinning a box
   somewhere bad just to have one.
6. **The one slot rotates, and urgency preempts the rotation.** §2.5.
7. **Scrolls with the main output, and does not drift.** Its anchor survives lines being
   added or removed above it.

Rule 4 is a correction to an earlier draft of this section, which had widgets painting
over their rows. They do not. `dock.go`'s own comment records that overlay was tried and
reverted, and that judgment stands.

### §2.2 Why it happens today

`findSlot` (`dock.go:283`) scans top-down and returns the first run of rows where
`free[i] >= width+WidgetGap`. During and after a model answer the top of the viewport is
full-width prose, so every one of those rows fails. The first run that passes is the
short-line region at the tail — the newest tool rows, the live thinking bubble, and,
decisively, the blank rows below the content.

Those blanks are real rows in the region the dock measures. `app.go:2212` appends
`m.scroll.Slack()` blank rows below the text, and `app.go:2221` pads out to
`res.Transcript` once the transcript is scrolling. `FreeWidth` reports them as maximally
free. So the widget is placed exactly where the next line is about to arrive, and the next
frame covers it.

**`reliableWidth` cannot prevent this, despite being written to.** Its comment claims the
look-ahead is what stops streaming flicker, but it looks ahead in *space* — over rows that
are blank right now. They are blank because the content has not arrived yet, not because
they are margin. It defends against a wide line already on screen below; it cannot see a
line that does not exist yet. At the bottom edge it is weakest of all: `end` clamps to
`len(free)`, so there are fewer rows to take the minimum over precisely where the threat
is.

Everything else follows from that one bad choice of row. `fits` fails next frame, the
`default:` arm (`dock.go:243`) hides the widget in place for `RehomeFrames` — 120 frames,
long enough to read as gone rather than as hysteresis — and it reappears elsewhere once
the churn moves. That is the flashing.

One separate mechanism contributes a jump of its own: `contentHeight < d.lastHeight`
(`dock.go:194`) wipes **every** anchor, and a reasoning trace collapsing from nine lines
to one triggers it on an ordinary turn.

Two notes on the report's wording, neither of which changes the diagnosis:

- Widgets never use the left side. `Col` is always `totalWidth - w.Width()`
  (`dock.go:214`) — the right margin, unconditionally. `anchor.Side` is stored and never
  read for placement, and `DEVIATIONS.md` already records left-side widgets as
  unreachable. The empty space they chase is the ragged right edge.
- "A little higher up" is not a fixed offset. The settled region's boundary moves with the
  content, so §2.3 defines it by what the rows *are*, not by how far up they sit.

### §2.3 The settled region

`Dock.Layout` takes the `Owner` array and a block-kind lookup and computes, before it
places anything, the last row it is allowed to touch:

```
settledEnd = the first row owned by the live tail
             — the newest block if it is Streaming, else the end of content —
             minus SettleMargin
```

Rows at or below `settledEnd` are off-limits for placement. Above it, a row is dockable if:

```
dockable[i] = i < settledEnd
           && (Owner[i] == -1 || kind(Owner[i]) != BlockAssistant)
```

`Owner[i] == -1` covers chrome — the header, the inter-block gaps — which is content
nothing will rewrite. Blank slack and scroll padding sit below `settledEnd` by
construction, so they stop being candidates without needing a rule of their own.

`SettleMargin` is a small guard band, a handful of rows, so a widget is not placed
immediately above a tail that is about to grow into it. Leave it a named constant with a
comment: the right value depends on how fast a model streams and how tall the terminal is,
and it will want tuning against a real session rather than against a test.

Placement rules:

- A run of consecutive dockable rows tall enough for the widget is a slot. Column is the
  right margin as today.
- `fits` keeps **both** its checks — `occupied` and `free[i] >= width+WidgetGap`. Rule 4.
  Settled placement stops the free-width test from failing spuriously; it does not make it
  unnecessary, and a settled row can still be a full-width line of finished prose.
- A widget whose anchor is still inside the settled region and still fits **holds its
  slot**. Since settled rows do not change, this becomes the overwhelmingly common case.
- No slot → no widget. There is no fallback placement.
- `reliableWidth`'s look-ahead may become dead once placement is settled-only: it exists
  to predict a slot that will not stay clear, and a settled slot stays clear by
  definition. Delete it only if F2.2's reproduction confirms nothing regresses — it is
  harmless to keep and cheap to remove later.

### §2.4 Anchors that do not drift

`anchor.ContentTop` is an absolute transcript line, so anything that changes the number of
lines *above* a widget silently changes what its anchor points at. Two things do that
routinely: a reasoning trace collapsing when the answer starts, and a live trace scrolling
its own content inside the fixed `ThinkingLines` frame (`transcript.go:495`,
`DefaultThinkingLines = 6`).

The wholesale anchor wipe at `dock.go:194` is the current defense, and it is worse than
the problem — one collapsing trace re-homes the widget.

Replace both with one mechanism: the anchor stores a **block index plus an offset from
that block's first row**, for every kind rather than only for reasoning. `Owner` gives
`firstRowOf(block)` directly, and the screen row is recomputed each frame as
`firstRowOf(block) + offset - scrollTop`. Lines added or removed above the block do not
move it, a block's internal churn does not move it, and the `contentHeight < lastHeight`
wipe is deleted rather than repaired.

### §2.5 Which widget gets the slot

There is one slot and up to seven candidates (`app.go:2536` `activeWidgets`). Today
priority is static `WidgetKind` order and the list is sorted by it (`app.go:2589`), so
with a single slot the top-ranked candidate would win permanently. `ModelInfoWidget` is
added unconditionally (`app.go:2555`) and outranks BackgroundTasks, SwarmStatus and Tips —
those three would never appear again, and `m.swarmDocked` would be permanently false,
feeding a permanent falsehood to `m.swarm.ObserveDock` (`app.go:2308`).

So the slot rotates, and importance preempts the rotation. Each candidate carries a score;
the highest score holds the slot.

```
score = urgency + airtime  (+ incumbent bonus, if it already holds the slot)
```

**`urgency`** is what the widget's state deserves right now. Only one kind needs a bespoke
ramp:

| widget | urgency |
|---|---|
| ContextUsage | 0 below ~60% of `contextMax()`, ramping steeply toward the maximum as it approaches full |
| everything else | a small floor — ModelInfo and Tips lowest, since they are the "nothing is happening" widgets |

**Change is generic, not per-widget.** Rather than teaching each widget to announce that
its todos changed or its background task finished, hash the widget's rendered `Lines`.
When the hash changes, that kind's urgency takes a boost that decays over a few seconds.
One mechanism covers "a todo was completed", "memory recalled something", "a background
task finished", and every widget added later, without any of them knowing about it.

**`airtime`** is the rotation pressure: it accrues while a kind is *not* holding the slot
and resets when it takes it. It is **capped well below the urgency a genuine alert
produces**, which is the whole design — a context meter at 95% must pin the slot, not be
rotated away by a tip that has been waiting its turn.

**Stability, because rotation is itself a flicker source and flicker is the bug being
fixed:**

- The incumbent gets a bonus, so a challenger must beat it by a margin rather than by a
  point.
- A widget that takes the slot holds it for a minimum dwell, and only an urgency above a
  high-water mark may preempt that dwell.
- Score is evaluated on a slow tick, not every frame.

Every threshold, cap and duration above is a **named constant with a comment**, not a
literal. The right values are a matter of feel at a real terminal in a real session, and
the first set will be wrong. Leave the knobs where they can be found.

**Deterministic mode.** `airtime` and the change boost are wall-clock driven, so under
`Deterministic()` both are frozen at zero and selection falls back to `urgency` alone,
which is derived from state and reproducible. Without this every golden churns —
invariant 5, the same trap `MemoryActivityWidget` already sidesteps at `app.go:2569`.

**Where it lives.** The score is computed in `activeWidgets`, which already holds every
piece of state it needs, and rides on the `Widget` struct as one new field. `Dock.Layout`
picks the maximum and places it. The dock stays a placement engine rather than becoming a
policy engine — it has enough jobs.

## §3 Quick view

### §3.1 What it is

A transient, full-height overlay next to the transcript that shows one thing, opened by
clicking a transcript row, closed by Esc. Items 9 and 10 both describe it:

> you should be able to click on a write or read, opens the side by side with a diff or
> the file, pressing esc closes. its a quick view system seperate from the /diff system

> you should be able to click on it to view it side by side like the quickdiff system, it
> should show a side by side of what it would look like if the command ran in a terminal

### §3.2 Separate from `/diff`, sharing its plumbing

"Separate from the /diff system" is a statement about **state**, not about pixels. The
`/diff` system is a persistent mode: `DiffMode` cycles with Alt+G, `DiffPinned` and
`DiffFile` keep the panel open across turns, and every new diff replaces `m.panel`
(`app.go:571`). Quick view must not disturb any of that — you click a row, you look, you
press Esc, and the panel is exactly as you left it.

So quick view reuses `RenderSidePanel` and `PanelContent` (`internal/tui/sidepanel.go:51`,
`:70`) and adds only the state that makes it transient:

```go
// quickView is the transient click-to-look overlay. Non-nil means it is
// showing; Esc clears it and the persistent /diff panel underneath is
// untouched.
quickView *PanelContent
```

- Render: when `m.quickView != nil`, the side pane draws `*m.quickView` instead of
  `m.panel`, and is open regardless of `m.panelOpen` and `m.diffMode`.
- `m.panel`, `m.panelOpen`, and `m.diffMode` are **never written** by any quick-view path.
- Opening a second quick view while one is open replaces the content. It does not stack.

### §3.3 Esc

`m.escape()` (`app.go:1852`) is currently a two-case ladder: interrupt if processing, else
clear input. Quick view takes the **first** rung — above interrupt. Closing something you
are looking at is what Esc means in every other overlay in this program, and a user who
opened a quick view mid-turn and pressed Esc meant "close this", not "kill the turn".

```
esc: quick view open  → close it
     processing       → interrupt (and disarm poke)
     otherwise        → follow bottom, clear input
```

### §3.4 What each row shows

| clicked row | quick view shows |
|---|---|
| `read` tool | the file, syntax-highlighted, at the path in `ToolTarget` |
| `write` / `edit` tool | the block's `Diff`, rendered as the panel already renders diffs |
| `bash` tool | §3.5 |
| anything else | nothing; the click falls through to existing handling |

A `read` whose file has since been deleted or moved shows the error in the panel rather
than silently not opening. A click that does nothing is indistinguishable from a click
that missed.

### §3.5 The bash quick view

The report is specific:

> it should show a side by side of what it would look like if the command ran in a
> terminal for example the bash command rm -rf would appear in the side by side like
> '> rm -rf' and the output would be under it like a real terminal

So: prompt line, then output, in a monospace block with no transcript styling applied.

```
> rm -rf build/
rm: cannot remove 'build/': No such file or directory
```

The full command — not `shortCmd`'s 48-character truncation — and the full captured
output. This requires the transcript to **retain** the bash output, which today it does
not: `Block.ToolTokens` is `len(e.Output)/4` and the output itself is dropped on the
floor at `app.go:553`. Storing it is F3.2's real work; the rendering is the easy half.

Cap what is retained. `internal/tools/exec.go` already has a `Truncate` for background
tasks; retain at most that much per block, and say `… output truncated` when it bites. A
transcript that keeps every byte of every `find /` is a memory leak with a nice UI.

## §4 Markdown paths as links

> .md files can present as links, clicking opens another terminal running that file in
> 'glow' the terminal md viewer

Two halves.

**Presenting.** A `ToolTarget` ending in `.md` already renders in `RoleFileLink`
(`transcript.go:355`), which is the color but not the affordance. It gains an underline so
it reads as clickable, and only when the file exists — an underlined path to a file that
is not there is a promise the click cannot keep.

**Opening.** Click → spawn a terminal running `glow <path>`. Both `glow` and `kitty` are
present on this machine; neither is guaranteed on another, so:

- Terminal: `$TERMINAL` if set, else the first of `kitty`, `wezterm`, `alacritty`,
  `foot`, `xterm` found on `PATH`.
- Viewer: `glow` if on `PATH`. If it is not, fall back to opening the quick view of §3
  with the file's contents. A missing optional dependency degrades; it does not error.
- Detached. `exec.Command(...).Start()`, not `Run`, and explicitly **not** `syscallExec`
  (`internal/tui/exec_linux.go:8`) — that replaces this process, which is right for
  `/reload` and catastrophic here.
- The child must not inherit the TUI's stdin. A second process reading the same keyboard
  is the bug `exec_linux.go`'s own comment warns about.

## §5 The missing commands

### §5.1 Prompt commands — item 3

> needs commands that give automatic prompts like /review /bugfix /describe

`/plan` (`app.go:1528`) and `/fix` (`app.go:1752`) are already exactly this shape: build a
prompt, hand it to `submitHidden`, set a notice. Three more, same shape, same file:

| command | argument | what the prompt asks for |
|---|---|---|
| `/review` | optional path or "this branch" | Read the diff (or the named path), report correctness, then clarity, then anything genuinely dangerous. No praise, no scope creep, one line per finding with `file:line`. |
| `/bugfix` | the symptom, required | Reproduce first — write the smallest failing test and say it failed — then find the root by grepping every caller, then fix, then show the test passing. Refuse to claim done without the fail-then-pass pair. |
| `/describe` | optional path, defaults to cwd | Explain what this code does and how it is put together, for someone who has never seen it. Structure before detail. No line-by-line narration. |

`/bugfix`'s prompt is the `plan2.md` §0.2 loop, stated to the model instead of to the
agent. That is the point of having it as a command: the discipline is the feature.

Each is a bare `submitHidden`. No mode flag, no permission gate, no state — same reasoning
as `/plan`'s comment: the handoff is conversational.

### §5.2 `/stats` — item 4

> missing alot of commands like /btw /stats

`/btw` already exists (`commands.go:114`, `app.go:1761`) and works. `/stats` does not.

It is not `/productivity`: that is the multi-day habits dashboard — streaks, sparkline,
chronotype — and it is a card. `/stats` is **this session, right now**, and everything it
needs is already on the model:

- `m.status.TokensIn`, `m.status.TokensOut` (`app.go:609`)
- `m.ctxUsed` against `m.contextMax()` (`app.go:2561`)
- `m.genMS` (`app.go:612`)
- `m.promptCount`, `len(m.blocks)` and a count of `BlockTool` among them
- session name, model, provider from `m.header`

One `BlockNotice` block. No new collection, no new state, no card. If it wants to be a
card later, it can become one later.

### §5.3 `/login` — item 12

> needs a /login that lets you input ollama cloud api key

Today the `ollama-cloud` provider (`internal/config/config.go:168`) resolves its key from
`$OLLAMA_API_KEY` via `APIKeyValue()` (`config.go:329`). If the variable is not exported
in the shell that launched evilcode, cloud models fail at request time with a 401 and no
guidance.

`/login` closes that with the smallest thing that works:

- Prompts for the key in the composer with **echo masked**. The key must never reach the
  transcript, `m.notice`, the session JSONL, or a screenshot. This is the one place in
  this plan where getting it wrong is a security bug rather than an annoyance, and the
  masking is not optional.
- Writes it to the config file's `[[providers]]` entry for `ollama-cloud` as `api_key`,
  creating the file if absent. `config.go` has `LoadFrom` and no writer; F4.3 adds one.
- File mode `0600`, written atomically — temp file in the same directory, `fsync`,
  rename. `internal/tools/fs.go` already does exactly this for the write tool (H1.5);
  reuse the approach rather than re-deriving it.
- `$OLLAMA_API_KEY` still wins if set. `APIKeyValue` prefers the environment and that
  ordering does not change — `/login` is for people who do not want to manage an env var,
  not an override of the people who do.
- `/login` with no stored key and no env var says so. `/login status` reports whether a
  key is present and **never prints it**, not even truncated.

## §6 Self-update

> it needs a self-update 'evilcode update' pulls from github and updates itself

A new `case "update"` in `main.go:49`'s switch. Note the report says GitHub; `origin` is
`https://git.evileko.dev/evileko/evilcode.git` (Forgejo). Update follows `origin`,
whatever it points at — hardcoding a forge is how this breaks the first time the remote
moves.

`/rebuild` (`internal/tui/selfdev.go:75`) is the model: build, test, and only then swap.
`update` is that with a fetch on the front, and it runs outside the TUI, so it prints
rather than setting notices.

```
1. Refuse if the working tree is dirty. Say what is dirty. Do not stash.
2. git fetch origin, then compare HEAD to origin/<current branch>.
   Already current → say so, exit 0.
3. git merge --ff-only. A non-fast-forward means local commits exist:
   refuse and say so. Never reset, never force.
4. go build -o <tempfile in the install dir>.
5. go test ./... — a failure leaves the running binary untouched and the
   merge in place. Say which tests failed.
6. Atomic rename tempfile over the installed binary, preserving mode.
7. Print the old and new version.
```

Steps 1, 3, and 5 are all refusals. That is deliberate: an updater that is willing to
throw away work is worse than no updater, because you only find out once.

The installed path comes from `os.Executable()`, resolved through symlinks. If it is not
writable — a system install — say so and print the manual command rather than attempting
a privilege escalation.

## §7 Visual identity

Item 11 asks for a document, not a change:

> we need to divert looks away from jcode look. come up with ways to make changes that
> give evilcode its own visual identity without removing features and write a looks.md
> with as many changes as you can think of and ill pick the ones i like

`looks.md` is therefore a **menu**, and the constraint is explicit: no feature loss. Every
entry names what changes, what it costs, and which file it lives in, so picking an entry
is picking a task. Nothing in `looks.md` is implemented until it is picked — see F6.3.

Items 1 and 2 are the two the report already picked, so they are built in F6 rather than
listed in `looks.md`.

**Item 1 — the launch screen.**

> current launch screen is ugly, the buttons dont look like buttons they only have the
> rounded edges on the sides no background to complete the button look, buttons need to
> be selectable

`welcomeText` (`internal/tui/header.go:169`) draws each chip as
`dim("  ◖") + chip(" text ") + dim("◗")`. The `◖`/`◗` are half-block glyphs meant to be
the rounded *ends* of a filled pill — but the text between them has no background set, so
the ends float against the terminal background and read as stray punctuation. The fix is
one style: give the label a `Background()` matching the caps' foreground. That is the
whole of "look like buttons".

"Selectable" is the larger half: arrow keys move a highlight between chips, Enter puts
that chip's text into the composer and submits it, and any other key returns to normal
typing. The welcome screen is the only screen where the transcript is empty, so there is
no scroll to conflict with and the arrow keys are free.

**Item 2 — the black hole.** Kept.

> remove black hole and replace with something else or just dont have anything at all

That was the original ask; it is reversed. The black hole stays alongside the eye — both
are already built (`internal/tui/idleart.go`), `PickVariant` (`idleart.go:38`) keeps its
50/50 coin flip between `VariantEye` and `VariantBlackhole`, and `SamplerFor`
(`idleart.go:277`) keeps both arms. No code change. Filed here so the reversal is
discoverable rather than silent.

---

# PART II — Phases

## Phase F1 — The mechanism three items need

Nothing here changes what the screen looks like. F2 and F3 are both blocked on it, and
both would otherwise invent their own half of it.

- [x] **F1.1** `/tmp/jcode` (gone) — re-fetch the jcode source. `lazy.md:4` was written
  against `/tmp/jcode` (Rust workspace, ~80 crates) and `/tmp/oh-my-pi`; `/tmp` has since
  been cleared and neither survives, so every "peer implementation" citation in `lazy.md`
  is now unverifiable. Re-clone to a **persistent** path — `~/src/jcode`, not `/tmp` —
  and record the path and the upstream URL at the top of `lazy.md` so this does not
  happen a third time. The upstream URL is not recorded anywhere in this repo; if it
  cannot be recovered from shell history or a package cache, **ask** rather than guessing
  at a plausible GitHub path. Note while doing it: the report cites overscroll as a
  wanted jcode feature, and `internal/tui/scroll.go:288-320` already implements it
  (`Overscroll`, `OverscrollPull` default, `OverscrollDwell`) — so that specific gap is
  already closed and the fetch is for the *rest*.  ⟨prep⟩ ⟨new.md#6⟩
- [x] **F1.2** `internal/tui/app.go:2025` `transcriptLines` — returns a flat `[]string`,
  so no consumer can tell which block a screen row came from. Build §1.2: return a `Rows`
  carrying `Lines` and `Owner`, with `-1` for chrome. Update `contentHeight`
  (`app.go:2020`) and the two `stack()` call sites (`app.go:2090`, `app.go:2423`) that
  call it. The per-block cache is untouched — record provenance around it, not inside it.
  Assert `len(Lines) == len(Owner)` at the single construction point. Reproduce first:
  a test that renders a known block sequence and asserts `Owner` names the right block
  for a row in the middle of each one, including across a `needsGapAfter` gap.  ⟨fix⟩
  ⟨new.md#5,9,10⟩
- [x] **F1.3** `internal/tui/sidepanel.go:51` `PanelContent` — add the transient quick-view
  state of §3.2: a `quickView *PanelContent` on the model, rendered in preference to
  `m.panel` when non-nil, opening the pane regardless of `m.panelOpen`/`m.diffMode`, and
  writing none of them. Wire Esc as §3.3's first rung in `m.escape()` (`app.go:1852`).
  Nothing opens it yet — F3 does. Ship it with one test that opens a quick view, closes
  it with Esc, and asserts `m.panel`, `m.panelOpen`, and `m.diffMode` are bit-identical
  either side.  ⟨build⟩ ⟨new.md#9,10⟩
- [x] **F1.4** Verify F1: render a transcript with every block kind and assert `Owner`
  covers it; open and close a quick view over a pinned `/diff` panel and confirm the
  pinned diff is still there. `go test ./...` green. Tag `feat-1`.

## Phase F2 — The transcript stops lying

Both tasks here are things the screen does today that it should not. Both are ⟨fix⟩ and
both need a reproduction before a line changes.

- [x] **F2.1** `internal/tui/app.go:558` `applyEvent` / `internal/tui/app.go:2657`
  `toolTarget` — the grey text after a bash row is the command again. `toolTarget` reads
  the `cmd` arg and truncates to **60** cells; `internal/tools/exec.go:199,205` sets
  `Intent: shortCmd(a.Cmd)`, which truncates the same string to **48**. The dedupe guard
  at `app.go:557` is `!strings.Contains(e.Intent, b.ToolTarget)` — the 48-char intent
  cannot contain the 60-char target, so the guard passes and `renderTool`
  (`transcript.go:356`) prints the command twice. Fix at the root: bash's `Intent` should
  be a **summary**, not a repeat. Options in order of laziness — drop the `Intent` for
  exec entirely and let the target carry the command; or make it the exit status and
  output size (`exit 0 · 1.2k out`), which is information the row does not already have.
  Do not fix the guard: a guard that correctly suppresses a field that should never have
  been set is still shipping a wasted computation. Reproduce: a test asserting a rendered
  bash row contains the command exactly once.  ⟨fix⟩ ⟨new.md#10⟩
- [x] **F2.2** `internal/tui/dock.go:283` `findSlot`, `:184` `Layout` — **the root cause.**
  `findSlot` returns the topmost run with enough free width, and during a model answer the
  upper viewport is full-width prose, so the first run that passes is the tail: the newest
  tool rows, the live thinking bubble, and the blank slack and scroll padding that
  `app.go:2212`/`:2221` put below the content. `FreeWidth` reads those blanks as maximally
  free, so the widget is placed exactly where the next line will arrive. `reliableWidth`
  (`dock.go:166`) does not save it — it looks ahead in *space* over rows that are blank
  because the content has not arrived yet, and it is weakest at the bottom edge where
  `end` clamps to `len(free)`. Implement §2.3: compute `settledEnd` from `Owner` and
  refuse to place at or below it. **`fits` keeps both checks** — `occupied` and the
  free-width test — the no-overlay behavior is wanted and stays. Reproduce: stream an
  assistant block into a transcript with a placed widget and assert the widget's
  `Placement` is identical on every frame.  ⟨fix⟩ ⟨new.md#5⟩
- [x] **F2.3** `internal/tui/dock.go:184` `Layout` — `Layout` places as many widgets as
  fit, and `activeWidgets` (`app.go:2536`) offers up to seven. Cap it at **one**, with
  zero a legitimate outcome: no fallback placement, no pinning a box somewhere bad just to
  have one. `occupied` loses its cross-widget job and may go with it; delete it rather
  than leaving it as decoration. Check what `m.swarmDocked` (`app.go:2638`) meant when
  several widgets could dock and whether `m.swarm.ObserveDock` (`app.go:2308`) still gets
  a true statement — F2.5 is what keeps it from being permanently false.  ⟨fix⟩
  ⟨new.md#5⟩
- [x] **F2.4** `internal/tui/dock.go:112` `anchor`, `:194` `Layout` — `ContentTop` is an
  absolute transcript line, so a reasoning trace collapsing from nine lines to one moves
  everything anchored below it, and the current defense is to wipe **every** anchor when
  `contentHeight < d.lastHeight`. Implement §2.4: store block index plus an offset from
  that block's first row, for every kind, and recompute the screen row from `Owner` each
  frame. Delete the wipe rather than repairing it. Reproduce twice — collapse a reasoning
  block above a placed widget, and stream a 30-line trace beside one; the widget's row is
  constant in both.  ⟨fix⟩ ⟨new.md#5⟩
- [x] **F2.5** `internal/tui/app.go:2536` `activeWidgets`, `dock.go:184` `Layout` — with
  one slot and static `WidgetKind` priority (`app.go:2589`), the unconditional
  `ModelInfoWidget` (`app.go:2555`) wins forever and Tips, BackgroundTasks and SwarmStatus
  never appear again. Implement §2.5: a `Salience` field on `Widget`, scored in
  `activeWidgets` as `urgency + airtime` with an incumbent bonus and a minimum dwell;
  `Layout` places the maximum. ContextUsage is the one bespoke urgency ramp — near-full
  context must pin the slot and outrank any amount of accrued airtime. Change detection is
  a hash of the widget's rendered `Lines` with a decaying boost, so no widget has to
  report its own news. Every threshold a named constant with a comment; they are feel
  values and the first set will be wrong. Freeze airtime and the change boost under
  `Deterministic()` or every golden churns (invariant 5). Test: a context meter crossing
  its threshold takes the slot from an incumbent and holds it; with nothing urgent, every
  candidate gets the slot within a bounded number of ticks.  ⟨build⟩ ⟨new.md#5⟩
  **Superseded 2026-07-31 — the slot does not rotate.** Built as specced, and the
  rotation was itself the complaint: "widgets disappear and get replaced with another
  widget like on a clock". Both the last clause of the test above and §2.5's premise are
  withdrawn. A widget is a *resident* — it spawns into a settled pocket, rides its anchor,
  scrolls off with the content it was placed beside, and is never exchanged while it is up.
  `Salience`, the airtime cap and the change boost survive with a narrower job: they break
  the tie for a *spawn*, which spreads spawns across the kinds without a timer deciding
  what is on screen. The incumbent bonus, the minimum dwell and the urgency high-water
  mark are gone — there is no incumbent to defend when nothing may challenge it. See
  `LOOPS.md` and `Dock`'s doc comment.
- [x] **F2.6** `internal/tui/dock.go:24` `RehomeFrames` — 120 frames of hide-in-place
  hysteresis was compensating for slots that churned every frame. Settled placement does
  not churn, so the compensation is either dead or is what makes a genuinely displaced
  widget take two seconds to return. Re-home on the next frame; keep the constant only if
  F2.2's reproduction shows real churn without it. If it goes, delete `anchor.BadFrames`
  and `anchor.everPlaced` with it rather than leaving dead fields.  ⟨fix⟩ ⟨new.md#5⟩
- [x] **F2.7** Verify F2: drive a turn with streaming prose, a tool batch, and a
  collapsing reasoning trace; capture PNGs across it and **look at them** — never more
  than one widget, never beside model prose, never moving or blinking, and never in the
  churning tail. Let a session run long enough to see the slot change hands, and drive
  context usage to near-full to confirm the meter takes the slot and keeps it. Run a bash
  tool call and confirm the command appears once. `go test ./...` green. Tag `feat-2`.

## Phase F3 — Click to look

- [x] **F3.1** `internal/tui/app.go:461` `MouseClickMsg` — only `dismissWidgetAt` runs; a
  click on a transcript row does nothing. Add block hit-testing on F1.2's `Owner`, after
  the widget check (a widget covering a row wins the click — it is on top). Route a
  `read` click to a quick view of the file and a `write`/`edit` click to a quick view of
  `Block.Diff`, per §3.4. A file that no longer exists shows the error in the panel;
  a silent no-op is indistinguishable from a missed click.  ⟨build⟩ ⟨new.md#9⟩
- [x] **F3.2** `internal/tui/app.go:553` `applyEvent` — the bash quick view of §3.5 needs
  the command output, and `applyEvent` computes `len(e.Output)/4` and drops the string.
  Retain it on the block, capped at `internal/tools/exec.go`'s existing `Truncate` budget
  with an explicit `… output truncated` marker, and render it as prompt-line-plus-output
  with no transcript styling. Store the **full** command too — `shortCmd`'s 48 chars are
  for the row, not for the view. Watch the memory: a transcript holding every byte of
  every command is a leak, so the cap is load-bearing, not decoration.  ⟨build⟩ ⟨new.md#10⟩
- [x] **F3.3** `internal/tui/transcript.go:355` `renderTool` — a `.md` `ToolTarget`
  renders in `RoleFileLink` but has no affordance and no behavior. Underline it **only
  when the file exists** (§4), and route its click to a detached
  `<terminal> -e glow <path>`: `$TERMINAL`, else the first of kitty/wezterm/alacritty/
  foot/xterm on `PATH`. `glow` missing → fall back to the F1.3 quick view rather than
  erroring. Use `exec.Command(...).Start()`, and explicitly **not** `syscallExec`
  (`internal/tui/exec_linux.go:8`), which replaces this process. The child must not
  inherit the TUI's stdin — two processes on one keyboard is the failure that file's own
  comment warns about.  ⟨build⟩ ⟨new.md#8⟩
- [x] **F3.4** Verify F3: click a read, a write, and a bash row in a live session; each
  opens its quick view and Esc closes it with the `/diff` panel untouched. Click a `.md`
  path and confirm a terminal opens with `glow` and the TUI keeps the keyboard. `go test
  ./...` green. Tag `feat-3`.

## Phase F4 — The commands that are missing

- [x] **F4.1** `internal/tui/commands.go:21` `Commands`, `internal/tui/app.go:1500`
  `runCommandWithArg` — add `/review`, `/bugfix`, `/describe` as prompt commands per §5.1,
  each a bare `submitHidden` in the shape of `/plan` (`app.go:1528`) and `/fix`
  (`app.go:1752`). Register each in `Commands` with a `Long` that states its argument, and
  add them to `HelpSections` (`commands.go:160`) — `UncoveredCommands` would surface them
  anyway, but under "More commands", which is where things go to be ignored.
  `/bugfix`'s prompt is the `plan2.md` §0.2 reproduce-then-fix loop stated to the model;
  that is the feature, so do not soften it into "please fix this bug".  ⟨build⟩ ⟨new.md#3⟩
- [x] **F4.2** `internal/tui/commands.go:21` — add `/stats` per §5.2: one `BlockNotice`
  summarizing this session from state the model already holds (`m.status.TokensIn/Out`,
  `m.ctxUsed` vs `m.contextMax()`, `m.genMS`, `m.promptCount`, tool-call count,
  `m.header`). No new collection, no card, no new state. Note in the commit that `/btw` —
  the other command the report calls missing — already exists at `commands.go:114` and
  works, so the item is half a false positive.  ⟨build⟩ ⟨new.md#4⟩
- [x] **F4.3** `internal/config/config.go:217` `Load` — there is no config *writer*, so
  `/login` (§5.3) adds one: atomic temp-file-plus-rename in the config directory, mode
  `0600`, preserving keys the current struct does not know about if that is achievable
  without a second TOML round-trip — and if it is not, say so in `DEVIATIONS.md` rather
  than silently dropping them. Then the command: masked composer input, write to the
  `ollama-cloud` provider's `api_key`, `$OLLAMA_API_KEY` still wins per `APIKeyValue`
  (`config.go:329`). **The key must never reach the transcript, `m.notice`, the session
  JSONL, or a screenshot** — that is the one requirement in this plan whose failure is a
  security bug, and it needs a test of its own asserting the key appears in none of them.
  `/login status` reports presence and never prints the key, not even truncated.  ⟨build⟩
  ⟨new.md#12⟩
- [x] **F4.4** Verify F4: run each of `/review`, `/bugfix`, `/describe` and confirm the
  injected turn arrives and the notice reads right; `/stats` against a session with real
  token counts; `/login` end to end, then grep the session JSONL and a screenshot PNG for
  the key and find nothing. `go test ./...` green. Tag `feat-4`.

## Phase F5 — It updates itself

- [x] **F5.1** `main.go:49` `run` — add `case "update"` per §6, plus its line in `usage`
  (`main.go:19`), which is currently also missing `dictate` from its subcommand list
  while listing `completions` twice. Follow `origin` rather than hardcoding a forge: the
  report says GitHub, `origin` is `https://git.evileko.dev/evileko/evilcode.git`, and a
  hardcoded host breaks the first time the remote moves. Model it on `rebuildCommand`
  (`internal/tui/selfdev.go:75`) — build, then test, then swap — with fetch on the front.
  The three refusals (dirty tree, non-fast-forward, failing tests) are the feature: an
  updater willing to discard work is worse than none, because you find out once.  ⟨build⟩
  ⟨new.md#7⟩
- [x] **F5.2** `main.go` `update` — resolve the install path with `os.Executable()` through
  symlinks, and swap by atomic rename preserving mode. Not writable — a system install —
  → say so and print the manual command; do not attempt privilege escalation. Print old
  and new version (`internal/tuicmd/tuicmd.go:28`, `Version = "v0.1.0"`, which is a
  constant and will need to move or be stamped for the printout to mean anything; if it
  stays a constant, say so in `DEVIATIONS.md`).  ⟨build⟩ ⟨new.md#7⟩
- [x] **F5.3** Verify F5: `evilcode update` on a clean tree already at origin says so and
  exits 0; on a dirty tree refuses and names the files; with a deliberately failing test
  refuses and leaves the running binary in place. `go test ./...` green. Tag `feat-5`.

## Phase F6 — It stops looking like jcode

Last, because this is the only phase whose "right" is taste. Every task here ends by
looking at a PNG.

- [x] **F6.1** `internal/tui/header.go:169` `welcomeText` — the suggestion chips draw as
  `dim("  ◖") + chip(" text ") + dim("◗")`. The `◖`/`◗` are the rounded ends of a filled
  pill, but the label between them sets no `Background()`, so the caps float and read as
  punctuation. Set the label's background to the caps' foreground — that is the entire
  "look like a button" half. Then make them selectable per §7: arrows move a highlight,
  Enter loads that chip into the composer and submits, any other key returns to typing.
  The transcript is empty on this screen, so the arrow keys are free. Reproduce the
  cosmetic half by looking: capture the welcome PNG before and after.  ⟨fix⟩ ⟨new.md#1⟩
- [~] **F6.2** `internal/tui/idleart.go:32` `VariantBlackhole` — delete the variant, its
  sampler, and the coin flip in `PickVariant` (`idleart.go:38`); `SamplerFor`
  (`idleart.go:277`) keeps its `default:` arm and every session gets the eye. Delete
  `BlackholeSampler` outright rather than leaving it unreferenced. If the eye alone reads
  as thin at full width, do not invent a replacement here — add "no art at all" and any
  replacement ideas as `looks.md` entries and let them be picked.  ⟨build⟩ ⟨new.md#2⟩
  **Dismissed:** `new.md` item 2 was reversed — the black hole stays. See §7 and the
  dismissed-findings ledger.
- [x] **F6.3** `looks.md` (new) — write the menu of §7: as many concrete visual changes as
  you can think of that move evilcode away from the jcode look **without removing
  features**. Each entry names what changes, the file it lives in, and what it costs, so
  picking an entry is picking a task. Cover at least: the border character set and weight
  in `widgets.go:22` `RenderWidget`; the tool row's `✓`/`✗` and `·` separators in
  `transcript.go:341` `renderTool`; the prompt band and its rainbow decay in §7.7's ramp;
  the status line's segment order and separators in `statusline.go`; the built-in palettes
  in `theme/palettes.go`; the diff frame in `renderDiffLang`; the spinner and phase
  glyphs; the header layout in `header.go:66`; the welcome art itself; the todo and plan
  card chrome. Ship it with **nothing implemented** — F6.3 is the document, and each
  picked entry becomes its own task later. Use F1.1's re-fetched jcode to say what the
  jcode look actually *is* rather than guessing at it.  ⟨prep⟩ ⟨new.md#11⟩
- [x] **F6.4** Verify F6: capture the welcome screen, arrow through the chips, submit one
  with Enter. Read `looks.md` end to end as a menu — every entry must be pickable without
  further investigation. `go test ./...` green. Tag `feat-6`.

---

# PART III — Ledger

## Item → task map

| new.md | what it asked for | tasks |
|---|---|---|
| 1 | launch screen buttons, selectable | F6.1 |
| 2 | ~~remove the black hole~~ (kept) | — |
| 3 | `/review` `/bugfix` `/describe` | F4.1 |
| 4 | `/btw` `/stats` | F4.2 (`/btw` already exists) |
| 5 | widgets stop flashing | F1.2, F2.2, F2.3, F2.4, F2.5, F2.6 |
| 6 | re-fetch jcode source | F1.1 |
| 7 | `evilcode update` | F5.1, F5.2 |
| 8 | `.md` links open in `glow` | F3.3 |
| 9 | click read/write → quick view | F1.3, F3.1 |
| 10 | bash grey text + terminal quick view | F2.1, F3.2 |
| 11 | `looks.md` | F6.3 |
| 12 | `/login` for the Ollama Cloud key | F4.3 |

## Corrections to the report

Recorded rather than silently worked around, because the report is the source and a
source that is wrong in a small way is worth knowing about:

- **Item 4 — `/btw` already exists**, at `commands.go:114` and `app.go:1761`, and answers
  in the side panel via the smol role. Only `/stats` is genuinely missing.
- **Item 6 — overscroll already exists**, at `scroll.go:288-320`: `OverscrollPull` is the
  default mode, with dwell and gesture timings. The jcode fetch is still worth doing for
  everything else, but that specific named gap is closed.
- **Item 7 — the remote is not GitHub.** `origin` is
  `https://git.evileko.dev/evileko/evilcode.git`. `update` follows `origin`.
- **Item 9 — "separate from the /diff system" is about state, not pixels.** Quick view
  reuses `RenderSidePanel` and touches none of `/diff`'s state. Building a second panel
  renderer to honor the word "separate" would be reading the report more literally than
  it was meant.
- **Item 5 — widgets have no left side.** The follow-up describes them filling empty space
  on the left; `Col` is always `totalWidth - w.Width()` (`dock.go:214`), the right margin,
  and `anchor.Side` is never read for placement. `DEVIATIONS.md` already records left-side
  widgets as unreachable. The space they chase is the ragged right edge, and the diagnosis
  is unaffected.

## Dismissed findings

`[~]` is not available in this plan for ⟨fix⟩ tasks — see §0.2 step 2 — so anything that
lands here is a ⟨build⟩ task deliberately not built, with its reason, never deleted.

- **F6.2 — delete the black hole.** `new.md` item 2 asked for it; the ask was reversed
  before the phase ran and both idle variants stay. No code changed. The task was first
  deleted from this document outright, which is what this section exists to prevent; it
  is restored above as `[~]` so the reversal is discoverable rather than silent.

---

# Definition of done

Every box above is `[x]` or `[~]` with its reason in the ledger. `go build ./... && go vet
./... && go test ./...` is green, and each of `feat-1` … `feat-6` tags a commit that was
green when it was made.

At most one widget is on screen at a time, sometimes none, and the one that is there never
moves, blinks, or vanishes across a turn that streams prose, runs a tool batch, and
collapses a reasoning trace. It sits on settled rows — never in the churning tail, never
beside a line of model text, never overlapping anything. The slot changes hands over a
long session, and a context meter approaching full takes it and keeps it.

Clicking a read, a write, or a bash row opens a quick view; Esc closes it and the `/diff`
panel is exactly as it was. A bash row names its command once. A `.md` path is underlined
only when the file is really there, and clicking it opens `glow` in a new terminal without
taking the keyboard away from the TUI.

`/review`, `/bugfix`, `/describe`, `/stats`, and `/login` all exist, appear in `/help`
under a real section, and do what their `Long` says. The Ollama Cloud key set by `/login`
survives a restart, and appears in no transcript, no notice, no session JSONL, and no
screenshot.

`evilcode update` fast-forwards from `origin`, builds, tests, and swaps the binary
atomically — and refuses, loudly and without losing anything, on a dirty tree, on a
non-fast-forward, or on a failing test.

The welcome screen's chips read as buttons and can be chosen with the keyboard. The eye
and the black hole both stay as idle art. `looks.md` exists and is a menu the user can pick from without
investigating anything first, and `lazy.md` names a jcode checkout that is still on disk.
