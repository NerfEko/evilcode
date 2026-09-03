# Chat Scroll Layout Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Give the web UI independent sidebar/transcript scrolling, open live chats at the newest message, and keep the composer pinned and focused at the bottom.

**Architecture:** Keep the existing `#sidebar`, `#chat`, `#roster`, `#transcript-scroll`, `#ask-dock`, and `#composer` elements. Constrain the application and chat to the viewport with CSS grid; make the roster and transcript the only vertical overflow regions. Reuse the existing `app.js` bottom-preservation logic and add one explicit composer focus seam for initial live-session opens.

**Tech Stack:** Embedded HTML, authored CSS, browser JavaScript modules, Go asset-contract tests, Node's built-in test runner, browser smoke verification.

## Global Constraints

- No API, backend, route, or theme changes.
- Preserve the existing history-load scroll anchoring and new-activity affordance.
- Preserve the existing mobile `--kb-overlap` keyboard compensation.
- Keep the composer in normal grid flow; do not overlay it over transcript content.
- Respect visible focus and reduced-motion behavior already established by the shell.

---

### Task 1: Add a failing shell layout contract

**Files:**
- Modify: `internal/daemon/webshell_test.go`
- Read: `internal/daemon/webassets/index.html`, `internal/daemon/webassets/css/app.css`

**Interfaces:**
- Consumes: embedded `webAssets` and the existing `webShellHTML` helper.
- Produces: `TestWebShellHasIndependentScrollLayout`, a regression guard for the viewport/grid/overflow contract.

- [ ] **Step 1: Write the failing test**

Add a test that reads `webassets/css/app.css` and fails unless the shipped stylesheet contains the following declarations: `.app` has a viewport `height` and `overflow: hidden`; `.sidebar` has `height: 100%`, `min-height: 0`, and `overflow: hidden`; `.roster` has `overflow-y: auto` and `min-height: 0`; `.chat` has `height: 100%`, `min-height: 0`, and an explicit `minmax(0, 1fr)` grid track; `.chat-scroll` has `overflow-y: auto` and `min-height: 0`; and `.rail` has `min-height: 0`. Keep the test focused on these observable asset-level layout requirements, using the same `fs.ReadFile(webAssets, ...)` pattern as the existing CSS guard.

- [ ] **Step 2: Run the test to verify it fails**

Run:

```sh
go test ./internal/daemon -run TestWebShellHasIndependentScrollLayout -count=1
```

Expected: FAIL because the current `.app`, `.sidebar`, `.chat`, and `.chat-scroll` rules do not yet contain the complete viewport-constrained layout contract.

### Task 2: Implement independent pane sizing and scrolling

**Files:**
- Modify: `internal/daemon/webassets/css/app.css:74-89,150-157,552-584,1034-1039,1170-1201`

**Interfaces:**
- Consumes: existing CSS tokens, grid structure, `--kb-overlap`, and mobile media-query overrides.
- Produces: a viewport-sized shell whose roster, transcript, and optional right rail scroll independently while the composer remains a bottom grid row.

- [ ] **Step 1: Add the viewport shell constraints**

Update `.app` to set both `height: 100vh` and `height: calc(100dvh - var(--kb-overlap, 0px))`, retain the existing minimum-height fallback, and add `min-height: 0` plus `overflow: hidden`. Update `.sidebar` with `height: 100%`, `min-height: 0`, and `overflow: hidden`. Keep the mobile `.sidebar` definite-height override intact.

- [ ] **Step 2: Give chat explicit rows and a definite height**

Change the base `.chat` rule to use `grid-template-rows: auto minmax(0, 1fr) auto auto`, `height: 100%`, `min-height: 0`, and `overflow: hidden`. Keep the mobile override's five rows (`auto auto minmax(0, 1fr) auto auto`) and keyboard-adjusted definite height because the mobile backbar is visible there.

- [ ] **Step 3: Constrain each scrolling region**

Keep `.roster`'s existing `overflow-y: auto` and `min-height: 0`. Add `min-height: 0` and `overscroll-behavior: contain` to `.chat-scroll`, retaining its vertical overflow and padding. Add `min-height: 0` to `.rail` so its existing `overflow-y: auto` cannot force the page to grow. Do not add `position: fixed` or a second transcript scroll element.

- [ ] **Step 4: Run the layout contract test**

Run:

```sh
gofmt -w internal/daemon/webassets_test.go

go test ./internal/daemon -run TestWebShellHasIndependentScrollLayout -count=1
```

Expected: PASS.

### Task 3: Pin and focus the composer while preserving transcript floor behavior

**Files:**
- Modify: `internal/daemon/webassets/js/views/composer.js:350-388`
- Modify: `internal/daemon/webassets/js/app.js:494-552`

**Interfaces:**
- Consumes: existing `mountComposer().bind()`, `renderMirrorState(..., { forceBottom: true })`, `transcriptScroll()`, and `mobile` keyboard shim.
- Produces: `composer.focus()` plus initial live-session focus/bottom behavior; existing streaming/history behavior remains unchanged.

- [ ] **Step 1: Add the initial-focus behavior test expectation**

Extend the live browser smoke checklist to assert that opening a live session leaves `document.activeElement === document.getElementById("composer-text")` after the route finishes loading. The existing module tests do not mount the full app DOM, so this contract is verified on the actual shell in Task 5 rather than by a fake DOM.

- [ ] **Step 2: Expose one composer focus method**

Add `focus() { text.focus(); }` to the object returned by `mountComposer()`. Do not focus from `bind()`; route binding also runs for internal transitions and must not create repeated keyboard/focus side effects.

- [ ] **Step 3: Focus only after a live route is rendered**

In `app.js`, after the live-session branch has called `composer.bind(route.name)`, rendered the initial mirror, opened the stream, and assigned `scroll.scrollTop = scroll.scrollHeight`, call `composer.focus()`. Keep stored-session behavior unchanged (`composer.bind("")` stays hidden), and leave `renderMirrorState`'s `forceBottom`/preserve-scroll logic untouched.

- [ ] **Step 4: Run JavaScript module tests**

Run:

```sh
node --test internal/daemon/webassets/js/views/*.test.mjs internal/daemon/webassets/js/*.test.mjs
```

Expected: all existing frontend unit tests pass.

### Task 4: Document the changed interaction contract

**Files:**
- Modify: `README.md` in the existing “The web UI” section
- Append: `docs/LOOPS.md`

**Interfaces:**
- Consumes: the existing web UI usage text and append-only loop log.
- Produces: user-facing guidance that the sidebar and conversation scroll independently and the composer stays at the bottom; a dated verification record.

- [ ] **Step 1: Update README guidance**

Add one sentence to the web UI description: on desktop and phone chat views, the session list and conversation have independent scroll positions, and the message composer remains pinned at the bottom; opening a live session starts at the newest message and focuses the message box.

- [ ] **Step 2: Append the loop entry after verification**

Append a dated `docs/LOOPS.md` entry naming the CSS grid/overflow change, focus behavior, and browser verification viewports. Do not rewrite previous entries.

### Task 5: Run full checks and verify the real UI

**Files:**
- No source changes.

**Interfaces:**
- Consumes: the updated embedded web shell served by the running daemon.
- Produces: test and browser evidence for the requested behavior.

- [ ] **Step 1: Run the Go web checks**

Run:

```sh
go test ./internal/daemon -run 'TestWebShellHasIndependentScrollLayout|TestWeb' -count=1
go test ./... -count=1
go vet ./...
```

Expected: focused and full checks pass.

- [ ] **Step 2: Build and restart the daemon**

Run:

```sh
go build -o evilcode ./cmd/evilcode
./evilcode serve --stop
./evilcode -l
```

Expected: the rebuilt daemon starts with the existing Tailscale web route.

- [ ] **Step 3: Verify desktop behavior in the browser**

Open the live Tailscale URL at a desktop viewport. Open a session with enough messages to overflow both regions. Confirm `#roster` and `#transcript-scroll` have separate scroll positions, `#composer` stays at the bottom while transcript content scrolls behind neither it nor the header, the initial transcript is at its bottom, and the textarea is focused.

- [ ] **Step 4: Verify phone behavior in the browser**

Set a phone-sized viewport, open the same session, and confirm the backbar remains visible, the transcript scrolls inside the chat, the composer remains above the keyboard/safe area, and the sidebar does not move when the transcript is scrolled.

- [ ] **Step 5: Commit the implementation**

```sh
git add internal/daemon/webshell_test.go internal/daemon/webassets/css/app.css internal/daemon/webassets/js/app.js internal/daemon/webassets/js/views/composer.js README.md docs/LOOPS.md
git commit -m "fix(web): separate chat pane scrolling"
```
