# new.md — backlog

Ordered for execution: foundation and reference first, then visual identity (a
decision point), then the TUI surfaces that follow it, then commands, then
infrastructure. `[x]` items are done.

## Foundation

1. **`/login`.** A command to input an Ollama Cloud API key. Do this early — cloud
   model access enables testing throughout the rest of the work.

2. **jcode source.** Download it, or find it in `/tmp`. It has quirky features we're
   missing (overscroll and others) that I want to implement, and it's the reference
   for the visual-identity work below.

## Visual identity

3. **`looks.md`.** Divert the look away from jcode — read the jcode source in `/tmp`
   and compare. Brainstorm changes that give evilcode its own visual identity without
   dropping features, write them to `looks.md`, and I'll pick the ones I like.
   *Decision point: the items below should follow the identity chosen here.*

4. **Launch screen.** The current one is ugly: the buttons only have rounded side
   edges with no background, so they don't read as buttons. Add a background to
   complete the button look, and make the buttons selectable.

5. **~~Idle art.~~** Dropped — keeping the black hole. The eye and the black hole both
   stay as built (`internal/tui/idleart.go`).

## TUI rendering

6. **Widgets flashing in and out.** Widgets currently appear, get covered by text,
   then disappear. They should render on top and never appear next to model text
   output — only next to tool calls or thinking blocks. They scroll with the main
   output only, never scroll inside thinking blocks, and never disappear.

7. **Quick-view popup system.** A new quick-view system, separate from `/diff`:
   clicking something opens a popup window centered in the terminal that you can
   scroll through until you're done, then Esc closes. It should look nice and stay
   consistent with the current theme. Three triggers:
   - **`.md` links.** `.md` files render as links; clicking opens the file in a full
     markdown renderer.
   - **`read` / `write`.** Clicking a `read` or `write` opens a diff or the file
     itself.
   - **Bash summary line.** The grey text after a bash command currently just repeats
     the command, renders on top of widgets, and breaks alignment. It should be a
     summary of the command instead, and clicking it opens a terminal-style view —
     e.g. `rm -rf` shows as `> rm -rf` with the output beneath it, like a real
     terminal.

## Commands

8. **Auto-prompt commands.** Add commands that fire automatic prompts, e.g.
   `/review`, `/bugfix`, `/describe`.

9. **Missing commands.** `/btw`, `/stats`, `/init` (the latter needs an `agents.md`
   system too).

## Infrastructure

10. **Self-update.** `evilcode update` pulls from forgejo and updates itself.

## Done

11. **[x] `planfiles.md`.** Read `plan.md` and `plan2.md`, create a `planfiles.md` that
    describes how to author plan files with the same layout and quirks — dense, not
    bloated, with the loop confirmed at the top (write, fix, checkbox in `plan.md`,
    `LOOPS.md`, `README.md`, commit, codex review, next problem).