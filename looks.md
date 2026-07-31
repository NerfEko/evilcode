# Visual menu

This is a pick-list for future visual passes. Each entry names the surface, the
file to change, and the cost/risk. Nothing in this document is implemented by
itself.

## High-leverage surfaces

1. **Widget silhouette** — `internal/tui/widgets.go`, `RenderWidget`.
   Compare the current heavy `╭─╮`/`│`/`╰─╯` frame with a lighter left rule,
   tinted title bar, or a compact card. Cost: every docked widget changes
   silhouette and width calculations.
2. **Tool rows** — `internal/tui/transcript.go`, `renderTool`.
   Replace the generic `✓`/`✗` and repeated `·` separators with a compact
   status badge, verb-first label, and quieter metadata. Cost: tool rows are
   also the click affordance, so contrast and underline behavior must remain.
3. **Prompt band** — `internal/tui/transcript.go`, `renderUser`, plus the
   rainbow ramp helpers.
   Keep the numbered prompt identity but reduce the full-width band feeling:
   use a narrow accent rail, a stronger current prompt, and a quicker decay.
   Cost: user prompts are the main navigation landmarks and golden frames use
   their exact colors.
4. **Status segments** — `internal/tui/statusline.go`.
   Try phase → model → context → token rate → queue in a consistent order, with
   one separator language and a deliberate quiet state. Cost: the status line
   is packed into one row and must still fit narrow terminals.
5. **Palette voice** — `internal/theme/palettes.go`.
   Rebalance built-ins around one background, one readable prose color, one
   accent, and a restrained success/error pair instead of many equally loud
   colors. Cost: palette tests and every screenshot golden move.

## Detail surfaces

6. **Diff frame** — `internal/tui/transcript.go`, `renderDiffLang`, and
   `internal/tui/sidepanel.go`.
   Make hunks read as a file change rather than a second transcript: calmer
   gutters, clearer add/delete tint, and a single strong hunk marker. Cost:
   syntax highlighting and line-number alignment must survive.
7. **Spinner and phases** — `internal/tui/statusline.go` and `status.go`.
   Replace the current glyph cadence with a smaller phase vocabulary and a
   stable running marker. Cost: deterministic frame tests must freeze every
   frame and accessibility depends on text surviving glyph fallback.
8. **Header hierarchy** — `internal/tui/header.go`, `RenderHeader`.
   Make the session title the anchor, demote provider/model details into one
   readable metadata line, and keep status dots secondary. Cost: the header is
   part of row provenance and its height affects every dock anchor.
9. **Welcome art** — `internal/tui/idleart.go` and `header.go`.
   Keep the bat identity but give the art a deliberate quiet zone around the
   greeting and starter chips. Cost: terminal width, font coverage, and idle
   animation all affect the composition.
10. **Starter chips** — `internal/tui/header.go`.
    Treat the selected chip as a real control: filled background, clear focus,
    and enough spacing between choices. Cost: keyboard selection and the empty
    transcript layout must agree.
11. **Todo card** — `internal/tui/todowidget.go` and `internal/tui/todo*`.
    Reduce box chrome, make the active item the visual anchor, and reserve
    color for state rather than decoration. Cost: the card is also a dock
    exclusion and must keep its height stable.
12. **Plan card** — `internal/tui/plancard.go`.
    Distinguish plan headings, decisions, and handoff state with typography and
    spacing instead of additional borders. Cost: plan text is model-authored
    and must retain terminal sanitization.

## Rules for picking an entry

- Capture a deterministic PNG before and after.
- Keep one visual change per task so regressions have a name.
- Check narrow, centered, SSH, and light/dark palette behavior when the surface
  is shared.
- Preserve click targets, transcript ownership, and the one-row status budget.
