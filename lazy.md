# lazy.md — lazy feature implementations in evilcode

**Peer checkouts (recorded here so `/tmp` clearing does not make citations unverifiable
again):**
- **jcode** — `/home/eko/src/jcode`, upstream `https://github.com/1jehuang/jcode.git`
  (Rust workspace, ~80 crates). Re-clone: `git clone https://github.com/1jehuang/jcode.git /home/eko/src/jcode`.
- **oh-my-pi** — was `/tmp/oh-my-pi`, fork of Pi (~55k LOC Rust core + TS packages). It
  is gone from `/tmp`; its upstream was not recoverable from shell history (only
  `curl https://jcode.sh/install` and `jcode` appear in `fish_history`). Re-fetch from
  source before citing oh-my-pi again, and record the URL here when you do.

Reviewed against **oh-my-pi** (per above) and **jcode** (`/home/eko/src/jcode`). Each entry cites
the evilcode file, the peer implementation, and the specific way evilcode shipped thin
where a peer went deep. Ordered roughly by impact.

Features where evilcode is **already on par or better** are noted at the end so this
isn't a one-sided list — the todo discipline, the swarm file-conflict registry, anchored
edits, session rewind/transfer, the advisor, and the theme/screenshot machinery are
genuinely strong and not listed below.

---

## 1. Model providers — two protocols, hand-wired, no auth

**evilcode:** `internal/config/config.go:25-31` defines exactly three `ProviderKind`:
`ollama`, `openai`, `mock`. `internal/provider/{openai,ollama}.go` are the only two real
clients. There is no Anthropic Messages client, no Gemini, no Bedrock/AWS sigv4, no
Vertex/Azure, and no OAuth/device-flow providers (Copilot, Cursor, Antigravity, GitLab
Duo). The only auth path is a static bearer token (`APIKey`).

**Peers:**
- oh-my-pi `packages/ai/src/providers/` ships ~32 provider modules including
  `anthropic`, `google`/`google-vertex`, `amazon-bedrock` (with `aws-sigv4.ts`,
  `aws-eventstream.ts`, `aws-credentials.ts`), `github-copilot-headers.ts`,
  `gitlab-duo`, `cursor`, `openai-codex`, plus an `auth-broker`/`auth-gateway`/
  `auth-storage` for refreshable OAuth. README claims 40+ providers.
- jcode `crates/jcode-provider-*` has dedicated crates for anthropic, openai, gemini,
  bedrock, copilot, cursor, antigravity, openrouter, plus `jcode-azure-auth` and
  `jcode-auth-types` for OAuth.

**Laziness:** "OpenAI-compatible" is doing a lot of heavy lifting. Any vendor whose API
diverges from OpenAI chat-completions (Anthropic's `messages` shape, Gemini's
`generateContent`, Bedrock's event-stream) is unreachable. A user on a Copilot/Cursor
subscription — the common case jcode and omp both handle — cannot use evilcode at all.

---

## 2. Embeddings are Ollama-only and silently degrade

**evilcode:** `internal/provider/openai.go:293-295`:
```go
func (o *OpenAI) Embed(...) ([][]float32, error) {
    return nil, fmt.Errorf("openai: embeddings not wired for provider %q", o.name)
}
```
`memory.Manager.embed` (`internal/memory/pipeline.go:182`) calls whatever `Embedder` the
resolved provider provides. Use OpenAI/Anthropic/OpenRouter as your provider and **every
memory recall silently falls back to substring matching** (`store.go:329 substringHits`)
with no warning to the user that semantic recall is off.

**Peers:**
- jcode `crates/jcode-embedding` bundles a self-contained local ONNX model
  (`all-MiniLM-L6-v2` via `tract`) — embeddings work with **zero** external service and
  **zero** coupling to the chat provider.
- omp `packages/mnemopi/src/core/embeddings.ts` supports multiple embedding backends
  and a `useCloud` toggle.

**Laziness:** memory is advertised as a first-class feature, but its recall quality is
gated on "did you pick Ollama as your provider." The `Embed` stub returning a hardcoded
error is the textbook lazy implementation: the interface is satisfied, the feature is
named, the work isn't done.

---

## 3. LSP — 6 ops where the peer ships 14, and no multiplexed defaults

**evilcode:** `internal/tools/lsp.go:53-64` exposes `diagnostics, definition, references,
hover, symbols, rename`. That's it. `internal/lsp/client.go:526-533` ships default
commands for `go`, `typescript`, `javascript`, `python` (pyright), and `rust`
(rust-analyzer) — but no clangd (C/C++), no lua-language-server, no java/C#; anything
beyond those five needs manual config or is unreachable.

**Peer (omp):** `packages/coding-agent/src/prompts/tools/lsp.md` lists 14 ops:
`diagnostics, definition, type_definition, implementation, references, hover, symbols,
rename, rename_file, code_actions, status, capabilities, request (raw LSP), reload`.
omp's `lspmux` manages many servers; `lsp-linter-client` runs diagnostics continuously.

**Laziness:** `type_definition`, `implementation`, and `code_actions` are the ops that
actually make an LSP worth reaching for over grep — "show me the concrete impls", "apply
the quick-fix the compiler offered". Their absence is why the `lsp` tool description
itself (`lsp.go:20`) spends half its length telling the model *not* to use LSP. The
`client.go:6` package comment ("speaks just enough of the protocol… nothing else") is an
honest confession of laziness framed as minimalism.

---

## 4. No debugger (DAP) at all

**evilcode:** `grep -ri "dap\|debug adapter\|breakpoint" internal/` → nothing. There is no
debug adapter protocol support, no breakpoints, no stepping, no variable inspection. The
only "debug" surface is reading `panic` output through `bash`.

**Peer (omp):** `packages/coding-agent/src/dap/` (client, session, config, defaults) plus
`prompts/tools/debug.md` documenting ~17 documented actions (launch/attach,
set/remove_breakpoint, continue, step_over/in/out, pause, threads, stack_trace, scopes,
variables, evaluate, output, sessions, terminate) across gdb/lldb-dap/debugpy/dlv. README
advertises "28 dap ops".

**Laziness:** a coding agent that can read a stack trace but cannot stop at a breakpoint
or inspect a live variable is doing program-state work by reading source and guessing.
This is the single largest capability gap.

---

## 5. Memory bank — brute-force cosine over a flat list

**evilcode:** `internal/memory/store.go:8` says it plainly: "A JSONL file with a
brute-force cosine scan is the same behavior at this scale." `Search` (`store.go:293`)
loops every record, computes cosine, sorts. Four kinds (`fact/preference/project/episode`),
kind-weight only (`store.go:46`), cosine dedupe at 0.95, substring fallback. No entities,
no relations, no trust, no decay.

**Peer (omp mnemopi):** `packages/mnemopi/src/core/` has `entities.ts` (entity
extraction), a **triple store** (`TripleStoreLike`: subject/predicate/object), an
**annotation store**, trust tiers (`STATED | OBSERVED | INFERRED | SYSTEM`), veracity
tracking (`true/false/stated/inferred/contested/...`), a Beam config with
`workingMemoryTtlHours`, `recencyHalflifeHours`, `importanceWeight`, and **hybrid
scoring** (`vecWeight + ftsWeight + importanceWeight`) over SQLite with migrations.

**Laziness:** evilcode's bank is a bag of strings with similarity search. mnemopi is a
knowledge store: it knows *who did what to whom*, how reliable each fact is, and forgets
old things on a half-life. The DEVIATIONS note ("sqlite-vec would mean cgo") is a real
tradeoff for the storage layer, but **none** of the missing features (entities, trust,
decay, hybrid ranking) require sqlite-vec — they were just not built.

---

## 6. Compaction is manual, crude, and never automatic

**evilcode:** `Conversation.Compact(summary string)` (`internal/agent/context.go:116`)
replaces history with a single user message and bumps an epoch. It is only ever called
from `/compact` in `internal/tui/selftest.go:25`, which builds the transcript by
**truncating every message to 2000 chars** (`selftest.go:42`) and asking the smol role
to summarize. The agent loop (`agent.go:293 Loop`) has **no context-fill check** — it
will happily stream a 200k-token conversation at a 128k model until the provider errors.

**Peer (jcode):** `crates/jcode-compaction-core/src/lib.rs:9`:
```rust
pub const COMPACTION_THRESHOLD: f32 = 0.80;
```
Auto-triggers at 80% of the context budget, has a synchronous-vs-background threshold
distinction (`lib.rs:11-15`), special-cases base64 images at hard-threshold time
(`lib.rs:28`), and emits `BackgroundStarted { trigger }` events. omp has a whole
compaction subsystem: `session/compact-modes.ts`, `session/snapcompact-inline.ts`,
`session/snapcompact-savings-journal.ts`, `modes/utils/context-usage.ts`,
`modes/components/status-line/context-thresholds.ts`.

**Laziness:** a session that outgrows its window just dies unless the user remembers to
type `/compact`. And when they do, the summarizer sees each message chopped at 2000
chars — a large tool result or pasted file is bisected mid-sentence. "Compaction exists"
is technically true; "evilcode manages its context" is not.

---

## 7. No command-risk gate and no permission prompts

**evilcode:** `internal/tools/exec.go:126` `bashTool` runs whatever `cmd` string arrives
with `exec.CommandContext(ctx, "bash", "-c", script)` and no inspection. There is no
risk classification, no allow/deny, no "ask before running" path. `rm -rf ~` executes
immediately.

**Peer (jcode):** `crates/jcode-command-risk/src/lib.rs` is a **deterministic risk
classifier** built explicitly because "a model that decides to run `rm -rf ~` is obeyed
immediately… that is issue #604, where a user lost their home directory." It classifies by
**blast radius** (not command-name denylist, which misses `find -delete`, `shred`,
`truncate`, `dd`, `>file`), biases hard toward recall, and feeds a stage-2 reflection
gate. `crates/jcode-tui-permissions/src/lib.rs` is an interactive approval TUI
(`PermissionRequest`, `Urgency`, approve/deny/deny-with-reason).

**Laziness:** this is a safety feature, not a nice-to-have. evilcode ships a live shell
with less protection than jcode's authors decided was acceptable after losing a home
directory. The `Confine` flag in `fs.go` confines *file* writes; nothing confines
`bash`.

---

## 8. MCP — stdio-only, and it drops non-text content

**evilcode:** `internal/mcp/mcp.go:91` only ever builds a `sdk.CommandTransport` (a
subprocess). `mcp.go:156-162` iterates `res.Content` and keeps **only** `*sdk.TextContent`:
```go
for _, content := range res.Content {
    if text, ok := content.(*sdk.TextContent); ok {
        b.WriteString(text.Text)
        ...
    }
}
```
Image, audio, and embedded-resource results are silently discarded. There is no
SSE/streamable-HTTP transport, no resource subscription, no prompt handling.

**Peers:** omp's MCP integration handles rich content types and multiple transports
(stdio + HTTP/SSE); jcode routes MCP through the same tool registry as built-ins with
full content-type handling.

**Laziness:** an MCP server that returns an image (screenshot, diagram) or a resource
link returns nothing useful to the model. The adapter is named "MCP support" but only
covers the text-over-stdio subset.

---

## 9. No multimodal / vision support

**evilcode:** `provider.Message` (`internal/provider/provider.go:31-52`) is `Content
string` plus `Reasoning string`. `toOAIMessages`/`toOllamaMessages` only ever serialize
text. `readTool` (`fs.go:177`) refuses binary files outright with "looks like a binary
file." There is no way to put an image into a user message, and no tool produces one.

**Peers:** omp has `tools/inspect-image.ts`, `tools/image-gen.ts`, `ImageContent` in the
AI package (`tools/fetch.ts` imports `ImageContent`), resizes images per model
(`webpExclusionForModel`), and can read screenshots into context. jcode has
`jcode-terminal-image`.

**Laziness:** any vision-capable model on a configured OpenAI-compatible endpoint is
used as a text-only model. "Paste a screenshot of the error" — a thing both peers can do
— is impossible.

---

## 10. No cost / pricing / budget accounting

**evilcode:** `provider.Usage` (`provider.go:78`) carries `PromptTokens`,
`CompletionTokens`, `ContextMax`. The TUI shows token counts (`agent.go:479
EventTokenUsage`). There is **no** price-per-model table, no USD cost, no session spend,
no budget alarms.

**Peers:** jcode `crates/jcode-provider-core/src/pricing.rs` is a dedicated pricing
module; `jcode-usage-types` carries spend. omp `packages/ai/src/usage/` tracks cost and
reports it in a usage overlay.

**Laziness:** token counts without prices tell you nothing about money. A long session
on a premium model burns dollars that neither the model nor the user is told about until
the invoice arrives.

---

## 11. Productivity dashboard and overnight reporting are thin

**evilcode:**
- `internal/tui/productivity.go` (220 lines) reports sessions, messages, prompts, a
  by-day sparkline, and busiest day — read from session logs so it survives a crash.
- `internal/tui/overnight.go` (~110 lines) is a supervised long-run loop with hard caps
  (turns, token budget, deadline) plus a stall detector and a `Stopped` reason. The
  breaker design is good — but when it stops, the only output is the reason string in the
  TUI. No report is produced and nothing is sent anywhere.

**Peers (jcode):**
- `crates/jcode-productivity-core` (1716 lines): streaks (current + longest), chronotype
  (peak hour → "night owl"/etc.), gamification titles and badges ("🔥 7-day streak",
  "Edits per prompt off the charts"), SVG hour-of-day and weekday charts (usvg/fontdb),
  and markdown export.
- `crates/jcode-overnight-core` (1471 lines) renders an HTML morning report
  (`render_task_cards_html`, `morning_report_posted_at`), and `crates/jcode-notify-email`
  ships real SMTP delivery (lettre, STARTTLS, auth) so an unattended run emails its
  result.

**Laziness:** evilcode's overnight loop does the *safe* part (it stops) but not the
*useful* part (it never tells you it finished unless you're watching the terminal — the
whole point of an overnight run is that nobody is). The productivity dashboard is a count
and a sparkline where jcode built a streaks/charts/chronotype dashboard. Both align as
features and both are thinner than they should be.

---

## Smaller lazy spots

- **LSP default servers** (`lsp/client.go:528-533`) cover Go/TS/JS/Python/Rust. No
  clangd (C/C++), no lua-language-server, no java/C# defaults — omp ships a wider default
  set and broader auto-detection. Users of those languages must hand-configure or get
  nothing.
- **No web/fetch tool.** omp `tools/fetch.ts` is a full scraper (HTML→markdown, archive
  sniffing, parallel-extract, image resize). evilcode has nothing between "shell out to
  `curl`" and nothing. (jcode also omits this, so it's the weakest alignment — but omp
  is a direct peer and the gap is real.)
- **`git_overview` is the only git surface.** `tools/git.go` gives overview/file-diff/hunk
  — all read-only. omp `tools/gh.ts` drives GitHub (issues, PRs, CI runs, checkout);
  evilcode has no PR/CI tooling at all. (`git` here aligns only loosely; flagged for
  completeness.)
- **No self-update.** jcode `crates/jcode-update-core` does git-pull/binary download with
  progress and divergence detection. evilcode has no update path.
- **`grep` hard-requires `rg` installed** (`exec.go:294` returns a hard error if
  ripgrep is absent) rather than falling back. A brittle "lazy" choice: the tool errors
  out instead of degrading.

---

## What evilcode did *not* get lazy with (for honesty)

- **Todo discipline** (`internal/todo/`): priority-weighted confidence, completion
  confidence, tool-owned append-only history, `blocked_by` dependency edges, `p2verify`
  gate verification, poke/stall detection. This is more developed than jcode's flat
  plan items and rivals omp's todo.
- **Swarm file-conflict registry** (`internal/daemon/registry.go`): read/write tracking
  with turn attribution, stale-conflict notices delivered at safe point D, compact
  notice folding. A real coordination layer, not a stub.
- **Anchored edits** (`internal/tools/anchor.go` + `fs.go`): hash-anchored line edits
  with staleness detection on mtime+size, refusing loudly on drift. Genuinely ahead of
  omp's hashline in its staleness check.
- **Session resume/rewind/transfer** (`internal/session/`): JSONL append-only with
  meta-checkpoints, `.bak`-guarded rewind, collapse-summary handoff, durable-state
  survival. Solid.
- **Advisor** (`internal/agent/advisor.go`): compressed-view second-model conscience with
  one-concern-per-turn limit, repeat suppression. Cleanly designed.
- **Smoothness / screenshot / theme** machinery in `internal/tui` and `internal/theme` —
  the kind of polish neither peer ships.

The pattern: evilcode is deep where it chose to be (todos, swarm coordination, TUI
polish, anchored edits) and thin where the work was "plumbing a third-party surface"
(providers, embeddings, LSP/DAP, MCP content, vision). That is exactly the laziness this
list documents — the features that align with the peers are the ones left half-built.
