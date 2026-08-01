# plan5 — resident widget history

Rework the dock so widgets are permanent, anchored residents of the scrollback
instead of a single disappearing box. This plan is self-contained: every change
is listed with its file, what to delete, what to add, and how to verify. Follow
it in order. Do not improvise beyond it; where a judgment call is genuinely
needed the plan says so explicitly.

## 0. The spec (agreed with the user — do not renegotiate)

1. Widgets spawn in the **right margin**, in blank space, **near the bottom**
   of the settled content — never on rows owned by model prose
   (`BlockAssistant`) **or thinking bubbles (`BlockReasoning`)**, never inside
   the streaming tail or its guard band.
2. Spawn selection: existing salience ranking (urgency + airtime + change
   boost) picks **which** widget spawns. It never decides **when**.
3. **When** is decided by spacing alone: a new widget may spawn only if the
   previous spawn's anchor row is at least **one viewport height** above the
   candidate slot (in content rows). This guarantees ≤1 widget on screen when
   the user follows the tail. 0 widgets on screen is fine. Urgency never
   overrides spacing.
4. Spawns happen even while the user is scrolled up reading history — the
   spawn lands offscreen near the tail and they find it later.
5. Once spawned, a widget instance is **permanent**: it never changes kind,
   never moves, never re-homes, never expires. It rides its anchor, scrolls
   offscreen with the content, and **is still there when the user scrolls back
   up**. History of instances accumulates for the whole session.
6. Content is **always live**: every instance re-renders current data each
   frame, even old instances deep in scrollback. Duplicates of a kind may
   coexist, all showing "now".
7. If an instance's data source is empty (todos cleared, no bg tasks), it
   renders a small **empty-state stub** — the box never vanishes.
8. If rewrapped/resized content occupies an instance's margin space, the
   instance **hides in place** (anchor intact, not clickable) and reappears
   when space returns. It never relocates.
9. The **only** way an instance dies early: the user clicks it. Click kills
   **that instance only**, forever. The kind may respawn later under normal
   cadence. Spacing is still measured from the dismissed instance's anchor.
10. Widget history is **UI state only**: never part of agent/LLM context,
    never serialized. `/clear`, session switch, and restart drop it. Plain
    resize does NOT drop it.
11. All widgets spawn on the right. The left-side preference machinery is
    deleted.

## 1. Current code map (read these before editing)

- `internal/tui/dock.go` — `Dock` (single `resident *anchor`), `Layout`,
  `FreeWidth`, `fits`, `findSlot`, `anchorAt`, `screenRow`, `Forget`, `Reset`,
  `Hit`, constants (`SpawnCooldown`, `SettleMargin`, `SpawnLift`, …),
  `WidgetKind`, `Side`/`PreferredSide`, `Widget`, `Placement`.
- `internal/tui/app.go`:
  - `dockWidgets` (~line 3121) — per-frame entry point. Called from the frame
    builder (~line 2607) as
    `rows = m.dockWidgets(rows, res.Transcript, start, len(content), owner)`
    where `content` is the FULL rendered transcript (every line, not just the
    viewport), `owner` is the full per-line block provenance, and `start` is
    the scroll offset (index into `content` of the first visible row).
  - `activeWidgets` (~line 2958) — builds + salience-ranks the candidate list.
  - `widgetUrgency` (~line 3068), salience constants (~line 2943).
  - `hiddenWidgets` field (~line 139), init (~357), `clear` (~1346), filters
    (~2972, ~3178), set-on-click (~3423).
  - Click path (~3419): `m.dock.Hit(m.placements, …)` → `m.dock.Forget(kind)`.
  - `m.dock.Reset()` call sites: ~1166, 1189, 1198, 1208, 1336, 1348, 1926.
- `internal/tui/widgets.go` — per-kind renderers, `RenderWidget`.
- Tests: `dock_test.go`, `docksettled_test.go`, `dockpaint_test.go`.

Key insight for the rewrite: today `Layout` works in *viewport* coordinates
(sliced rows) and maps back through `scrollTop`. The rewrite moves everything
into **content coordinates** (index into the full `content`/`owner` slices,
where `owner[i]` is simply the owner of `content[i]` — no scrollTop mapping),
and converts to screen rows only at paint time. This is what makes offscreen
spawning and scrollback persistence natural.

## 2. dock.go rewrite

### 2.1 Delete

- `SpawnCooldown` constant and every use. Spacing replaces it.
- `Side`, `SideRight`, `SideLeft`, `PreferredSide`, the `Side` field on
  `anchor`, and the centered-fallback branch in the spawn loop.
- `ownerAt` (owner is now indexed directly).
- `Forget` (replaced by `Dismiss`).

### 2.2 New state

```go
// instance is one spawned widget riding the transcript. Permanent: it is
// removed only by a click, or by Reset when the transcript itself is replaced.
type instance struct {
	Kind WidgetKind

	// Block and Offset identify the content row the widget's top border rides.
	// Block is -1 for chrome rows (header/gaps), where Offset is the absolute
	// content row. Same semantics as the old anchor.
	Block  int
	Offset int
}

type Dock struct {
	// residents is every live instance, oldest first.
	residents []*instance

	// lastSpawn anchors the spacing rule. It is the most recent spawn and is
	// deliberately NOT cleared when that instance is dismissed by click —
	// dismissal must not open the door to an immediate respawn.
	lastSpawn *instance
}

func NewDock() *Dock { return &Dock{} }
```

### 2.3 Anchor resolution (content coordinates)

```go
// contentRow resolves an instance to its current row in the full content,
// or ok=false if its block no longer exists (compacted away).
func (a *instance) contentRow(owner []int) (int, bool) {
	if a.Block < 0 {
		return a.Offset, true
	}
	first := -1
	for i, b := range owner {
		if b == a.Block {
			first = i
			break
		}
	}
	if first < 0 {
		return 0, false
	}
	return first + a.Offset, true
}
```

`anchorAt` keeps its job but drops `scrollTop` (row IS the content row):

```go
func anchorAt(owner []int, row int) (int, int) {
	if owner == nil || row < 0 || row >= len(owner) || owner[row] < 0 {
		return -1, row
	}
	block := owner[row]
	first := row
	for first > 0 && owner[first-1] == block {
		first--
	}
	return block, row - first
}
```

### 2.4 Placement

`Placement` gains the instance index so clicks can name a specific instance:

```go
type Placement struct {
	Kind  WidgetKind
	Index int // index into Dock.residents
	Row   int // SCREEN row of the top border (may have been clipped from content space)
	Col   int
	Width, Height int
}
```

### 2.5 Layout — new signature and shape

```go
// Layout runs in content coordinates over the full transcript, then converts
// surviving placements to screen rows.
//
//   render      — every widget kind currently renderable, INCLUDING empty-state
//                 stubs for resident kinds whose data emptied (see §3.2)
//   candidates  — salience-ranked spawn candidates (non-empty, spawn-eligible)
//   content     — full rendered transcript lines
//   owner       — full provenance, owner[i] owns content[i]
//   viewH       — transcript viewport height (res.Transcript)
//   scrollTop   — content index of first visible row
func (d *Dock) Layout(render map[WidgetKind]Widget, candidates []Widget,
	content []string, owner []int, kindOf func(int) BlockKind,
	streamingBlock, totalWidth, scrollTop, viewH int) []Placement
```

Body, in order:

1. Bail (`return nil`) if `totalWidth <= WidgetMinWidth+WidgetGap`.
2. `free := FreeWidth(content, totalWidth)` — unchanged function, now fed the
   full content.
3. Compute `settledEnd` exactly as today but in content coordinates: if
   `streamingBlock >= 0`, find the first `i` with `owner[i] == streamingBlock`
   and set `settledEnd = i - SettleMargin`; if the streaming block owns no
   rows, `settledEnd = len(content)`. If nothing is streaming,
   `settledEnd = len(content) - SettleMargin`. Clamp to ≥0. When `owner` is
   nil (unit tests), skip the settled constraint entirely, as today.
4. `dockable(row, height)` — as today, plus `BlockReasoning`:

   ```go
   if o != -1 && (kindOf(o) == BlockAssistant || kindOf(o) == BlockReasoning) {
       return false
   }
   ```

5. **Residents pass.** For each `residents[i]`:
   - Resolve `contentRow`. If `!ok` (block compacted): remove the instance
     from the slice (this is transcript death, not a UI decision). Also nil
     out `lastSpawn` if it was this instance's anchor row source — simplest
     correct rule: if `lastSpawn` fails to resolve later in step 6, treat the
     spacing constraint as absent.
   - If `render` has no entry for its kind, skip painting this frame (should
     not happen once §3.2 stubs exist — but never delete the instance here).
   - `fits(free, row, h, w)` in content space. If it does not fit: **skip**
     (hidden in place — spec §8). Keep the instance.
   - Otherwise convert to screen: `screen := row - scrollTop`. If the box is
     entirely outside `[0, viewH)`, skip (offscreen, still alive). If
     partially visible, emit the placement with the real (possibly negative)
     `Row` — the painter clips (§4).
   - Emit `Placement{Kind, Index: i, Row: screen, …}`.
6. **Spawn pass** (runs every frame, after residents):
   - Compute the spacing floor: resolve `lastSpawn.contentRow(owner)`. If
     `lastSpawn == nil` or it fails to resolve, there is no constraint.
     Otherwise a candidate slot at content row `r` is legal only if
     `r - lastRow >= viewH`.
   - Walk `candidates` in order; for each, `findSlot` over the full content
     (unchanged logic: lowest settled pocket, `SpawnLift` clearance) but with
     the spacing floor folded into `usable`:

     ```go
     usable := func(row int) bool {
         return dockable(row, 1) && fits(free, row, 1, width) &&
             (noFloor || row-lastRow >= viewH)
     }
     ```

     (Extend `findSlot` to take the extra predicate, or wrap `dockable`.)
   - First candidate that lands: append
     `&instance{Kind, Block, Offset}` (via `anchorAt(owner, row)`) to
     `residents`, set `lastSpawn` to it, emit its placement (converted to
     screen coords the same way), and stop — at most one spawn per frame.
7. Return all placements.

### 2.6 Dismiss / Reset / Hit

```go
// Dismiss kills one instance forever. lastSpawn is untouched: dismissal must
// not license an immediate respawn.
func (d *Dock) Dismiss(index int) {
	if index >= 0 && index < len(d.residents) {
		d.residents = append(d.residents[:index], d.residents[index+1:]...)
	}
}

// Reset drops all history. Only for transcript replacement (/clear, session
// switch) — never for resize.
func (d *Dock) Reset() { d.residents, d.lastSpawn = nil, nil }
```

`Hit` returns `(Placement, bool)` (or keep `(WidgetKind, bool)` plus the
index — caller needs `Index`). Simplest: return the matched `Placement`.

## 3. app.go changes

### 3.1 dockWidgets

Change the call site (~2607) and signature to pass the full content:

```go
rows = m.dockWidgets(rows, content, res.Transcript, start, owner)
```

Inside:
- Build `widgets := m.activeWidgets()` as today (ranked, non-empty).
- Build `render := map[WidgetKind]Widget` from `widgets`.
- For every resident kind (expose `func (d *Dock) ResidentKinds() []WidgetKind`
  or iterate a small accessor) missing from `render`, insert the empty stub
  (§3.2).
- Build `candidates` = `widgets` minus spawn-ineligible kinds. Today the only
  ineligibility: `WidgetTodos` while `m.showTodoCard` is true. IMPORTANT
  BEHAVIOR CHANGE: the todo-card check moves out of `activeWidgets` (where it
  currently suppresses rendering entirely) into candidate filtering only — a
  *resident* todos widget keeps rendering while the card is open, because
  residents never disappear (spec §5). Same treatment for `hiddenWidgets`:
  delete that mechanism entirely (§3.3).
- Call the new `Layout`.
- Paint loop: unchanged in spirit, but drop the `m.hiddenWidgets[p.Kind]`
  filter, and pass clipping bounds (§4).
- `m.widgetLastShown[p.Kind] = m.widgetClock` — keep, but ONLY for placements
  that are actually visible on screen (screen row intersects `[0, viewH)`);
  an offscreen resident is not "being seen" and must keep accruing airtime…
  actually the reverse: airtime measures "not shown", and a permanently
  offscreen old instance should NOT suppress its kind's airtime forever.
  Since Layout only emits placements for visible boxes (step 5 skips fully
  offscreen ones), marking every emitted placement is already correct.

### 3.2 Empty-state stubs (widgets.go)

Add:

```go
// EmptyWidget is the stub a resident renders when its data source is empty.
// A resident never vanishes (plan5 §0.7), so it needs something to say.
func (r *Renderer) EmptyWidget(kind WidgetKind) Widget {
	dim := r.style(theme.RoleDim)
	text := map[WidgetKind]string{
		WidgetTodos:           "no todos",
		WidgetBackgroundTasks: "no tasks",
		WidgetMemoryActivity:  "idle",
		WidgetSwarmStatus:     "no agents",
		WidgetTips:            "·",
	}[kind]
	if text == "" {
		text = "·"
	}
	return Widget{Kind: kind, Lines: []string{dim.Render(text)}}
}
```

Note: a stub changes the widget's height vs its full render. That is fine —
`fits` re-checks each frame and the box hides if it stopped fitting; height
shrinking never breaks anything.

### 3.3 Delete hiddenWidgets

Remove the field (~139), its init (~357), the `clear` (~1346), both filters
(~2972, ~3178), and the assignment in the click handler (~3423).

### 3.4 Click handler (~3419)

```go
p, ok := m.dock.Hit(m.placements, mouse.X-pad, mouse.Y)
if ok {
	m.dock.Dismiss(p.Index)
}
```

Careful: `Hit` must ignore placements whose box is clipped (a partially
visible widget is clickable only on its visible cells — the existing
row/col bounds test already handles that if `Row` is the true screen row and
the caller only gets clicks inside the transcript region).

### 3.5 Reset call sites

Visit each: ~1166, 1189, 1198, 1208, 1336, 1348, 1926. Read the surrounding
code. Keep the `m.dock.Reset()` only where `m.blocks` is being replaced or
cleared (`/clear`, session switch/load). Delete it where the trigger is only
a width, alignment, or layout-mode change — resize must preserve history
(spec §10); hide-in-place absorbs any overlap a rewrap causes. If a site is
ambiguous, the rule is: does the block list survive? If yes, no Reset.

### 3.6 activeWidgets

- Remove the `!m.hiddenWidgets[w.Kind]` condition in `add`.
- Remove the `!m.showTodoCard` guard from the todos `add` (moved to candidate
  filtering, §3.1).
- Everything else (salience, airtime, change boost, sorting) stays.

## 4. paintWidget clipping

`paintWidget(rows, lines, row, col, limit)` currently assumes `row >= 0`.
Placements can now start above the viewport (negative screen row) or extend
past `limit`. Make it skip out-of-range rows:

```go
for i, line := range lines {
	r := row + i
	if r < 0 || r >= limit || r >= len(rows) {
		continue
	}
	// existing cut-and-write logic
}
```

## 5. Tests

Run: `go test ./internal/tui/`. Fix in this order.

### 5.1 Existing tests to update (dock_test.go)

Most call `Layout` with the old signature and synthetic rows + `owner=nil`.
Mechanical update: `Layout(render, candidates, rows, nil, kindOf, -1, width,
0, len(rows))` with `render` built from the candidate list. Semantic changes:

- `TestResidentRetiresOffTheTopAndAnotherSpawnsAfterAPause` — REWRITE.
  New semantics: a resident scrolled above the viewport is NOT retired; it
  produces no placement while offscreen and reappears when scrolled back.
  A second spawn happens only once a slot ≥ viewH below the first anchor
  exists. Rename accordingly (e.g. `TestOffscreenResidentSurvivesScroll`).
- `TestDockPlacesAtMostOneWidget` — keep the assertion but the mechanism is
  spacing: with content shorter than 2×viewH only one spawn can happen.
- `TestLeftWidgetsFallBackToTheRightMargin` — DELETE (Side machinery gone).
- `TestResidentIsNeverSwappedForAnotherWidget`,
  `TestDockSecondCandidateGetsNoSlotWhileFirstHolds`,
  `TestBlockedResidentHidesInPlaceRatherThanMoving`,
  `TestDockHoldsItsAnchorAcrossFrames`, `TestDockScrollsWithTheText` — keep
  intent, mechanical signature fixes.
- Any test referencing `SpawnCooldown` or `Forget` — update to the new API
  (`Dismiss` takes an index from the returned placement).

### 5.2 docksettled_test.go

Signature updates; owner slices are now indexed directly (drop scrollTop
mapping from expectations). Add:

- `TestReasoningRowsAreNotDockable` — mirror of
  `TestSettledRegionExcludesAssistantProse` with `BlockReasoning`.

### 5.3 New tests (add to dock_test.go)

1. `TestScrolledOffWidgetReappearsOnScrollUp` — spawn, scroll it above the
   viewport (no placement emitted), scroll back, same instance places at the
   same content row.
2. `TestSpawnSpacingIsOneViewport` — after a spawn at row R, no second spawn
   until a settled slot at ≥ R+viewH exists, even with a high-salience
   candidate offered.
3. `TestDismissKillsInstanceOnly` — spawn, Dismiss its index, instance gone;
   grow content by ≥ viewH; same kind spawns again.
4. `TestDismissDoesNotResetSpacing` — after Dismiss, no spawn until the
   spacing floor (measured from the dismissed instance's anchor) is cleared.
5. `TestSpawnLandsOffscreenWhileScrolledUp` — scrollTop=0 (user at top),
   content 3×viewH, spawn happens near the tail (no placement emitted this
   frame because it is offscreen); scrolling to the bottom shows it.
6. `TestEmptyDataRendersStub` — app-level: resident todos widget whose items
   empty renders the "no todos" stub, box still present.
7. `TestResizeKeepsResidents` — app-level if feasible, else Dock-level: after
   a width change (no Reset), residents still resolve and place.

### 5.4 Goldens / app-level tests

`grep -rn "dock\|widget" internal/tui/*_test.go` for app-level tests that
assert the old single-resident behavior (narrow_test.go, provenance_test.go,
quickview_click_test.go may touch placements). Update expectations, do not
weaken unrelated assertions.

## 6. Verification

```
go build ./...
go test ./internal/tui/
go vet ./internal/tui/
```

Then rebuild the installed binary (user requirement — `ec` symlinks to it):

```
go build -o ~/.local/bin/evilcode .
```

(Confirm the build entrypoint first: `ls cmd/` — if the binary builds from
`./cmd/evilcode`, use that path.)

Manual smoke (if running interactively): start a session, stream a few turns,
confirm: one widget appears low-right; keeps updating live; scrolls up with
its text; scrolling back down/up shows it in place; next widget appears only
about a screenful of new content later; clicking one removes just it; resize
does not wipe them.

## 7. Pitfalls

- **Do not** make widgets part of transcript blocks, agent context, or
  session persistence. They are paint-time overlay + Dock state only.
- **Do not** cap or GC `residents`. Instances are ~3 ints; a long session
  accrues a few hundred at most (spacing = 1 per viewport of content).
- `lastSpawn` survives Dismiss on purpose (spec §9). Do not "fix" that.
- The linear scans in `contentRow` (find block's first row) run per resident
  per frame. With ≤ a few hundred residents over a few thousand rows this is
  fine; do not add an index structure unless a profile demands it.
- `Deterministic()` mode freezes `widgetClock` — salience airtime/change
  boosts are already guarded; touch none of that.
- Offscreen residents must NOT write `widgetLastShown` (they aren't shown);
  Layout already only emits visible placements, so painting/marking the
  emitted list is correct as long as you keep that property.
- Watch `start` vs `scrollTop` naming at the call site: `start` is clamped
  and slack-adjusted (app.go ~2563). Pass exactly the value the visible slice
  was built from, or screen conversion will be off by the slack.
