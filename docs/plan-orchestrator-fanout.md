# evilcode orchestrator fan-out plan

Status: proposed, 2026-09-15. Grounded in a code read of the current tree and
research into how other coding agents do subagent orchestration (Claude Code,
OpenCode, Codex CLI, Oh My Pi, Goose, Gemini CLI, Cline; Anthropic's
orchestrator-workers pattern). Evidence per claim inline.

## Why

evilcode has all the primitives of a swarm orchestrator — foreground blocking
`spawn_worker` with JSON-Schema-validated results, async `/summon`, per-worker
daemon sessions with their own MCP sets, effect-based tool batching, breakers,
heartbeats — but three gaps stop it from behaving like an orchestrator:

1. `spawn_worker` declares no `Effect`, so it defaults to `EffectUnknown` and
   `RunBatch` runs it as a barrier: a batch of N spawns in one round runs
   serially, 30-minute waits back to back. OpenCode shipped this exact bug
   twice and closed one report as "not planned"
   (github.com/anomalyco/opencode issues #14195, #29638) — parallelism must be
   a scheduler guarantee, not prompt-dependent behavior.
2. Workers cannot choose a model: `spawn()` hardcodes `Model: s.Model`
   (`internal/daemon/spawn.go:103`). Codex's inheriting-only design generated
   sustained user complaints (codex issues #31814, #32674).
3. Nothing teaches the model when or how to fan out. Codex shipped native
   collab and users still had to explicitly instruct the model to spawn agents
   (codex discussion #11041). Oh My Pi layers the same guidance in four places
   because one place is not enough.

## Evidence base

- **Claude Code**: per-call `model` on the Agent tool, agent-definition
  frontmatter model, env default, inherit — with `resolvedModel` in the result;
  parallelism = N tool calls in one message, system prompt says "launch
  multiple agents concurrently whenever possible"; subagents cannot spawn
  subagents (one level). code.claude.com/docs/en/subagents.
- **OpenCode**: `task` tool, `background: true` returns a task id; model is
  per-agent-definition, not per-call; sequential-batch bugs above.
- **Codex CLI** (experimental collab): `spawn_agent` fire-and-forget + explicit
  `wait` to join + `close_agent`; `max_threads`/`max_depth` config; spawned
  agent inherits tools AND model by default; parallel agents share the
  environment and the template warns them not to modify/revert each other's
  work.
- **Oh My Pi** (local fork + installed docs, verified): one session-scoped
  semaphore (`task.maxConcurrency`, default 32, resizable live), batch
  `{context, tasks[]}` wire shape, async job registration + auto-delivery of
  settled results into the parent conversation, 3-layer model routing
  (per-call override → agent frontmatter ordered model list acting as a retry
  fallback chain → parent model), per-spawn `effort` (lo/med/hi) mapped onto
  the model's ladder, per-worker token+cost rollup surfaced in a live roster,
  hidden `yield` tool with a 3-reminder ladder ending in forced toolChoice,
  soft request budgets (90 default, 40 for explore/quick_task), and
  post-spawn advisories (specialization nudge when ≥2 role-less clones,
  sibling-coordination nudge when ≥2 live siblings).
- **Anthropic multi-agent research**:
  anthropic.com/engineering/multi-agent-research-system — multi-agent used
  ~15× chat tokens; token spend explained ~80% of performance variance;
  effort-scaling rubric (simple: 1 agent/3–10 tool calls; comparisons: 2–4
  subagents; complex: >10); vague delegation caused duplicated work —
  assignments must carry objective + output format + tool/file scope +
  boundaries + stopping condition; workers write artifacts, return lightweight
  references.
- **Aider** (no subagents, aider issues #4428, #669) is the standing
  counterargument: parallel agents over one working tree create lost-update
  problems. evilcode's effect batching is the structural answer — the skill
  must state it explicitly.

## Design decisions

### D1 — Parallelism is a scheduler guarantee (EffectSpawn)

Add `EffectSpawn` to the `tools.Effect` enum. Semantics:

- A maximal run of consecutive spawn calls in one batch shares the bounded
  pool (`MaxConcurrent = 8` ≥ `MaxLiveWorkers`), like read-only calls.
- Spawns still barrier against mutating/interactive/undeclared calls in the
  same batch, exactly like mutations do today: effects land in the order the
  model asked.
- Undeclared tools keep the safe serialized default; only `spawn_worker`
  (and any future spawn-shaped tool) declares `EffectSpawn`.

This makes parallel fan-out structural. No prompt text can be the only
mechanism (OpenCode's bugs prove it); a per-call `parallel: true` flag is not
needed either — the effect class IS the flag.

### D2 — One tool, two return modes

Keep `spawn_worker` as the single tool. Add an optional `wait` boolean:

- `wait: true` (default, current behavior): foreground blocking; the call
  waits for the validated result. Simple, and the easy case for most workers.
- `wait: false`: returns immediately with the worker name; the result arrives
  as a message when the worker finishes (this is what `/summon` uses today).
  This is Codex's spawn/wait shape without adding new tools: a fan-out batch
  of `wait:false` spawns runs concurrently and the parent keeps working,
  joining results as messages.

Codex needed `wait`, `list_agents`, `close_agent` because spawn was
fire-and-forget only. evilcode avoids that surface by keeping the blocking
default.

### D3 — Model routing: per-call param with precedence + auditability

`spawn_worker` gains `model?: string` (`model@provider` ref). Precedence
(Claude Code's chain, minus env):

1. per-call `model` param →
2. `[features] default_worker_model` config (the "level 3" default;
   per-worker-type definitions are NOT built here — evilcode has no
   agent-definition files, and inventing a second config surface to shadow
   config is not justified by any evidence in the survey) →
3. daemon's session model (today's behavior).

Mechanics:

- Resolve the ref at spawn time with `cfg.Resolve` — a bad ref fails in
  milliseconds with a readable error the model can correct, before any tokens
  are spent (`internal/daemon/hub.go:178` already does this for schemas;
  same pattern).
- `resolvedModel` is attached to the worker's result/delivery so the
  orchestrator can audit what actually ran (Claude Code parity; omp's
  `SingleResult.resolvedModel` shows the fallback case matters).
- The ref may name any model the session's own catalog resolved at startup —
  model override redirects spend, so arbitrary strings are simply refused by
  resolution. No new trust surface: `.evilcode.toml` repo overrides still
  cannot set credentials, and repo-pinned `default_worker_model` joins the
  existing repo-override allowlist (model/roles only).
- Skip per-worker reasoning effort for now (`ReasoningEfforts` is global
  config; a per-spawn effort knob is omp's opt-in `task.enableEffort`, not a
  field-consensus feature).

### D3b — Model resolution failures advance, never strand

When the resolved provider/model fails at worker start (auth, rate limit,
model-not-found), the spawn fails with the provider error in the tool result.
Do NOT silently retry on a different model (unlike omp's ordered fallback
chain): evilcode's model list is user-curated; falling back to an unrequested
model would silently change worker capability. The orchestrator sees the
error and can re-spawn explicitly with a different model. This is a
deliberate divergence from omp — simpler, and honest about what ran.

### D4 — Result contract: schema failure semantics

Current behavior (verified, `internal/daemon/hub.go:225-300`): schema
validated before delivery; prose answer → one retry with an instruction to
reply with only the JSON; failure after the retry → `workerFailure` error.
Amend to match the field's converged practice (omp's ladder, Anthropic's
partial contract):

- Cap schema retries at 3 total prompts (currently 1). A worker that cannot
  produce schema-valid output after 3 attempts returns a failed result whose
  text carries the worker's last message — the orchestrator gets the content
  AND the fact it was unvalidated, instead of nothing.
- Every result carries `status: complete|failed|partial` (partial = worker
  hit its timeout with salvageable output; see D6). Anthropic's system
  treats worker failure as data and re-spawns with accumulated knowledge —
  that needs the orchestrator to see partial output, not just an error.

### D5 — Orchestration guidance: three layers, not one skill

omp layers the same rules in four places; a skill alone underweights it.
evicode ships:

1. **System prompt bullet** (`internal/agent/prompts.go` `toolGuidance`):
   the fan-out contract in one bullet — batch independent spawns in one
   round, wait only for the batch, brief self-containedly, per-worker model
   optional, never spawn for what one read answers.
2. **`orchestrate` skill** (shipped, repo skill dir): the playbook —
   decomposition test ("can task B run without A's output? If no,
   sequence"), brief format (objective, files, done-condition, result
   schema, stopping condition — Anthropic's finding that vague delegation
   causes duplicated work), sizing rubric (2–4 workers for comparisons, more
   for wide surveys; Anthropic's data), merge rules (parent validates and
   integrates; workers don't coordinate each other; evilcode's effect
   barrier is the mutation-safety story — state it), failure path (a failed
   worker is data; re-spawn the gap, don't absorb), and "message an existing
   idle worker instead of spawning fresh when a follow-up needs its
   context" (omp's park/revive lesson).
3. **Tool description carries the trigger test** (`spawn_worker`'s `Desc`):
   fan out only when 3+ independent briefs exist; the assignment format; and
   a pointer to the `orchestrate` skill. The tool description is the one
   place a model reaching for the tool certainly looks.

### D6 — Cancellation and failure semantics (the survey's biggest gap)

Today: `WorkerTimeout` (30 min) wall clock only; no cancel; no partial
result. Add:

- `cancel_worker` capability: cancel(id) aborts the worker's in-flight
  provider request (evilcode already has urgent-interrupt plumbing —
  `ClientMsg.Urgent`, next-safe-point delivery), marks the worker failed, and
  releases its live slot. Exposed first to the orchestrator (tool result
  error path), TUI/web later.
- Timeout = cancel, not detach: a timed-out worker's last assistant text is
  salvaged as the returned result with `status: partial`. A worker holding a
  mutation at timeout is released by the existing registry conflict
  machinery (file registry knows who touched what).
- Keep `WorkerTimeout` as the wall clock; add an optional per-worker
  tool-call budget (`[features] worker_max_steps`, default 0=unlimited,
  reuses the existing `max_steps` machinery in `agent.go`).

### D7 — Cost accounting (cheap, differentiating)

No surveyed harness has per-subagent budgets; omp rolls up tokens+cost per
spawn. evilcode already accumulates per-session usage; extend:

- Per-worker token total recorded on the swarm state at spawn registration
  and updated at result delivery (`swarmState` already keys by worker name).
- `/agents` and the web roster render `worker ×: <model> · <tokens> tok`
  instead of names only. Display-only; the model does not see it unless the
  parent asks (`agents` tool exists).
- Aggregate per batch in the spawn tool's batch result when `wait:false`.

### D8 — Caps and knobs

- `MaxLiveWorkers = 4` → config `[features] max_live_workers` (default
  stays 4). The per-session `MaxWorkersPerSession = 12` stays a constant:
  it is the anti-recursion breaker, and survey precedent (Codex V2 counts
  the root thread in its concurrency cap; document which convention
  evilcode uses — evilcode counts only live workers, the root is not
  charged).
- `WorkerTimeout` stays a constant unless evidence asks for config.
- Depth: **depth 1 for now — workers do not get `spawn_worker` in their
  tool set** (field consensus: Claude Code, OpenCode default, Gemini CLI,
  Cline). Today workers can spawn (bounded only by the caps,
  `internal/daemon/spawn.go:176`); that stays available behind a config
  flag `[features] worker_spawning = false` default, because orchestrator
  trees are the expensive failure mode and nobody has asked for depth 2.
  Revisit if real usage wants it.

### D9 — files_hint stays the parallel-safety primitive

`files_hint` is unique to evilcode. Make it load-bearing: EffectSpawn
eligibility for a spawn batch is granted when the batch's declared
`files_hint` sets are pairwise disjoint or read-only; overlapping hints
downgrade the batch to serialized (barrier), as today. Undeclared hints do
not block parallelism (hints are advisory) — mutations are already
barriered by effect, and the registry conflict check covers the rest.
Simpler alternative considered and rejected: requiring disjoint hints would
punish read-only surveys, which are the main fan-out win.

## Change list (file-by-file)

| Phase | File | Change |
|---|---|---|
| 1 | `internal/tools/tools.go` | `EffectSpawn` enum value + RunBatch scheduling |
| 1 | `internal/tools/swarm.go` | declare Effect on `spawn_worker`; `wait` field |
| 1 | `internal/daemon/hub.go` | non-blocking path returns (name, session handle) |
| 2 | `internal/tools/swarm.go` | `model` field, validation, resolution errors |
| 2 | `internal/tools/hub.go` | signatures gain model |
| 2 | `internal/daemon/spawn.go` | `spawn()` accepts model; resolve before reserve |
| 2 | `internal/daemon/protocol.go` | `MsgSpawn` reuses existing `ClientMsg.Model` |
| 2 | `internal/daemon/server.go:2455` | pass `msg.Model` through |
| 2 | `internal/attachcmd/attach.go:418` | `summon()` gains model |
| 2 | `internal/tui/swarmwidget.go` | `/summon -m <model> <task>` parsing |
| 3 | `internal/agent/prompts.go` | fan-out bullet in `toolGuidance` |
| 3 | repo skill dir | `orchestrate` skill (markdown) |
| 3 | `internal/tools/swarm.go` | Desc pointer to the skill |
| 4 | `internal/config` | `max_live_workers`, `default_worker_model`, `worker_max_steps`, `worker_spawning` |
| 3/4 | `internal/daemon/hub.go` | depth-1 default: strip `spawn_worker` from worker tool sets unless `worker_spawning` |
| 4 | `internal/daemon/spawn.go` | cancel + partial-result salvage path |
| 4 | `internal/daemon/hub.go` | per-worker token rollup; roster display |

## Test plan

- EffectSpawn: a batch of 4 spawns completes concurrently (sample
  `swarm.live` like `reserve_worker_test.go` does); spawn batch still
  barriers against a mutation in the same round.
- Model override: worker's `sess.built.Agent.Model` equals the requested
  ref; invalid ref fails at spawn with a readable error before any session
  exists; `resolvedModel` appears in the result.
- `wait:false`: tool returns immediately with the worker name; result
  arrives as a message; batch aggregation reports tokens.
- Schema: 3-attempt cap; partial result on timeout carries last text and
  `status: partial`.
- cancel: cancel(id) aborts in-flight provider request (mock provider hang),
  worker marked failed, live slot released, result salvageable.
- Depth: worker tool set has no `spawn_worker` when `worker_spawning=false`.
- Caps: `max_live_workers` config honored under concurrent load
  (existing reserve tests are the template); `MaxWorkersPerSession` still
  fires.
- Guidance: skill indexes and loads; `toolGuidance` bullet present in
  system prompt snapshot test.

## Order of work

1. **Phase 1 — parallel spawn scheduling** (EffectSpawn + `wait:false`).
   Independently shippable; alone it makes fan-out real.
2. **Phase 2 — model routing** (per-call model + resolution + audit).
3. **Phase 3 — guidance** (system-prompt bullet + `orchestrate` skill + tool
   Desc pointer). Land with Phase 1 ideally, never later than Phase 2 —
   scheduling only helps a model that emits parallel calls.
4. **Phase 4 — operations** (config knobs, cancel/partial, depth flag, cost
   rollup).

## Explicitly not building

- `fan_out` mega-tool taking an array of tasks: duplicates RunBatch +
  EffectSpawn with its own scheduler/breakers/timeout story. One tool,
  N calls, one message.
- Shared MCP registry across workers: per-session fan-out is a documented,
  deliberate trade-off (`internal/mcp/mcp.go:8-16`).
- Streaming worker transcripts into the parent context: no surveyed harness
  does this; context compression is the value. Progress belongs to the
  heartbeat/roster surface.
- Isolated worktrees per worker (omp/Codex route): evilcode's shared-tree +
  effect-barrier + file-conflict registry is a legitimate alternative; revisit
  only if real contention appears.
- Per-worker reasoning effort, service tiers, prewalk downshift: opt-in
  complexity with no demonstrated need yet.
- DAG enforcement: no runtime acyclicity checker anywhere in the field;
  depth cap + breakers are the guard.