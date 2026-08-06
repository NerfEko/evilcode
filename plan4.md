# evilcode — the plan that closes the gap with jcode

`plan.md` built evilcode. `plan2.md` made it survive being used. `plan3.md` made it feel
like evilcode instead of a jcode reskin. This document is narrower and harsher than any of
them: it is the list of places where **jcode is measurably better than evilcode**, read out
of jcode's source rather than its README, with the mechanism named and the fix specified.
It is the working spec for that pass and is checked off here.

The source is a full read of both codebases at the commits pinned in §0.5. Every claim
below was verified in the code, not in documentation — three of jcode's advertised
behaviors turned out not to exist, and those are in PART III rather than in a task. What
remains are the places where jcode had the better idea and the better implementation, and
evilcode should have them.

This plan is deliberately not a port. jcode is 653k lines of Rust across 78 crates;
evilcode is 44k lines of Go in one module and that ratio is a feature, not a debt. Every
task below asks for the *idea* at evilcode's scale — the retrieval maths, the truncation
policy, the safety verdict — not the crate that carries it. A task is done when the
behavior is on par, not when the line count is.

---

# PART 0 — Process: how the building agent works

## 0.1 What is different from `plan.md`, `plan2.md` and `plan3.md`

Every previous plan had one source of truth about whether a task was finished: the test
suite, plus a PNG for anything on screen. This one has a second, and it is the whole point
of the plan.

**A task in this plan is not complete until you have read jcode's implementation of the
same thing and concluded that evilcode's is at least on par.** Not "we shipped something in
that area" — read the file, read the function, compare behavior case by case, and write the
verdict down. If jcode still handles a case evilcode drops, the task stays `[ ]`.

That gate exists because this plan's failure mode is unique among the four. `plan2.md`
could not be faked: the reproduction failed or it did not. This one can be faked trivially
— write a `bm25` function, watch a unit test pass, mark it done, and ship retrieval that is
still worse than what it was copied from. The gate is the only thing standing between the
plan and that outcome.

Two things are stricter here than in any predecessor, and they are the reason this plan can
be trusted at all:

- **Every task ends in its own commit.** Including the ones that wrote no code. The history
  is the record of what this plan did; a task that landed inside somebody else's commit did
  not happen as far as anyone reading later is concerned.
- **Every commit that touched code goes through codex review, and the findings are worked
  before the next task starts.** `plan.md` and `plan2.md` treated codex as optional colour.
  Here it is mandatory — see §0.2 step 11. Between it and the parity gate, every line this
  plan produces is read twice by something that did not write it.

Tasks carry a loop tag, as in `plan3.md`:

- **⟨fix⟩** — evilcode's behavior is wrong today, not merely thinner. Run the `plan2.md`
  loop: reproduce, fail-then-pass, no exceptions.
- **⟨build⟩** — the behavior does not exist. Implement against PART I, leave one runnable
  check that fails if the logic breaks.
- **⟨port⟩** — the behavior exists in a weaker form. Neither a reproduction nor a green
  field: an existing test must be *strengthened* to assert the new behavior, and the old
  assertion must be shown to have passed under the old code.

## 0.2 The loop (repeat until this plan is complete)

1. Pick the next unchecked `[ ]` task in the current phase.
2. **Read jcode first.** Every task cites `⟨jcode: path:line⟩`. Open it in
   `/tmp/jcode-src` and read the whole function plus its tests before writing anything.
   The point is not to copy it — it is Rust, and half of it is scaffolding evilcode does
   not need — the point is that you cannot judge parity against something you have not
   read, and step 8 will ask you to.
3. **⟨fix⟩ only — reproduce.** Smallest test that fails *because of this bug*. Watch it
   fail. Cite the failure in `LOOPS.md`. It passes instead → your theory is wrong, keep
   looking; `[~]` is not available in this plan.
4. **Find the root, not the symptom.** Grep every caller before editing. Several tasks
   below share a mechanism (J1.6 and J2.3 both need the tool layer to know what the model
   has already been shown) — build it once.
5. Implement. Reuse what is here: `memory.Store`, `tools.Set`, `Exec.Bg`, `session.Store`,
   `daemon.Registry` and `agent.Compactor` all already exist and all get extended below
   rather than replaced.
6. `go build ./... && go vet ./... && go test ./...` green.
7. **⟨fix⟩ only** — reproduction from step 3 passes now. A fix without a fail-then-pass
   pair is not done.
8. **The parity gate — the step this plan exists for.**

   Go back to the jcode file from step 2 and compare it, function by function, against
   what you just wrote. Not a skim, not the README, not a summary: the code. Enumerate the
   inputs jcode handles and check each one against evilcode. Then write **one line** into
   the `LOOPS.md` entry, in exactly this shape:

   ```
   parity: crates/jcode-app-core/src/tool/read.rs:346-421 — on par
           (image → base64 to model, terminal display, dimensions; PDF via mmdc-free path
            deferred, see DEVIATIONS)
   ```

   The verdict is one of three words:

   - **better** — evilcode handles everything jcode does, plus something it does not. Say
     what the something is.
   - **on par** — same cases, same outcomes. Say which cases you checked.
   - **worse** — jcode handles a case evilcode drops. **The task stays `[ ]`.** Add a
     sub-task naming the dropped case and keep going.

   "worse, but deliberately" is not a verdict. If evilcode should not have a jcode
   behavior, that is a `DEVIATIONS.md` entry written *before* the verdict, and then the
   verdict is "on par (X deliberately not carried, see DEVIATIONS)".

   Two rules keep this honest:
   - **Name the case, not the feature.** "jcode has PDF support" is not a finding.
     "reading a `.pdf` returns the file's bytes as garbled text instead of an error" is.
   - **A test evilcode passes that jcode would fail is worth writing down** in the entry.
     Parity runs both ways and this plan should end with a list of them.

9. TUI-visible change → probe rig, PNG, **look at the image**. Very little of this plan is
   visible; where it is (J3.3, J8.1), the frame matters as much as the test.
10. Mark `[x]`. **Commit.** One task, one commit, always green — no exceptions and no
    batching. A ⟨prep⟩ task that wrote no code still commits: its `LOOPS.md` entry is the
    commit. A task that needed three attempts is still one commit; squash before landing,
    do not leave the false starts in the history.
11. **Codex review — required, not advisory.** Every commit that touched code goes through
    `codex:rescue` on that commit's diff. Kick it off in the background so it does not
    block the commit, but it must **run**, and its findings must be **resolved before the
    next task starts**:
    - Real finding → fix it, **its own commit**, subject `fix(J<n>.<m>): …`, and append a
      `LOOPS.md` entry for it. A finding fixed inside the next task's commit is a finding
      nobody can find later.
    - Dismissed → say why in the `LOOPS.md` entry and add it to the Dismissed findings
      ledger in PART III. "Codex was wrong" is a reason only when followed by *how*.
    - Nothing found → say that. A silent review is indistinguishable from a review that
      never ran.

    The one thing codex does not do is gate the commit. `go build ./... && go vet ./... &&
    go test ./...` is the gate; codex is a second reader whose findings are worked, not a
    green light waited on. Do not start `J<n>.<m+1>` with an unread review outstanding.
12. Append one entry to **`LOOPS.md`** (append-only, never edit old):
    `## <date> J<n>.<m>` — what was done, the reproduction for ⟨fix⟩, verification (test
    names / PNG filenames), **the parity line from step 8**, **the codex line from step
    11**, deviations. Two verdict lines per entry, both mandatory:

    ```
    parity: crates/jcode-app-core/src/tool/read.rs:346-421 — on par (…)
    codex:  2 findings — 1 fixed in a1b2c3d (nil deref on a zero-byte image),
            1 dismissed (suggested caching decoded bytes; read is once-per-turn)
    ```
13. Behavior changed → `README.md` in the same commit. Specced behavior deliberately not
    built → `DEVIATIONS.md`.

## 0.3 Reading a task

```
- [ ] **J5.2** `internal/memory/store.go:293` `Store.Search` — description — fix.
      ⟨port⟩ ⟨jcode: crates/jcode-base/src/memory.rs:830-897⟩
```

- **ID** — `J<phase>.<n>`. `plan.md` used `P`, `plan2.md` `H`, `plan3.md` `F`; `J` is this
  plan, so a `LOOPS.md` heading or a commit subject names its spec without looking.
- **`file:line` `symbol`** — evilcode paths, lines as-of the commit in §0.5. Lines drift
  the moment you start; the symbol does not. Trust the symbol.
- **⟨fix⟩ / ⟨build⟩ / ⟨port⟩** — which loop from §0.1.
- **⟨jcode: path:line⟩** — relative to `/tmp/jcode-src`. This is the file step 2 makes you
  read and step 8 makes you answer to. A range means read the whole range.

## 0.4 Ordering

| phase | what it is | why here |
|---|---|---|
| J1 | tool results the model can act on | every turn touches these; cheapest wins in the plan |
| J2 | search that costs less context than it saves | unblocks J1.6's exposure tracking |
| J3 | `bash` that survives long-running work | the most common way a turn is wasted today |
| J4 | the destructive-command gate | evilcode has *nothing* here; ordered after J3 only because J3 restructures the same file |
| J5 | retrieval that ranks correctly | biggest single quality gap in the plan |
| J6 | compaction that does not throw away the present | depends on J5's embedder |
| J7 | sessions that survive and can be found | |
| J8 | swarm notices worth reading | |
| J9 | skills that are found at all | 17 skills on this machine load as 0; J9.1–J9.3 depend on nothing and can be pulled to the front of the plan |
| J10 | overnight that reports what it did | last: it is the least-used path |

J1 first because it is the highest ratio of quality to effort in the plan and it needs
nothing else. J5 before J6 because the semantic half of compaction reuses J5's embedder;
building compaction first means building that twice. J4 could go first on a safety
argument and would be defensible — it is third only because J3 rewrites `bashTool`, and
gating a function that is about to move is wasted work.

Within a phase, order is yours. Adjacent tasks in one file are worth batching into one
investigate-fix cycle — still one commit each.

## 0.5 The jcode source, and the commits this plan is pinned to

**jcode source lives at `/tmp/jcode-src`.** If it is not there:

```sh
git clone https://github.com/1jehuang/jcode.git /tmp/jcode-src
```

Pinned reading (every line number in this plan is as-of these):

| | commit | note |
|---|---|---|
| evilcode | `8a983aa` | working tree was dirty at authoring time; re-grep the symbol if a line looks wrong |
| jcode | `0b0ce09`, `v0.64.2` | `master` |

Map of jcode, for the reading in step 2 — it is 78 crates and only six matter here:

| crate / path | what lives there |
|---|---|
| `crates/jcode-app-core/src/tool/` | every tool: `read.rs`, `edit.rs`, `bash.rs`, `bg.rs`, `agentgrep*`, `session_search*`, `batch.rs` |
| `crates/jcode-app-core/src/server.rs` + `server/` | the daemon, swarm state, file-activity coordination |
| `crates/jcode-base/src/memory.rs` + `memory_rerank.rs` | retrieval |
| `crates/jcode-base/src/compaction.rs` | compaction policy |
| `crates/jcode-base/src/session/` | persistence, journal replay, crash detection |
| `crates/jcode-command-risk/` | the destructive-command verdict |

Two reading habits that pay off: jcode keeps tests in the same file or a sibling
`*_tests.rs`, and they are the fastest way to learn what a function actually promises —
`server/file_activity_tests.rs` is 59 lines and tells you more about jcode's conflict
policy than 200 lines of `server.rs`. And the doc comments are load-bearing: `memory.rs:843`
explains the vector-space gate better than this plan does.

---

# PART I — What "on par" means, per area

Normative. Where a task cites a §, that § is what the parity gate in §0.2 step 8 is judged
against.

## §1 Tool results

### §1.1 `read` handles what a coding agent is handed

Today `read` accepts a text file and refuses everything else, and it refuses binaries with
a byte count and no next step. Required:

- **Images.** `.png .jpg .jpeg .gif .webp .bmp` are read, attached to the assistant turn as
  vision content via the existing `provider.Message.Images` path, and displayed inline
  through `internal/graphics` when the terminal supports it. Over the vision size ceiling:
  say the dimensions and the size and do not attach. A model that cannot see images must be
  told that rather than handed bytes.
- **Long lines.** A single line over 2000 characters is truncated with a marker. One
  minified bundle currently costs the whole read budget.
- **Missing paths suggest.** `read` on a path that does not exist scans the parent
  directory for names containing, or contained by, the requested one and names up to three.
  Costs one `ReadDir` on a path that was already an error.
- **Binary stays refused**, but the refusal says what to do instead.

Not required: PDF. `mmdc` is already a Node dependency and a second one for PDFs is worse
than the refusal. Log it in `DEVIATIONS.md` and move on.

### §1.2 `edit` explains a failed match

`old string not found` is true and useless. When the exact match fails, before returning:
try the trimmed string, and try a line-by-line comparison with whitespace normalized. If
either matches, say *which* and *where* — "found at line 412 with different indentation" —
because that is the error the model can act on without re-reading the file. Only if both
fail does the current message stand.

After a successful edit, return three lines of context either side of the change. Two
consecutive edits to one region currently need a re-read between them.

### §1.3 Several edits to one file, once

A `multiedit` form: one path, an ordered list of `{old, new, all}`, applied sequentially
against the accumulating content, reported per-edit as applied or failed with a reason, and
written **once**. Partial application is the correct outcome — an edit that fails does not
roll back the ones before it, it is reported and the rest continue. This is not new
capability, it is the removal of N-1 file rewrites and N-1 round trips.

The existing `lockPath` and `writeAtomic` invariants hold unchanged. This is one lock, one
atomic write.

### §1.4 A misspelled argument is repaired, not refused

`unmarshalArgs` uses `DisallowUnknownFields`, which was the right call when the target was
a frontier model and is the wrong one now that the default model is an 8B local. Before
rejecting: accept the common aliases a model reaches for (`file_path` for `path`,
`command` for `cmd`, `pattern` for `query`), coerce a number given as a string, and only
then fail. The strict error stays as the last resort and keeps naming the field.

The rule that does not bend: a repair is silent to the model but **visible in the tool
row**, because an argument that was quietly rewritten is one nobody will find later.

## §2 Search

### §2.1 `grep` returns structure, not just lines

Today `grep` shells to `rg` and hands back `file:line:text`. A model that gets twenty hits
across six files then reads all six files to find out what they are. Required: each hit
carries the **enclosing symbol** — the function, method or type it is inside — derived from
the file, so the model can rule a file out without opening it.

Do not write a parser per language. The existing `internal/lsp` client already answers
`documentSymbol` for any configured language, and the fallback for the rest is the nearest
preceding line matching a small set of declaration patterns. A wrong guess is acceptable;
an expensive one is not.

### §2.2 An outline mode

`grep` with no pattern and a path returns that file's symbol list with line numbers. This
is the cheapest possible "what is in this file" and it currently does not exist, so the
answer is a full `read`.

### §2.3 Results shrink to what has not been seen

Track, per session, which `file:line` ranges the model has already been shown — by `read`,
by a previous `grep`, or in `bash` output. A hit inside an already-shown range collapses to
a reference (`internal/tui/app.go:1204 — shown above`) instead of repeating the line. The
tracking resets at compaction, because after a compaction the model has not seen it any
more.

This is the single largest context saving available to evilcode and it is not a model
behavior, it is bookkeeping the harness already has enough information to do.

## §3 `bash`

### §3.1 A command that outlives its timeout is not killed

Today a command past its deadline is killed and the turn gets an error. Required: on
timeout, the command **keeps running** and is adopted by `Exec.Bg`; the tool returns the
task id, where its output is accumulating, and an instruction not to re-run it. A 4-minute
test suite under a 2-minute default currently costs the run twice — once killed, once
re-run longer.

### §3.2 Progress is parsed out of output

While a background command runs, parse its output lines for progress and expose it on
`BackgroundTask`: an explicit `EVILCODE_PROGRESS {json}` marker first, then the ordinary
shapes — `42%`, `3/10 tests`, `3 of 10 steps`, `1.5/3.0 GiB`, and phase lines
(`Compiling …`, `Building …`, `Running …`). Unparsed lines are output as before. The
existing background-tasks widget reads it; nothing new is drawn.

### §3.3 A tool to ask about background work

`Exec.Bg` tracks tasks and only the UI can see them. A `bg` tool with `list`, `status`,
`output`, `tail`, `wait`, `cancel`. **`wait` is the point**: without it a model polls, and
polling a 4-minute build costs a turn per poll. `wait` blocks until the task finishes or a
supplied timeout elapses and returns the tail.

### §3.4 An interactive prompt reaches the user

A command that blocks reading stdin currently hangs until the timeout with no output. Detect
it — on Linux, the child process tree's `wchan`/state, which is where jcode reads it — and
surface a prompt in the composer; the answer is written to the child's stdin. A `sudo` or a
`git rebase` that stalls silently for two minutes is the failure this removes.

If the detection proves unreliable, the fallback is a `stdin` parameter on `bash` supplying
input up front, and the detection goes in `DEVIATIONS.md`. Do not ship a heuristic that
fires on a busy CPU-bound process.

### §3.5 Large temp files go somewhere sane

Export `TMPDIR` and `EVILCODE_SCRATCH_DIR` into every `bash` child, pointing at a directory
under the data dir, and say so in the tool description. `/tmp` is RAM-backed on this
machine; a model that puts a worktree there fills memory.

## §4 The destructive-command gate

evilcode runs whatever the model emits. There is no confirmation, no classification, no
refusal. This is the largest single safety gap between the two harnesses and it needs a
mechanism, not a regex.

Required, as its own package so the policy seam is one file anyone can review:

1. **Tokenize** the command well enough to find the verbs and their targets through pipes,
   `&&`, `;`, subshells and simple quoting. Not a shell parser — a tokenizer that fails
   *closed*: anything it cannot parse is treated as higher risk, never lower.
2. **Classify the target's blast radius.** `/`, `$HOME`, `~/.ssh`, `~/.config`, the config
   and data dirs, `.git`, device nodes, and anything outside the workspace are separate
   tiers from a file inside the workspace.
3. **Verdict**, three outcomes:
   - *runs immediately* — the overwhelming default. A gate that fires often is a gate that
     gets disabled.
   - *refuse* — catastrophic target. Not overridable by the model.
   - *reflect* — plausible but broad. Return a refusal naming the blast radius and
     requiring a `justification` argument that says which user request it serves. A blind
     retry of the identical call must not satisfy it.

The distinction that makes this worth building rather than prompting: the reflect verdict
is *deterministic*, so it cannot be talked out of, and the justification is recorded in the
transcript where the user can see what the model claimed.

## §5 Retrieval

### §5.1 Embeddings from different models are never compared

`Store.Search` compares vectors when `len(r.Vec) == len(vec)`. Two different embedding
models with the same dimension — 768 is not rare — produce cosines that are noise, and
noise that outranks a real hit is worse than no recall.

Required: every `Record` stores the model id that produced its vector. Dense scoring only
considers records whose model matches the active embedder. Mismatched records stay
reachable through the lexical path (§5.2) rather than disappearing, and a `/memory` line
says how many are pending re-embedding.

This is the one task in the plan that is a **correctness bug**, not a capability gap.

### §5.2 Lexical and semantic are fused, not alternated

Today the substring fallback runs *only* when the embedding path returned nothing, so an
exact term match is invisible whenever recall is working. Required: both retrievers always
run — BM25 over the memory text, cosine over the vectors — and the results are fused by
reciprocal rank (`1/(k+rank)`, `k=60`), not by comparing incomparable scores. Kind weights
apply after fusion.

BM25 over a few thousand short strings needs no index. It is one pass for document
frequencies and one pass for scores.

### §5.3 The tail is cut where the scores fall off

A fixed `RecallCount = 4` injects four memories whether or not the fourth is any good.
Required: after ranking, walk the sorted scores and cut at the first drop larger than a
quarter of the range between the top score and the threshold. One strong hit stays one hit;
five near-equal hits stay five. The cap remains as a ceiling, not as the policy.

### §5.4 An embedder that is always there

Recall currently depends on the provider offering embeddings, so an OpenAI-compatible
endpoint without an embeddings route silently degrades every session to substring matching.
Required: a local embedder that needs no server, loaded from a model file fetched once and
cached in the data dir, with the provider path preferred when it is available and this as
the floor.

The scale check first: this is the largest new dependency in the plan. Do it as its own
⟨prep⟩ task that answers, in `LOOPS.md`, whether a pure-Go ONNX or GGUF path exists that
does not drag in cgo. If it does not, the honest outcome is a `DEVIATIONS.md` entry and
§5.1–§5.3 alone — which are most of the win regardless, because they fix ranking rather
than availability.

### §5.5 Recall is scoped

Memories carry a scope: project — keyed on the workspace root — or global. Recall in a
repository searches project ∪ global; `/memory` can list either. A preference stated in one
codebase currently surfaces in every unrelated one, which is how a memory bank becomes
noise.

## §6 Compaction

### §6.1 The recent turns survive

`Compactor.Compact` summarizes the whole conversation and replaces it with the summary.
Everything the model was in the middle of is gone. Required: summarize the **old** portion
and keep the most recent N turns verbatim, so a compaction that lands mid-task does not
cost the task. The boundary never splits a tool call from its result — an unanswered
`tool_use` is rejected by strict endpoints, which is the same invariant `runTools` and safe
point C already defend.

### §6.2 Compaction is predicted, not detected

`ShouldCompact` fires at 85% of the window, which means the turn that crosses 85% pays for
the summary. Required: track per-turn token deltas as an exponentially weighted moving
average, project forward a few turns, and compact when the *projection* crosses the
threshold — while there is still headroom, off the critical path of a turn.

### §6.3 A topic change is a free compaction point

When an embedder is available (§5.4), compare the mean embedding of the older half of the
active window against the newer half. A low similarity means the previous topic closed, and
that is the cheapest possible moment to summarize it — nothing in flight is lost. Falls back
to §6.2 when embeddings are unavailable; never blocks on them.

### §6.4 Relevance can hold a message back

Before summarizing the old portion, score its messages against the current goal (the recent
turns). A message far back that is highly relevant to what is happening now moves the cutoff
rather than being summarized away. Gaps are not allowed — the cutoff moves to *before* the
earliest relevant message, so the summarized range stays contiguous and tool-call integrity
holds.

## §7 Sessions

### §7.1 A torn line does not cost the tail

`session.Read` skips a line it cannot parse. That is right for the classic truncated-final-
line crash and wrong for the other one: when a writer dies mid-append without a newline, the
next append starts on the same line and produces `<torn json><complete entry>`. The whole
line is dropped and every entry in it with it. Required: on a parse failure, scan the line
for entry starts and stream-parse consecutive complete entries out of it, and log how many
were salvaged. The same treatment applies to `memory.jsonl`.

### §7.2 Sessions are searchable by content

The session picker searches names and, through episode memories, summaries. Neither finds
"the session where I fixed the anchor parser". Required: a `session_search` tool over the
session JSONL files, with a role filter (user / assistant / any), returning session name,
date and the matching excerpt. A cheap per-file index — size, mtime, and a term set — keyed
so an unchanged file is not re-read.

### §7.3 Sessions from other harnesses resume here

Claude Code, Codex and OpenCode all write session logs on this machine in known formats.
Reading one into an evilcode session is a converter, not an integration: map their message
shapes onto `provider.Message`, write a new session file, resume it. `evilcode resume
--from claude <id>`.

Ordered last in its phase and the first candidate to cut if the phase runs long: it is the
highest-effort, lowest-frequency item in the plan.

## §8 Swarm coordination

evilcode's conflict notice is better than jcode's in the case that matters — jcode
**excludes prior readers from its alerts entirely** (`server/state.rs:86-90`, and its own
test `latest_peer_touches_excludes_previous_readers_from_modification_alerts` says so), so
its README's headline swarm claim is not what its code does. evilcode notifies readers,
dedupes until re-read, and folds many notices into one. None of that changes.

What jcode's notice carries and evilcode's does not:

### §8.1 The notice says what the other agent was doing

jcode's writing tools take an `intent` argument and its notice quotes it plus a six-line
diff preview. evilcode's says a file changed. Required: `write` and `edit` accept an
optional `intent`; the notice carries it and the first few lines of the diff. "Rewrote the
anchor parser" plus three lines of diff is a notice an agent can act on without re-reading
the file; "app.go changed" is not.

### §8.2 The writer hears about the other writer

Today only readers are notified. Two agents that both *wrote* the same file are the actual
conflict and neither is told. Required: a write to a file another session has written since
this session last saw it notifies **both**, and says so in those terms.

### §8.3 Coordination state expires

`Registry.reads` and `Registry.writes` grow for the process lifetime, and a read from four
hours ago produces a notice about a file the agent has long since forgotten. Required:
accesses older than a bound stop generating notices, and the write log is trimmed. A stale
notice is worse than no notice — it trains the reader to ignore the next one.

### §8.4 A worker that stopped answering is visible

`spawn_worker` returns and the result arrives as a message. A worker that dies produces
nothing, forever, and the spawner waits. Required: workers heartbeat; a worker silent past
a bound is marked stale in `peers` output and the spawner is told once. Whether it can be
retried is out of scope; knowing it is gone is not.

## §9 Skills

The starting position, measured on this machine rather than assumed: `~/.agents/skills`
holds **17 skills** and evilcode can see **none** of them, for two unrelated reasons that
both have to be fixed before any of §9.4–§9.6 is worth building. `~/.claude/skills` is
likewise invisible. What evilcode does see is two files in `.evilcode/skills/`, and
`~/.config/evilcode/skills/` does not exist.

### §9.1 The skills evilcode can see are the skills on the machine

`SkillDirs` searches `<repo>/.evilcode/skills` and `<configDir>/skills`. Nothing else.
`~/.agents/skills` is where this machine's skill library actually lives, and
`~/.claude/skills` is where a second one does.

Required search order, nearest first, first name wins so a repo can shadow a global:

1. `<repo>/.evilcode/skills`
2. `<repo>/.agents/skills`
3. `~/.config/evilcode/skills`
4. `~/.agents/skills`
5. `~/.claude/skills`

There is precedent and it is already in the codebase: `internal/agent/context.go` searches
cwd → git root → config dir for `AGENTS.md` *and* `CLAUDE.md`, because the convention is
shared and refusing to read the neighbour's file helps nobody. Skills are the same
situation and got the narrower answer.

A skill found in more than one directory is loaded once, from the nearest, and `/skills`
says which directory each came from — otherwise shadowing is invisible and debugging it
means guessing.

### §9.2 A skill is a directory

Every skill in `~/.agents/skills` is `<name>/SKILL.md`, most with sibling material —
`agent-architect/references/`, and seven others with subdirectories. `LoadSkills` indexes
top-level `*.md` and would find nothing in any of them even once §9.1 lands.

Required: `<name>/SKILL.md` is a skill, named for its directory, with the directory as its
own working material — a skill can ship the script it tells the model to run, and the
`skill` tool's result names the directory so the model can read what is beside it. The flat
`<name>.md` form keeps working; `.evilcode/skills/selfdev.md` is one and must not break.

### §9.3 Front matter is parsed, not pattern-matched

`skillSummary` finds a description by `CutPrefix(line, "description:")` on each line of the
front matter. Against the real files that is wrong twice:

- `agent-architect/SKILL.md` opens `description: >` with the text folded across the four
  following indented lines. Current code returns `>` as the description — a skill index
  entry that says nothing, in the prompt, forever.
- A description spanning lines with no folding marker keeps only the first.

Required: parse the front-matter block as YAML rather than scanning it for a prefix, and
handle at least `description:` inline, `>` folded and `|` literal. It is a small block at
the head of a file; the naive scan was cheap and is wrong on the majority of skills that
actually exist here.

Verify against `~/.agents/skills/agent-architect/SKILL.md` specifically. It is the file that
breaks the current parser.

### §9.4 A skill can narrow the tool set

Front matter `allowed-tools:` restricts the tool set for the turns after that skill is
loaded. `~/.agents/skills/agent-browser/SKILL.md` already declares one and evilcode ignores
it. A review skill that cannot write files is a meaningfully different thing from a review
skill that asks nicely.

### §9.5 A skill arrives when it is relevant

Every skill's name and one-liner sits in the system prompt, which is cache-stable — the
reason it was built that way — and stops scaling somewhere around thirty skills. Required,
once §5 lands: skills are embedded alongside memories and a strong match injects the skill's
*summary* with a note that the body is one `skill` call away.

The index stays in the prompt. The trade is explicit and belongs in `DEVIATIONS.md`: a
per-turn injection costs prompt-cache stability, so it is gated behind a config key and off
by default until the skill count justifies it. Note that §9.1 alone takes this machine from
2 skills to 19, which is most of the way to the point where the index stops being free.

### §9.6 Skills reload without a restart

`LoadSkills` runs once at startup. Editing a skill mid-session requires a restart, which is
exactly what makes skills annoying to author. `/skills reload`, and re-read a body whose
file mtime changed.

## §10 Overnight

### §10.1 It knows what it is starting from

Before the first turn: git state (branch, dirty files, HEAD), the todo list, and the token
budget, recorded. An overnight run that ends cannot currently answer "what did it change",
because nothing recorded what it started from.

### §10.2 Per-task outcome, with validation

For each todo the run touches, record what it was before, what it is after, and **what was
run to check it** — the tests, the build, the command. A todo marked done with no validation
recorded is reported as unvalidated. The `todo` tool's confidence gates already collect most
of this; overnight currently ignores it.

### §10.3 A report exists in the morning

At the end, write a single self-contained HTML file to the data dir and name its path in the
transcript: what ran, per-task cards from §10.2, the timeline, tokens spent, git diffstat,
and which limit stopped the run. Scrolling back through eight hours of transcript is the
thing this replaces.

---

# PART II — Phases

Line numbers as-of §0.5. Trust the symbol.

## Phase J1 — Tool results the model can act on

- [x] **J1.1** `internal/tools/fs.go:178` `FS.readTool` — reading an image returns a binary
      refusal. Detect image extensions, attach the bytes to the result for the vision path,
      and display inline via `internal/graphics` when the protocol allows. Over the ceiling:
      report dimensions and size without attaching. §1.1. ⟨build⟩
      ⟨jcode: crates/jcode-app-core/src/tool/read.rs:346-421⟩
- [x] **J1.2** `internal/tools/fs.go:178` `FS.readTool` — a single 2000+ character line
      consumes the read budget. Truncate per line with a marker; count how many were
      truncated and say so once. §1.1. ⟨port⟩
      ⟨jcode: crates/jcode-app-core/src/tool/read.rs:13,210-221⟩
- [x] **J1.3** `internal/tools/fs.go:178` `FS.readTool` — `read` on a missing path returns
      the bare `os.Stat` error. Scan the parent directory and name up to three near matches.
      §1.1. ⟨build⟩ ⟨jcode: crates/jcode-app-core/src/tool/read.rs:307-330⟩
- [x] **J1.4** `internal/tools/fs.go:477` `FS.editTool` — a failed match says only "not
      found". Try trimmed, then whitespace-normalized line windows; on either, report which
      and at what line. §1.2. ⟨port⟩
      ⟨jcode: crates/jcode-app-core/src/tool/edit.rs:256-290⟩
- [x] **J1.5** `internal/tools/fs.go:477` `FS.editTool` — return three lines of context
      either side of a successful edit, so a consecutive edit needs no re-read. §1.2.
      ⟨build⟩ ⟨jcode: crates/jcode-app-core/src/tool/edit.rs:234-254,139-147⟩
- [x] **J1.6** `internal/tools/fs.go:168` `FS.Tools` — add `multiedit`: one path, ordered
      edits, applied against accumulating content, reported per-edit, **one** `lockPath` and
      **one** `writeAtomic`. Partial application is the correct outcome. §1.3. ⟨build⟩
      ⟨jcode: crates/jcode-app-core/src/tool/multiedit.rs:78-161⟩ — note jcode writes this
      one non-atomically and with no lock; evilcode's must not.
- [x] **J1.7** `internal/tools/tools.go:229` `unmarshalArgs` — `DisallowUnknownFields`
      rejects a model that spells a field the way three other harnesses do. Alias the common
      forms, coerce string-wrapped numbers, then fail as now. Repairs show in the tool row.
      §1.4. ⟨port⟩ ⟨jcode: crates/jcode-app-core/src/tool/batch.rs:105-164,
      crates/jcode-app-core/src/tool/serde_coerce.rs:52-140⟩
- [x] Verify J1: read an image, a minified JS file, a misspelled path; run a two-hunk
      `multiedit`; call `edit` with wrong indentation and read the error; call `read` with
      `file_path` instead of `path`. Tag `jcode-1`.

## Phase J2 — Search that pays for itself

- [x] **J2.1** `internal/tools/exec.go:341` `Exec.grepTool` — hits carry no structure.
      Attach the enclosing symbol per hit, via `internal/lsp` `documentSymbol` where a
      server is configured and a declaration-pattern scan otherwise. §2.1. ⟨build⟩
      ⟨jcode: crates/jcode-app-core/src/tool/agentgrep.rs:1-120 and its `outline` mode⟩
- [x] **J2.2** `internal/tools/exec.go:341` `Exec.grepTool` — no way to ask what is in a
      file short of reading it. `grep` with a path and no pattern returns the symbol
      outline. §2.2. ⟨build⟩ ⟨jcode: crates/jcode-app-core/src/tool/agentgrep.rs, `run_outline`⟩
- [x] **J2.3** `internal/tools/tools.go:26` `Result` — nothing tracks what the model has
      already been shown, so `grep` after `read` repeats lines that are already in context.
      Record shown `file:line` ranges per session; collapse a hit inside one to a reference;
      reset at compaction. §2.3. ⟨build⟩
      ⟨jcode: crates/jcode-app-core/src/tool/agentgrep/context.rs:1-80⟩
- [x] Verify J2: grep a symbol that appears in six files and confirm the enclosing names are
      right; outline a large file; read a file then grep inside it and confirm the second
      result collapses. Tag `jcode-2`.

## Phase J3 — `bash` that survives long work

- [x] **J3.1** `internal/tools/exec.go:140` `Exec.bashTool` — a command past its timeout is
      killed and the work is lost. Adopt it into `Exec.Bg` instead; return the task id and
      where output is landing, and tell the model not to re-run it. §3.1. ⟨fix⟩
      ⟨jcode: crates/jcode-app-core/src/tool/bash.rs:885-925⟩
- [x] **J3.2** `internal/tools/exec.go:239` `Exec.runBackground` — background output is
      opaque until the task ends. Parse progress: explicit `EVILCODE_PROGRESS {json}` first,
      then `42%`, `3/10 tests`, `3 of 10 steps`, `1.5/3.0 GiB`, phase prefixes. Expose on
      `BackgroundTask`. §3.2. ⟨build⟩
      ⟨jcode: crates/jcode-app-core/src/tool/bash.rs:167-398⟩
- [x] **J3.3** `internal/tools/exec.go:69` `Exec.Tools` — the model cannot see its own
      background tasks. Add a `bg` tool: `list`, `status`, `output`, `tail`, `wait`,
      `cancel`. `wait` blocks to completion or a timeout — without it a model polls and
      each poll costs a turn. §3.3. ⟨build⟩
      ⟨jcode: crates/jcode-app-core/src/tool/bg.rs:34-120,460-501⟩
- [x] **J3.4** `internal/tools/exec.go:140` `Exec.bashTool` — a command blocking on stdin
      hangs to the timeout with no output. Detect via the child process tree and prompt in
      the composer; write the answer to the child's stdin. Unreliable → fall back to a
      `stdin` parameter and log it. §3.4. ⟨build⟩
      ⟨jcode: crates/jcode-app-core/src/tool/bash.rs:799-860, and its `stdin_detect` module⟩
- [x] **J3.5** `internal/tools/exec.go:140` `Exec.bashTool` — children inherit `/tmp`, which
      is RAM-backed here. Export `TMPDIR` and `EVILCODE_SCRATCH_DIR` under the data dir and
      say so in the description. §3.5. ⟨build⟩
      ⟨jcode: crates/jcode-app-core/src/tool/bash.rs:453-472,32⟩
- [x] Verify J3: run a command that exceeds its timeout and confirm it finishes in the
      background and the model is told; `bg wait` on it; run `sleep 1 && read x` and answer
      the prompt; check `$TMPDIR` inside a `bash` call. PNG of the background-task widget
      mid-progress. Tag `jcode-3`.

## Phase J4 — The destructive-command gate

- [x] **J4.1** `internal/tools/` — new package `commandrisk`: a tokenizer that finds verbs
      and targets through pipes, `&&`, `;`, subshells and simple quoting, and **fails
      closed** — unparseable is high risk, never low. §4. ⟨build⟩
      ⟨jcode: crates/jcode-command-risk/src/tokenize.rs and tokenize_tests.rs⟩
- [x] **J4.2** `internal/tools/commandrisk` — blast-radius classification of targets: `/`,
      `$HOME`, `~/.ssh`, config and data dirs, `.git`, device nodes, outside-workspace, and
      inside-workspace as distinct tiers. §4. ⟨build⟩
      ⟨jcode: crates/jcode-command-risk/src/paths.rs and paths_tests.rs⟩
- [x] **J4.3** `internal/tools/commandrisk` — the verdict: run / refuse / reflect. Reflect
      returns a refusal naming the blast radius and requires a `justification`; an identical
      blind retry must not satisfy it. §4. ⟨build⟩
      ⟨jcode: crates/jcode-command-risk/src/gate.rs and gate_tests.rs⟩
- [x] **J4.4** `internal/tools/exec.go:140` `Exec.bashTool` — wire the gate ahead of
      execution and ahead of the background branch, add `justification` to the schema, and
      render a held command distinctly in the transcript. §4. ⟨build⟩
      ⟨jcode: crates/jcode-app-core/src/tool/bash_destructive_gate.rs (whole file — 84 lines)⟩
- [x] Verify J4: `rm -rf /` refused; `rm -rf $HOME/projects` held then allowed with a
      justification; `rm -rf ./build` runs untouched; `git clean -xfd` at a repo root
      classified; a hundred ordinary commands from `LOOPS.md` run with zero false
      positives — **that last one is the acceptance test**, a gate that fires on ordinary
      work gets turned off. Tag `jcode-4`.

## Phase J5 — Retrieval that ranks correctly

- [ ] **J5.1** `internal/memory/store.go:293` `Store.Search` — vectors from different models
      with equal dimension are compared and produce noise. Store the model id on `Record`;
      dense scoring considers only the active model; mismatched records stay reachable
      lexically; `/memory` reports how many are pending. §5.1. ⟨fix⟩
      ⟨jcode: crates/jcode-base/src/memory.rs:830-897 — read the comment at :843⟩
- [ ] **J5.2** `internal/memory/store.go:293,329` `Store.Search`, `substringHits` — lexical
      matching runs only when the semantic path found nothing, so an exact term match is
      invisible whenever recall works. Run both always; fuse by reciprocal rank (`k=60`);
      apply kind weights after fusion. Replace `substringHits` with BM25. §5.2. ⟨port⟩
      ⟨jcode: crates/jcode-base/src/memory.rs:668-728 (RRF), 1991-2055 (BM25)⟩
- [ ] **J5.3** `internal/memory/pipeline.go:68,219` `RecallCount`, `Manager.Recall` — a fixed
      four injects the fourth memory whether or not it is any good. Cut at the first score
      drop wider than a quarter of the range from top to threshold; keep the count as a
      ceiling. §5.3. ⟨port⟩ ⟨jcode: crates/jcode-base/src/memory.rs:899-927⟩
- [x] **J5.4** ⟨prep⟩ — answer in `LOOPS.md` whether a local embedder can be had in pure Go
      without cgo, and at what model size and startup cost. No code. The answer decides
      J5.5. Prep complete; see `LOOPS.md` and `DEVIATIONS.md`.
      ⟨jcode: crates/jcode-embedding/src/lib.rs:89-250 for what it costs them⟩
- [x] **J5.5** `internal/memory/pipeline.go:182` `Manager.embed` — recall dies with the
      provider's embeddings route. Provider embeddings remain preferred and BM25 is the
      availability floor; the local runtime is skipped per J5.4 and `DEVIATIONS.md`.
      §5.4. ⟨build⟩
      ⟨jcode: crates/jcode-base/src/memory.rs:815-828⟩
- [ ] **J5.6** `internal/memory/store.go:60` `Record` — one flat bank means a preference
      stated in one repo surfaces in every unrelated one. Add scope (project keyed on
      workspace root, or global); recall searches project ∪ global; `/memory` filters.
      §5.5. ⟨build⟩ ⟨jcode: crates/jcode-base/src/memory.rs:734-791 (`MemoryScope`)⟩
- [ ] Verify J5: two banks embedded by different models with equal dimension, confirm no
      cross-scoring; a memory matching by exact term but not semantically now recalls; a
      query with one strong and four weak hits injects one; a project memory does not leak
      into another workspace. Tag `jcode-5`.

## Phase J6 — Compaction that keeps the present

- [ ] **J6.1** `internal/agent/compact.go:101` `Compactor.Compact` — the whole conversation
      is replaced by a summary, so a compaction mid-task costs the task. Summarize the old
      portion, keep the recent N turns verbatim, never split a tool call from its result.
      §6.1. ⟨fix⟩ ⟨jcode: crates/jcode-base/src/compaction.rs:1143-1281⟩
- [ ] **J6.2** `internal/agent/compact.go:136` `Compactor.ShouldCompact` — firing at 85%
      means the turn that crosses 85% pays. Track per-turn deltas as an EWMA, project
      forward, compact on the projection. §6.2. ⟨port⟩
      ⟨jcode: crates/jcode-base/src/compaction.rs:514-548⟩
- [ ] **J6.3** `internal/agent/compact.go:136` `Compactor.ShouldCompact` — a topic change is
      the free compaction point and nothing looks for it. Compare mean embeddings of the old
      and new halves of the window; low similarity triggers. Falls back to J6.2 without an
      embedder; never blocks on one. §6.3. ⟨build⟩
      ⟨jcode: crates/jcode-base/src/compaction.rs:550-601⟩
- [ ] **J6.4** `internal/agent/compact.go:78` `Transcript` — a highly relevant old message is
      summarized away with its neighbours. Score the old portion against the recent turns and
      move the cutoff to before the earliest relevant message, keeping the range contiguous.
      §6.4. ⟨build⟩ ⟨jcode: crates/jcode-base/src/compaction.rs:603-675⟩
- [ ] Verify J6: compact mid-task and confirm the last turns are verbatim and no `tool_use`
      is unanswered; confirm compaction fires before the threshold rather than at it;
      confirm a hard topic switch triggers one; confirm a relevant early message survives.
      Tag `jcode-6`.

## Phase J7 — Sessions that survive and can be found

- [ ] **J7.1** `internal/session/store.go:379` `Read` — a torn append glued to the next one
      drops both and every entry on that line. Scan a failed line for entry starts,
      stream-parse the complete entries out of it, log the salvage count. Same for
      `internal/memory/store.go:113` `Store.load`. §7.1. ⟨fix⟩
      ⟨jcode: crates/jcode-base/src/session/persistence.rs:26-129⟩
- [ ] **J7.2** `internal/tools/` — no way to find a session by what was said in it. A
      `session_search` tool over the session files with a role filter, returning name, date
      and excerpt; a per-file size+mtime+term-set index so unchanged files are not re-read.
      §7.2. ⟨build⟩ ⟨jcode: crates/jcode-app-core/src/tool/session_search.rs:125-300,
      session_search_index.rs:1-120⟩
- [ ] **J7.3** `internal/session/` — sessions written by Claude Code, Codex and OpenCode sit
      on this disk and cannot be resumed. Convert their message shapes onto
      `provider.Message`, write a session, resume it. `evilcode resume --from claude <id>`.
      §7.3. **Cut this first if the phase runs long.** ⟨build⟩
      ⟨jcode: crates/jcode-base/src/import.rs:598-760 (claude), :1057-1120 (codex),
      :1187+ (pi)⟩
- [ ] Verify J7: hand-corrupt a session with a glued line and confirm the tail survives;
      search for a phrase from a week-old session; import one Claude Code session and
      continue it. Tag `jcode-7`.

## Phase J8 — Notices worth reading

- [ ] **J8.1** `internal/daemon/registry.go:42` `Conflict.Notice` — the notice says a file
      changed and nothing about what changed. Add an optional `intent` to `write` and `edit`;
      carry it and the first lines of the diff into the notice. §8.1. ⟨build⟩
      ⟨jcode: crates/jcode-app-core/src/tool/edit.rs:10-11,118-137 (intent + preview),
      crates/jcode-app-core/src/server.rs:2044-2107 (the notice text)⟩
- [ ] **J8.2** `internal/daemon/registry.go:138` `Registry.Write` — only readers are
      notified, so two agents that both wrote the same file are the one conflict nobody
      hears about. Notify both writers, in those terms. §8.2. ⟨fix⟩
      ⟨jcode: crates/jcode-app-core/src/server.rs:2108-2163, server/state.rs:79-104 —
      and note jcode does *only* this and never notifies readers; evilcode keeps both⟩
- [ ] **J8.3** `internal/daemon/registry.go:78` `Registry` — `reads` and `writes` grow for
      the process lifetime and a four-hour-old read still fires a notice. Expire accesses
      past a bound; trim the write log. §8.3. ⟨fix⟩
      ⟨jcode: crates/jcode-app-core/src/server.rs:1930-1939 (`TOUCH_EXPIRY`)⟩
- [ ] **J8.4** `internal/daemon/spawn.go` — a worker that dies is silent forever and the
      spawner waits forever. Heartbeat; mark a silent worker stale in `peers`; tell the
      spawner once. §8.4. ⟨build⟩
      ⟨jcode: crates/jcode-app-core/src/server/swarm.rs:554-620
      (`refresh_swarm_task_staleness`)⟩
- [ ] Verify J8: two attached sessions, one edits with an intent, confirm the other's notice
      quotes it with diff lines; two sessions write the same file and both are told; a read
      from past the expiry produces no notice; kill a worker and watch `peers` mark it. PNG
      of the notice. Tag `jcode-8`.

## Phase J9 — Skills that scale

**J9.1–J9.3 are one bug in three parts.** This machine has 17 skills in `~/.agents/skills`
and evilcode loads zero of them. Do not start J9.4 until `/skills` lists all 17 with real
descriptions — the later tasks are refinements of a feature that currently does not reach
its input.

- [ ] **J9.1** `internal/tools/skill.go:34` `SkillDirs` — the search path is
      `.evilcode/skills` and `<configDir>/skills` only, so `~/.agents/skills` (17 skills on
      this machine) and `~/.claude/skills` are invisible. Search, nearest first:
      `<repo>/.evilcode/skills`, `<repo>/.agents/skills`, `<configDir>/skills`,
      `~/.agents/skills`, `~/.claude/skills`. Nearest wins on a name clash; `/skills` names
      the source directory per skill. §9.1. ⟨fix⟩
      ⟨jcode: crates/jcode-base/src/skill.rs:222-295 (`load`, `load_global`,
      `load_project_overlay`, `merge_overlay`)⟩
      — evilcode already does exactly this for `AGENTS.md`/`CLAUDE.md` in
      `internal/agent/context.go`; match that shape.
- [ ] **J9.2** `internal/tools/skill.go:50` `LoadSkills` — indexes top-level `*.md`, so
      every `<name>/SKILL.md` is skipped; all 17 skills on this machine are that layout, and
      eight ship sibling directories they reference. Load `<name>/SKILL.md` named for its
      directory, expose the directory path in the `skill` tool result, keep the flat
      `<name>.md` form working (`.evilcode/skills/selfdev.md` must not break). §9.2.
      ⟨fix⟩ ⟨jcode: crates/jcode-base/src/skill.rs:416-476⟩
- [ ] **J9.3** `internal/tools/skill.go:83` `skillSummary` — finds the description by
      `CutPrefix(line, "description:")`, which returns `>` for a YAML folded block and drops
      every continuation line. `~/.agents/skills/agent-architect/SKILL.md` is exactly this
      and yields an empty index entry. Parse the front-matter block as YAML; handle inline,
      `>` folded and `|` literal. §9.3. ⟨fix⟩
      ⟨jcode: crates/jcode-base/src/skill.rs:505-523 (`parse_frontmatter`, via serde_yaml)⟩
- [ ] **J9.4** `internal/tools/skill.go:178` `NewSkillTool` — a skill cannot narrow what the
      model may do, and `~/.agents/skills/agent-browser/SKILL.md` already declares
      `allowed-tools` that evilcode ignores. Restrict the tool set for turns after load.
      §9.4. ⟨build⟩ ⟨jcode: crates/jcode-base/src/skill.rs:14-33,478-503⟩
- [ ] **J9.5** `internal/tools/skill.go:114` `SkillSet.Index` — the whole index sits in the
      prompt; J9.1 takes it from 2 entries to 19 and it stops being free somewhere past 30.
      Embed skills alongside memories (needs J5); a strong match injects the summary.
      Config-gated, **off by default**, prompt-cache trade written into `DEVIATIONS.md`.
      §9.5. ⟨build⟩ ⟨jcode: crates/jcode-base/src/memory.rs:777-806
      (`synthetic_skill_entries`), crates/jcode-memory-types/src/lib.rs:779-820
      (`skill_retrieval_bonus`)⟩
- [ ] **J9.6** `internal/tools/skill.go:50` `LoadSkills` — authoring a skill needs a
      restart, which is what makes skills annoying to write. `/skills reload`, and re-read a
      body whose mtime moved. §9.6. ⟨build⟩
      ⟨jcode: crates/jcode-base/src/skill.rs:547-580⟩
- [ ] Verify J9: `/skills` lists all 17 from `~/.agents/skills` plus the 2 in
      `.evilcode/skills`, each with a real one-line description and its source directory;
      `agent-architect` specifically shows its folded description, not `>`; load
      `niri-screenshot` and confirm the body and its directory arrive; a repo skill shadows
      a global one of the same name; a skill that forbids `write` is respected; edit a skill
      mid-session and reload; with J9.5 on, a matching prompt surfaces one unasked.
      Tag `jcode-9`.

## Phase J10 — Overnight that reports

- [ ] **J10.1** `internal/tui/overnight.go:56` `Overnight.Start` — nothing records the
      starting state, so the run cannot say what it changed. Snapshot git (branch, HEAD,
      dirty files), the todo list, and the budget. §10.1. ⟨build⟩
      ⟨jcode: crates/jcode-overnight-core/src/lib.rs:155-202 (`GitSnapshot`,
      `OvernightPreflight`)⟩
- [ ] **J10.2** `internal/tui/overnight.go:193` `Model.stepOvernight` — a todo marked done
      overnight carries no evidence. Record before / after / what was run to check it per
      touched todo; report a done-with-no-validation todo as unvalidated. The `todo` tool's
      gates already hold most of this. §10.2. ⟨build⟩
      ⟨jcode: crates/jcode-overnight-core/src/lib.rs:176-258 (`OvernightTaskCard*`)⟩
- [ ] **J10.3** `internal/tui/overnight.go:67` `Overnight.Stop` — the only record is eight
      hours of transcript. Write one self-contained HTML file to the data dir and name its
      path: what ran, J10.2's cards, timeline, tokens, git diffstat, which limit stopped it.
      §10.3. ⟨build⟩ ⟨jcode: crates/jcode-overnight-core/src/lib.rs:461-720
      (`render_task_cards_html`, `build_review_html`, `render_timeline_html`)⟩
- [ ] Verify J10: a short overnight against a three-item todo list; open the HTML; confirm
      the stop reason, the diffstat and one unvalidated todo all appear. Tag `jcode-10`.

---

# PART III — Ledger

## Deliberately out of scope

Named here so nobody re-derives them from the comparison and adds them back.

| area | why not |
|---|---|
| **Providers / auth** | jcode has ~35 providers, OAuth for four subscription vendors, multi-account, pricing and quota tracking. Out of scope by instruction. The one small thing that is *not* provider breadth — honoring `Retry-After` in the retry loop at `internal/agent/agent.go:546` — is left here as a note rather than a task, because the file is provider-edge. Twenty lines whenever the exclusion lifts. |
| **MCP** | evilcode wraps the official SDK in 219 lines; jcode hand-rolled 3.8k. jcode handles image and resource content blocks and evilcode drops them, which is the one real gap — and it also auto-executes MCP servers named by files in the *repository*, which evilcode deliberately does not. Not a task. |
| **Widgets / dock** | Identical design and identical constants already. jcode implements 15 kinds to evilcode's 8; that is `plan3.md`'s territory, not this plan's. |
| **GUI / transcript rendering** | Excluded by instruction. |
| **Themes** | Both do Oklab, harmony scoring, seed generation and gamut mapping. jcode's adjacency-graph scoring is better than evilcode's fixed pair list; excluded by instruction. |
| **Self-dev** | jcode's canary + smoke-test + rollback + cross-session build queue is better than `/rebuild`. Excluded by instruction. Worth one line anyway: evilcode gates the restart on `go test ./...` and jcode does not, so the two are not strictly ordered. |

## Where jcode's README is ahead of jcode's code

Verified while reading. Do not port these; there is nothing there to port, and knowing it
saves the next reader the trip.

- **"When agent A edits a file agent B has read, the server notifies agent B."**
  `crates/jcode-app-core/src/server/state.rs:86-90` filters to modifications only, and the
  comment at `server.rs:2007` says plain reads deliberately do not alert. Their own test is
  named `latest_peer_touches_excludes_previous_readers_from_modification_alerts`. evilcode
  already does the advertised thing; §8 only adds what jcode's notice *carries*.
- **Worker results validated against a schema.** jcode's swarm has no `result_schema`
  anywhere. `spawn_worker` at `internal/tools/swarm.go:64` already does this and is the
  better design; keep it.
- **Atomic file writes.** jcode has a careful atomic writer at
  `crates/jcode-storage/src/lib.rs:540-600` and uses it for its own state, but `edit.rs:113`,
  `write.rs:78` and `multiedit.rs:129` all call `tokio::fs::write` — truncate then write —
  with no per-path lock, inside a batch that runs ten subcalls concurrently. evilcode's
  `writeAtomic` and `lockPath` are ahead here. **J1.6 must not regress to jcode's shape**
  when it lands `multiedit`.

## Dismissed findings

Empty at authoring. Two sources feed it, neither ever deleted:

- **Parity gate** (§0.2 step 8) — a jcode behavior judged deliberately not worth carrying.
  Note the `DEVIATIONS.md` entry it pairs with.
- **Codex review** (§0.2 step 11) — a finding not acted on. Format:
  `J<n>.<m> · <finding in one line> · dismissed: <why>`. "Codex was wrong" needs the *how*
  after it, otherwise it is not a reason, it is a shrug.

### J1.1 dismissed findings

- **J1.1 · inline images paint at the frame cursor, not over their reserved block rows · dismissed:** the whole inline-image pipeline (`pendingImages` appended after the frame, drawn at the terminal's cursor) is shared with mermaid diagrams, which ship with the same placement; a per-block cursor-positioning rework is its own task, not a J1.1 regression.
- **J1.1 · inline image display is kitty-only, not sixel · dismissed:** `graphics.SixelCommand` is defined but the inline pipeline never dispatches on protocol — mermaid has the identical kitty-only limitation; wiring sixel inline display is a shared-pipeline change.
- **J1.1 · retained image blocks are not requeued when images are toggled back on · dismissed:** the toggle-on requeue gap is shared with mermaid (its `BlockImage` is retained but `toggleImages` only flips the flag); the placeholder-when-off display is correct, and fixing the requeue is the same per-block positioning rework as the first dismissed finding.
- **J1.1 · daemon/remote `evilcode attach` sees no inline image or placeholder · dismissed:** `Event.Images` is `json:"-"` by design (display-only, bytes are large); plan §1.1 targets the local TUI, and a remote-attach placeholder is a daemon refinement, not a parity item against jcode's `read` tool.

---

# Definition of done

Every task `[x]`, every phase tagged, `go build ./... && go vet ./... && go test ./...`
green at every commit, and every task its own commit.

Every code-touching commit reviewed by codex, with its findings either fixed in a named
follow-up commit or dismissed with a reason in the PART III ledger. No task started with an
unread review behind it.

Concretely, when this plan is finished:

`read` returns an image a vision model can see, truncates a minified line instead of
drowning in it, and suggests a neighbour when the path is wrong. `edit` says *why* a match
failed and hands back the surrounding lines. Several edits to one file are one atomic
write. A field name a model guessed is repaired rather than refused.

`grep` says which function a hit is in, outlines a file without reading it, and stops
repeating lines the model has already been shown.

A command that runs longer than its timeout finishes in the background instead of being
killed; the model can `wait` on it rather than polling; a command that stops for input
asks the user; large temp files land on disk rather than in RAM.

`rm -rf /` is refused outright, `rm -rf $HOME/x` requires a justification the model has to
write, and a hundred ordinary commands from `LOOPS.md` run without a single prompt.

Recall never compares two embedding models' vectors, always runs lexical and semantic
together and fuses them by rank, cuts the tail where the scores fall off, and keeps a
project's memories out of unrelated repositories.

A compaction keeps the recent turns verbatim, happens before the wall rather than at it,
takes a topic change when one is offered, and does not summarize away the one old message
that is still relevant.

A session survives a torn write with its tail intact, and can be found by a phrase said in
it a week ago.

A swarm notice says what the other agent was doing and shows the diff; two writers on one
file both hear about it; a four-hour-old read no longer fires; a dead worker is visible.

`/skills` lists every skill on the machine — the 17 in `~/.agents/skills` included — each
with the description its own front matter states, from a directory it can ship scripts
beside; a skill can narrow the tool set, and reloads without a restart.

An overnight run leaves an HTML report naming what it changed, what validated it, and which
limit stopped it.

And — the claim this plan actually rests on — **for every one of those, `LOOPS.md` holds
two lines: a parity line naming the jcode file and range that was read, verdict `on par` or
`better`; and a codex line naming what the review found and what happened to it.** A
finished plan with a `worse` verdict anywhere in the log is not finished, and neither is one
with a task whose codex line is missing.
