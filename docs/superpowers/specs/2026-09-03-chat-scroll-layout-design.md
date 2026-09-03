# Chat Scroll Layout Design

## Goal

Make the web shell behave like a two-pane workspace: the sidebar and conversation
scroll independently, the conversation opens at its newest content, and the
composer stays pinned to the bottom of the available chat viewport.

## Current Structure

`index.html` already provides the required regions: `#sidebar` with `#roster`,
`#chat` with `#transcript-scroll`, `#ask-dock`, and `#composer`, plus the optional
right rail. `app.css` already gives the roster and transcript overflow ownership
and `app.js` already preserves the transcript floor while streaming or loading
older history. The change tightens the parent sizing and grid tracks rather than
introducing a second shell.

## Layout Contract

- The application shell consumes the viewport and suppresses a document-level
  scrollbar.
- The sidebar owns its vertical scrollbar through `#roster`; its header, status,
  and footer remain visible.
- The chat pane is a height-constrained grid. Its header/backbar, transcript,
  ask dock, and composer are separate rows.
- `#transcript-scroll` is the only scrolling region for conversation content and
  may shrink to the grid's remaining height (`min-height: 0`).
- The composer is the final normal-flow grid row, not an overlay. Its textarea,
  slash palette, attachments, and buttons can grow upward while the composer
  remains attached to the bottom edge.
- The desktop and phone row definitions differ only because the phone has a
  visible backbar; the transcript/composer relationship is identical.
- The right rail remains independently scrollable when present.

## Interaction Contract

- Opening a live session forces `#transcript-scroll` to its bottom after the
  initial render.
- Subsequent updates keep the user at the bottom when they were already there;
  reading history preserves position and continues to show the existing activity
  affordance.
- Opening a live session focuses `#composer-text` after binding the composer.
  Stored read-only sessions keep the composer hidden and do not steal focus.
- The existing mobile keyboard overlap variable remains the sole keyboard
  compensation mechanism.

## Implementation Boundary

Modify only the existing shell stylesheet and app/composer wiring. No API,
backend, route, or visual-theme changes. Use existing DOM elements and scroll
helpers. Add a small shipped-asset contract test for the CSS/markup assumptions;
use browser verification for computed layout and scroll behavior at desktop and
phone widths.

## Verification

- Existing Go web shell tests continue to pass.
- Frontend module tests continue to pass.
- Browser checks confirm independent sidebar/transcript scroll containers,
  bottom-pinned composer, initial transcript bottom position, and composer focus.
- Browser checks confirm the layout remains usable at a phone viewport with the
  existing mobile shell.
