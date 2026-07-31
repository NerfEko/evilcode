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

6. **jcode source.** Download it — it has quirky features we're missing (overscroll
   and others) that I want to implement.

7. **Self-update.** `evilcode update` pulls from forgejo and updates itself.

8. **Markdown links.** `.md` files render as links; clicking opens a side-by-side
   full markdown renderer view of the file, scrollable, Esc to close.

9. **Quick view for read/write.** Clicking a `write` or `read` opens a side-by-side
   diff or file view; Esc closes. This is a quick-view system separate from `/diff`.

10. **Bash command summary line.** The grey text after a bash command just repeats
    the command, renders on top of widgets, and breaks alignment. It should be a
    summary of the command instead. Make it clickable to open a side-by-side
    terminal-style view — e.g. `rm -rf` shows as `> rm -rf` with the output beneath
    it, like a real terminal; Esc closes.

11. **Visual identity.** Divert the look away from jcode. Brainstorm changes that
    give evilcode its own visual identity without dropping features, write them to
    `looks.md`, and I'll pick the ones I like.

12. **`/login`.** A command to input an Ollama Cloud API key.

13. **[x] `planfiles.md`.** Read `plan.md` and `plan2.md`, create a `planfiles.md`
    that describes how to author plan files with the same layout and quirks — dense,
    not bloated, with the loop confirmed at the top (write, fix, checkbox in
    `plan.md`, `LOOPS.md`, `README.md`, commit, codex review, next problem).

