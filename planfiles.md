# planfiles.md — how to author a plan file

A plan file is the working spec an agent builds from. It is checked off as it goes
and pairs with three companion files. This preserves the layout and loop used by the
completed feature and hardening plans, which remain available in Git history.

## The loop (confirmed at the top of every plan file)

The plan opens with `# PART 0 — Process`, which states the loop. Two flavors share
one spine — **write → fix → checkbox in the plan file → commit → codex review →
LOOPS.md + README.md → next problem** — and a fix plan wraps that spine in
*reproduce* on the front and *fail-then-pass* on the back.

**Build loop** (features from a known-good spec):

1. Pick the next unchecked `[ ]` task in the current phase.
2. Implement. Reuse existing packages before writing new.
3. `go build ./... && go vet ./... && go test ./...` green.
4. TUI-visible: boot the probe rig, drive the scenario, render to PNG, **look at the
   image**. Compare against the spec § the task references.
5. Mark `[x]` in the plan file. Commit — one task per commit, always green.
6. Kick off a background codex review of the commit. Don't block; fold findings into
   the next iteration. Fix real findings before unrelated new work; log dismissed
   findings with a reason in `LOOPS.md`.
7. Append one entry to `LOOPS.md` (append-only, never edit old): `## <date> <task-id>`
   — what was done, how verified (test names / PNG filenames), codex verdict when
   known, deviations.
8. Keep `README.md` current in the same commit when behavior changes.
9. If a task is impossible as specced: do the closest working thing, log in
   `DEVIATIONS.md`, move on. Never stall.

**Fix loop** (bugs from a report that is *not* known-good): the loop
gains one step before the fix — **reproduce**. Write the smallest test that fails
*because of this bug* and watch it fail; cite the failure in `LOOPS.md`. Three
outcomes: it fails → you understand the bug, continue; it passes → the finding is
wrong or the path is unreachable, mark `[~]`, log why, move on, do not fix what you
could not break; you cannot construct the test → say so, mark `[~]`, name which of
"unreachable" vs "untestable" applies. Then find the root, not the report (grep every
caller before editing; fix once where all callers route through), fix, run the
reproduction again — it must now pass — then the same probe/commit/codex/LOOPS/README
steps. **A fix without a fail-then-pass pair is not done.**

## Layout

```
# <project> — <one-line descriptor>             title names the project and its flavor in one line
<intro paragraph>                               complete, self-contained, normative; implement exactly
                                                or log a deviation in DEVIATIONS.md

# PART 0 — Process: how the <building|fixing> agent works
## 0.1 <context>                                build: development model · fix: what differs from plan.md
## 0.2 The loop (repeat until plan complete)     the loop above, verbatim in spirit
## 0.3 Reading a task                           (fix plans) the task-line format
## 0.4 Ordering                                  (fix plans) phases ordered by cost-when-it-fires

# PART I — <Context, constraints, architecture | Phases>
<normative spec>                                 build plans: the full spec in numbered § sections with
                                                 tables; fix plans skip this — the repo is the spec

# PART ... — Phases
## Phase <n> — <name>
- [ ] **<id>** `file:line` `symbol` — description — fix. ⟨source⟩
...
- [ ] Verify <phase>: <round-trips to check>. Tag `<tag>`.

# PART ... — <Gotchas ledger | Ledger>
## Dismissed findings                            (fix plans) [~] tasks with their reason, never deleted

# Definition of done
<one paragraph of concrete, checkable claims>
```

## Quirks (the ones that matter)

- **Checkboxes**: `[ ]` unchecked, `[x]` done, `[~]` (fix plans only) not-a-bug or
  untestable. Check off in the plan file itself — that copy is the working spec.
- **Task IDs**: cited in the commit subject and the `LOOPS.md` heading. Build plans
  use `P<phase>.<n>`; fix plans use `H<phase>.<m>`.
- **Task line (fix plan)**: `- [ ] **H1.3** `internal/agent/agent.go:384`
  `commitPartial` — description — fix. ⟨both⟩`. Line numbers are as-of a named commit
  and drift; the symbol does not. `⟨both⟩` = two reviewers found it independently;
  `⟨codex⟩`/`⟨fable⟩` = one only — not weaker as a bug, weaker as evidence.
- **One task per commit, always green.** `go build ./... && go vet ./... && go test
  ./...` is the real gate; codex review is advisory, never blocking.
- **`LOOPS.md` is append-only.** Never edit old entries. Heading `## <date> <task-id>`.
  Name the failing test that proved the bug was real; for `[~]`, name the test that
  refused to fail.
- **`README.md`** changes in the same commit as the behavior it describes.
- **`DEVIATIONS.md`** is append-only: what the spec said, what was built instead, why,
  and when it would need revisiting.
- **Phases end daily-drivable.** Each phase's last task is a `Verify` task listing the
  concrete round-trips to check, and tags the commit (`phase-1`, `harden-1`).
- **A golden only proves a frame hasn't changed, never that it's right.** The PNG is
  for looking at. Regenerate a drifted golden only after confirming the new frame is
  right.
- **Never stall.** Impossible as specced → closest working thing → `DEVIATIONS.md` →
  next task.

## Companion files

| file | role |
|---|---|
| `plan*.md` | the temporary working spec; remove it after completion once its durable evidence is in Git and `LOOPS.md` |
| `LOOPS.md` | append-only flight recorder, one entry per task |
| `README.md` | kept current on every behavior-changing commit |
| `DEVIATIONS.md` | append-only spec-deviation log |
