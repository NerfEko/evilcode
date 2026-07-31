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
