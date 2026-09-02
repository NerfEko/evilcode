# evilcode web — a local web UI for the daemon: roster, chat, and an iPhone webapp

A browser frontend for `evilcode serve`: view and manage the daemon's agents from a
desktop browser or an installed iPhone webapp. The daemon gains an opt-in HTTP
surface next to its unix socket; the browser becomes a fourth consumer of the
agent event stream (after the TUI, `run`, and `serve`/attach clients) with no new
state ownership. Layout is hybrid — sidebar fleet + wide chat on desktop, roster
as the phone's home screen with chat drill-in. Theme is the daemon's own palette
(catppuccin-frappé default), served as CSS tokens generated from the Go palette
so the web can never drift from the TUI.

Scope v1: chat + roster at full functionality, plus deep transcript history read
from the durable session store. Explicitly deferred (§11): Web Push, todo/memory
surfaces, history search, session deletion, multi-user.

## Grilled decisions (locked 2025, this session)

| # | Decision |
|---|---|
| 1 | Transport is Tailscale; the daemon stays loopback-bound. Plain HTTP is acceptable — WireGuard encrypts the tunnel. No HTTPS termination in the daemon, ever, in v1. |
| 2 | The web token persists in a 0600 file next to the socket and survives daemon restarts, including self-update restarts. |
| 3 | Pending asks notify by in-app badge only in v1. Web Push (SW + VAPID + push crypto) is v2. |
| 4 | v1 surfaces are chat + roster only. No todo cards, memory widget, or history search. |
| 5 | Deep history IS in v1: a paginated reader over the durable session store. |
| 6 | New-session workspaces come from a configured `[webui] workspaces` list; no server-side directory browsing. |
| 7 | Hybrid layout. Phone home screen = roster (assumption stated in-session; chat drills in). |
| 8 | Hand-rolled vanilla JS. Swapping to one vendored Preact ESM file is pre-approved (§8.5) — still no build step, no npm. |
| 9 | An open SSE subscription counts as a client for the idle watchdog; phone-first setups are documented toward `serve -idle 0`. |
| 10 | Phone-allowed destructive op: interrupt only. Worker-stop, rename, delete are desktop-breakpoint only. No delete endpoint exists or is added in v1. |

# PART 0 — Process: how the building agent works

## 0.1 Context

The daemon (`internal/daemon`) already hosts N sessions in one process. The event
stream (`internal/agent/events.go`) is the single contract between the agent core
and every frontend; the daemon fans it out via `Session.pump` → `ring` +
`sess.subs`. Everything a browser needs is already a method on `*Server` or
`*Session` (see §4). The web layer is therefore an adapter, not new machinery:
it lives inside `internal/daemon` (like `hub.go` and `registry.go` do) and calls
the same functions the unix-socket protocol handlers in `Server.handle` call.

## 0.2 The loop (repeat until plan complete)

1. Pick the next unchecked `[ ]` task in the current phase.
2. Implement. Reuse existing packages before writing new.
3. `go build ./... && go vet ./... && go test ./...` green. Daemon/shared-state
   changes also require `go test -race ./...` — all of Phase 2 and 3 qualify.
4. Visible check, by task kind:
   - API tasks: `httptest` round-trip against a real `Server` (the daemon tests
     already build servers in-process; follow `shared_test.go` helpers).
   - UI tasks: render in a browser at both breakpoints (desktop ≥900px, phone
     <900px) and look at a screenshot. For 4D/5 tasks, on a real iPhone.
   - Daemon-backed tasks: drive the real server plus at least one attach client
     (TUI) and compare logical events, per `docs/planfiles.md` server/client loop.
5. Mark `[x]` in this file. Commit — one task per commit, always green.
6. Kick off a background codex review of the commit; fold findings into the next
   iteration; log dismissed findings with a reason in `LOOPS.md`.
7. Append one entry to `LOOPS.md`: `## <date> <task-id>` — what was done, how
   verified (test names / screenshots), codex verdict, deviations.
8. Keep `README.md` current in the same commit when behavior changes.
9. Impossible as specced → closest working thing → `DEVIATIONS.md` → next task.

**Server/client loop invariant** (every phase that touches state): the window is
not the owner of live work. Disconnecting every browser mid-turn must leave the
daemon working; a reconnecting browser must get snapshot + gap replay by `since`;
two browsers plus one TUI on one session must see identical logical events.

# PART I — Context, constraints, architecture

## §1 Ownership boundary

**The daemon owns** (in-process, behind `s.mu`/`sess.mu`): sessions and their
agents, turn serialization, the ask broker, the event ring, swarm state
(`hub.go`), background tasks, the file-touch registry.

**Durable state**: session JSONL logs (`internal/session`), config, and the web
token file (§3). Nothing else on the web path writes to disk.

**Per-client display state** (deliberately browser-side): transcript mirror,
expand/collapse state, selected session, scroll position, per-session
last-seen-seq for reconnect, roster poll cache.

**TUI-disconnect guarantee**: closing the tab is a disconnect, not a shutdown.
**Daemon-crash limit**: everything in memory (roster, live turns) is lost on a
daemon crash; the token survives (file), so the UI reconnects to an empty roster
once the daemon is back. This limit is documented in README, not fixed in v1.

## §2 Placement and lifecycle

- New file `internal/daemon/web.go` (plus small siblings `webauth.go`,
  `webtheme.go` if they grow): the HTTP server, handlers, and auth. Lives in
  package `daemon` for the same reason `hub.go` does — it needs the private
  session surface (`subscribe`, `snapshot`, `deliver`).
- New dir `internal/daemon/webassets/`: static assets, embedded with
  `go:embed` (first embed in the repo — no build step, no npm).
- `Server.ListenWeb(addr string) error` binds HTTP; `Server.Close()` also closes
  it. The web listener and the unix socket are independent: web failing to bind
  must not prevent socket-only operation, and vice versa.
- The HTTP handlers must never hold `s.mu` or `sess.mu` across client I/O.
  Streaming writers take a snapshot of what they need, then write.
- Off by default. Enabled by config `[webui]` or `serve -web` (§10).

## §3 Security model

The socket is 0600 + peer-credential-checked because anything that can connect
runs commands as this user. HTTP over loopback is connectable by any local
process, so the web surface carries a token and an origin discipline. The
blessed remote path is Tailscale (loopback + tailnet = WireGuard-encrypted);
the daemon never binds a LAN interface as the blessed flow.

| Control | Spec |
|---|---|
| Bind | `127.0.0.1:7749` default; `[webui] addr` or `-web-addr` to change. Non-loopback binds are allowed but README marks them unsupported-without-HTTPS. |
| Token | 32 bytes from `crypto/rand`, hex. Stored at `<socket>.web-token` (e.g. `$XDG_RUNTIME_DIR/evilcode.sock.web-token`), written 0600 and re-chmod'd on load. Created on first web start; reused forever; delete the file to rotate. |
| Handoff | `GET /?token=<hex>` exchanges the token for a cookie (`HttpOnly; SameSite=Strict; Path=/; Max-Age=1y`) and 302s to `/` stripping the token. The token never appears in HTML, links, or logs; the full tokenized URL is printed once, at first mint. |
| Requests | All `/api/*` require the cookie or `Authorization: Bearer <token>`. Wrong/missing → 401. |
| CSRF/rebinding | Every mutating verb (POST) requires `Origin` (or `Referer`) host == `Host`. `Host` must match the configured addr or `localhost`/`127.0.0.1` — rejects DNS-rebinding names. Failure → 403. |
| Lifecycle | No daemon-stop, no session-delete, no worker-kill endpoint exists in v1. Stop a turn with interrupt; stop the daemon with `evilcode serve -stop`. |
| Headers | `Content-Security-Policy: default-src 'none'; script-src 'self'; style-src 'self'; img-src 'self' data: blob:; connect-src 'self'; manifest-src 'self'; base-uri 'none'; frame-ancestors 'none'`, `X-Content-Type-Options: nosniff`, `Referrer-Policy: no-referrer`. |
| Compression | gzip for JSON GET responses only. SSE is never buffered or compressed. |

## §4 HTTP surface

Handlers call the same code the unix protocol does (`Server.handle` in
`internal/daemon/server.go`); nothing re-implements semantics.

| Method & path | Maps to | Notes |
|---|---|---|
| `GET /` | shell `index.html` | template-injected `theme-color` + manifest link |
| `GET /assets/*` | embedded files | `Cache-Control: no-store` in v1 |
| `GET /theme.css` | `renderThemeCSS(theme.ByName(cfg.Display.Theme))` | §7 |
| `GET /manifest.webmanifest` | template | colors from the same palette |
| `GET /api/status` | `Server.Status()` | header widget |
| `GET /api/sessions` | `Server.Sessions()` | roster |
| `GET /api/sessions/{name}` | live: `Session.snapshot()`; stored: metadata + store history | a stored session is viewed read-only; no agent boots |
| `GET /api/sessions/{name}/events?since=N` | `sess.subscribe()` + `ring.Since(N)` | SSE, §5 |
| `GET /api/sessions/{name}/messages?before=B&limit=L` | durable store reader | §6 |
| `POST /api/sessions/{name}/input` | `Session.InputRequestHidden` | `{text, images?, hidden?, request_id?}`; images are base64 → `[][]byte` |
| `POST /api/sessions/{name}/interrupt` | `Session.Interrupt(text, urgent)` | `{text?, urgent?}` |
| `POST /api/sessions/{name}/answer` | `asks.Answer(requestID, answers)` | from `Snapshot.Pending` |
| `POST /api/sessions/{name}/model` | `Session.SetModelWithEffort` | `{model, effort?}` |
| `POST /api/sessions/{name}/effort` | `Session.SetReasoningEffort` | `{effort}`; capabilities from `Snapshot.ReasoningEfforts` |
| `POST /api/sessions/{name}/command` | `Session.Command(text, arg, secret)` | slash commands, incl. `secret` (password field) |
| `POST /api/sessions/{name}/message` | `Server.deliver(name, text)` | poke a busy agent |
| `POST /api/sessions` | `Server.OpenWithOptions` | create/reopen; `cwd` must be in `[webui] workspaces` (or daemon cwd when the list is empty); name empty → `session.PickFreeName` |
| `POST /api/spawn` | `Server.SpawnFor(spawner, task, files, schema)` | attributed to the named spawner |
| `GET /api/models` | aggregate `provider.Models(ctx)` over configured providers | cached ~5 min; includes `ContextWindow`, efforts, favorites marked |
| `GET /api/workspaces` | `[webui] workspaces` | drives the new-session picker |

Errors are uniform: `{"error": "..."}` with 400 (malformed), 401 (auth), 403
(origin/host), 404 (unknown session — `session.ValidName` first, then lookup),
409 (turn conflict surfaced by the daemon, rare). Daemon errors are user-error
4xx; a bare 500 is a bug.

## §5 Live events (SSE)

- One EventSource per tab, for the open transcript. The roster does **not**
  stream — iOS caps ~6 HTTP/1.1 connections per origin; the roster polls
  `GET /api/sessions` every 2 s while `document.visibilityState === 'visible'`,
  and immediately on `focus`/`visibilitychange` and on any stream frame.
- Response: `Content-Type: text/event-stream`, flushed per frame, heartbeat
  comment `: ping` every 15 s.
- Frame `id:` is the event's ring sequence; `data:` reuses the wire shapes the
  daemon already speaks: `{"kind":"snapshot",...}` and
  `{"kind":"event","event":{...}}` (same JSON as `ServerMsg`).
- Connect sequence mirrors `MsgAttach`: subscribe → send snapshot (always — it
  is self-healing) → replay `ring.Since(lastSeen)` from the `since` query param
  or `Last-Event-ID` → live tail. An epoch change mid-stream sends a fresh
  snapshot frame; the client resets its mirror (same rule the TUI follows).
- Disconnect: the request context ends → `unsubscribe`. The subscription's
  presence in `sess.subs` is what makes the stream count toward
  `SessionInfo.Clients` and the idle watchdog (decision 9).
- **Slow-client rule**: a web client must never stall `sess.pump`. Verify
  `Session.broadcast`'s non-blocking behavior when wiring this; if any send can
  block, web subscribers get a bounded queue with drop-close (the client
  reconnects with its last seq and the daemon replays the gap). Bounded also
  applies per-stream: the SSE writer drains from a channel with a cap; a client
  that cannot keep up is disconnected, not buffered without bound.

## §6 Deep history (durable store reader)

- `GET /api/sessions/{name}/messages?before=B&limit=L`: `B` is an exclusive
  upper bound (0-based, oldest message = 0) into the session's conversation
  list; `L` defaults 50, max 200. Response: `{messages: [...], hasMore, oldest}`.
- The reader reuses the resume-path loader over `internal/session` JSONL
  (`session.Store`, `session.Dir`); it must not grow a second parser.
  `session.ValidName` guards the `{name}` path segment.
- Concurrent-safety: reading a log the daemon is appending to (and that compact
  or rewind rewrites by rename) must tolerate a torn/short read with one retry;
  never hold `sess.mu` during the read.
- History carries no image bytes — same rule as `Snapshot.Messages`
  (`MaxServerFrameBytes` rationale in `protocol.go`); the UI renders an
  "image omitted from history" placeholder.
- The seam: the live transcript = snapshot window + older pages from the store.
  The UI merges on `before`-boundaries; a compact/rewind (epoch bump) invalidates
  the seam and the client resyncs from a fresh snapshot.

## §7 Theme — one source of truth, zero drift

- `renderThemeCSS(p *theme.Palette) string` iterates `theme.AllRoles()`
  (`internal/theme/roles.go`) and emits `--<role-kebab>: #hex;` for each, plus
  the `Prose` palette as `--prose-*`, plus the `TintDiff` green/red pair as
  `--diff-add`/`--diff-del`. Default is `CatppuccinFrappe()` (base `#303446`,
  text `#c6d0f5`, mauve `#ca9ee6`, lavender `#babbf1`).
- `app.css` uses **only** those variables. There are no hex literals in any
  authored stylesheet outside the generator. If the user changes the daemon
  palette in config, the web follows the TUI.
- `theme-color` meta and manifest colors are rendered server-side from the same
  palette (index.html/manifest are tiny templates), so there is no second copy
  of the published spec anywhere — the repo already carries a redundant-copy
  test to prevent palette drift; this plan adds none.

## §8 Frontend architecture

- Files under `internal/daemon/webassets/`: `index.html`, `css/app.css`,
  `js/app.js` (ES-module entry), `js/api.js` (fetch wrappers), `js/sse.js`
  (EventSource + reconnect), `js/mirror.js` (the reducer), `js/views/*`
  (roster, chat, sheets), `manifest.webmanifest`, `icons/` (192/512 +
  apple-touch 180). Vanilla ES modules; no build step; vendored third-party
  files are single-file ESM, reviewed at vendoring time.
- **Mirror reducer** (`js/mirror.js`): the same conversation mirror the TUI
  keeps. State: message list, per-message streaming buffers, tool calls keyed by
  call ID, asks keyed by request ID, background tasks keyed by ID, latest usage
  per turn, `running`, `epoch`, `seq`. Actions: `SNAPSHOT` (reset),
  `EVENT` (all `agent.Event` kinds from `internal/agent/events.go`:
  `text_delta`, `reasoning_delta`, `tool_start`, `tool_result`, `token_usage`,
  `notice`, `ask`, `ask_resolved`, `background`, `turn_start/end`, `error`,
  `model`, `reasoning_effort`), `HISTORY_PAGE` (prepend, seam merge),
  `EPOCH_BUMP` (reset from fresh snapshot). Reducer is pure and unit-tested.
- Streaming: deltas coalesced on a rAF batch (flush ≤33 ms) so long turns stay
  smooth; caret pulse while deltas flow.
- Virtualization: `content-visibility: auto` + `contain-intrinsic-size` on
  message cards; DOM pruning of far-above-viewport nodes beyond ~400 cards.
  Autoscroll sticks to bottom unless the user scrolled up (>48 px from bottom),
  with a "↓ new activity" pill.
- Images: live `Event.Images`/`Message.Images` → object URLs (revoked on prune);
  upload via paste or file picker; client pre-checks ~6 MiB total per send
  (the daemon's 8 MiB frame limit minus base64 overhead) and surfaces the
  daemon's refusal verbatim when it fires anyway.
- Markdown: one vendored single-file renderer + sanitizer (e.g. marked +
  DOMPurify as vendored ESM), rendering to sanitized DOM — no inline script can
  survive CSP anyway. Diffs colorize line-by-line using `--diff-add`/`--diff-del`.
- **Preact escape hatch** (decision 8): if the reducer produces race/regression
  bugs faster than fixes during Phase 5, or transcript state passes ~600 LOC,
  swap the view layer to one vendored Preact+htm ESM file. Pre-approved; still
  no build step.
- Routing: `#/s/<name>` for a session; roster at `#/`; last-open session kept
  in `localStorage` (with the token cookie, this survives standalone restarts).

## §9 Layout and mobile

- Breakpoint 900 px.
- **Desktop (≥900 px)**: left sidebar 288 px (daemon status line: sessions /
  running / clients; roster with status dots, worker/task lines, pending-ask
  badges; New-session button; workspace picker) · main chat column centered at
  ~46 rem max width · right rail 260 px at ≥1280 px (context meter
  CtxUsed/CtxMax + cache rate, background tasks, MCP status, skills).
- **Phone (<900 px)**: the roster is the home screen. Tapping a session drills
  into a full-screen chat (back returns to the roster). Sheets from a bottom
  bar: model/effort, slash command, spawn, menu. Pending asks render as
  full-width cards with option buttons; an unanswered ask pulses the roster
  badge. Composer is sticky above `env(safe-area-inset-bottom)`.
- Viewport: `viewport-fit=cover`, `100dvh` layout (with
  `-webkit-fill-available` fallback), `interactive-widget=resizes-content`,
  `visualViewport` resize listener moves the composer above the iOS keyboard.
  Never fight the keyboard with `position: fixed`.
- Touch: minimum 44 px targets everywhere; `@media (hover: none)` fallbacks so
  no affordance is hover-only; momentum scrolling; no long-press gestures in v1.
- PWA: manifest (`display: standalone`, Frappé `theme_color`/`background_color`
  from the palette), `apple-mobile-web-app-capable`, `status-bar-style:
  black-translucent`, `apple-touch-icon`. Add-to-Home-Screen yields a real
  standalone app. No service worker in v1 (that is the Web Push v2 door).
- Wake Lock while a turn is running, when the API is available — note: plain
  HTTP over a Tailscale IP is not a secure context, so Wake Lock may be
  unavailable; the code degrades silently (decision 3's "badge-only" stands).

## §10 Configuration

```toml
[webui]                # NOT [web] — that table holds web-capability credentials
enabled = false        # serve -web overrides to true
addr = "127.0.0.1:7749"
workspaces = []        # cwd menu for POST /api/sessions; empty → daemon cwd only
```

Flags on `evilcode serve` (in `internal/servecmd/serve.go`): `-web` (enable),
`-web-addr` (override). Startup prints one line:
`evilcode web: http://127.0.0.1:7749 (token: <path>)` — and the full tokenized
URL once, only when the token file was first minted.

Idle interplay (decision 9): an open SSE subscription counts as a client. A
browser sitting on the roster with no session open holds no subscription; the
daemon may idle-exit under it. README documents `serve -idle 0` for phone-first
setups.

## §11 Deferred ledger (v2, do not build in v1)

- **Web Push** for pending asks: service worker + VAPID keys + a Go web-push
  dependency (or hand-rolled push crypto). The single strongest v2 candidate.
- Todo cards, memory widget, history search (each needs new daemon surface).
- Session deletion; worker-kill and rename as first-class endpoints (rename
  exists via the `/rename` slash command; deletion has no endpoint yet).
- Multi-user auth; HTTPS termination; bind-anywhere hardening.
- ANSI-rich tool output via `internal/ansirender` → images.
- Multiplexed `/api/stream` (one SSE for roster + session) if roster polling
  proves janky in practice.

# PART II — Phases

## Phase 1 — HTTP server skeleton, auth, static shell

- [x] **P1.1** `internal/config/config.go` `[webui]` — add `WebUIConfig`
  (`enabled`, `addr`, `workspaces`), validation (addr parses; loopback default),
  `Config.Clone` copy — with `config_test.go` cases.
- [x] **P1.2** `internal/servecmd/serve.go` — `-web`, `-web-addr` flags; wire
  into `Server.ListenWeb`; status line in `-status` output.
- [x] **P1.3** `internal/daemon/web.go` `listenWeb` — listener lifecycle,
  mux, shutdown inside `Server.Close()`, independence from the unix socket
  (either can fail without taking the other down).
- [x] **P1.4** `internal/daemon/webauth.go` — token file mint/load (0600,
  `crypto/rand`, chmod-on-load), cookie/bearer check, Origin+Host check on
  mutating verbs.
- [x] **P1.5** `internal/daemon/webassets/` — `index.html` shell + manifest +
  icons, `go:embed`, asset handler, security headers (§3), template injection
  of palette-derived `theme-color`.
- [x] **P1.6** `internal/daemon/webtheme.go` — `renderThemeCSS` over
  `AllRoles()` + `Prose` + `TintDiff`; test asserts every role is emitted and
  the Frappé defaults come out byte-identical to the palette spec.
- [x] **P1.7** `internal/daemon/web.go` — startup print line; token provenance
  (minted vs reused) logged once.
- [x] Verify Phase 1: httptest — no token → 401; wrong token → 401; cross-origin
  POST → 403; rebinding Host → 403; token survives a daemon restart; token file
  is 0600; socket-only start works with web unconfigured and with web refusing
  to bind. `go test -race ./internal/daemon/... ./internal/config/...`.
  Tag `web-1`.

## Phase 2 — Read-only surface: roster, snapshot, SSE, deep history

- [x] **P2.1** `GET /api/status` → `Server.Status()` (+ test).
- [x] **P2.2** `GET /api/sessions` → `Server.Sessions()` (+ test).
- [x] **P2.3** `GET /api/sessions/{name}` — live snapshot vs stored
  metadata+history; `session.ValidName` first (+ tests for both branches and
  404s).
- [x] **P2.4** SSE endpoint — subscribe → snapshot → `ring.Since` replay → tail;
  `Last-Event-ID`/`since`; heartbeat; drop-close on overflow; unsubscribe on
  context done (+ tests incl. the slow-client rule from §5).
- [x] **P2.5** durable history reader — resume-path loader reuse, append/compact
  tolerance, no `sess.mu` across reads (+ tests incl. a torn-read retry).
- [x] **P2.6** `GET .../messages` endpoint (+ pagination tests, `limit` cap).
- [x] **P2.7** idle-watchdog accounting — an open web subscription keeps the
  daemon alive; a closed one does not (+ test following the existing watchdog
  harness).
- [x] Verify Phase 2: TUI attach + two browsers on one session see identical
  logical events; kill the tab mid-turn → work continues → reconnect with
  `since` replays exactly the gap; deep-history scroll to message 0 on a
  session longer than the snapshot window; a stored (not live) session renders
  read-only without booting an agent; roster polling pauses when hidden.
  `go test -race ./...`. Tag `web-2`.

## Phase 3 — Command surface

- [x] **P3.1** `POST .../input` — text, base64 images, `hidden`, `request_id`
  (+ test with an image-bearing turn).
- [x] **P3.2** `POST .../interrupt` (soft/urgent) and `POST .../answer`
  (+ tests: interrupt cancels; answer unblocks a waiting ask).
- [x] **P3.3** `POST .../model` and `POST .../effort` (+ tests).
- [x] **P3.4** `POST .../command` — slash commands with `arg` and `secret`
  (+ test; secret never logged).
- [x] **P3.5** `POST .../message` → `deliver` (+ test: lands at next safe point).
- [x] **P3.6** `POST /api/sessions` — create/reopen with workspace allowlist;
  name generation via `session.PickFreeName` (+ tests incl. rejected cwd).
- [x] **P3.7** `POST /api/spawn` — attribution, files, schema passthrough
  (+ test).
- [x] **P3.8** `GET /api/models` — provider aggregation, 5-min cache, config
  overrides, favorites (+ test with `Mock.Models`).
- [x] **P3.9** error-mapping middleware + malformed-body/battery tests (bad
  JSON, wrong types, >8 MiB body, path tricks in `{name}`).
- [x] Verify Phase 3: scripted round-trip per verb; TUI and browser sending to
  one session concurrently (one turn, no double-start); ask answered from HTTP
  unblocks the agent; spawn result returns to the spawner; image renders in the
  live stream. `go test -race ./...`. Tag `web-3`.

## Phase 4 — Design system and app shell

- [ ] **P4.1** `css/app.css` on tokens only — cards, chips, callouts, meters,
  buttons, sheets; dark-only Frappé.
- [ ] **P4.2** desktop shell: sidebar + chat + right rail (≥1280 px), with the
  status line wired to `/api/status`.
- [ ] **P4.3** phone shell: roster home, drill-in chat, back behavior, bottom
  sheets; `@media (hover: none)` fallbacks.
- [ ] **P4.4** routing + roster poll loop (`visibilitychange`-gated) + last-open
  persistence.
- [ ] **P4.5** guard test — grep the authored CSS for hex literals; only the
  theme generator may contain them.
- [ ] Verify Phase 4: screenshots at both breakpoints against the palette
  (look at them); roster reflects `SessionInfo` fields (running, worker, task,
  pending, stale, crashed). Tag `web-4`.

## Phase 5 — Transcript engine

- [ ] **P5.1** `js/mirror.js` reducer + unit tests covering every event kind and
  epoch resync.
- [ ] **P5.2** renderers: markdown (vendored + sanitized), reasoning collapse,
  tool cards with args/output/diff, notices by level, per-turn usage meter,
  image placeholders in history.
- [ ] **P5.3** streaming polish: rAF coalescing, caret, autoscroll stickiness +
  "↓ new activity" pill.
- [ ] **P5.4** history seam: infinite scroll-up, `before`-merge, epoch invalidates
  the seam.
- [ ] Verify Phase 5: drive a real turn with tool calls + a conflict notice + a
  background task + a compact (epoch) mid-turn; compare the rendered transcript
  against the TUI's rendering of the same scenario; screenshots. Tag `web-5`.

## Phase 6 — Interaction surface

- [ ] **P6.1** composer: auto-grow textarea, paste/file image attach with 6 MiB
  pre-check, send/stop states tied to `running`.
- [ ] **P6.2** interrupt affordance (soft; urgent behind a confirm).
- [ ] **P6.3** ask cards: options as buttons, resolved state, multi-ask support.
- [ ] **P6.4** model/effort pickers from `/api/models` + `Snapshot.ReasoningEfforts`.
- [ ] **P6.5** command row: slash palette, `arg` field, `secret` password field.
- [ ] **P6.6** spawn dialog: workspace picker, files, schema JSON textarea.
- [ ] **P6.7** poke/message affordance while busy; new-session + reopen-stored
  flows.
- [ ] Verify Phase 6: the full chat+roster capability matrix — every row gets a
  scripted HTTP round-trip plus a manual click-through on both breakpoints.
  Tag `web-6`.

## Phase 7 — Mobile and PWA

- [ ] **P7.1** manifest, icons, standalone metas, install hint when not
  standalone.
- [ ] **P7.2** safe areas, `100dvh`, `visualViewport` keyboard handling.
- [ ] **P7.3** touch audit: 44 px targets, no hover-only affordances.
- [ ] **P7.4** reconnect resilience: backgrounded EventSource suspend →
  reconnect on focus with last-seen seq; snapshot reconciliation; pending-ask
  badge + roster pulse.
- [ ] **P7.5** Wake Lock when available; silent degradation otherwise.
- [ ] Verify Phase 7 (real iPhone, on the tailnet): Add-to-Home-Screen install;
  token cookie survives a daemon restart mid-session (exercise the self-update
  path); lock the phone mid-turn → unlock → gap replays exactly; composer rides
  the keyboard; roster polls only while visible. Tag `web-7`.

## Phase 8 — Hardening and docs

- [ ] **P8.1** `go test -race ./...` green across the suite, web included.
- [ ] **P8.2** malformed-input battery: every endpoint, bad JSON/types/sizes,
  unknown kinds, session-name tricks; SSE stream under a stalled reader.
- [ ] **P8.3** backpressure test: slow SSE client → drop-close → reconnect with
  `since` → no loss, no stall of `sess.pump`.
- [ ] **P8.4** README: `serve -web` + `[webui]`, Tailscale setup (the blessed
  path), Add-to-Home-Screen steps, token file management/rotation, `-idle 0`
  guidance, daemon-crash limit; `DEVIATIONS.md` entries for anything that
  deviated; `LOOPS.md` entries current.
- [ ] Verify Phase 8: the adapted server/client round-trips from
  `docs/planfiles.md` — start/attach from a browser; run a turn with tool call,
  background task, ask; disconnect all clients mid-work; reconnect and verify
  snapshot + gap replay; two browsers identical; reopen the durable session
  after daemon restart with model/workspace/transcript recovery. Full gates:
  `go build ./... && go vet ./... && go test ./...` (+ `-race`). Tag `web-8`.

# PART III — Gotchas ledger (pre-registered)

- **Idle-exit under a roster-only browser**: no open stream = no client; the
  daemon can idle-exit while the user watches the roster. Documented behavior +
  `-idle 0` guidance (§10). Do not "fix" by pinning the daemon on any web hit.
- **iOS HTTP/1.1 connection cap (~6/origin)**: never open a stream per roster
  row. One stream per tab; roster polls.
- **DNS rebinding / CSRF**: the Origin+Host check on mutating verbs is
  mandatory, not optional; a missing check is a launch blocker.
- **Token leakage**: token appears in a URL exactly once (handoff), is stripped
  by redirect, `Referrer-Policy: no-referrer` covers the rest; the persisted
  token file is 0600 and chmod'd on load.
- **Slow SSE client must not stall `sess.pump`**: verify broadcast's
  non-blocking behavior when wiring Phase 2; bounded queue with drop-close
  otherwise.
- **Snapshot size on phones**: snapshots strip images and may be `Truncated`;
  deep history is the store reader's job, not a bigger snapshot. gzip JSON
  responses; never wrap SSE.
- **Rename races**: `renameSession` republishes identity; the UI follows
  snapshots, never a cached name.
- **Never hold `s.mu`/`sess.mu` across client I/O** — streaming writers copy
  what they need first (the `handle()` relay pattern).
- **PWA standalone quirks** (viewport height, keyboard, A2HS caching) are
  device-verified in Phase 7, not emulator-verified.
- **Feature pressure against "100%"**: the capability matrix in §4/§6 is the
  checklist; a new row requires a test round-trip before its checkbox flips.
- **Escaped-scope creep**: §11 items arrive in v2 with their own plan, not as
  Phase 8 leftovers.

# Definition of done

`go build ./... && go vet ./... && go test ./...` and `go test -race ./...` are
green with the web package included; every §4/§6 endpoint has an httptest
round-trip and a uniform error shape; an open browser subscription counts as a
client for the idle watchdog; TUI + two browsers on one session see identical
logical events with exact gap replay after reconnect; deep history scrolls to
message 0 on a session longer than the snapshot window without booting a stored
session's agent; the UI renders catppuccin-frappé tokens served from the Go
palette with zero hex literals in authored CSS; the webapp installs via
Add-to-Home-Screen on an iPhone and passes the device round-trips in Phase 7
(token survives daemon restart, locked-phone gap replay, keyboard-safe
composer); README documents the Tailscale path, token management, `-idle 0`
guidance, and the daemon-crash limit; `LOOPS.md` carries an entry per task and
`DEVIATIONS.md` any deviation.