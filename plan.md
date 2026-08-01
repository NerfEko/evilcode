# evilcode — personal agent harness (Go + Charm stack), evil-flavored

This document is the **complete, self-contained specification and build plan**. It assumes no other context. Every hex value, glyph, formula, timing constant, and behavior in Parts II–III is battle-tested and normative — implement exactly what the tables say, or log the deviation in `DEVIATIONS.md`. **Never name or reference other harness projects in this repo's code, docs, commits, or strings — evilcode stands on its own.**

---

# PART 0 — Process: how the building agent works

## 0.1 Development model

An external agent (Claude Code / Codex / opencode) builds evilcode until evilcode can build itself (`/selfdev`, the Phase 5 graduation milestone). Because the builder is an agent, **everything — including visuals — must be verifiable by an agent without a human watching**: that is what the probe rig (§14) is for. It ships in Phase 0 and later becomes evilcode's own self-dev loop.

## 0.2 The loop (repeat until plan complete)

First action ever: copy this plan to `plan.md` at the repo root; that copy is the working spec — check tasks off there.

1. Pick the next unchecked `[ ]` task in the current phase of `plan.md`.
2. Implement it. Reuse existing packages in the repo before writing anything new.
3. `go build ./... && go vet ./... && go test ./...` must pass.
4. For any TUI-visible task: boot the probe rig, drive the scenario, capture frames, **render to PNG and look at the image**. Compare against the spec section the task references. Iterate until it matches.
5. Mark the task `[x]` in `plan.md`. Commit — at minimum once per task, always with a green build.
6. Kick off a **codex review of the commit in the background** (codex review skill / `codex:rescue`). Don't block; fold findings into the next iteration. Fix real findings before unrelated new work; log dismissed findings with a reason in `LOOPS.md`.
7. Append one entry to **`LOOPS.md`** (append-only, never edit old entries): `## <date> <task-id>` — what was done, how verified (test names / PNG filenames), codex verdict when known, deviations.
8. Keep **`README.md`** current in the same commit whenever behavior changes. Sections: what evilcode is, build/install, quickstart, what works today, config reference, keymap, probe rig usage.
9. If a task is impossible as specced: do the closest working thing, log in `DEVIATIONS.md`, move on. Never stall.

---

# PART I — Context, constraints, architecture

## 1.1 What this is

A personal AI coding agent harness: terminal UI, agentic tool-calling loop, first-class **Ollama Cloud** support, built in **Go with the Charm stack**. Linux-only. Single user. No telemetry, no installers, no multi-platform. Repo: `/mnt/cachyos-home/eko/projects/evilcode` (greenfield). Identity: **evil but tasteful** (§2).

**The #1 priority is that the TUI is beautiful, alive, and fun.** The agent loop is table stakes; the feel is the product.

## 1.2 Verified facts (July 2026 — do not re-verify)

- **Charm v2 is current.** Import paths: `charm.land/bubbletea/v2` (≥ v2.0.8), `charm.land/lipgloss/v2`, `charm.land/bubbles/v2`, `charm.land/glamour/v2`. Bubble Tea v2 owns all terminal I/O; synchronized output (Mode 2026) gives atomic flicker-free frames; truecolor→256 downsampling is automatic. Online tutorials are mostly v1 (different APIs and import paths) — trust official v2 docs/godoc only.
- **Ollama Cloud is just a remote Ollama host**: base URL `https://ollama.com`, header `Authorization: Bearer $OLLAMA_API_KEY`, identical native API to localhost: `POST /api/chat` (NDJSON streaming, `tools` field), `GET /api/tags`, `POST /api/embed`. One client serves both; only BaseURL + token differ. Cloud model tags: `qwen3-coder:480b-cloud`, `deepseek-v3.1:671b-cloud`, `gpt-oss:120b-cloud`, `kimi-k2.7-code:cloud`, etc. Ollama also exposes OpenAI-compatible `/v1` but the native API is primary.
- **Embeddings**: `/api/embed` (batched input) on **local** ollama, model `nomic-embed-text` (768-dim). Cloud embedding availability is thin; default embed endpoint = `http://localhost:11434`, configurable.
- **sqlite-vec without CGO**: `github.com/asg017/sqlite-vec-go-bindings/ncruces` + `github.com/ncruces/go-sqlite3` (WASM driver). Static binary stays static.
- Sub-pixel "smooth pixel scrolling" seen in some experimental setups is a *terminal-side* GPU feature — out of scope. The in-app scroll feel (§4) is what you feel in a normal terminal and IS in scope.

## 1.3 Non-negotiable invariants (day 0; retrofitting any of these = rewrite)

1. **`internal/agent` never imports bubbletea.** The agent core emits typed events on a channel. The TUI, `evilcode run`, and the daemon are three consumers of one stream. This is what makes headless mode, serve/attach, swarms, and the probe rig cheap.
2. **Context is append-only.** Never rewrite/reorder earlier messages; system prompt stable; dynamic state (todos, memories, nudges) is injected as *new tail messages*. Keeps the Ollama KV/prompt cache warm. (`/compact` is the one sanctioned rewrite; it bumps a `context_epoch`.)
3. **Overlays reserve zero layout height** (§5.1, §5.2). Opening the slash palette or history search must never move the transcript.
4. **The screen never jumps.** Widgets anchor to content lines with hysteresis (§8.3); the scrollbar width decision is hysteretic (§3.6); any decision that feeds back into layout gets hysteresis. When choosing between "reacts instantly" and "stays put" — stays put.
5. **Deterministic test mode**: `EVILCODE_DETERMINISTIC=1` ⇒ fixed session name (`dracula`), animations frozen at frame 0, no wall-clock text, fixed spinner glyph — golden frames become reproducible.
6. **Every auto-continuation path has a circuit breaker** (§12.6). Each breaker in that table exists because the unguarded version caused a real infinite loop in production somewhere.
7. **Emoji: single widely-supported codepoints only.** No ZWJ sequences, no Unicode 13+. A unit test iterates every name/icon table and rejects violations. Emoji width bugs are the #1 TUI corruption source. Normalize `⚠️` (VS16) → `⚠` before render — the variation selector is repaint-unstable.

## 1.4 Module layout

```
evilcode/                          go.mod: module evilcode
├── main.go                        stdlib flag subcommand switch: tui|run|serve|attach|probe|dictate
├── plan.md                        this plan (working copy; tasks checked off here)
├── LOOPS.md                       append-only flight recorder
├── README.md                      kept current every behavior-changing commit
├── DEVIATIONS.md                  spec deviations log
├── docs/                          soft-interrupt.md and other design docs, written as built
├── internal/
│   ├── agent/       agent.go (loop, §15) · events.go (Event union) · context.go (append-only
│   │                messages, AGENTS.md/CLAUDE.md, skills index) · hooks.go (post-turn seam)
│   ├── provider/    provider.go (interface) · ollama.go · openai.go · mock.go (test provider)
│   ├── tools/       tools.go (contract) · fs.go (read/write/edit+anchors) · bash.go · grep.go ·
│   │                ask.go · git.go · todo.go · memory.go (P3) · lsp.go (P5)
│   ├── todo/        model.go · gates.go · poke.go (§12)
│   ├── core/        names.go (creature/modifier tables §2.2) · ids.go
│   ├── session/     store.go (JSONL) · history.go (prompt history §6.1) · checkpoint.go
│   ├── config/      TOML + env; XDG paths (~/.config/evilcode, ~/.local/share/evilcode)
│   ├── theme/       roles.go · palettes.go · substitute.go (two-pass §7.4) · harmony.go (§7.5) ·
│   │                literals.go
│   ├── mcp/         (P2) wrapper over modelcontextprotocol/go-sdk
│   ├── memory/      (P3) sqlite-vec store, embed, recall, reflect, consolidate
│   ├── server/      (P4) daemon, unix-socket NDJSON, swarm coordinator
│   ├── advisor/     (P5) watcher model injecting concerns (§21)
│   ├── ansirender/  ANSI→PNG renderer (probe rig + /screenshot)
│   └── tui/         app.go · transcript.go · composer.go · overlay.go · statusline.go ·
│                    header.go · widgets/ · markdown.go · codeblock.go · diff.go ·
│                    plancard.go · todocard.go · picker.go · sessionpicker.go · helpview.go ·
│                    idleart.go · keymap.go · scroll.go · theme_bridge.go
├── probe/           probe.sh · scenarios/*.txt · goldens/*.txt · README.md
└── testdata/
```

## 1.5 Dependencies (exact, with rationale)

| dep | why |
|---|---|
| `charm.land/bubbletea/v2` | TUI framework; Mode-2026 sync output = flicker-free |
| `charm.land/lipgloss/v2` | styling (pure in v2; downsampling handled by bubbletea) |
| `charm.land/bubbles/v2` | textarea (composer), viewport, spinner primitives |
| `charm.land/glamour/v2` | markdown prose (custom style JSON §9.1; code blocks rendered by us §9.2) |
| `github.com/alecthomas/chroma/v2` | syntax highlighting for code blocks, diffs, file panel |
| `github.com/charmbracelet/log` | structured logging **to file only** — never stdout, the TUI owns it |
| `github.com/BurntSushi/toml` | config |
| `github.com/modelcontextprotocol/go-sdk` | MCP client (P2) — never hand-roll a protocol |
| `github.com/ncruces/go-sqlite3` + `github.com/asg017/sqlite-vec-go-bindings/ncruces` | memory (P3), no CGO |
| `github.com/charmbracelet/harmonica` | spring animation for panel slide-in (P5, only if panels feel dead) |
| `golang.org/x/image` | font rendering in ansirender |

**Deliberately NOT used**: the ollama Go SDK (drags the whole ollama module; a native NDJSON client is ~200 lines of stdlib), openai-go (~250-line SSE client suffices), wish (one user, one machine — serve mode is a unix socket), cobra/viper (a handful of subcommands = stdlib `flag`), diff libraries (render tool-produced diffs; `git diff --no-index` for ad-hoc).

---

# §2 Design language: "evil, not obnoxious"

**The rule**: flavor lives in *names, palette, idle art, and a small approved lexicon* — never in noise, clutter, or every string. Status messages stay functional. If a screen would read as cringe to a stranger, cut the flavor.

## 2.1 Approved evil lexicon (complete — additions need user sign-off)

- Welcome: `Welcome to evilcode 🦇`.
- Idle art variants: `eye` and `blackhole` (§10.1). NOT a pentagram — too much.
- All-todos-done: `🦇 All rites complete. Completion confidence: 97%.`
- `/summon` = alias for spawn-swarm-worker. `/graveyard` = alias for `/resume`.
- Everything else — `thinking…`, `streaming…`, errors, help text — plain and functional.

## 2.2 Session naming

`internal/core/names.go`: two tables, each entry = name + exactly one safe emoji.

```
creatures (client/session names): bat 🦇, snake 🐍, dracula 🧛, raven ☾, wraith 👻,
  viper 🐍, spider 🕷, crow 🐦, wolf 🐺, imp 👿, ghoul 💀, hex ✴, omen ☄, banshee 🌀,
  lich ⚱, moth 🦋, serpent 🐉, widow 🕸, shade 🌑, fang 🗡, talon 🪶, thorn 🌹,
  ember 🔥, frost ❄   (+ extend to ~40; fallback 💫)
modifiers (server names): crypt ⚰, coven 🕯, lair 🌋, abyss 🕳, tomb 🪦, manor 🏚,
  catacomb 🦴, altar 🔮, gallows 🌘, hollow 🌲   (fallback 🔮)
```

Server identity `Crypt ⚰`, client `Bat 🦇`. Collisions get `-2` suffixes. Unit test rejects multi-codepoint glyphs (invariant 7).

## 2.3 Glyph vocabulary (proven single-cell set; use these, nothing exotic)

`● ○ ◖ ◗ ▰ ▱ █ ░ ▸ ▶ ▎ ▌ ✓ ✗ ⊳ ↻ ↗ ⚠ ⓘ × ❯ › » ⛭ 📌 ╭ ╮ ╰ ╯ ├ │ ─ ┌ └ ┐ ┘ ╷ ╵` + braille spinner `⠋ ⠙ ⠹ ⠸ ⠼ ⠴ ⠦ ⠧ ⠇ ⠏`. Emoji inventory (all single-codepoint): `💡 🔍 💰 🧠 ⚡ 🦇 👻 💀 🧛 ⏳ ❌ 💥 🔄 📦 🧭 👉 🔎 🛑 ⌨ ☁`.

---

# PART II — Complete visual & interaction specification

Every number and behavior in this part is normative. Where a table gives exact values, implement exactly those values.

## §3 Screen layout

### 3.1 Horizontal split (outermost, carved right-to-left)

1. **Diagram pane** (P5; mermaid/images), position `Right` or `Top`. Right: min width 30; Top: min height 6 with min chat height 8; ratio clamped 20..100.
2. **Side pane** (diffs / pinned markdown): min diff width **30**, min chat width **20**; ratio = pane_ratio clamped 25..100; auto-bumps to **55** when showing images unless the user manually resized.
3. Remainder = **chat column**.

### 3.2 Vertical stack inside the chat column (top→bottom)

| # | content | height |
|---|---|---|
| 0 | Transcript | **packed**: exact content height · **scrolling**: min 3 |
| 1 | Queued-message rows | `pending_count.min(3)` |
| 2 | Swarm strip (P4) | variable |
| 3 | Status line | 1 |
| 4 | Notice line | 0–1 |
| 5 | Inline picker (model/account/ask) | variable |
| 6 | picker↔input gap | 1 when picker present |
| 7 | Composer | `wrapped_lines.min(10) + hint_line` |
| 8 | Overscroll facts line | 0–1 (elastic, §4.4) |
| 9 | Idle art | variable |

**Packed-vs-scrolling** is the signature structural trick: while `content + fixed ≤ available`, the transcript gets its *exact* height so content hugs the composer — no dead gutter above the input. On overflow, chunk 0 becomes a min-3 scrolling viewport. Implement this first; everything sits on it.

### 3.3 Borders

**No box chrome on the main surface** — transcript, status line, composer are borderless. Borders only on:
- Side pane: LEFT border only (1 col) + 1-row header; border = focus color when focused, dim otherwise.
- Info widgets: rounded, `rgb(70,70,80)` dimmed (§8.3).
- Inline picker: rounded, border `#55556e`, **box background `#12121a`** (§5.3).
- Ad-hoc rounded box helper (used by plan cards, updates box, memory tiles): builds lines manually — top `╭─ Title ─╮` with title centered as ` {title} ` inside the border run, sides `│ content │` padded to uniform width, bottom `╰───╯`. Box width = content + 4.

### 3.4 Left inset

Left-aligned mode has a 1-column left gutter; centered mode has 0 (centering provides its own margins).

### 3.5 Scrollbar (transcript)

1 column on the right; transcript wraps 1 col narrower when visible. Glyphs: thumb-of-height-1 `•`, thumb top `╷`, thumb bottom `╵`, thumb middle `│`, track = blank with Reset bg. Thumb color `rgb(188,208,240)` focused / `rgb(136,148,172)` unfocused.

### 3.6 Scrollbar hysteresis (invariant 4 example)

The *previous frame's* visibility decides which wrap width to prepare first, so steady state wraps once. If hiding the bar would make content fit → the wide no-scrollbar layout wins. Without this the layout oscillates two frames forever.

## §4 Scrolling feel

State: `scrollOffset int` (lines from bottom; 0 = pinned) + `autoScrollPaused bool`. `followChatBottom()` = offset 0, unpause, request snap.

### 4.1 Mouse wheel: momentum + inferred velocity

- Base **3 lines per notch**.
- Acceleration inferred from inter-notch gap (terminals don't report velocity): gap ≤ **30ms** → ×2 multiplier, capped at **5** lines/notch.
- Momentum queue capped at **30** lines. One notch's worth drains immediately (responsiveness); the rest drains per redraw tick.
- **Ease-out drain**: queue ≥ 6 → 3 lines/frame; ≥ 3 → 2; else 1. This is the glide.
- Switching scroll target (chat / help overlay / picker preview) zeroes the queue.
- `scrollUp/Down` return "did position actually change"; scrollDown clamps to bottom so phantom offset never accumulates (otherwise scrolling down at the bottom silently builds offset that must be undone before scrolling up moves anything).

### 4.2 Tail-follow catch-up (kills "the big pop")

While pinned at bottom, a large append (committed message, tool result) must not teleport content up a whole block in one frame:
- jump < **4** rows → snap normally (paced streaming).
- otherwise animate at ≤ **3 rows per frame**.
- lag capped at **one viewport height** so a giant paste can't leave the tail arbitrarily behind.
- Explicit user actions (submit, Esc, jump keys) set a snap-pending flag that bypasses the animation and lands exactly at bottom. Automatic growth never sets it.
- Disabled when decorative animations are off; keeps the redraw loop at animation cadence while sliding.

### 4.3 Keyboard navigation

PageUp/PageDown = 10 lines. Up/Down = 1 line **when the composer is empty**. Ctrl+J/Ctrl+K (also Ctrl+]/Ctrl+[) = jump to next/previous **user prompt**. Ctrl+Shift+J/K = 1 line. Alt+U/Alt+D = page. Ctrl+5..9 = jump to Nth most recent prompt. **Ctrl+G scroll bookmark**: scrolled-up + no bookmark → save position, jump to bottom, notice `📌 Bookmark set - press again to return`; bookmark exists → teleport back, `📌 Returned to bookmark`; at bottom no bookmark → no-op. Esc when idle → follow bottom + clear input (§6.7).

### 4.4 Elastic overscroll ("pull to reveal")

Wheel-down while already pinned reveals a 1-row facts line below the composer (model · effort · provider · auth · context bar · cwd) with a **live countdown** `(overscroll 1.4)` italic `rgb(150,150,165)` right-aligned, then rebounds away.
- Dwell **1500ms** after last tick; gesture gap **500ms** (a pause longer than this starts a new gesture; must exceed idle redraw cadence so one flick isn't split).
- **Only reveals if the gesture *began* at the bottom** — momentum merely arriving at the bottom is swallowed. This is what makes it feel intentional, not twitchy.
- Scrolling up cancels instantly. Config modes: `off` / `on` (pinned always, no countdown) / `overscroll` (default).

### 4.5 Typing scroll lock

Alt+S toggles: when locked, typing does not snap the view to bottom. Notices: `Typing scroll lock: ON - typing stays at current chat position` / `…OFF - typing follows chat bottom`.

### 4.6 Streaming-reasoning GC

In `current` thinking-display mode, old reasoning traces are deleted once provably above the viewport (`total − trace_lines > viewport + 2`). **Never while `autoScrollPaused`** — don't remove what the reader may be reading.

### 4.7 Full-repaint-on-scroll

Request a full repaint on every scroll: cell-diff renderers fail to re-emit the trailing cell after a wide grapheme, leaving ghost chars in kitty/foot. Use repaint, not clear-screen (an ED2 clear makes inline images flicker).

## §5 Overlays, menus, pickers

### 5.1 Slash-command palette (zero layout height — invariant 3)

Drawn **last**, floating over the finished frame (in bubbletea: splice lines into the rendered view string, or lipgloss layers). Explicit clear of the covered cells. Position: prefer directly below the composer; not enough room → flip above it (covering transcript tail); last resort clip below. Never reserves a row — opening it never moves anything.

Row format: `{command}  {description}`, no border, no background.
- 1 suggestion → whole line `rgb(255,213,128)` (warm gold).
- N suggestions → selected row: command+description both `rgb(255,213,128)`; unselected: command `rgb(128,203,196)` (teal), description dim.
- **Fuzzy-match highlight = recolor, not underline**: matched chars lifted toward white `c' = c + (255−c)/2` + bold; unmatched dimmed `c' = c/2`. The match pops while staying in the palette's hue.
- Window ~8 rows; first row appends `  ↑{n}` when scrolled past items; last appends `  +{n} more` (both dim).
- Navigation: Up/Down (only without Ctrl/Alt/Super) or Ctrl+J/Ctrl+K, **wrapping**. Enter accepts. Tab = autocomplete cycle (separate tab-state machine `(base, idx)`).
- Active when composer is in slash mode, or plain chat while idle.

**Ranking**: bucket 1 = literal case-insensitive **prefix** match (absolute priority); bucket 0 = fuzzy by score. Sort: bucket desc, score desc, shorter name, lexicographic. Exact typing always beats fuzzy.
**Memoize per frame** (the palette may be consulted several times per frame; key = input + UI-state signature; epoch bumped each frame).
**Suppression**: (a) while an interactive prompt awaits typed input (API key, rename, pending `ask`) the composer is an answer box — suggest only `/cancel`; (b) while an inline picker preview is open for the command being typed, the picker is the surface — suppress the list.
**Argument completion**: per-command sub-lists — bare `/cmd` → full unranked sub-command list; `/cmd <partial>` → ranked. `/model <spec>`: spec containing `@` completes providers for that model, else completes models; no candidates → fall back to `[("/model", "Open model picker")]`. Skills appear as `/{skill}` → "Activate skill", deduped against commands.

### 5.2 Ctrl+R history search (floating, same positioning machinery)

```
(history search) fix the auth█  ↑↓ select · ↵ insert · Esc cancel
  ▸ fix the auth redirect loop
    fix the auth token refresh
    ...  +12 more
```
Label dim; query + block cursor `█` in `rgb(255,213,128)`; selected row `▸ ` + gold; others teal; hints dim. **8 visible rows**, windowed around the selection. Empty query → dim `  type to search history`; no match → `  no matches`.

Semantics (readline-style): saves original composer draft + cursor on open; empty query matches nothing; typing filters with **free-form fuzzy** (matches anywhere — not the anchored command scorer); sort by score then recency; max 50 matches. **Live preview**: the selected match is written into the composer as you navigate; cancel restores the saved draft. Keys: Up / Ctrl+R again = older; Down = newer; Enter = accept + close; Esc / Ctrl+G / Ctrl+C = cancel + restore. Drawn after the palette so it wins.

Storage (§6.1): `~/.local/share/evilcode/prompt-history.jsonl`, append-only, one JSON string per line; cap 1000 after dedupe (keep most recent), compaction rewrite at 2000 lines; prompts > 10k chars not recorded. **Cross-session**: merged persisted + current-session prompts (Up-arrow recall also reaches prior sessions).

### 5.3 Model picker (`/model`) — inline interactive box

Unlike the palette this **does** reserve layout height (stack row 5). Chrome: rounded border `#55556e`, box bg `#12121a`, width intrinsic (content-derived, capped), horizontally centered in centered mode.

**Hint line lives OUTSIDE, above the box**, italic `rgb(120,120,150)`, full width: `keys: Ctrl+O set default · Ctrl+N favorite · Shift+Tab switch active model to next favorite`.

Header row inside: column labels (`model | provider | via`); the **focused column** (←/→ switches) is accent bold, others dim; then `  "filterquery"` + ` (12/47)` dim + submit hint `rgb(60,60,80)`.

Row marker gutter (3 cells ` {m} `): `×` unavailable (`rgb(180,120,120)` bold) · `⚠` limited/fallback · `▸` selected (white bold) · ` ` otherwise.

Primary-name style cascade (first match wins):
```
unavailable      → fg rgb(80,80,80)
selected col-0   → fg White on bg #3c3c50, BOLD      ← the selection highlight
is_current       → accent (#ff79c6)
is_favorite      → rgb(255,160,210) bold
recommended      → rgb(255,220,120)
old              → rgb(120,120,130)
default          → rgb(200,200,220)
```
Provider column `rgb(140,180,255)`; via/method column `rgb(220,190,120)`; selected cell in any column = white-on-`#3c3c50` bold. Trailing detail dim, or `rgb(180,120,120)` italic when unavailable.

Name suffixes appended: ` new`, ` ♥` (favorite), ` ★` (recommended), ` old`, ` default`.
Notice line under the header when the selection has a caveat: `× unavailable · detail` / `⚠ detail` (italic `rgb(210,150,110)`) or `ⓘ detail` (italic dim).
Scroll keeps the selection **centered** (`start = selected − height/2`, clamped). Empty → `   no matches` dim italic.
Filter-match chars are **underlined** here (deliberately different from the palette's recolor).
Ctrl+O = set default; Ctrl+N = toggle favorite; Shift+Tab (global) = cycle favorites.
The same box chrome renders the `ask` tool's option picker (§17).

### 5.4 Session picker (`/resume`, alias `/graveyard`) — full screen

Vertical: optional crash banner, search bar (1 row), then the split. Horizontal: **40% list / 60% preview**.

Search bar: whole row bg `rgb(25,25,30)`: `🔍 query▎  Esc to clear` (inactive: `  / to edit`) — magnifier + `▎` cursor in accent, query white bold, hint `rgb(60,60,60)`.

Session rows (2+ lines each):
```
● 🦇 bat-3 📌 ⠹  working 12s  "wire the auth flow"  [BATCH]  ◀ current  ▸ here
     42 messages · ~18.4k tok
```
- Multi-select: `● ` marked `rgb(140,220,160)` bold / `○ ` unmarked `rgb(90,90,90)`.
- Session emoji in `rgb(110,210,255)`; name white (selected → `rgb(140,220,160)` bold).
- Marker badges: saved `📌` `rgb(255,180,100)`; `[BATCH]` `rgb(255,140,140)` bold; `◀ current` `rgb(110,210,255)` bold; `▸ here` (same cwd) `rgb(120,200,140)` bold.
- Status glyph/label: live-streaming = braille spinner `rgb(255,193,7)` + `working 12s` · live-idle `●` `rgb(100,220,130)` + `ready` · active `▶` `rgb(100,200,100)` · closed `✓` `rgb(100,100,100)` + `closed 3h ago` · crashed `💥` `rgb(220,100,100)` · reloaded `🔄` `rgb(138,180,248)` · compacted `📦` `rgb(255,193,7)` · rate-limited `⏳` accent · errored `❌` red. Crash rows add a `reason:` line.
- Search-term highlighting via per-character mask (OR across tokens, merged runs).

Preview pane: rounded border, title ` Preview `, border `rgb(130,130,160)` focused / `rgb(70,70,70)` unfocused.
Confirmation modal pattern: cleared centered rect `min(w−4, 74) × min(h−2, 8)`, rounded amber `rgb(255,193,7)` border, title in the border, action line `Enter/Y confirm · Esc/N cancel` amber bold.

**Session titles are derived from the plan**: current in-progress todo's *group* label → plan's `user_intention` → todo content. The list is labeled by what the agent understood you wanted.

### 5.5 Help overlay (`/help`, `/?`)

Full-screen box, square corners, title `┌ Help  34%  ─…` — **scroll percentage lives in the title**. Sectioned content: section headers accent bold; command names bold; descriptions dim. Footer inside the bottom border: `Esc to close · mouse wheel/j/k scroll · Space/PageUp page · /help <cmd> for details`. `/help <cmd>` shows a long-form description.
**Anti-drift**: hand-curated sections, then compute `uncovered = registered − shown` and dump the remainder under "More commands" — a newly registered command can never be invisible. Unit test: no duplicate command names.

### 5.6 Full-screen list pattern (reusable)

Rounded box, title ` Title (3 pending) ` white bold, border `rgb(80,80,90)`. Cursor `❯` `rgb(140,180,255)`. Urgency dots: high `●` red, normal `●` amber, low `○` gray. Done screen: `  ✓ N approved` green / `  ✗ N denied` red.

## §6 Composer & input

### 6.1 Composer anatomy

Row 1 = `{N}{prompt}{text}` where `{N}` = next prompt number colored **full red** (rainbow distance 0, §7.7). Continuation rows indent by the prefix width. Max **10** visible rows, internal scroll follows the cursor. Prompt glyphs by state:

| state | glyph | color |
|---|---|---|
| shell mode (`!` prefix) | `$ ` | `rgb(110,214,151)` |
| processing | `… ` | queued yellow |
| skill active | `» ` | accent |
| default | `> ` | user color |

Hint line (1 row, hidden while palette is open): shell mode → `  shell mode · Enter runs locally` green; new-session armed → `  ↗ Next prompt opens a new session` `rgb(120,200,255)`; else `  Ctrl+Enter to queue` / `to send now` dim.

**Send-mode indicator**, right-aligned on the composer's last row, single glyph: shell `$` green · new-session `↗` `rgb(120,200,255)` · queue-mode `⏳` yellow · connection type when attached (websocket `󰌘` teal / subprocess `󰆍` `rgb(180,160,220)` / http `󰖟` `rgb(140,180,255)` — Nerd Font, degrade to nothing without font support).

### 6.2 Newline entry (three paths, priority order)

1. **Shift+Enter** — requires the kitty keyboard protocol (`ESC[13;2u`); request it at startup. `/terminal-setup` prints fixes (tmux: `extended-keys`; WezTerm: `enable_kitty_keyboard`).
2. **Alt+Enter** — arrives as ESC+CR, works everywhere.
3. **Trailing `\` + Enter** — universal fallback; the backslash is consumed; `\\` is a literal backslash and still submits (parity: even count of trailing backslashes ⇒ submit). First use per session → one-shot tip `Tip: run /terminal-setup to make Shift+Enter insert newlines`.

### 6.3 Send model — Submit / Queue / Interleave (get this exactly right)

```go
func sendAction(processing bool, queueMode bool, input string, alternate bool) SendAction {
    if !processing { return Submit }
    if strings.HasPrefix(trim(input), "/") || strings.HasPrefix(trim(input), "!") { return Submit }
    if alternate { if queueMode { return Interleave }; return Queue }
    if queueMode { return Queue }
    return Interleave
}
```

| state | Enter | Ctrl+J |
|---|---|---|
| idle | Submit | Submit |
| processing, default mode | **Interleave** | Queue |
| processing, queue mode | Queue | **Interleave** |
| processing, `/` or `!` input | Submit | Submit |

Ctrl+J = "opposite of my current mode", one message at a time. **Ctrl+Enter (also Ctrl+T, Ctrl+Tab) toggles queue mode** — the persistent control: once queue mode is on, Enter queues until the turn ends; off, Enter interleaves immediately. Toggle notices `Queue mode: messages wait until response completes` / `Immediate mode: messages send next (no interrupt)`.

Interleaves are delivered once: staging a message as a soft interrupt hands it to the agent immediately, and the pending row it leaves behind (`↻`) is a receipt, not a queued message — at turn end only messages that were never delivered (queued ones) are submitted, so nothing reaches the model twice.

**Interleave = soft interrupt** — the KV-cache-friendly path. The message is NOT a new API request; it is appended as a user message into the live conversation at a *safe point* so the next loop iteration carries it with the cache prefix intact. Safe points:
- **B**: stream ended, no tool calls — always safe.
- **C**: between tool executions — *urgent only*; every remaining tool first gets a stub result `[Skipped: user interrupted]` (`isError: true`) because the API requires tool_use → tool_result adjacency.
- **D**: after all tool results, before the next request — the default injection point.
Multiple pending interrupts group by source (User / System / BackgroundTask), each group joined `\n\n`, different sources flushed separately (a system nudge never merges into a user message). The queue persists to disk and survives restarts. On injection while streaming: flush the stream buffer, commit in-flight text as an assistant message, then show the injected content; if tools were skipped → notice `⚡ {n} tool(s) skipped`. Write the full semantics into `docs/soft-interrupt.md` as implemented — this mechanism deserves its own doc.

### 6.4 Pending-message rows (stack row 1, max 3 shown)

`{number} {glyph} {text}` — number in rainbow-decay color (distance = pending_count − i, so the *nearest* is most saturated):

| kind | glyph | color | text style |
|---|---|---|---|
| Pending (already sent as soft interrupt) | `↻` | pending gray `#8c8c8c` | normal |
| Interleave (staged, sends next) | `⚡` | asap cyan `#8be9fd` | normal |
| Queued (waits for turn end) | `⏳` | queued yellow `#f1fa8c` | dim |

Staging an interleave → notice `⏭ Sending now (interleave)`.
**Ctrl+Up (also Alt+Up)** with empty composer retrieves pending messages back for editing: drain soft-interrupts → interleave → queued, join `\n\n`, notice `Retrieved {n} pending message(s) for editing`; nothing pending → falls through to prompt-history navigation.

### 6.5 Editing keys (readline set)

Ctrl+U kill-to-start · Ctrl+K kill-to-end (plain Ctrl+K only; Ctrl+Shift+K is scroll) · Ctrl+W / Alt+Backspace / Ctrl+Backspace word-delete · Ctrl+A/E home/end (Ctrl+A copies viewport when input empty) · Ctrl+B/F & Ctrl+←/→ word-move · Ctrl+Z input undo (remember undo state before every destructive edit) · Ctrl+X cut line to clipboard · Ctrl+S stash/pop input. Esc-clear shows `Input cleared - Ctrl+Z to restore`.

### 6.6 Paste rules

- Bracketed paste **never inspects the clipboard for images** (Wayland multi-MIME misidentification); image paste only on explicit Ctrl+V/Alt+V.
- **150ms trailing-Enter guard**: a bare Enter within 150ms of a paste is swallowed, not submit.
- **≥5-line pastes collapse** to `[pasted {n} lines]` placeholder in the composer; contents stored; expanded on send by replacing the *last* occurrence of each placeholder (iterate stored pastes in reverse).
- File drops: path lists detected; images (`png/jpg/jpeg/gif/webp/bmp/tiff`) read + attached as `[image {n}]`; other paths inserted (quoted if whitespace). Notice: `Dropped 2 images and 1 file`. Attach notice: `Pasted image/png (412 KB)`.

### 6.7 Interrupt / cancel — Esc layered priority

1. Picker/overlay open → close it (+ clear input).
2. Processing → interrupt: cancel stream; keep partial text as a message; clear staged interleaves; **disarm auto-poke** (interrupt means *stop* — the harness must not immediately re-poke). Notice one of: `Interrupting...` / `Interrupting... Auto-poke OFF`.
3. Idle → follow bottom + clear input.

**Ctrl+C / Ctrl+D**: same interrupt but does **NOT** disarm auto-poke (it's "skip this", not "stand down"); when idle → quit (press twice to confirm). In selection mode with a non-empty selection, Ctrl+C copies instead (quitting while the user is copying an error is a classic bug — don't ship it).

### 6.8 Hotkey feedback (config `display.keybinding_hints`, default on)

- **Rare-hotkey feedback**: pressing a recognized chord you've used < 4 times → `⌨ Ctrl+G → toggle scroll bookmark`. Usage counts persist (`hotkey_usage.json`); reappears once after 45 days unused.
- **Near-miss**: unhandled *modified* chord → `⌨ Ctrl+Shift+P isn't bound · nearest: Ctrl+P → toggle auto-poke` instead of silent swallow. Rate-limit: ≥1.2s gap, ≤3 per chord per session. Check after text-input resolution so AltGr symbols still type.

## §7 Color & theming

### 7.1 The 22 semantic roles — "dracula" default palette

| role | hex | | role | hex |
|---|---|---|---|---|
| user | `#bd93f9` | | user_text | `#f8f8f2` |
| ai | `#50fa7b` | | user_bg | `#2a2440` |
| tool | `#787878` | | ai_text | `#dcdcd7` |
| file_link | `#b4c8ff` | | header_icon | `#ff79c6` |
| dim | `#505050` | | header_name | `#bd93f9` |
| accent | `#ff79c6` | | header_session | `#ffffff` |
| system | `#ffb86c` | | success | `#50fa7b` |
| queued | `#f1fa8c` | | warning | `#ffb86c` |
| asap | `#8be9fd` | | error | `#ff5555` |
| pending | `#8c8c8c` | | info | `#8cb4ff` |
| border | `#44475a` | | selection_bg | `#44475a` |

Freeze the default palette with a test (a redundant copy the real table must equal) so it never drifts. Additional themes (Phase 2): `nosferatu` (near-mono, blood-red accents), `gloom` (slate + sickly green), `daywalker` (light via luminance flip). Ad-hoc `rgb()` literals used throughout this spec (status ambers, picker golds…) stay as-is — the substitution pass (§7.4) re-expresses them relative to configured roles.

### 7.2 Markdown palette (§9.1 uses this)

```
h1 #ffd764 bold underline   h2 #f0be5a bold underline   h3 #dcaa50 bold   h4+ #c89b4b bold
body #c8c8c3   bold-text #f0f0eb   inline-code #b4b4b4 on #2d2d2d   link #78b4f0 underline
md-dim #646464 (markers, rules, fences)   table #969696   math #64a0ff   inline-math #b9c8e1
html #8c8c96
```

### 7.3 Diff colors

`diff_add #64c864` · `diff_del #c86464`. Tint formula §9.3.

### 7.4 Two-pass frame color substitution (the theming architecture)

Widgets emit *default-palette* colors and ad-hoc literals freely. Substitution happens once per frame, at the buffer level, in this exact order:

```
widgets → frame → pass 1: light/dark adapt → pass 2: user palette → terminal
```

- Pass 1 (only when terminal bg is light): hue/saturation-preserving luminance flip — rgb→hsl, `l → 1−l`, hsl→rgb.
- Pass 2 (never flipped): substitute configured role colors. A deliberately dark red must not become pale pink on light terminals — that's why pass 2 runs after and is exempt from pass 1.
- Role accessor functions return the *default* color (not the configured one) so a cell is never remapped twice.
- **Ad-hoc literals within a small perceptual radius of a role's default are re-expressed relative to the configured role color, preserving their own lightness/chroma offset** — "a slightly dimmer warning" stays a slightly dimmer warning under any theme.
- `Reset` colors are never substituted (terminal's own background shows through).
- Fast path: an atomic "has overrides" flag; an unconfigured palette pays nothing.

In bubbletea, implement as a post-render transform over the final frame string's SGR sequences (regex-free tokenizer; cache substitution per literal), or resolve at style-construction time through a theme context — profile both, correctness first. Enumerate all ad-hoc literals in `theme/literals.go` with tests: every literal reachable from a role; every role claims ≥1 literal.

### 7.5 Harmony scoring + palette generation (`/theme score`, `/theme generate <hex>`)

All math in **Oklab**. Gamut-map by iterative chroma reduction (×0.92, ≤24 iters), never channel clamping (preserves hue+lightness).

| criterion | weight | critical? | measures |
|---|---|---|---|
| readability | 3.0 | yes | Oklab lightness contrast vs actual terminal bg; target **0.40** |
| distinctness | 2.0 | yes | Oklab distance ≥ **0.20** between must-distinguish pairs |
| hue harmony | 2.0 | no | fit to analogous/complementary/triadic/tetradic/split-comp |
| chroma coherence | 1.5 | yes | saturation consistency, comfortable band |
| colorblind safety | 1.0 | no | distinctness under deuteranopia + protanopia |

Must-distinguish pairs: success↔error, success↔warning, warning↔error, user↔ai, accent↔system, info↔success, queued↔asap, system↔queued. Deliberately NOT paired: user↔info, ai↔accent, dim↔tool (real palettes make those similar on purpose).
Aggregation is **worst-weighted**: per-criterion `0.4·mean + 0.6·worst`; overall `0.75·weighted_mean + 0.25·worst_critical`. Only critical criteria can sink the score — unconventional hues are taste, not defects.
CVD simulation (linear-RGB projection): deuteranopia `r'=.625r+.375g, g'=.700r+.300g, b'=.300g+.700b`; protanopia `r'=.567r+.433g, g'=.558r+.442g, b'=.242g+.758b`. Separate must-pairs by **lightness as well as hue** (under red-green CVD, hue collapses to a blue-yellow axis; lightness survives) — success/warning/error sit on three distinct lightness levels.
Calibration pins (dark bg): Dracula ≈76, Solarized Dark ≈70, Nord ≈69, Gruvbox ≈67, neon-chaos ≈56, unreadable-mud ≈38. A test asserts these orderings hold.

Generation from seed: lightness band 0.36–0.94 (outside it gamut mapping strips chroma → smudge); seed chroma clamped 0.06–0.14; near-neutral seed (<0.02) falls back to role-user purple; split-complementary hue layout; fg lightness anchored a full 0.40 from bg (`bg.l ± 0.40`, dim roles at 0.75×, panels ±0.06), direction always *away* from bg on both light and dark; success/warning/error keep conventional hues; **repair pass scores candidate moves by the palette's global weakest pair** (greedy pairwise repair provably cycles on the s/w/e triangle). Test floor: every seed (pure red, pure gray, near-black) generates ≥70 on both backgrounds.

### 7.6 Terminal capability

Detect truecolor vs 256 once (bubbletea downsamples). `EVILCODE_GLYPH_SAFE_MODE=on|off` as an override for terminals whose glyph atlas chokes on continuous color animation (seen on some GPU-accelerated terminals — unlikely on Linux, keep the escape hatch).

### 7.7 Procedural color (identity cues)

**Rainbow prompt numbers** — the strongest identity cue in the transcript. 7-stop ramp indexed by distance-from-newest prompt, blended toward gray `(80,80,80)` with exponential decay:

```
stops: (255,80,80) (255,160,80) (255,230,80) (80,220,100) (80,200,220) (100,140,255) (180,100,255)
color(d) = lerp(gray, stops[min(d,6)], e^(-0.4·d))     // d=0 → full red (newest)
```

**Animated tool color** — ~1.5s sine cycle cyan↔purple:
```
t = sin(elapsed·2.0)·0.5 + 0.5
r: 80→186   g: 200→139   b: 220→255      (lerp by t)
```
Returns flat tool-gray when decorative animations are disabled.

**blend(from,to,t)** = plain per-channel linear lerp; used everywhere.

## §8 Widgets & chrome

### 8.1 Spinner

Frames `⠋⠙⠹⠸⠼⠴⠦⠧⠇⠏` — circular spin (not grow/recede; a test asserts the exact sequence). **12.5 fps** on an 80ms tick (each tick = exactly one frame). When decorative animations are off, the single-cell spinner *keeps* 12.5 fps via a one-cell repaint patch (the UI must never read as frozen), while full-line indicators drop to 1.5 fps.

### 8.2 Status line (stack row 3) — full content matrix, priority-ordered

1. Rate limited → `⠹ Rate limited. Auto-retry in 4m 20s...` all `rgb(255,193,7)`, spinner 4fps.
2. Processing, by state:
   - Sending → `⠹ sending… 3s` (spinner ai-green, text dim)
   - Connecting(phase) → `⠹ {phase}… 3s`; label turns amber on retry or when a single attempt exceeds **10s**
   - Thinking → `⠹ thinking… 3s`
   - Streaming → `⠹ streaming… 12s · 47.3 tps · ↑12k ↓840`; prefix `⚠ 8k cache miss · ` (whole line amber) when KV-cache trouble detected (prompt_eval_count unexpectedly large on a warm session)
   - WaitingForNetwork → `↻ network disconnected, waiting to retry · 8s` amber
   - RunningTool(name) → **knight-rider bar**:
     ```
     ·●· bash ·●·  · reading foo.go · 4s · ⌥B bg
     ```
     two mirrored 3-cell bars; `●` at `filled_pos` sweeping, `·` elsewhere; right bar mirrors (`2−filled_pos`); color = animated tool color (§7.7); tool name BOLD same color; intent + timer dim; `· ⌥B bg` hint `rgb(100,100,100)`. Batch variant: `· 3/7 done, running: read, grep, last done: bash`.
3. Idle + history warning → `⚠ {warning}`; red `rgb(255,100,100)` when tokens ≥3× context or ≥3 compactions, else amber.
4. Idle → rotating tip or blank. Tips: 90s period, visible seconds 28–40, prefix `💡 `, suppressed under 16 cols. Widget-embedded tips cycle every 15s.

Every branch appends ` · +3 queued` in queued yellow when the queue is non-empty. Centered mode centers the whole line.

### 8.3 Info widgets docked in negative space (the signature feature)

The layout engine measures, **per transcript row**, how many trailing columns are blank (both margins in centered mode; right only in left-aligned) and docks rounded boxes into the empty rectangles.

Constraints: width **24–40**, min height **5**. Chrome: rounded border `rgb(70,70,80)` dimmed; only WorkspaceMap gets a title. **A widget that would render no content bails before drawing its border** — never an empty box.

**Anchoring (the anti-jitter mechanism)**: each widget pins `content_top` = the absolute transcript line its top row rides; it scrolls with the text. Phase 1: pinned widgets hold their slot, only shrinking (with hysteresis) or hiding-in-place when a wide line slides under them. Phase 2: re-homing only after the slot has been unusable for **120 consecutive frames**; re-dock against a **look-ahead "reliable width" profile** (columns free across a band of upcoming lines) so a fresh placement isn't covered next frame; fall back to instantaneous widths only if the reliable profile admits nothing.

Priorities (lower = wins) & preferred side: Diagrams 0 R · WorkspaceMap 1 R · Overview 2 R · **Todos 3 R** · ContextUsage 4 R · UsageLimits 5 L · KvCache 6 L · MemoryActivity 7 R · ModelInfo 8 · Compaction 9 L · BackgroundTasks 10 L · GitStatus 11 · SwarmStatus 12 L · AmbientMode 13 L · Tips 14. Left-side widgets only exist in centered mode (only it has a left margin).
Overview-style widgets page when content overflows: cycle pages on a timer, dot row `● ○ ○` (`rgb(170,170,180)` current / `rgb(100,100,110)` other). Widgets are suppressed entirely while idle art is showing.

### 8.4 Todo widget (compact dock form)

Header: `Todos 4/9  ●●●●○○○○○ · confidence 78%` — label `rgb(180,180,190)` bold (label is `Plan` when items are the shared swarm plan); counter `rgb(140,140,150)`; **pip meter** 1:1 pip per todo when it fits (floor 12), else proportional with ≥1 pip per non-empty bucket — `●` done `rgb(100,180,100)`, `●` active `rgb(255,200,100)`, `○` open `rgb(90,90,105)`; pip budget `(width−12)/2` clamp 0–10 compact / 0–14 expanded.
Aggregate confidence = priority-weighted mean (high 3 / medium 2 / low 1, cancelled excluded): 90–100 green `rgb(100,180,100)`, 70–89 amber `rgb(220,190,100)`, <70 red `rgb(220,120,100)`, unknown gray + `?%`. Per-group suffix ` · loop 62%` (closed-feedback-loop score, bands keyed to threshold 96: ≥96 green, ≥76 amber, else red).
Item rows: `⊳` blocked `rgb(180,140,100)` + suffix ` (blocked)` (wins unless completed) · `✓` `rgb(100,180,100)` · `▶` `rgb(255,200,100)` · `✗` `rgb(120,80,80)` · `○` `rgb(120,120,130)`. Text: completed `rgb(100,100,110)` · blocked `rgb(120,120,130)` · in-progress `rgb(200,200,210)` · else `rgb(160,160,170)`. High-priority `!` `rgb(255,120,100)` (expanded only). Per-item ` · 85%`.
Sort: in_progress → pending → completed → cancelled. Group headers `rgb(255,210,130)` bold if any item active else `rgb(170,175,205)` bold; items indent 2. Footer `  +N more` / `  +N done` / `  +N more (3 done)` `rgb(100,100,110)`. Compact budget: `available.clamp(1,5)` lines. Swarm plan statuses (P4) normalize onto the todo vocabulary so live `running` tasks render `▶` and sort first.

### 8.5 Context / usage meters

Two bar styles: segmented pill `▰`/`▱` (quota + context) and solid `█`/`░` (budget bars); track `rgb(50,50,60)`. **Fill color driven by REMAINING, not used**: ≤20% left → red `rgb(255,100,100)`; ≤50% → amber `rgb(255,200,100)`; else green `rgb(100,200,100)`.
Context line: `Context 84k/200k ▰▰▰▰▰▰▰▰▱▱` — label `rgb(140,140,150)`, token counts in band color + bold, pill ≤24 cells.
Quota bar: 7-char right-aligned label, ≥12-cell bar, suffix degrading with width: ` 62% left · 4h 5m` → ` 62% · 4h 5m` → ` · 4h 5m`; exhausted → ` resets 12m` (never `0% left`).
Cost: `💰 $0.0412` (icon `rgb(140,180,255)`, amount `rgb(180,180,190)` bold) + `1.2K in + 340 out` `rgb(140,140,150)`.

### 8.6 Right fact stack (quiet always-on HUD, bottom-right)

Four rows, all `rgb(140,140,150)`, ` · ` separators:
```
ollama-cloud · api-key
qwen3-coder 480b
~/projects/evilcode   main
84k/200k ▰▰▰▰▰▱ 42%
```
Bottom-anchored beside the composer; may climb into ≤4 transcript-tail rows; 2 cells clearance left, 1 cell right pad; context bar 6 cells. **Collision-check against the actual final frame buffer** (cell must be blank symbol + Reset bg + no modifiers); refuse to overwrite the cursor cell; shrink by 1 when the scrollbar shows. The stack is **one visual object**: any collision slides the whole block up a row and re-probes — facts never split. Stands down while the overscroll line shows (it owns the same facts), while processing, and while scrolled up.

### 8.7 Header (top of transcript, borderless, all-dim)

```
evilcode · client · srv↑ · cli↑
server: Crypt ⚰ · v0.3.1
client: Bat 🦇 · v0.3.1
/model to switch · api-key:ollama-cloud · Ollama: qwen3-coder:480b-cloud
/login to add provider
● openrouter
~/projects/evilcode (main)
```
`evilcode` in header_name purple bold; model name in orange (system role); provider dots: `●` colored = authed, dim hollow = unconfigured; everything else dim. Version labels turn amber on client/server mismatch. `mcp: name (12 tools), … +2 more` and `skills: /foo /bar +3 more` lines when present. Unseen-changelog entries render a rounded ` Updates ` box in the top padding (bullets `• `, overflow `  …N more · /changelog to see all`).

### 8.8 Memory activity widget (P3)

4-step bracket pipeline + status line above:
```
Now: searching memories · 3s
╭ Find matches      12 candidates
├ Check relevance   4 above threshold
├ Inject context    820 tok
╰ Update memory     1 saved
```
Badge colors: IDLE `rgb(120,120,130)` · SEARCH `rgb(140,180,255)` · VERIFY `rgb(255,200,100)` · READY/DONE `rgb(100,200,100)` · SAVE `rgb(200,150,255)` · UPDATE `rgb(120,220,180)` · TOOL `rgb(140,200,255)` · FAILED `rgb(255,100,100)`.

### 8.9 Model info widget

```
⚡ Qwen3 Coder 480b  cloud
3 sessions · bat
 ~/projects/evilcode   main
☁ ollama-cloud
```
`⚡` `rgb(140,180,255)`; model name `rgb(255,150,200)` bold; meta `rgb(140,140,150)`; branch `rgb(150,170,140)` with the Nerd-Font branch glyph when available (plain `git:` fallback).

## §9 Streaming text presentation

### 9.1 Markdown rendering

Glamour/v2 with a custom style JSON implementing §7.2, **prose only** — code blocks are extracted and rendered by us (§9.2) for the streaming chrome. Details: bullets `{indent}• ` marker dim, indent 2/depth; ordered markers right-aligned; task lists `[x] `/`[ ] ` dim; blockquotes `│ `×depth dim prefix, soft breaks hardened inside quotes; hrule `─`×24 dim, left-aligned always; links = colored underlined text + ` (url)` dim appended; tables: separator ` │ ` in `#969696`, min col width `(available/cols).clamp(1,5)`; images → `[image: alt] (url)`.
**Caching**: finished messages render once and cache the string; only the streaming tail re-renders per frame (O(len) per frame otherwise — a known ceiling with a known fix).

### 9.2 Code blocks (custom renderer)

```
┌─ go
│ func main() {
│     fmt.Println("hi")
│ }
└─
```
Chrome `┌─ {lang}` / `│ ` / `└─` all `#646464`, always left-aligned (even in centered mode). Body chroma-highlighted, cached by hash(code+lang). Streaming variants: header `┌─ go (streaming...)`, live cursor row `│ ▌`. Offscreen lazy placeholder: `  [go block: 42 lines]` dim italic (render on scroll-into-view).

### 9.3 Inline diffs (transcript)

```
┌─ diff
│ - old line
│ + new line
│ ... 12 more changes ...
└─ (+8 -5 total)
```
Frame dim, forced left. `+`/`-` prefix spans in add/del colors. **Body = chroma-highlight then tint**: `out = (syntax·70 + diff·30)/100` per channel — code keeps its highlighting yet reads unmistakably as add/delete. Long diffs: first half + last half + `│ ... N more changes ...`; footer gains the total. Per-line overflow `…` dim. Diff display modes cycle **Off → Inline → Pinned → File** via Alt+G / `/diff`.

### 9.4 File diff (side panel, File mode)

Gutter `{n:>W} │`, `W = max(digits(total), 3)`: unchanged `{n} │ line`; added `{n} │+line` gutter in add-green; deleted `    │-line` — **blank number** (deleted lines don't exist in the new file), gutter del-red. Body highlighted+tinted as §9.3. Render lazily: only the visible window materializes, memoized; cache ≤8 files keyed (path, msg_index, mtime, size). Active-edit overlay markers in the transcript: first line `→ edit#3 ` file_link bold, continuations `  │ ` file_link.

### 9.5 Tool-call rows

One line per completed call:
```
  ✓ read src/main.go · load entry point · 1.2k tok (+8 -5)
```
Icon `✓` green / `✗` `rgb(220,100,100)` / `⚠` `rgb(214,184,92)` partial. Name tool-gray; model-supplied `intent` after ` · ` (dim separator); technical summary shown only when `show_tool_call_details` is on **or the call errored** (errors always stay diagnosable). Edit tools append `(+8 -5)` (counts in diff colors, parens dim). Token badge ` · 1.2k tok` is **width-protected**: truncation preserves the suffix, squeezes the middle.
Live batch block while running:
```
  ⠹ batch · 3/7 done · last done: read
    ⠹ grep "foo"
    ✗ bash go test
    … 3 completed
```
Completed sub-calls fold into `… N completed` while running; a finished batch collapses to `3/7 succeeded`. Memory recall renders as a tile box: `🧠 recalled 4 memories · 820 tok`, border `rgb(150,180,255)`.

### 9.6 User prompts

`7› what does this function do?` — number in rainbow-decay (§7.7) **on** `user_bg` `#2a2440`; `› ` in user purple on the bg; text `#f8f8f2` on the bg; continuations indent prefix-width, keep the band. The band = the design's only "bubble".

### 9.7 Reasoning / thinking

Streamed thinking renders dim italic (`#646464`), trickling token-by-token; `Thought for 3.4s` footer same style; collapsed on replay to `▸ thought (12 lines)` dim italic. Modes `/thinking-display off|full|current`; `current` GCs offscreen traces (§4.6).

### 9.8 Error rows

`  ✗ {message}` in error red; raw text registered as a copy target (one keystroke to copy).

### 9.9 Retry / recovery messaging

- Rate limit: `⏳ Rate limit hit. Will auto-retry in {n} seconds...` → `✓ Rate limit reset. Retrying... (+{n} queued)`.
- Context overflow: auto-compact → `✓ Context compacted. Retrying...` (emergency variant labeled).
- Model unavailable: **3-second escape-hatch countdown** — `⚠ {model} became unavailable - evilcode will switch to {fallback} in 3 seconds unless you cancel. Reason: {r}. Press Esc to cancel.` Manual variant explains what would be sent and points to `/model`.
- Pending messages carry `auto_retry`; disconnect-resend only fires when set.

## §10 Animations

All decorative animation is gated by a policy tier (off under SSH / `EVILCODE_DETERMINISTIC` / config `idle_animation=false` / per-name config list). When off: tool color goes flat, bars drop to 1.5fps, but the 1-cell spinner keeps 12.5fps via cell-patch (never look frozen).

### 10.1 Idle art (welcome/empty screens; widgets suppressed while showing)

Subpixel ASCII renderer: **3×3 subpixels per cell** (`sw=cols·3, sh=rows·3`); a sampler writes hit/luminance per subpixel; glyph chosen by `shapeChar(pattern uint16, brightness)` from the 9-bit occupancy pattern × 3 brightness tiers — full coverage `@ # %`; 7/9 `# % *`; diagonal `\` or `/` (+ `.`); horizontal band `= - ~` (bottom band `= _ .`); vertical `|`. Color = travelling hue wave: `hue=(elapsed·40 + lum·160) mod 360`, `sat=0.5+lum·0.4`, `val=(0.10+lum²·0.90)·(0.55+coverage·0.45)`, HSV→RGB — the shape rotates while a rainbow flows across at 40°/s.
Variants (per-process pick by hash(start,pid)): **`blackhole`** (accretion-disk parametric rings + gravity-lens arc) and **`eye`** (2D SDF — two eyelid arcs + iris + pupil, slow lid blink every 7–13s, iris hue wave). Classic-torus params if a third variant is ever wanted: r1=1, r2=2, k2=5, aspect 0.5, θ step 0.04, φ step 0.014, rotA=elapsed·1.0, rotB=elapsed·0.5.
Performance: precompute trig tables for fixed angle sequences; repaint **only the art rows** (partial repaint, ~50× cheaper); welcome-screen art height ~18 rows.

### 10.2 Prompt-entry animation (~600ms on submit)

The just-sent prompt's rows: **pulse** fg toward `rgb(255,230,120)` (triangular envelope `t<0.5 ? 2t : 2(1−t)`, ×0.7) + **spotlight** bg toward `rgb(58,66,82)` (`ease_in=1−(1−t)³`, `ease_out=(1−t)²`, `phase=clamp(ei·eo·1.65)`, ×0.85 — rises fast, decays smooth) + **shimmer** band width 0.18 sweeping left→right (`travel=clamp(t·1.15)`, per-span intensity `(1−dist/width)^2.2`, global fade `(1−t)^0.55`, toward `rgb(255,248,210)`, ×0.7, positioned by each span's horizontal center). Net effect: warm flash, spotlight, light sweep.

### 10.3 Others

Knight-rider tool bar (§8.2). Scroll ease-out (§4.1). Tail catch-up (§4.2). Overscroll rebound (§4.4). Widget page dots (§8.3). Swarm strip spinners 12.5fps with **sticky hysteretic stand-down** while the dock widget shows the same agents (P4). Anti-pattern rule (test-enforced): transcript cards must never contain a spinner frame — content being *read* never animates.

## §11 Full keymap

| key | action |
|---|---|
| Enter | Submit / Interleave / Queue (§6.3) |
| Ctrl+J | opposite send mode (one message) |
| Ctrl+Enter / Ctrl+T / Ctrl+Tab | toggle queue mode |
| Shift+Enter / Alt+Enter / trailing `\` | newline (§6.2) |
| Esc | layered cancel (§6.7) |
| Ctrl+C / Ctrl+D | interrupt; idle → quit (twice) |
| Ctrl+T (Ctrl+Tab) | toggle queue mode |
| Ctrl+R | history search (§5.2) |
| Ctrl+Up / Alt+Up | retrieve pending for edit; else prompt history |
| Tab | autocomplete cycle |
| Ctrl+U/K/W/A/E/B/F/Z/X/S | readline edits (§6.5) |
| Ctrl+V / Alt+V | paste (text or image) |
| PageUp/Down · Up/Down (empty input) | scroll 10 · 1 |
| Ctrl+J/K (Ctrl+]/[) | next/prev user prompt |
| Ctrl+Shift+J/K | scroll 1 line |
| Alt+U/D | page up/down |
| Ctrl+G | scroll bookmark |
| Ctrl+5..9 | jump Nth recent prompt |
| Ctrl+1..4 | side panel 25/50/75/100% |
| Alt+C | centered ↔ left |
| Alt+I | info widgets toggle |
| Alt+X | todo card toggle |
| Ctrl+P | auto-poke toggle |
| Alt+G | diff mode cycle |
| Alt+M | side panel toggle |
| Alt+S | typing scroll lock |
| Alt+Y | selection/copy mode |
| Alt+A | quick-copy viewport |
| Shift+Tab | cycle favorite models |
| Alt+←/→ | cycle effort (if provider supports) |

All configurable via `[keybindings]` TOML (names: `scroll_up`, `centered_toggle`, `todo_card_toggle`, `diff_mode_cycle`, `info_widget_toggle`, `scroll_bookmark`, …).

## §12 The plan/todo/discipline system (the harness argues back — implement faithfully)

### 12.1 `/plan` — plan mode as a prompt, plan card as a renderer

`/plan [goal]` is a **one-shot synthetic user turn** — no mode flag, no permission gate. Inject exactly this:

> You are entering planning mode.
>
> Goal: {goal}    *(bare `/plan`: "produce a plan for the task or request currently in focus in this session. If the goal is ambiguous, infer the most useful interpretation from the recent conversation and repo state, and state your assumption.")*
>
> Your job is to produce a clear, concrete, actionable plan. Do NOT implement anything yet: do not edit files, write patches, or change git state. You may freely read, search, run read-only commands, and analyze the codebase so the plan is grounded in how things actually work.
>
> When the plan is ready, present it directly in your reply inside a fenced code block whose language is `plan` (```plan ... ```). The UI renders that block as a dedicated plan card. Structure the plan inside the block with these sections: a top-level `# <short plan title>` heading, then Goal, Scope / affected areas, Approach (concrete ordered steps), Validation (how each part will be verified), and Open questions / decisions.
>
> Keep it tight and high-signal. Avoid speculative rewrites and busywork. After presenting the plan card, stop and wait for the user. Do not start implementing.
>
> Only once the user approves, use the `todo` tool to turn the plan into an executable todo list and then begin the work.

Approval is **conversational** — the plan→execution handoff is todo-tool adoption (the user then sees the todo card materialize). If a turn is running, `/plan` interrupts and queues. Notices: `🧭 Planning {goal}... (plan-only; no edits)` / `👉 Interrupting and planning...`.

**Plan card renderer**: scan assistant content for ```` ```plan ```` segments (fast-path: skip entirely when the substring is absent). Segmentation rules: opening fence must be exactly ```` ```plan ```` with nothing after; **track nested fences** (a ```` ```bash ```` inside the plan toggles a nested flag and must NOT terminate the card — its body renders inside the borders); an opening plan-fence inside another fence is ignored; **an unterminated fence renders anyway** — the card materializes and grows during streaming instead of popping in at the end. The streaming behavior is what sells it.
Card: rounded box, border violet `rgb(158,135,255)`; width `clamp(w−4, 28, 100)`, inner = width−4; title = first `#`/`##`/`###` heading in the body, rendered `⛭ {title}` centered in the top border, and that heading line is removed from the body; body = markdown-render then **hard-wrap BEFORE boxing** (the box truncates; unwrapped text would clip at the border); trim blank rows; empty body → dim `(empty plan)`.

### 12.2 Todo data model (3 levels; separate JSON files per session)

```
~/.local/share/evilcode/todos/{session}.json         []TodoItem       (bare array)
                             {session}-goals.json    []TodoGoal
                             {session}-plan.json     TodoPlan
                             {session}-gates.json    []GateObservation (turn-scoped)
```

**TodoItem**: `content, status(pending|in_progress|completed|cancelled), priority(high|medium|low), id, group *string, blocked_by []string, confidence *uint8, completion_confidence *uint8, confidence_history []uint8`.
**TodoPlan** (one/session): `user_intention *string, understands_user_intent *uint8 (0-100), understands_user_intent_history []uint8`.
**TodoGoal** (one per group; nil group = the flat list): `closed_feedback_loop *uint8, feedback_loop *string` (the concrete command + metric, e.g. `go test ./internal/auth/...`), `end_to_end_ownership *uint8`, + histories. Rationale for goal-level (not item-level) scoring: *"optimize grep latency" can close its loop because progress has a metric; "design an onboarding screen" cannot; "read the auth code" has no meaningful score of its own.*

**Histories are tool-owned, append-only. Model-supplied history fields are discarded. Each todo-tool write contributes AT MOST ONE observation per score** — a single completion update cannot manufacture an apparent intermediate step. The trail distinguishes an evidence-driven rise (75→85→95→100) from a bulk end-stamp (75→100).

**Confidence spike** = a completed todo whose final recorded increase ≥ **15**: empty history → compare `confidence` vs `completion_confidence`; single entry → never a spike; else `hist[n−1] − hist[n−2] ≥ 15`.

### 12.3 Quality gates — "deferred, not forgiven"

Thresholds: quality gate **96** (both intent-understanding and closed-feedback-loop); severe intent misunderstanding **60**.

Low scores during the turn do NOT nag per-write (that punishes the healthy low-then-rising pattern and burns reasoning re-justifying the plan). Instead each qualifying write appends `GateObservation{kind: intent|loop, group, score}` (cap 256, oldest dropped). **One exception fires immediately**: the *first* plan write with intent < 60 — the agent is admitting it doesn't know the task; a whole turn of wrong work can't be undone at turn end.

At turn end, `buildGateDigest` collapses repeats (one line each + count); wording selected by trajectory:
- intent never cleared: *"your understanding of what the user actually wants never became solid. Re-read the request, confirm the work you did matches it, and state any interpretation you had to guess at."*
- intent cleared late: *"you started this work without understanding what the user actually wants, and only settled it later. Re-check the work you did before it settled against the request you now understand."*
- loop never closed: *"the goal{ for \"X\"} never closed its feedback loop: no observation reported back on whether the work satisfied the requirements. Confirm the result is actually better, with concrete evidence rather than inspection."*
- loop closed late: *"the goal{ for \"X\"} was worked on before its feedback loop was closed, so the loop you ended up with never ran over that earlier work. Run it over the whole result now and report what it actually reported back."*

Digest prefix: `[automated todo quality review - not a user message] Before you treat this turn as finished, double-check the weak points it surfaced. Do not reply conversationally or wait for the user.`

**One hard-blocking gate**: a write that completes a group is **REJECTED** (stored list unchanged, explanation in the tool result) unless that group's `end_to_end_ownership ≥ 96`.

### 12.4 Auto-poke — the turn-end decision tree (post-turn hook)

Config `features.auto_poke` default true; toggle Ctrl+P / `/poke on|off|status`.

```
consecutive-refusal breaker tripped? → disarm everything, explain
incomplete todos exist?
  → system line: "👉 3 incomplete todos. We poked it for you. /poke off to stop."
  → queue: "[automated todo completion gate - not a user message] You have 3 incomplete
     todos. Continue working, or update the todo tool if the list is stale.
     Do not reply conversationally or wait for the user."
  → reset gate-attempt counter (open todos = still iterating)
no todos at all? → disarm silently (log the decision)
all complete, in order:
  1. unresolved gate observations → "🔎 We asked the agent to double-check this turn's
     weak points." + queue the digest (ONCE per completion cycle)
  2. weak completion confidence (priority-weighted avg < threshold, or any missing, or any
     item below threshold; weights 3/2/1, round half-up)
     → "🛑 The agent marked its work done without strong enough validation. We asked it
        to double-check." + queue continuation
  3. unchallenged confidence spike → "🛑 The agent's confidence jumped suddenly. We asked
     it to verify that independently." + queue continuation
  4. gate budget exhausted (default 3 attempts) → "⚠ We nudged the agent several times but
     its validation still isn't holding up. We stopped poking; review the remaining todos
     yourself." + disarm
  5. all good → "🦇 All rites complete. Completion confidence: 97%." + disarm + reset
     counters (re-arms on next todo write)
```

**Poke messages persist as user-role** (the model sees a normal continuation) but **render as system lines**: recognize the `[automated …]` prefix on load/replay/attach and map to a system display role. Every continuation contains "Do not reply conversationally or wait for the user" (models otherwise answer the reminder instead of acting).

### 12.5 Todo UI surfaces (five)

**1. Inline chat card** (`/todos` / Alt+X; one card only — re-toggle moves it to the bottom; dismiss when it's the trailing message; live-refresh by content hash):
```
Understands user intent 87%
  User intention · Ship the plan-level intent gate so low-confidence plans get
                   re-examined before work starts
auth flow  ●●●○○
  Closed feedback loop 92% · Ownership 88%
  Feedback · go test ./internal/auth/...
  ✓ Read the OAuth callback handler · 75→100%
  ● Wire the refresh path · 82%
  ⊳ Add the retry gate · 60% (blocked)
  ○ Write the integration test
other  ●○
```
Groups in first-seen order, ungrouped bucket last; no groups → flat list under a bare pip header. Glyphs/colors: `⊳` `rgb(225,165,90)` · `✓` `rgb(105,190,125)` · `●` in-progress asap-cyan · `✗` `rgb(190,105,115)` · `○` `rgb(135,145,160)`. Text: completed `rgb(135,150,145)` · cancelled `rgb(145,130,135)` · in-progress `rgb(225,232,240)` (brightest) · pending `rgb(195,202,212)`. **Completed items whose planning ≠ completion confidence show `75→100%` — the arrow is the anti-gaming tell.** Width: centered `min(w−4,120)`, left `min(w−2,100)`; no border; indent 2. Empty: `No tasks yet. The model populates them as work is planned.` (Card metadata colors are deliberately brighter than global dim — cards sit on the bare background; cool colors = structure, amber = priority/blocked.)

**2. Assessment-delta card** — when a write only changes scores, render the delta, not the whole plan:
```
Plan  updated
  Understands user intent 72% → 91%
auth flow  updated
  Closed feedback loop 80% → 96%
```
`→` label-color, old value meta-gray, new value score-green.

**3. What-changed delta under each todo tool call** — display-only, zero token cost; recover the previous list by scanning the transcript backward for the last todo *write* (skip reads) — reload-safe with no threaded state.
Form A (single status flip or lone edit): `  ↳ ▶ Wire the refresh path  (2/7)` or `  ↳ 1 started · 1 edited  (2/7)`.
Form B (first write, adds, removes, or >1 status change):
```
  ↳ 2 done · 1 started · 3 added  (5/9)
    ✓ Read the OAuth callback handler
    ▶ Add the retry gate
    + Write the integration test
    +2 more
```
≤6 item lines then `+N more`; `+` add-green, `-` remove-red, status icons for flips, gray icon for content edits; summary segments in order done · started · reopened · cancelled · added · removed · edited, fallback `updated`.

**4. Dock widget** — §8.4.

**5. Pinned band** (`/todos pin`, default off): full list pinned atop the transcript; refresh on a 1s throttle with hash change-detection; immediate refresh on toggle.

### 12.6 Circuit breakers (every self-re-prompting path needs one)

| guard | behavior |
|---|---|
| consecutive provider refusals | a refusal is deterministic for the same request → re-poking loops forever (observed in the wild: one refused call every ~7s). After N consecutive: disarm poke + overnight, log why |
| gate attempts | max 3 nudges without progress → stop poking, tell the user |
| non-retryable errors (auth, bad model) | stop auto-poke instead of spamming |
| connectivity errors | explicitly RETRYABLE — route to wait-for-network, never treat transient as permanent |
| plan/observation caps | todo plan ≤1024 items; gate observations ≤256 (coordination state, not a log) |
| advisor (P5) | ≤1 injection per turn, hard mute switch |
| swarm (P4) | spawn depth ≤2, live workers ≤6, worker wall-clock timeout |

## §13 Slash command registry

`RegisteredCommand{Name, Help, Hidden}` table. Final set (phase-tagged rollout):

**Core**: `/help /? /commands /model /models /refresh-model-list /clear /cls /compact /rewind /checkpoint /context /info /version /changelog /config /quit /fix /cancel`
**Flow**: `/plan /todos /poke /diff /alignment /thinking-display /tool-call-details /effort`
**Sessions**: `/resume /graveyard /sessions /session /rename /save /unsave /fork /transfer /catchup /back /active`
**Providers**: `/auth /login /logout /account /usage /cache`
**Memory (P3)**: `/memory /initiatives`
**Swarm (P4)**: `/swarm /summon /agents /swarm-prompt /observe /subagent-model`
**System**: `/reload /restart /rebuild /selfdev /update /keys /hotkeys /colors /theme /terminal-setup /skills /git /log /lsp (P5) /advisor (P5)`
**Self-test**: `/screenshot /screenshot-mode /record /debug-visual /onboarding-sim /smoothness`
**Fun (P5)**: `/overnight /productivity /wrapped /dictate /btw`
Hidden aliases: `/todo /color /keybindings /clear-view /resume-all /z /zz`.

`/fix` = recovery prompt when the model stalls. `/rewind` = numbered history + `/rewind N` (prunes exploratory context; durable state — todos, memories — survives). `/checkpoint` = mark the current point; later `/rewind` can collapse back to it and inject a one-paragraph summary of what happened since (collapse-and-report). `/btw` = side-question in the side panel without touching main context. `/smoothness` = report from the anchor-stability recorder (hash transcript rows per frame; classify off-anchor motion, downward pushes, single-frame pops, mass reflows, excluding expected motion — the objective "screen never jumps" metric).

## §14 Probe rig (agent self-testing, incl. visuals)

- `probe/probe.sh`: tmux driver. Boot: `tmux -L evilprobe new-session -d -x 140 -y 40 "env HOME=$FAKEHOME TERM=xterm-256color COLORTERM=truecolor EVILCODE_DETERMINISTIC=1 EVILCODE_PROVIDER=mock ./evilcode"`. Subcommands `boot / keys "<keys>" / frame <name> / png <name> / kill`. `TMUX_TMPDIR` must be short (unix socket 108-char path limit). Capture: `tmux capture-pane -e -p` (ANSI) and without `-e` (plain text for goldens).
- `internal/ansirender`: ANSI→PNG. Parse SGR: 0,1,2,3,7,22,27, 30–37/90–97, 40–47/100–107, 38;5;n / 48;5;n (256-color: 16 base, 6×6×6 cube `v = 0 | 55+40x`, grayscale `8+10n`), 38;2;r;g;b / 48;2, 39/49 defaults. Grid-render with an embedded monospace TTF (bold variant for bold). Default bg `rgb(18,18,24)`.
- **Mock provider** (`provider/mock.go`): deterministic canned streams (text deltas, tool calls, a ```` ```plan ```` block chunked mid-fence, a diff-producing edit) selected by scenario env var — TUI tests never need a live model.
- **Golden tests** (build tag `probe`): scenario files = key sequences + capture points; diff plain-text frames vs `probe/goldens/`; `UPDATE_GOLDENS=1` rewrites. PNGs are for the agent's own eyes (loop step 4) — goldens catch regressions, eyes catch ugliness.
- In-app cooperation: `/screenshot` (dump ANSI+PNG of the current frame to `~/.local/share/evilcode/shots/`), `/screenshot-mode` (auto-dump on frame change), `/record` (frame sequence), `/debug-visual` (paint layout rect overlays), `/onboarding-sim` (walk welcome screens), `/smoothness` (§13).

---

# PART III — Systems specification

## §15 Agent core

**Events** (`internal/agent/events.go`): `TurnStart · TextDelta · ReasoningDelta · ToolStart{Call} · ToolResult{Call, Output, Err, DiffStat} · TokenUsage{In, Out, CtxUsed, CtxMax} · Notice{Level, Text} · TurnEnd{Reason} · Error{Err}` — every event carries session id + seq. The channel is the contract between core and all three frontends.

**Loop** (`agent.go`): a plain function, not an actor system.
```
for {
  req := buildRequest()                      // full message list, tools, model (role-routed)
  stream := provider.ChatStream(ctx, req)
  accumulate text deltas + tool calls        // don't assume deltas precede tool calls
  if toolCalls:
      run tools (concurrent for batches, bounded; emit ToolStart/ToolResult)
      append tool results
      drain interleave queue (safe point D; urgent at C with stub results)
      continue
  drain interleave queue (safe point B)
  hooks.PostTurn()                           // auto-poke / gates / advisor — may append
  if nothing appended: emit TurnEnd; return
}
```
Cancellation via context; interrupt keeps accumulated partial text as an assistant message. Retry policy: 429/5xx/network → exponential backoff + Notice; 401/bad-model → surface and stop; rate-limit → countdown auto-retry.

**Context assembly** (`context.go`): system prompt = identity + tool guidance + AGENTS.md/CLAUDE.md (search order: cwd → git root → `~/.config/evilcode/`) + skills index (names + one-liners only; bodies via `skill` tool — the prompt-cache-friendly pattern). Target < ~1200 base tokens (a lean harness ships under 700). Dynamic state enters as tail messages only (invariant 2).

## §16 Providers & model routing

Interface: `ChatStream(ctx, Req) (<-chan Chunk, error)` · `Embed(ctx, []string) ([][]float32, error)` · `Models(ctx) ([]ModelInfo, error)`. Own structs for messages/tool-calls; map at the provider edge.
- `ollama.go`: `POST {base}/api/chat` `{model, messages, tools, stream:true, options:{num_ctx}}`; NDJSON lines: `message.content` delta, `message.tool_calls` (may arrive whole), `done`, `prompt_eval_count`/`eval_count` (feed TokenUsage + cache-miss detection §8.2). Bearer header when key set. Serves localhost AND ollama.com identically.
- `openai.go`: `POST {base}/v1/chat/completions` SSE; `data:` lines, `[DONE]`; tool_call deltas accumulate by index. Covers OpenRouter/DeepSeek/anything.
- Config: `[[provider]]` blocks `{name, kind: ollama|openai, base_url, api_key_env}`; model refs `model@provider`; `default_model`; env `EVILCODE_MODEL` override. Optional per-model `context_window` override. `lenient_tool_parse` flag enables a JSON-in-text tool-call fallback parser (off by default).
- **Role-based model routing**: `[roles]` TOML table maps roles → model refs with per-role fallback chains: `default` (main loop) · `smol` (cheap side-calls: memory extraction, session titles, digests, commit messages) · `plan` (used by `/plan`) · `commit`. Example: `smol = ["qwen3:8b@ollama-local", "gpt-oss:20b-cloud@ollama-cloud"]`. **Every internal side-call goes through `smol`** — never burn the big model on ambient work. Per-repo pinning: a `[roles]` block in `.evilcode.toml` at the repo root overrides.
- **Fallback offer**: on model failure with a configured fallback → `Fallback available: press Ctrl+Y to switch to {model} and resend.`

## §17 Tools

`Tool{Name, Desc string; Schema json.RawMessage; Run func(ctx, json.RawMessage) (string, error)}` — a slice, not a registry. Results > 50KB truncated with a note. Set: `read` · `write` · `edit` · `glob` · `grep` (shell out to `rg` — never reimplement ripgrep) · `bash` (timeout, cwd persistence, `background:true` → background task registry, completion = event + BackgroundTasks widget) · `ask` · `todo` (§12.2 schema; description teaches confidence/intent/loop reporting) · `skill` (lazy body load) · git tools · P3: `remember`/`recall`/`reflect` · P4: `send_message`/`broadcast`/`spawn_worker` · P5: `lsp`. MCP tools adapt into the same struct. Edit/write tools compute DiffStat and return the diff for §9.3 rendering.

**`edit` — hash-anchored lines (kills string-not-found retry loops).** `read` output prefixes every line with a short content hash: `a3f2|417| func main() {`. `edit` accepts anchor patches `{anchor: "a3f2", op: replace|insert_after|delete, lines: [...]}` alongside classic exact-string replace. Stale anchors (file changed since read) are detected and **rejected before corruption** with a "re-read the file" error. This eliminates edit-retry loops and saves output tokens (the model points at an anchor instead of retyping context). Classic mode remains for models that can't handle anchors; per-model config picks the default.

**`ask` — structured question tool.** The agent asks the user a question with typed options: `{question, options: [{label, description}], multi: bool}`. Renders as an inline picker (chrome of §5.3: rounded box, `▸` cursor, white-on-`#3c3c50` selection); keyboard nav + Enter; returns the chosen label(s); free-text "Other" always available. While pending, the composer is the answer box (§5.1 suppression).

**Git tools** (read-only helpers so the model stops burning bash calls): `git_overview` (branch, staged/unstaged counts, recent commits) · `git_file_diff{path}` · `git_hunk{path, n}`. Output formatted for §9.3 rendering.

**`lsp` (P5)** — generic LSP client speaking to configured servers (gopls preconfigured): ops `diagnostics · definition · references · hover · rename · symbols`. Rename applies workspace edits atomically and reports every touched file as a DiffStat. Config `[lsp]` maps language → server command.

## §18 Sessions

JSONL at `~/.local/share/evilcode/sessions/{name}.jsonl` — one envelope/line `{ts, type: user|assistant|tool|meta, data}`; append on every event; meta records model switches, token totals, goal scores, checkpoints. Resume = replay. Creature names §2.2. `/fork` = copy file + new name; `/transfer` = compact into a summary handoff + copy todos into a fresh session; `/save` pins (📌 in picker); crash detection = lockfile + clean-exit marker → crash banner in picker. `/checkpoint` writes a named meta marker; `/rewind` prunes back to a numbered point or checkpoint and injects a one-paragraph collapse-and-report summary (durable state survives). Prompt history §5.2/§6.1.

## §19 Memory (Phase 3)

sqlite-vec DB `~/.local/share/evilcode/memory.db`: `memories(id, text, kind: fact|preference|project|episode, session, ts)` + vec0 table (768-dim cosine). Pipeline async off the hot path; embed failures degrade silently (memory never blocks the loop). Passive recall: embed incoming user msg → top-4 above cosine ~0.55 (tune) → one `<memories>` tail message → renders as the 🧠 tile (§9.5). Explicit tools: `remember{text, kind}` · `recall{query}` · `reflect{question}` (synthesize an answer over the memory bank via the `smol` role). Ambient extraction every 8 turns (smol side-call: "extract durable facts worth remembering, JSON array, empty if none") → dedupe vs existing (cosine >0.95 = merge) → store. Consolidation on session close. Session RAG: per-session summary embeddings searchable from the picker. `/memory on|off|status`. MemoryActivity widget §8.8.

## §20 Daemon & swarms (Phase 4)

`evilcode serve`: one process, N sessions, NDJSON frames over `$XDG_RUNTIME_DIR/evilcode.sock`. Client→server: `attach|input|interrupt|spawn|list`; server→client: serialized agent Events + session snapshots. Event ring buffer per session for reconnect replay; JSONL on disk is the source of truth. `evilcode attach [name]`: the same TUI with the socket as its event source (invariant 1 payoff); header shows `server: Crypt ⚰ · client: Bat 🦇`. Headless workers = serve-sessions without clients; `evilcode run --remote` submits into the daemon.
Swarm coordination is all in-process (maps + mutexes): per-session file read/write registry (tools report paths) → agent B writes what A read this turn → inject to A: `⚠ bat modified auth.go which you read at turn 12` (delivered at safe point D); `send_message`/`broadcast` routed through the daemon; `spawn_worker{task, files_hint}` (`/summon`) creates a headless session and reports completion back to the spawner as a message; worker results are **schema-validated JSON** (spawner supplies a JSON Schema; the worker's final output must validate — no prose parsing). Shared plan = shared todo group namespace; SwarmStatus widget (left dock): `⠹ bat · wiring auth · 42s` per live agent, sticky hysteretic stand-down vs the swarm strip. Compact-notifications toggle collapses file-activity notices to one line. Breakers §12.6.

## §21 Advisor (Phase 5, optional but specced)

A second, cheap model (the `smol` role) watches the conversation: after each turn it receives a compressed view (last user msg, last assistant summary, todo state) and answers "any concern worth raising? one sentence or NONE". Non-NONE answers inject as a **system-source soft interrupt** at safe point D: rendered as `ⓘ advisor: {concern}` dim line; the model sees it as a system continuation. Hard limits: ≤1 injection per turn, `/advisor on|off|status`, and it never fires while auto-poke is mid-cycle (one arguing voice at a time). This is a cheap conscience, not a second driver.

---

# PART IV — Phases (each ends daily-drivable; tasks reference spec §)

## Phase 0 — Bootstrap + probe rig (the rig comes FIRST)

- [x] `git init`; `go mod init evilcode`; skeleton dirs; `.gitignore` (binary, probe/frames/, shots/).
- [x] Seed process files: `plan.md` (this file), `LOOPS.md` (first entry), `README.md` (skeleton per §0.2.8), `DEVIATIONS.md`. Commit.
- [x] Verify the codex review skill is callable (smoke review on the seed commit, background); record the exact invocation in `LOOPS.md`.
- [x] `internal/ansirender` per §14. Unit tests: SGR parse cases (truecolor, 256-cube, reverse video, bold).
- [x] `probe/probe.sh` + `evilcode probe` subcommand per §14.
- [x] Golden-test harness (build tag `probe`) + `UPDATE_GOLDENS=1` flow + `probe/README.md` (≤30 lines).
- [x] Verify: a hello-world bubbletea program boots under probe; frame → PNG; golden passes; regeneration works.

## Phase 1 — Drivable core

- [x] Provider interface + `ollama.go` + `openai.go` + `mock.go` (§16). Tests: NDJSON/SSE parsing fixtures, tool-call accumulation, whole-buffered tool calls.
- [x] Config (§16, §1.4 paths): TOML load, env overrides, provider registry, `default_model`, `-m` flag.
- [x] Tools: read/write/edit (classic mode)/glob/grep/bash + batch concurrency + DiffStat (§17).
- [x] Agent core: events, loop, safe-point interleave drain (mechanism only — UI in P2), retries, context assembly, AGENTS.md/CLAUDE.md (§15).
- [x] Sessions: JSONL store, creature names, `--resume <name>`, simple resume list (§18). Prompt-history recording (§6.1).
- [x] `evilcode run "prompt"`: headless one-shot printing events (deltas raw; tool lines `✓ read foo.go · 1.2k tok`); exit code by outcome.
- [x] TUI skeleton: layout stack + **packed-vs-scrolling** (§3.2) + left inset (§3.4).
- [x] Transcript: message blocks, render caching, user-prompt bands + rainbow numbers (§9.6, §7.7), tool rows (§9.5), error rows (§9.8).
- [x] Markdown: glamour style JSON (§9.1) + custom code blocks with streaming chrome (§9.2).
- [x] Inline diffs with tint formula (§9.3).
- [x] Composer: textarea, prompt glyphs, hint line, newline paths, `!` shell prefix, paste rules (§6.1, §6.2, §6.6).
- [x] Status line: full matrix incl. knight-rider bar + spinner discipline (§8.2, §8.1).
- [x] Header (§8.7, minus changelog box).
- [x] Slash palette overlay: zero-height float, ranking, fuzzy recolor, suppression, P1 command subset (§5.1, §13).
- [x] Model picker (§5.3).
- [x] Scrolling: momentum + ease-out + tail-follow + keyboard nav + scrollbar with hysteresis + full-repaint-on-scroll (§4.1–4.3, §3.5–3.6, §4.7).
- [x] Interrupt: Esc/Ctrl+C basic paths (§6.7 minus poke interactions).
- [x] Welcome screen: `Welcome to evilcode 🦇`, `◖ suggestion chips ◗` rotating, static eye placeholder (animation P2).
- [x] Probe scenarios + goldens: welcome, mock chat turn, palette open/filter, picker, tool row, diff, packed→scrolling transition. **Look at every PNG.**
- [x] Verify: `evilcode run` against local ollama AND cloud (with `OLLAMA_API_KEY`); a real multi-file edit interactively; goldens green. Tag `phase-1`.

## Phase 2 — The soul: plans, todos, discipline, polish, fun

- [x] Todo model + persistence + history merging + spike detection (§12.2). Table-driven tests for merge/spike edge cases.
- [x] `todo` tool (§17) + gates (§12.3) incl. the hard ownership gate + digest texts. Tests: deferred accumulation, immediate <60 fire, digest wording selection.
- [x] Auto-poke hook + full decision tree + breakers + hidden-poke rendering (§12.4, §12.6). Test: each branch with a scripted todo state.
- [x] `/plan` command + prompt injection + plan card renderer with nested/unterminated fence rules (§12.1). Probe scenario: mock provider streams a chunked plan fence — the card grows live.
- [x] Todo UI surfaces 1–3 + 5: inline card, assessment delta, Form A/B tool delta, pinned band (§12.5).
- [x] **Hash-anchored `edit` mode** + hashed `read` output + stale-anchor rejection (§17). Tests: anchor apply, stale reject, classic fallback.
- [x] **`ask` tool** + inline option picker rendering (§17, §5.3 chrome).
- [x] **Git tools** `git_overview/git_file_diff/git_hunk` (§17).
- [x] **Role-based model routing** `[roles]` + smol side-call plumbing + per-repo pinning (§16).
- [x] Send model: SendAction, queue mode, interleave staging + pending rows + Ctrl+Up retrieval (§6.3, §6.4). Write `docs/soft-interrupt.md`.
- [x] Ctrl+R history search overlay (§5.2).
- [x] Esc layered cancel with poke-disarm semantics (§6.7).
- [x] Full keymap + configurable bindings + hotkey feedback/near-miss (§11, §6.8).
- [x] Command registry completion: `/help` overlay + anti-drift + `/help <cmd>`, argument completion, remaining P2 commands (§5.5, §13).
- [x] Widget dock engine + anchoring/hysteresis (§8.3); widgets: Todos, ContextUsage, ModelInfo, GitStatus, BackgroundTasks, Tips (§8.4, §8.5, §8.9).
- [x] Right fact stack (§8.6). Elastic overscroll (§4.4). Typing scroll lock (§4.5).
- [x] Centered mode: 96-col cap, centering by literal left-padding (not per-line Center alignment — keeps copy/column math sane), per-role exemptions (tool/system/code-block rows stay left), left-side widget margin only exists here, cursor offset math, images split slack both sides, part of the render cache key, `/alignment` persist.
- [x] Theme engine: roles, palettes (dracula/nosferatu/gloom/daywalker), two-pass substitution, literals file + tests (§7.1–7.4).
- [x] Harmony scoring + generation + `/theme score|generate` + calibration tests (§7.5).
- [x] Prompt-entry animation (§10.2). Idle art: subpixel renderer + `eye` + `blackhole` (§10.1).
- [x] Reasoning display modes + GC (§9.7, §4.6).
- [x] Diff modes cycle + side panel + file-diff view (§9.4, §3.1, Ctrl+1..4).
- [x] Session picker full UI (§5.4) + `/fork /transfer /save /rename` + title derivation (§18). `/checkpoint` + collapse-and-report `/rewind` (§18).
- [x] Skills system + MCP client (§15, §17) + header mcp/skills lines.
- [x] `/compact` with context_epoch. `/fix`. `/btw` (side-panel side-question).
- [x] Self-test commands: `/screenshot /screenshot-mode /record /debug-visual /onboarding-sim /smoothness` (§14, §13).
- [x] Background bash UI: `⌥B bg` hint, BackgroundTasks widget, completion notices.
- [x] Probe scenarios: todo card w/ arrows, plan card streaming, pending rows (all 3 kinds), ask picker, centered toggle, docked widgets, overscroll, theme switch, help overlay scroll %, session picker, prompt-entry animation frames, idle eye. **Look at every PNG.**
- [x] Verify: 3-item task → auto-poke fires on early stop; low loop score → gate digest at turn end; `75→100%` arrows visible; ownership gate rejects a premature group close; anchor edit round-trips. Tag `phase-2`.

## Phase 3 — Semantic memory

- [x] Store + schema + vec search (§19). `remember`/`recall`/`reflect` tools. Passive recall + tile render. Ambient extraction (smol role) + dedupe. Consolidation. Session RAG in picker. MemoryActivity widget (§8.8). `/memory`. Tests: recall ranking fixture; probe: recall tile + widget states.
- [x] Verify: remember a fact → new session → passive recall injects it. Tag `phase-3`.

## Phase 4 — Daemon + swarms

- [x] Daemon + socket protocol + ring-buffer replay (§20). `attach` (TUI over socket). Headless workers + `run --remote`.
- [x] File-conflict registry + notices; agent messaging; `spawn_worker` with schema-validated results + `/summon`; shared plan groups; SwarmStatus widget + swarm strip w/ stand-down hysteresis; compact-notifications; breakers.
- [x] Probe: serve + 2 attached clients (two tmux panes) + worker editing shared repo → conflict notice golden.
- [x] Verify: kill terminal → attach resumes live session; conflict notice fires; `/summon` completes a task with validated JSON output. Tag `phase-4`.

## Phase 5 — Graphics, intelligence extras, graduation

- [x] Kitty graphics protocol images (+ sixel fallback); `Alt+Shift+I` toggle; centered/left slack rules.
- [x] Mermaid: ```` ```mermaid ```` → shell to `mmdc` → PNG → kitty inline/side panel; absent → styled source + `↻ mermaid (render requires mmdc)`. **Do not write a renderer.** Diagram pane position/zoom/pan keys (§11, §3.1).
- [x] **`lsp` tool** (gopls first) per §17. `/lsp status`.
- [x] **Advisor** per §21.
- [x] Shell completions: generated bash/zsh/fish completion scripts (`evilcode completions <shell>`) covering subcommands, flags, model names, session names.
- [x] `/productivity` (stats dashboard → PNG via ansirender). `/overnight` (supervised long-run loop over todos with hard budget caps — breakers prerequisite). `evilcode dictate` (configured STT command → composer).
- [x] Panel slide-in via harmonica (only if panels feel dead without it).
- [x] **`/selfdev` — graduation**: evilcode opens a session on its own repo with a skill encoding §0.2 (build → test → probe → PNG check → commit → codex review); `/rebuild` = build+test+restart; `/reload` = re-exec preserving session (exec + `--resume`). From here evilcode develops itself and the external agent retires.
- [x] Final polish audit: run every probe scenario, look at every PNG, cut anything reading as clutter, sweep the lexicon rule (§2.1).
- [x] Verify: mermaid renders as an image in kitty; a pasted image displays; `/productivity` emits a PNG; an `lsp` rename lands atomically; a `/selfdev` session completes one real task end-to-end. Tag `phase-5`.

---

# PART V — Gotchas ledger (hard-won lessons; each was a real production bug somewhere)

- Ollama models vary wildly at tool calling: qwen3-coder strong; deepseek quirky; small local models emit JSON-as-text (that's what `lenient_tool_parse` is for). Daily default: `qwen3-coder:480b-cloud`.
- Some models buffer the entire tool call before emitting — never assume text deltas arrive first.
- Streaming markdown re-render is O(len) per frame — cache finalized messages immediately.
- `⚠️` VS16 is repaint-unstable → normalize to `⚠`. Any multi-codepoint glyph in a repaint-patched cell is a bug.
- Wide-grapheme trailing cells ghost in kitty/foot with diff renderers → full repaint on scroll (not clear-screen; ED2 flickers images).
- A model refusal is deterministic for the identical request: re-poking loops forever. Count consecutive refusals, disarm.
- Esc-interrupt disarms auto-poke ("stop" means stop); Ctrl+C interrupt does not ("skip", not "stand down").
- Bracketed paste must never sniff the clipboard for images (Wayland multi-MIME). Image paste = explicit Ctrl+V only.
- Any layout decision that feeds back into layout (scrollbar↔wrap width, strip↔dock visibility) oscillates without hysteresis.
- Shift+Enter needs the kitty keyboard protocol; keep the trailing-`\` fallback forever; `/terminal-setup` prints tmux/WezTerm fixes.
- tmux probe: unix socket path ≤108 chars → short `TMUX_TMPDIR`.
- sqlite-vec/ncruces: register the vec extension before any query; degrade memory features gracefully if init fails.
- Nerd Font glyphs (` 󰌘 󰆍 󰖟`): use only with a plain-ASCII fallback path.
- Continuous color animation can garble some GPU terminals' glyph atlases — keep `EVILCODE_GLYPH_SAFE_MODE`.
- Stale edit anchors must be rejected loudly, not fuzzily matched — silent best-effort application corrupts files.

# Definition of done

`evilcode` is the daily driver: boots in <100ms to a welcome screen with a blinking ASCII eye and `◖ suggestion chips ◗`; streams qwen3-coder-cloud through styled markdown with rainbow prompt numbers, a knight-rider tool bar, and widgets docked in negative space that never jump; plans arrive as violet `⛭` cards that grow while streaming; the harness argues with the agent's confidence (`75→100%` arrows, gate digests, auto-poke with breakers); edits are hash-anchored and never loop on string-not-found; memory recalls your quirks in a 🧠 tile; `serve`/`attach` survives terminal restarts; swarms coordinate with file-conflict notices and schema-validated worker results; `/selfdev` lets it build itself — and every claim above is backed by a probe scenario whose PNG an agent has actually looked at, a LOOPS.md trail, and a codex review on every commit.
