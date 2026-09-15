# evilcode — the open-work plan

The single file of all active work. Everything checked off lives only in
`LOOPS.md` (flight recorder), `DEVIATIONS.md`, and git history — this file
carries no completed-work reporting. Rebuild the audit from those sources, not
from plans.

Work proceeds by the loop in `docs/planfiles.md`: pick the next unchecked
task → implement (fix tasks: **reproduce first, fail-then-pass**) → `go build
./... && go vet ./... && go test ./...` green (`-race` for daemon/web work) →
probe/visual check for UI tasks → check the box → one green commit per task →
append a `## <date> <task-id>` entry to `LOOPS.md` → keep `README.md` current
→ next task. Never stall: closest working thing → `DEVIATIONS.md`.

## Phase 1 — Land the in-flight working tree (do this first)

The tree holds a finished-looking, green fix pass (38 modified, 4 untracked
files): found_issues items 1–5 and 7; compaction summary carry-forward;
`[webui] trust_forwarded_headers`; `asks.test.mjs`. It is uncommitted and
unlogged — work at risk until it lands.

- [ ] **C1.1** Commit the tree in concern-sized commits, each green: (a)
  found_issues TUI fixes — images relayout (`images.go`, `graphics_test.go`),
  running tool rows + untruncated command (`app.go`, `transcript.go`,
  `bashrow_test.go`), bottom-gated overscroll (`scroll.go`), silent repairs
  (`events.go`, `provider.go`, `tools.go`, `runcmd/run.go`), composer info line
  (`composer.go`), sessioncmd/preview/bugfix tests; (b) compaction
  carry-forward of prior summaries (`agent/compact.go` + test); (c)
  `trust_forwarded_headers` webauth change (`config.go`, `web.go`,
  `webauth.go` + tests, README).
- [ ] **C1.2** Append one `LOOPS.md` entry per commit (what/verified/deviations)
  and add `DEVIATIONS.md` entries only where behavior intentionally diverges
  from an earlier spec claim (e.g. repairs were once "shown in the tool row";
  they are now metadata-only).
- [ ] **C1.3** Visual verify with the probe rig and **look at the PNGs**:
  running bash row with exact command and Esc hint, image blocks holding
  geometry through resize/scroll, bottom-only overscroll reveal, composer
  info line.
- [ ] **C1.4** Live token generation while streaming — the user's words:
  "when streaming, live token generation needs to be shown properly in the
  live panel, streaming the tokens in their place where the edit would go or
  whatever". Reproduce the misbehavior (streaming deltas / reasoning tail
  placement), fix, fail-then-pass, or mark `[~]` with the test that refuses
  to fail.
- [ ] **C1.5** Make `go test -race ./...` green under full-suite load: fix the
  `TestWebAndSocketClientsShareOneTurnStream` ordering sensitivity
  (`internal/daemon/webcmd_test.go:803` — "durable turns = [from the socket],
  want both inputs in one conversation"; passes 3× in isolation with `-race`,
  fails once under full-suite load). A race here is a product bug, a test
  race is a test fix — decide from the reproduction. This completes web P8.1.
- [ ] Verify Phase 1: full gates green incl. `-race`; probe PNGs inspected;
  LOOPS/README current; working tree clean. Tag `consolidate-1`.

## Phase 2 — web-8: verification and hardening (plan-web Phase 8)

- [ ] **P8.2** Malformed-input battery: every `/api/*` endpoint — bad JSON,
  wrong types, oversized bodies (>8 MiB), unknown kinds, session-name tricks,
  SSE under a stalled reader — uniform `{"error": ...}` shapes (extend
  `webapi_test.go`/`webcmd_test.go` patterns; table-driven).
- [ ] **P8.3** Backpressure round-trip test: slow SSE client → drop-close →
  reconnect with `since` → exact gap replay, no loss, `sess.pump` never
  stalled (extends `TestWebEventsSlowClientDoesNotStallTheSession`).
- [ ] **P8.4** README: document the daemon-crash limit (roster and live turns
  are in-memory and lost on a daemon crash) — the one specific doc gap the
  web plan still named — and confirm LOOPS/DEVIATIONS are current for
  everything web.
- [ ] Verify Phase 8 (web-8): browser-driven round-trips — start/attach from a
  browser; a turn with tool call, background task, and ask; disconnect all
  clients mid-work; reconnect with exact snapshot+gap replay; two browsers
  identical; reopen the durable session after daemon restart (model,
  workspace, transcript). Tag `web-8`.

## Phase 3 — Review-finding backlog (deduped; revalidate before each fix)

From the two Aug 26 review snapshots (deleted 2026-09-15; full text in git
history as `docs/codex_review.md` / `docs/codex_review2.md`). Fixed findings
already live in `LOOPS.md`; only the open ones are here. Order: High first,
then Medium, then Low. Every item starts with the fix loop's reproduction
step; if later work already closed an item, mark `[~]` in `LOOPS.md` with the
test that refuses to fail. review1 IDs are in `()` where review2 restates the
same finding — one fix closes both.

Provider/model surface:

- [ ] **R2-18 (A8)** Generic OpenAI-compatible endpoints assumed to support
  `reasoning_effort` — gate the parameter per provider capability.
- [ ] **R2-19 (B9/E5/E6)** Model-catalog failures swallowed; opening the picker
  can mutate effort state; picker identity sometimes only a model name.
- [ ] **R2-20 (A9)** `/config` advertised but unimplemented — implement or stop
  advertising it.
- [ ] **R2-22 (B8)** Codex OAuth coordination is per provider instance, not per
  auth file/account.
- [ ] **R2-38** Several small API contracts misleading or broken (enumerate at
  fix time).

Agent loop and automation:

- [ ] **C4** Duplicate tool names accepted first-wins: validate the assembled
  `tools.Set` once at wiring and reject duplicates (the MCP catalog-side
  refusal was already closed; this is the agent-side set).
- [ ] **C6** Compactor background work has no lifecycle join.
- [ ] **C7** Hidden prompts represented inconsistently (low impact, verified).
- [ ] **C8** Turn reservation released before all end-of-turn work publishes.
- [ ] **R2-30 (E7)** Hidden automation has a one-slot overwrite queue.
- [ ] **R2-31 (I3)** Todo status can contradict dependencies.
- [ ] **R2-32 (I4)** Overnight stall detection compares a lossy summary string.
- [ ] **R2-29 (E4)** Productivity statistics materially incorrect.
- [ ] **E3** `submit` ignores all non-busy `Agent.Run` errors.
- [ ] **R2-33 (D8)** `/fork` forks conversation storage only; swarm/UI state
  left behind.
- [ ] **I5** Memory enablement and semantic readiness conflated.
- [ ] **D4** `/connect brave` updates only the initiating live session.
- [ ] **D5** Credential changes skip busy sessions (check what R2-12 already
  fixed before planning).

Tools and filesystem:

- [ ] **R2-23 (F11)** Partial `multiedit` failure returned as a successful tool
  call.
- [ ] **R2-24 (F8)** 16-bit line anchors collide; paged reads invalidate prior
  anchors.
- [ ] **R2-25 (F9)** `glob` walks and sorts everything before applying `limit`.
- [ ] **R2-26 (F10)** Image input trusts filename extensions over content.
- [ ] **R2-27 (F12)** Shell cwd tracking spoofable by command output.
- [ ] **F6** Atomic writes do not fsync the containing directory.
- [ ] **F7** Confined/unconfined writes have inconsistent symlink semantics.
- [ ] **F14** Destructive-command classification presented too strongly.
- [ ] **E12** `boundedBuffer.Write` violates the `io.Writer` contract.
- [ ] **E14** Graphics override accepts arbitrary protocols silently (low).

Integrations and persistence:

- [ ] **R2-36 (G4)** LSP multi-file rename not crash-atomic despite API claim.
- [ ] **G5** LSP `documentChanges` ignores resource operations; URI parsing
  incomplete.
- [ ] **R2-37 (G6)** Brave hardcodes global 1 rps, no 429 backoff/adaptation.
- [ ] **G7** Cloud-usage failures can render an empty widget; response limit
  not enforced.
- [ ] **R2-34 (H2/H3/H4)** Imported sessions: never refresh, flatten executable
  structure, ambiguous substring ID resolution.
- [ ] **R2-35 (H5/H6/H7/H10)** Persistence semantics: per-record fsync,
  cross-process writer lock, blob GC, directory durability across update
  paths.
- [ ] **H9** Installer reinstall/remove does not coordinate with the daemon.
- [ ] **E11 (R2-28)** Mermaid cache can return a wrong or partial image.

Quality and maintainability (after correctness; one extraction at a time with
behavior tests around each):

- [ ] **R2-39 (J2)** Integration coverage for command/wiring/attach paths (MCP
  is covered; the rest are not).
- [ ] **R2-42** Finish extracting state machines from `internal/tui/app.go`
  (panel extraction already logged as a follow-up).
- [ ] **R2-43** Decompose `internal/daemon/server.go` synchronization
  responsibilities.
- [ ] **R2-44** One canonical owner for runtime state instead of duplicates.
- [ ] **R2-45 (J3)** Trim comments that promise more than the code supplies.
- [ ] **E8 (standalone-mode fate)** Decide standalone TUI wiring's fate and
  delete or wire it.
- [ ] **J4** Corral generated/demo provider fixtures out of search paths.

## Phase 4 — Live-behavior checks that only a real backend can answer

- [ ] **C4.1** Exercise the ollama-cloud 429 retry path in practice (from the
  nebula-4 postmortem, deleted 2026-09-15; the code path is verified, real
  back-to-back large prefills are not).

# Excluded — not scheduled in this plan

- **User review:** plan-web's Verify Phase 7 device pass (real iPhone on the
  tailnet: A2HS install flow, notch safe-area insets, real iOS keyboard,
  locked-phone gap-replay timing). All mechanical pieces are already
  browser-verified; this waits on the user's device.
- **v2 deferred (plan-web §11, "do not build in v1"):** Web Push for pending
  asks; todo cards, memory widget, history search; session deletion and
  first-class worker-kill/rename endpoints; multi-user auth, HTTPS
  termination, bind-anywhere hardening; ANSI-rich tool output → images;
  multiplexed `/api/stream` if roster polling proves janky.

# Gotchas

- The full `-race` suite is the gate for every daemon/web change; a
  load-sensitive failure is a finding, not noise.
- Review findings are two snapshots of the same code: always fix once at the
  root with both IDs, and re-run the review's own verification before editing.
- A golden frame proves only that output did not change; look at the PNG.
- Uncommitted work is work at risk: Phase 1 exists so this plan never has to
  guess what the tree contained.

# Definition of done

The working tree is committed and clean with `go build ./... && go vet ./... &&
go test ./...` and `go test -race ./...` green; every item above is fixed with
a fail-then-pass pair or marked `[~]` with the test that refused to fail;
LOOPS.md, DEVIATIONS.md, and README.md are current; nothing here waits
silently on the user except the explicitly excluded device pass.