# evilcode — hardening plan (post-`phase-5` review sweep)

`plan.md` built evilcode. This document makes it survive being used. It is the working
spec for the hardening pass and is checked off here, exactly as `plan.md` was.

Everything below comes from two independent full-repo reviews run at `f7b9a9b`
(2026-07-31): **Codex** (gpt-5.6-sol, reasoning effort high, 150 files / 42,380 lines,
46 min) and **Fable**
(claude-fable-5, same scope, 25 min). Neither saw the other's output.

---

# PART 0 — Process: how the fixing agent works

## 0.1 What is different from `plan.md`

`plan.md` §0.2 built features from a spec that was known-good. This plan fixes bugs from a
report that is **not** known-good. Two language models read the code and wrote down what
they believed. Some of it is wrong. Some of it is right about the symptom and wrong about
the cause. Line numbers were accurate at `f7b9a9b` and rot the moment you start editing.

So the loop gains one step before the fix: **make it fail on purpose.**

This is the Phase 5 polish-audit lesson stated the other way round. That audit found that
every bug it caught had a *passing* golden that had faithfully recorded the wrong
behaviour — a test proves behaviour is unchanged, never that it is right. The corollary:
a fix you did not first watch fail is a fix you have no evidence for. You changed code,
the suite stayed green, and green is exactly what it was before you started.

## 0.2 The loop (repeat until this plan is complete)

1. Pick the next unchecked `[ ]` task in the current phase.
2. **Reproduce.** Write the smallest test that fails *because of this bug*, and watch it
   fail. Cite the failure in the LOOPS.md entry. Three outcomes:
   - It fails → you understand the bug. Continue.
   - It passes → the finding is wrong, or the path is unreachable. Mark the task `[~]`,
     log why in LOOPS.md, move on. Do not fix what you could not break.
   - You cannot construct the test → say so in LOOPS.md and mark `[~]`. An unreachable
     bug and an untestable one are different things; name which.
3. **Find the root, not the report.** Every finding names one call site. Grep every caller
   of the function before editing — the reviews traced some call chains and guessed at
   others. Fix it once, where all callers route through. If the report says `app.go:658`
   and the real bug is in the function `app.go:658` calls, fix that and say so.
4. Fix it. `go build ./... && go vet ./... && go test ./...` green.
5. Run the reproduction from step 2 again. It must now pass. A fix without a
   fail-then-pass pair is not done.
6. TUI-visible change → boot the probe rig, drive the scenario, render to PNG,
   **look at the image**. Existing goldens that now differ: confirm the new frame is
   *right* before regenerating, then regenerate.
7. Mark `[x]`. Commit — one task per commit, always green.
8. Kick off a **background codex review of the commit** (`codex:rescue`). Don't block.
   Fold findings into the next iteration.
9. Append one entry to **`LOOPS.md`** (append-only, never edit old entries):
   `## <date> H<n>.<m>` — the reproduction and how it failed, the root cause if it
   differed from the report, the fix, verification, codex verdict when known.
10. Behavior changed → `README.md` in the same commit. Specced behavior deliberately not
    restored → `DEVIATIONS.md`.

## 0.3 Reading a task

```
- [x] **H1.3** `internal/tui/app.go:658` `stepOvernight` — description — fix.  ⟨both⟩
```

- **ID** — cite it in the commit subject and the LOOPS.md heading.
- **`file:line` `symbol`** — line numbers are as-of `f7b9a9b` and drift; the symbol does not.
- **⟨both⟩** — both reviewers found it independently. Two models reading separately and
  landing on the same file and the same mechanism is the strongest signal available here,
  and it is still not proof. Step 2 is what turns it into proof.
- **⟨codex⟩ / ⟨fable⟩** — one reviewer only. Not weaker as a bug, weaker as evidence.
  Nine of the highest-severity findings are single-source, including the two most likely
  to be silently corrupting files today (H1.4, H4.1).

## 0.4 Ordering

Phases are ordered by what a bug costs when it fires, not by how hard it is to fix:

| phase | what it costs | H |
|---|---|---|
| data you cannot get back | session content, file contents, budget | H1 |
| wrong behavior under concurrency | duplicated turns, clobbered state | H2 |
| the process dies or wedges | OOM, leaked goroutines, hung UI | H3 |
| the boundary was never a boundary | escape sequences, path escape, perms | H4 |
| it is simply wrong | off-by-ones, dead features, swallowed errors | H5 |

H1 first because everything in it fails *silently on an ordinary path* — no error, no
crash, no log line. The session compact bug has been eating messages since compaction
shipped and nothing in the suite noticed.

Within a phase, order is yours. Adjacent tasks in the same file are worth batching into
one reproduce-fix cycle — but still one commit each.

---

# PART I — Phases

## Phase H1 — Silent data loss

Everything here destroys user data or state with no error surfaced. Fix in order.

- [x] **H1.1** `internal/session/checkpoint.go:281` `Compact`/`Rewind` — the rewrite is
  temp-file + `os.Rename`, but every caller keeps the already-open `*session.Store` whose
  `O_APPEND` fd now points at the **orphaned pre-rename inode**. Every message persisted
  after an auto-compact, `/compact`, or `/rewind` is written to a zero-link inode and
  vanishes when the fd closes. Callers: `wiring.go:150`, `tuicmd.go:142`, `run.go:144`,
  `tui/sessioncmd.go:179` — none reopen. Fix: `Store.Reopen()` (close + reopen the path)
  called after `Compact`/`Rewind`, or perform the rewrite through the live `Store` under
  its lock. Reproduce: compact a session, append a message, resume, assert the message is
  there.  ⟨both — critical⟩
- [x] **H1.2** `internal/agent/agent.go:595` `runTools` — bails with `context.Canceled`
  mid-loop, leaving the round's remaining `tool_use` entries unanswered. The conversation
  and the JSONL then hold assistant `tool_calls` with no adjacent results; strict
  OpenAI-compatible endpoints reject the next request with 400. Fix: append a result
  (real, or a cancellation stub) for **every** call in the batch before returning.  ⟨both⟩
- [ ] **H1.3** `internal/agent/agent.go:384,438` `commitPartial` — same class, other path:
  it checks Content/Reasoning for emptiness and then appends the whole message including
  `msg.ToolCalls`. Fix: strip `ToolCalls`, or emit `[Skipped: interrupted]` stubs the way
  safe point C already does. Do H1.2 and H1.3 as one mechanism, two commits.  ⟨fable⟩
- [x] **H1.4** `internal/lsp/ops.go:299` — LSP character positions are **UTF-16 code
  units**; the edit path slices Go strings with them as **byte offsets**. Every rename or
  edit through the LSP tool corrupts any file containing non-ASCII, and can split a UTF-8
  sequence. Fix: convert every LSP position UTF-16→byte with validation. Reproduce: rename
  a symbol in a file with an emoji or accented identifier above the edit site.
  ⟨codex — high, and the most likely to be corrupting files right now⟩
- [x] **H1.5** `internal/tools/fs.go:252` `writeTool`/`editTool` — truncate the destination
  in place, so a crash, short write, or full disk leaves a partially written file. Fix:
  write + sync a same-directory temp file, preserve permissions, atomic rename.  ⟨codex⟩
- [x] **H1.6** `internal/tools/fs.go:331` `editTool` — batches run 8-way concurrent
  (`tools.go:101`) and edit/write are read-modify-write with no per-path lock. Two edits
  to one file in one batch both `strings.Count` against the same `before`; one is silently
  lost. Fix: per-canonical-path mutex, revalidate immediately before the replace from
  H1.5.  ⟨both⟩
- [x] **H1.7** `internal/agent/context.go:44,51` — the persistence sink cannot return an
  error, so disk-full and closed-store failures leave the durable transcript behind the
  in-memory conversation with nothing surfaced; and the conversation lock is released
  before the sink runs, so disk order can differ from memory order. Fix: sink and `Append`
  return errors, surface on the turn; serialize append-and-persist, or feed
  sequence-stamped records through one ordered writer.  ⟨codex⟩
- [x] **H1.8** `internal/session/checkpoint.go:104` — rewind and compact ignore
  backup-write failures before destructively replacing the primary log. Fix: require a
  written **and synced** backup before committing the replacement.  ⟨codex⟩
- [x] **H1.9** `internal/daemon/server.go:457` — session close cancels the turn and
  immediately closes the store without waiting for the turn to unwind; trailing messages
  are written to a closed store and dropped. Fix: track active turns, await completion
  after cancel, then close.  ⟨codex⟩
- [x] **H1.10** `internal/wiring/wiring.go:163,172` + `internal/todo/model.go:159` — the
  daemon builds every session with `TodoNamespace: "swarm"`, but each session gets its
  **own** `todo.Store` and memory store over the same files: separate in-memory copies, no
  reload before read, whole-file last-write-wins through a **shared** `.tmp` path, and
  memory IDs allocated from stale snapshots. The §20 shared plan does not share. Fix: one
  store per namespace, owned at server scope and shared by reference — the session
  registry already works this way.  ⟨both⟩
- [x] **H1.11** `internal/todo/model.go:323` — a todo transaction mutates live state and
  then writes four files sequentially; any failure leaves memory and disk disagreeing.
  Fix: apply to a clone, stage and sync every file, commit atomically, then publish.
  ⟨codex⟩
- [x] **H1.12** `internal/tui/app.go:658` — `m.stepOvernight()` is called **twice** per
  `EventTurnEnd`. Turn counters double-increment (halving the real cap), and both calls
  can pass `ShouldContinue` and each `submitHidden(OvernightPrompt)` — two concurrent
  `agent.Run`s on one agent. Fix: delete one line; add a one-turn/one-continuation test.
  ⟨both⟩
- [x] **H1.13** `internal/tui/app.go:653` — overnight accounting reads the turn's token
  count *after* status reset, and **assigns** rather than accumulates. The budget breaker
  never fires. Invariant 6 (`plan.md` §1.3) says every auto-continuation path has a working
  breaker; this one is decorative. Fix: capture tokens before reset, add to the running
  total, test that the breaker trips.  ⟨codex⟩
- [x] **H1.14** Verify H1: compact → append → resume round-trips; a cancelled turn's
  transcript replays against a strict endpoint; an LSP rename on a non-ASCII file is
  byte-exact; concurrent edits to one file in one batch both land; overnight stops at its
  budget. Tag `harden-1`.

## Phase H2 — Concurrency

Wrong results under load, on a machine where the daemon and the swarm make load normal.

- [x] **H2.1** `internal/daemon/server.go:492` `sess.cancel` — assigned, read, and cleared
  across input, interrupt, close, and worker paths with no consistent locking. Two attached
  clients, or an interrupt racing a new turn, can cancel the wrong turn. Fix: touch
  `cancel` only under `sess.mu`, and cancel the previous one before overwriting.  ⟨both⟩
- [x] **H2.2** `internal/daemon/server.go:486` — `Running()` and turn start are separate
  operations, so two clients can both see idle and both launch. Fix: reserve the session
  under its mutex before spawning; return busy otherwise.  ⟨codex⟩
- [x] **H2.3** `internal/agent/agent.go:344` `Loop` — no atomic rejection of an
  already-running turn; concurrent loops mutate one conversation and tool state. Fix:
  single-flight guard, busy result if held. This is the backstop that would have contained
  H1.12.  ⟨codex⟩
- [x] **H2.4** `internal/daemon/hub.go:79` — global and per-session worker limits are
  checked without reserving, so concurrent spawns exceed both. Fix: reserve both counters
  under coordinated locking, roll back on failure.  ⟨both⟩
- [ ] **H2.5** `internal/daemon/spawn.go:72` — worker-name collision resolution runs
  *after* the store and agent are built, so a suffixed worker can still be holding the
  original session log and identity. Fix: allocate the unique name before creating any
  resource.  ⟨codex⟩
- [ ] **H2.6** `internal/daemon/spawn.go:88` — the spawn goroutine calls `markFinished()`
  as soon as `Run` returns, but `reportWorkerResult`'s schema-retry path returns `false`
  precisely to mean *not finished* and then starts a second `Loop`. The worker is counted
  finished by `liveWorkers` while its retry burns tokens, and the retry can overlap the
  tail of the original run. Fix: `markFinished` only in `observe`/`reportWorkerResult`.
  ⟨fable⟩
- [ ] **H2.7** `internal/daemon/server.go:242` — concurrent opens of one session build
  duplicate stores outside the map lock; the loser writes lifecycle state to the shared log
  as it closes. Fix: per-session singleflight.  ⟨codex⟩
- [x] **H2.8** `internal/agent/events.go:119` — `a.seq++` unsynchronized. The daemon's
  `deliverConflicts` calls `Notice` from the pump goroutine while `Loop` emits from the
  turn goroutine. Fix: `atomic.Int64`, or allocate under `a.mu`.  ⟨both⟩
- [x] **H2.9** `internal/agent/agent.go:97,266` — `Attach` mutates `pendingImages` under
  `a.mu`; `Run` swaps it without. Safe today only because the TUI calls Attach before
  starting the run goroutine. Fix: take the lock around the swap.  ⟨fable⟩
- [ ] **H2.10** `internal/wiring/wiring.go:115` — repo overrides are applied by mutating a
  **shared** config object, so one repo's settings leak into other daemon sessions and race
  concurrent builds. Fix: deep-copy per build, apply overrides to the copy only.  ⟨codex⟩
- [ ] **H2.11** `internal/lsp/client.go:574` `Manager.For` — the lock is dropped across
  `Start` with no recheck, so two concurrent callers launch two servers and the loser is
  overwritten in `clients` and leaks until exit. Fix: per-language singleflight, close any
  loser.  ⟨both⟩
- [x] **H2.12** `internal/session/store.go:95` — creation picks a free generated name and
  then opens without `O_EXCL`; concurrent creators can select and append to the same log.
  Fix: `O_CREATE|O_EXCL`, retry on collision.  ⟨codex⟩
- [ ] **H2.13** `internal/tools/tools.go:106` `RunBatch` — a goroutine is created for every
  model-supplied call *before* the semaphore applies, so a pathological call list exhausts
  memory and the scheduler regardless of the concurrency cap. Fix: cap batch size, dispatch
  through a fixed worker pool.  ⟨codex⟩
- [ ] **H2.14** `internal/tools/exec.go:167` — parallel stateful shell calls snapshot the
  same working directory; last finisher wins and the others' `cd` is lost. Fix: serialize
  stateful execution.  ⟨codex⟩
- [ ] **H2.15** `internal/tools/ask.go:128` — `PendingAsk` is a single slot with no queue,
  so two `ask` calls in one batch deadlock the turn: `Set` overwrites the first, its
  `Reply` never arrives, its goroutine blocks until the user interrupts. The code comment
  claiming the batch bounds this is wrong. Fix: queue, or answer-with-nil the displaced
  request immediately.  ⟨both⟩
- [ ] **H2.16** `internal/tui/app.go:1741`, `selftest.go:38`, `sessioncmd.go:143` —
  `/compact` and `/rewind` do not check `m.processing`; an in-flight turn keeps appending
  across `Conv.Reset`, and its messages land after the rewrite. Fix: refuse while
  processing, or cancel and await `TurnEnd` first.  ⟨both⟩
- [ ] **H2.17** `internal/tui/app.go:1531` `/plan` — cancels the active turn and launches a
  hidden run immediately, without awaiting the cancelled turn's end. Fix: queue the prompt,
  start on the matching `TurnEnd`.  ⟨codex⟩
- [ ] **H2.18** `internal/tui/images.go:225` — concurrent mermaid renders share one atomic
  result slot; a lost result leaves `m.diagrams[source] = ""`, and that sentinel blocks any
  retry for the rest of the session. Fix: buffered completion queue, drain every result.
  ⟨both⟩
- [ ] **H2.19** Verify H2: two attached clients interrupting each other cancel their own
  turns; a swarm at the worker cap stays at the cap under concurrent `/summon`; `-race` is
  green across `./...` including the daemon tests. Tag `harden-2`.

## Phase H3 — Resource exhaustion and leaks

- [ ] **H3.1** `internal/provider/openai.go:295` and `internal/provider/ollama.go:198` —
  the stream producer sends a parse-error chunk and then `continue`s, but the consumer
  (`agent.streamOnce`) returns on the first `chunk.Err`. The producer blocks forever on the
  next unbuffered send, holding the response body, the connection, and its goroutine for
  the rest of the turn — while a retry opens another. Fix: `return` after emitting the
  terminal parse error, as the `resp.Error` branch already does.  ⟨both⟩
- [ ] **H3.2** `internal/tools/fs.go:173` — `FS.MaxReadBytes` is declared, documented as
  capping a single read, initialized, and **never referenced**. `readTool` does an
  unbounded `os.ReadFile` plus a full line split; output truncation happens only after the
  whole file is resident. A multi-GB file OOMs the process. Fix: stat or limited-reader
  before load.  ⟨both⟩
- [ ] **H3.3** `internal/tools/exec.go:169` — foreground and background execution both
  accumulate stdout and stderr unbounded; background commands hold it for up to 30 minutes
  before `Truncate` runs. Fix: bounded ring writers, kill on hard output limit.  ⟨both⟩
- [ ] **H3.4** `internal/tools/exec.go:167` — cancellation kills the shell but not its
  descendants, so grandchildren outlive timeouts and keep modifying the workspace. Fix:
  start in a process group, kill the group.  ⟨codex⟩
- [ ] **H3.5** `internal/agent/agent.go:285` — raw image bytes are persisted into transcript
  messages; four maximum-size attachments exceed the session reader's 16 MiB record limit
  and the session becomes **unresumable**. This is data loss with a resource cause; it sits
  here because the fix is the storage format. Fix: persist sanitized messages with image
  data replaced by external blob references.  ⟨codex⟩
- [ ] **H3.6** `internal/tuicmd/tuicmd.go:243` — a session switch **recurses** into `Run`,
  so the outer frame's defers (`store.Close`, `mcpClient.Close`, `lsps.Close`, `a.Close`,
  `bank.Close`) do not run until final unwind. Every switch spawns a fresh set of MCP
  server processes and LSP servers while all previous sets stay alive: N switches = N live
  copies of every MCP server. Fix: loop instead of recursing, tearing down before
  re-entering — the `/reload` path's `Reexec` already does this correctly.  ⟨fable⟩
- [ ] **H3.7** `internal/tuicmd/tuicmd.go:95` and `internal/runcmd/run.go:100` — `Resume` is
  called a second time purely to get the messages, and the returned `*Store` is discarded
  unclosed: a leaked fd and a redundant full-file parse per resume. Fix: reuse the messages
  from the first call, already in scope.  ⟨both⟩
- [ ] **H3.8** `internal/daemon/server.go:592` — each re-attach on a connection starts a new
  event relay without cancelling the previous one; the old goroutine blocks until the whole
  connection closes. Fix: per-subscription cancel, fire it on switch and detach.  ⟨both⟩
- [ ] **H3.9** `internal/daemon/server.go:537` — a writer-side encode failure exits only the
  writer goroutine; the reader, the connection, and the event producers stay live until
  queues wedge. Fix: close the connection and cancel its context on any writer failure.
  ⟨codex⟩
- [ ] **H3.10** `internal/lsp/client.go:311` — the protocol reader trusts `Content-Length`
  and allocates the whole body; a broken or hostile server exhausts memory. Fix: maximum
  frame size, reject negative and oversized lengths.  ⟨codex⟩
- [ ] **H3.11** `internal/tui/images.go:185` — `mmdc` is launched with no context and no
  timeout; a hung headless browser leaves a subprocess and a waiter goroutine forever. Fix:
  `CommandContext` with a bounded timeout, process-group cleanup.  ⟨codex⟩
- [ ] **H3.12** `internal/tui/attach.go:88` — clipboard commands run synchronously inside
  the UI update loop with no timeout or output bound; a paste can freeze the interface.
  `attach.go:132` reads a dropped image fully before checking the 4 MiB limit. Fix: async
  `tea.Cmd` with `CommandContext`; check size first, read at most limit+1.  ⟨codex⟩
- [ ] **H3.13** `internal/tui/app.go:1454` `loadModels` and `sessioncmd.go:288`
  `recallSessions` — synchronous network calls (5s model list, embed call) inside `Update`
  freeze the whole UI. Both are acknowledged in comments. Fix: convert to `tea.Cmd`.
  ⟨fable⟩
- [ ] **H3.14** Verify H3: a malformed SSE frame mid-stream leaves no goroutine behind
  (`goleak` or a runtime count); `read` on a 2 GB file refuses instead of dying; 20 session
  switches leave one MCP server set; a `bash` timeout leaves no orphan grandchildren.
  Tag `harden-3`.

## Phase H4 — Boundaries

Single-user, single-machine, no network listener — but "the workspace is trusted" is a
different claim from "model output is trusted", and the first does not imply the second.

- [ ] **H4.1** `internal/tui/highlight.go:114` and `internal/runcmd/run.go:222` — repository
  content and provider output reach the terminal without control-sequence sanitization. A
  file in a cloned repo, or a model persuaded to emit one, can run OSC 52 (clipboard write)
  or CSI operations against the user's terminal. `run.go:222` prints provider deltas
  straight to an interactive tty. Fix: strip C0/C1 and escape sequences, keeping newline
  and tab, then apply only application-owned styling; raw output only behind an explicit
  non-tty mode. Reproduce: a fixture file containing an OSC 52 payload, rendered, asserting
  the sequence does not reach the writer.  ⟨codex — high⟩
- [ ] **H4.2** `internal/session/store.go:81` — session names are joined into paths with no
  validation of absolute paths, separators, or `..`; only `Rename` (`checkpoint.go:189`)
  validates. `--resume '../x'` or `/fork ../../tmp/evil` escapes the sessions directory.
  Fix: hoist `Rename`'s validator into one basename check used by `Open`, `Fork`, and
  `Resume`, and assert the resolved path stays under the session root.  ⟨both⟩
- [ ] **H4.3** `internal/session/store.go:78` — session directories are `0755` and logs
  `0644`, so every prompt, tool output, and anything a model echoed is world-readable. Fix:
  `0700` directories, `0600` files, including temp and backup files.  ⟨codex⟩
- [ ] **H4.4** `internal/daemon/protocol.go:33` — the `/tmp/evilcode-$UID` fallback (used
  when `XDG_RUNTIME_DIR` is unset) does not verify ownership, mode, or symlink status, so a
  pre-existing attacker-created directory permits socket impersonation. `MkdirAll(0700)`
  does not fix an existing directory. Fix: `Lstat`, require current-user ownership and
  `0700`, reject symlinks, verify peer credentials on accept.  ⟨both⟩
- [ ] **H4.5** `internal/daemon/server.go:117` — TOCTOU in stale-socket cleanup: two daemons
  starting together can both fail the liveness dial, and the second's `os.Remove` unlinks
  the first's freshly bound socket, leaving daemon one running and unreachable. Fix: bind
  first, remove-and-retry only on `EADDRINUSE` after the dial check.  ⟨fable⟩
- [ ] **H4.6** `internal/tools/fs.go:89` — confinement resolves symlinks to validate and
  then opens the **original** path, leaving a swap race that escapes the workspace. Fix:
  descriptor-relative constrained open (`openat2` with `RESOLVE_BENEATH`), operate on that
  fd.  ⟨codex⟩
- [ ] **H4.7** `internal/lsp/ops.go:242` — rename applies server-supplied paths without
  confirming they stay inside the workspace. Fix: canonicalize each, reject any whose
  `filepath.Rel` escapes the client root.  ⟨codex⟩
- [ ] **H4.8** Verify H4: an OSC 52 payload in a repo file and in a mock provider response
  both render inert; `--resume ../escape` is refused; a fresh session dir is `0700`; the
  daemon refuses a squatted runtime dir. Tag `harden-4`.

## Phase H5 — Correctness and dead weight

Lower cost per firing, and the phase where a feature that never worked gets decided.

- [ ] **H5.1** `internal/daemon/registry.go:129` — `Write` stores the **display**
  (root-relative) path as the conflict key, but `Read`'s clearing loop matches on the
  **normalized absolute** path. The prefixes never match, so a delivered conflict is never
  cleared and re-reading a file never re-arms notification: the coordination feature
  silently degrades to fire-once-per-file-pair. Fix: canonical paths for identity, display
  paths for output only.  ⟨both⟩
- [ ] **H5.2** `internal/tui/app.go:1885` + `composer.go:51` — `!` shell mode is advertised
  in the help text (`app.go:1823`), has dedicated composer styling and a `syncShellMode`
  path, and **has no execution path**: `SendActionFor` routes `!`-prefixed input to
  `Submit`, and `submit` sends the literal `!cmd` to the model as a prompt. `plan.md`
  Phase 1 lists it as shipped. Decide: wire it to `Exec`, or delete the mode, the styling,
  and the help line. Do not leave a third state.  ⟨fable⟩
- [ ] **H5.3** `internal/tui/app.go:1706` `/rename` — after `session.Rename` the live
  `m.store.Name`/`Path` still hold the old name, so later `/rewind`, `/fork`, and `/save`
  target a path that no longer exists (appends still reach the renamed inode through the
  fd, so nothing is lost yet). Fix: update store identity with the rename, or enforce the
  notice the command already prints and refuse to rename the live session.  ⟨both⟩
- [ ] **H5.4** `internal/agent/agent.go:469` — a stream retried after deltas were already
  emitted replays visible content and events. Fix: retry only before the first emitted
  delta, or buffer attempts until success.  ⟨codex⟩
- [ ] **H5.5** `internal/provider/openai.go:245` — completed tool calls are emitted in
  first-arrival order rather than protocol index order, so out-of-order fragments can
  associate the wrong call with a later result. Fix: collect by index, sort before building
  the slice.  ⟨codex⟩
- [ ] **H5.6** `internal/provider/openai.go:150` `toOAIMessages` — tool-result messages
  never set `Name` from `m.ToolName`; some OpenAI-compatible gateways require it on
  `role:"tool"`. One line.  ⟨fable⟩
- [ ] **H5.7** `internal/provider/ollama.go:230` — synthesized tool-call IDs restart at
  `call_1` every request, so a multi-turn session persists duplicate `tool_call_id`s.
  Harmless to Ollama, breaks a session resumed against an OpenAI-kind provider. Fix:
  per-conversation or monotonic prefix.  ⟨fable⟩
- [ ] **H5.8** `internal/agent/compact.go:298` `Transcript` — truncates at
  `text[:CompactMessageCap]`, a byte index, splitting UTF-8 runes. The advisor's
  `truncateForAdvisor` already backtracks to a rune boundary; reuse it.  ⟨fable⟩
- [ ] **H5.9** `internal/tui/sessioncmd.go:187` — the rewind collapse summary computes
  `discarded := before[len(kept):]` where `before` came from `Conv.Messages()` (system
  message prepended) and `kept` came from the file (no system message), misattributing one
  boundary message. Fix: strip the system message from `before` first.  ⟨fable⟩
- [ ] **H5.10** `internal/session/store.go:253` — crash detection treats **any** historical
  `clean_exit` as proof the latest run exited cleanly, so a crash after a resume reports
  clean. Fix: derive status from the final lifecycle marker; append an explicit open marker
  on resume.  ⟨codex⟩
- [ ] **H5.11** `internal/session/checkpoint.go:166` `Save` — pin/unpin opens the session
  and `defer st.Close()`, and `Close` appends `MetaCleanExit`. Pinning therefore falsifies
  crash detection (compounding H5.10) and briefly runs a second writer on the live log.
  Fix: append the `saved`/`unsaved` meta directly, or add a close path that does not write
  the marker.  ⟨fable⟩
- [ ] **H5.12** `internal/session/store.go:160,102` — `Close` returns early on metadata or
  flush failure and can leave the descriptor open; a metadata-write failure during creation
  returns while leaving the new store live. Fix: always close, return `errors.Join`.
  ⟨codex⟩
- [ ] **H5.13** `internal/session/store.go:293`, `internal/memory/store.go:132`,
  `internal/todo/model.go:181` — all three skip malformed complete JSON records silently,
  hiding mid-log corruption and letting later writes bury the evidence. Fix, uniformly:
  tolerate only an unterminated **final** record; return a line-numbered error (or
  quarantine) for anything else. Todo's loader additionally swallows filesystem errors and
  treats corrupt state as empty.  ⟨codex⟩
- [ ] **H5.14** `internal/memory/store.go:206` — add, merge, and forget mutate in-memory
  records before appending their durable events, so a write failure leaves live state
  disagreeing with replay. Fix: persist first, commit to memory on success.  ⟨codex⟩
- [ ] **H5.15** `internal/memory/pipeline.go:323` — extraction drains its transcript batch
  *before* the provider call and JSON parse succeed; either error loses those turns
  permanently. Fix: clear only after success, or restore on failure.  ⟨codex⟩
- [ ] **H5.16** `internal/todo/model.go:282` — validation permits blank and duplicate IDs,
  invalid and self-referential dependencies, and confidence above 100, which produces
  ambiguous updates and bypasses the §12.3 gates. Fix: reject all of them.  ⟨codex⟩
- [ ] **H5.17** `internal/lsp/ops.go:258` — multi-file rename is compute-first (correct) but
  the write phase is sequential and in place, so a mid-way failure leaves the workspace
  partially renamed despite being described as atomic. Fix: stage and sync every
  replacement, verify sources are unchanged, commit with rollback.  ⟨both⟩
- [ ] **H5.18** `internal/wiring/wiring.go:79`, `internal/runcmd/run.go:66`,
  `internal/tuicmd/tuicmd.go:43` — provider and model resolution runs **before** repo
  overrides load, so a repo-pinned `default_model` never takes effect on the main run.
  §16 specifies per-repo pinning. Fix: load and apply overrides to the per-build config
  before resolving.  ⟨codex⟩
- [ ] **H5.19** `internal/wiring/wiring.go:160` — `todo.NewStore`'s error is swallowed
  (`if todos, terr := ...; terr == nil`), so a daemon or headless session silently has no
  todo tool and auto-poke reads empty state. Fix: log it at minimum; fail the build if the
  namespace was explicitly configured.  ⟨fable⟩
- [ ] **H5.20** Verify H5: a conflict clears after a re-read and re-fires on the next write;
  `!ls` either runs or is not offered; a corrupt session line is reported with its number
  rather than skipped; a repo-pinned model actually loads. Tag `harden-5`.
- [ ] **H5.22** `internal/session/store.go` `Messages` — replay does not check that every
  assistant `tool_call` has an adjacent result, so a session whose log was already
  malformed before H1.2/H1.3 landed — or truncated by a crash or a daemon shutdown mid-round
  — still produces the 400 those tasks fixed. Fix: stub the unanswered calls on replay, the
  way `runTools` stubs them live.  ⟨codex, reviewing H1.2⟩
- [ ] **H5.21** `internal/lsp/ops.go` `docPosition` — the outbound direction of H1.4.
  The `lsp` tool takes a 1-based column "as read prints it" and sends it as a protocol
  character, which is a UTF-16 code unit: on a line with non-ASCII text to the left of the
  symbol, the server is pointed at the wrong token, so `definition`, `references`, `hover`
  and `rename` act on the wrong thing or fail. Cannot corrupt a file the way H1.4 could —
  the server answers about something else rather than the edit landing wrong. Fix: convert
  rune column → UTF-16 unit against the line text at the boundary.  ⟨found while fixing H1.4⟩

---

# PART II — Ledger

## Dismissed findings

A finding that survived step 2 as *not a bug* goes here with its reason, and its task is
marked `[~]` rather than `[x]`. Deleting it instead guarantees the next review re-reports
it and the next agent re-investigates it.

| task | finding | why it is not a bug | date |
|---|---|---|---|
| | | | |

## What the reviews agreed on, and what that is worth

Twenty findings were reported independently by both models — same file, same mechanism.
Those are marked ⟨both⟩ and are the highest-confidence items here.

Severity did **not** correlate between the two on the items they shared: the daemon
runtime-directory issue was `high` to one reviewer and `low` to the other; the LSP atomic
rename likewise. Where they disagreed, this plan takes the higher severity, on the grounds
that the cheaper mistake is fixing something that turned out not to matter much.

Roughly forty findings were single-source. That is not evidence the other model checked
and disagreed — it read the same file and did not report it. Absence of a second sighting
means nothing either way.

## Notes for the next review sweep

- Both reviewers independently flagged unbounded reads and unbounded command output. The
  pattern — a documented limit that is declared and never enforced — is worth grepping for
  as a class rather than fixing three times.
- Both flagged silent `continue`-on-malformed-record in three separate packages. Same.
- Neither reviewer ran the code. Every finding is a static read. The reproduce-first step
  in §0.2 is the only thing standing between this plan and a set of confident, plausible,
  wrong edits.

---

# Definition of done

Every task is `[x]` or `[~]` with a reason. `-race` is green across `./...`. A session
survives compact, rewind, rename, and resume with no message lost. A cancelled turn
produces a transcript the provider will accept. Non-ASCII files survive the LSP tool.
Model output and repository content cannot drive the user's terminal. The overnight
breaker stops the loop it is supposed to stop. `LOOPS.md` carries one entry per task
naming the failing test that proved the bug was real — and for every `[~]`, the test that
refused to fail.
