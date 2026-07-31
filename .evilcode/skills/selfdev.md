---
name: selfdev
description: How evilcode develops itself — the loop from plan.md §0.2. Use whenever working on the evilcode repository itself.
---

# Working on evilcode

You are editing the program you are running inside. That is the whole point of
this loop, and it is also the reason every step below exists: a change that
builds but renders wrong is invisible from a test suite, and a change that
renders right but breaks the build takes the next session with it.

## The loop

One task at a time, in this order. Do not batch.

1. **Pick the next unchecked task in `plan.md`.** The plan is the source of
   truth for what is left. If the task is unclear, read the section it
   references (`§n`) before starting, not after.

2. **Implement it.** Match the surrounding code: comment density, naming, error
   wording. Comments say *why*, never *what* — the code already says what.

3. **Make it pass:**
   ```
   go build ./... && go vet ./... && go test ./...
   ```
   All three, every time. A vet failure is a real failure.

4. **If the change is visible in the TUI, look at it.** Tests do not see
   layout. Boot the probe, capture a frame, render it to PNG, and *open the
   image*:
   ```
   go build -o evilcode ./
   ./probe/probe.sh boot --scenario=<name> tui
   ./probe/probe.sh keys "..." ; ./probe/probe.sh keys Enter
   ./probe/probe.sh png <name>
   ```
   Then read `probe/frames/<name>.png`. Every serious bug in this program's
   history was found this way and not by a test: widgets that never docked, a
   box with no border, an infinite poke loop, a memory tile drawn twice.

5. **Update the goldens if the frames legitimately changed:**
   ```
   UPDATE_GOLDENS=1 go test -tags probe ./probe/...
   go test -tags probe ./probe/...
   ```
   Run the second one at least twice. A golden that passes once and fails once
   is a golden with a clock or a race in it, and it will waste an hour later.

6. **Check the task off in `plan.md`**, commit, and append to `LOOPS.md`: what
   the task was, what broke, and what the fix taught you. Write the entry for
   someone who was not there.

7. **Log deviations in `DEVIATIONS.md`** — anything built differently from the
   plan, with the reason and what would make it worth revisiting.

## Rules that are not negotiable

- **Never mark a task done you have not verified.** "It should work" is not
  verification. The gates in §12.3 exist because models do this.
- **`internal/agent` must never import bubbletea.** A test enforces it. It is
  what makes headless, the daemon, and the probe rig possible.
- **The conversation is append-only.** `/compact` is the one sanctioned rewrite.
- **The screen never jumps.** Any decision that feeds back into layout needs
  hysteresis (§8.3, invariant 4).
- **Fixtures live in git.** `testdata/` is reset from git before every probe
  run, so a fixture fix that is not committed has no effect. This has cost real
  time twice.

## When something looks wrong

Read the frame, not the code, first. The PNG shows what the user sees; the code
shows what you meant. When those disagree the frame is right.
