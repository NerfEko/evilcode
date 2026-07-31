# new.md — backlog

1. **Launch screen.** The current one is ugly: the buttons only have rounded side
   edges with no background, so they don't read as buttons. Add a background to
   complete the button look, and make the buttons selectable.

2. **Idle art.** Remove the black hole — replace it with something else like evilcode ascii art, or have nothing at all.

3. **Auto-prompt commands.** Add commands that fire automatic prompts, e.g.
   `/review`, `/bugfix`, `/describe`.

4. **Missing commands.** `/btw`, `/stats`, `/init` (the latter needs an `agents.md`
   system too).

5. **Widgets flashing in and out.** Widgets currently appear, get covered by text,
   then disappear. They should render on top and never appear next to model text
   output — only next to tool calls or thinking blocks. They scroll with the main
   output only, never scroll inside thinking blocks, and never disappear.

6. **jcode source.** Download it or find it in /tmp — it has quirky features we're missing (overscroll
   and others) that I want to implement.

7. **Self-update.** `evilcode update` pulls from forgejo and updates itself.

8. **Quick-view popup system.** A new quick-view system, separate from `/diff`:
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

9. **Visual identity.** Divert the look away from jcode. jcode is in /tmp read it and compare. Brainstorm changes that
   give evilcode its own visual identity without dropping features, write them to
   `looks.md`, and I'll pick the ones I like.

10. **`/login`.** A command to input an Ollama Cloud API key.

11. **[x] `planfiles.md`.** Read `plan.md` and `plan2.md`, create a `planfiles.md`
    that describes how to author plan files with the same layout and quirks — dense,
    not bloated, with the loop confirmed at the top (write, fix, checkbox in
    `plan.md`, `LOOPS.md`, `README.md`, commit, codex review, next problem).
