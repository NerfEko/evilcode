# Soft interrupts

How a message reaches a model that is already mid-turn, without throwing away
the turn or the prompt cache.

This is the mechanism behind `Enter` while a response is streaming. It is
written down because the failure modes are not obvious and every one of them
produces a plausible-looking wrong behavior rather than a crash.

## The problem

The obvious way to handle "user typed while the model was working" is to cancel
the request and start a new one with the extra message appended. That works, and
it is wrong in three ways:

1. **It throws away the turn.** Whatever the model had produced is discarded,
   including tool results that cost real time to obtain.
2. **It throws away the prompt cache.** A new request re-evaluates the whole
   prefix. On a long session against a large model that is the single most
   expensive thing the harness can do.
3. **It is not what the user meant.** "Also check the tests" is an addition, not
   a correction. Cancelling treats every aside as a retraction.

## The mechanism

An interleaved message is **not a new API request**. It is appended to the live
conversation as an ordinary user message, at a point where doing so is safe, so
the next iteration of the agent loop carries it with the cache prefix intact.

The loop already re-requests between iterations — after tool results, and after
a stream ends. Those boundaries are the injection points.

## Attached mode

When the TUI is attached to the daemon, the daemon remains the owner of the live
turn. The client does not keep a second queue or try to cancel a local provider
request:

- A prompt submitted while the session is busy crosses the socket as `input` and is
  queued by the server behind the active turn. The queue is shared by every attached
  TUI, so closing the window cannot strand a staged prompt.
- A user interleave crosses as `interrupt` with text and urgency. The daemon injects
  it into the real agent, which applies the same B/C/D safe-point rules below.
- `Esc` and `Ctrl+C` send an empty `interrupt`, which cancels the server-owned context.
  The local TUI immediately reflects the requested state, but the daemon's turn-end
  event is the authority for the final result.
- The resulting events are broadcast to every attached client. A second window sees
  the same interjection, cancellation, partial assistant message, and turn boundary.

This distinction is what makes a closed terminal harmless: a TUI may disappear between
the input and the safe point, but the agent loop and its durable conversation remain in
the daemon.

## Safe points

    ┌─ stream ends, no tool calls ──────────────── B: always safe
    │
    ├─ tool calls requested
    │    ├─ between tool executions ─────────────── C: urgent only
    │    └─ after all tool results ──────────────── D: the default
    │
    └─ next request carries the injected message

**B — the stream ended with no tool calls.** Nothing is in flight. Appending
here is unconditionally safe and needs no special handling.

**D — after all tool results, before the next request.** The default. Every
`tool_use` already has its adjacent `tool_result`, so the message slots in
cleanly and the next request is a normal continuation.

**C — between tool executions.** Only for messages marked urgent, because
getting here means abandoning tools the model asked for. The wire format
requires every `tool_use` to be followed by a matching `tool_result`, so the
abandoned calls cannot simply be dropped: each one gets a stub result

    [Skipped: user interrupted]

flagged as an error. Skipping the stub produces a malformed conversation that
some providers reject outright and others answer incoherently.

## Grouping

Multiple pending interrupts are grouped **by source** and joined with a blank
line within each group. Different sources become separate messages.

    User          → "also check the tests\n\nand the docs"
    System        → "[automated todo completion gate ...]"
    BackgroundTask → "the build finished: 2 failures"

This matters because a harness nudge merged into the middle of a user's sentence
reads as though the user wrote it. The model then argues with the user about
something the user never said.

## What the UI shows

Staged messages appear as rows above the status line, numbered with the rainbow
ramp so the one going in next is the most saturated:

| kind | glyph | meaning |
|---|---|---|
| Pending | `↻` | already injected as a soft interrupt |
| Interleave | `⚡` | staged, goes in at the next safe point |
| Queued | `⏳` | waits for the turn to end entirely |

`Ctrl+Up` on an empty composer pulls staged messages back out for editing,
in the order they would have reached the model.

## Interaction with interrupts

`Esc` and `Ctrl+C` both cancel the turn, but they mean different things and the
harness treats them differently:

- **Esc** means *stop*. It clears staged interleaves and disarms auto-poke,
  because a harness that immediately re-poked after being told to stop would be
  ignoring the instruction.
- **Ctrl+C** means *skip this*. It cancels the same way but leaves auto-poke
  armed, because the user is redirecting rather than standing the harness down.

Either way the partial response is kept as a real assistant message. Discarding
half-written output because the user interrupted loses information they may have
been reading at the moment they pressed the key.

## Where this lives

- `internal/agent/agent.go` — `Interject`, `DrainInterrupts`, and the safe-point
  calls inside `Loop` and `runTools`.
- `internal/daemon/server.go` — the one-turn reservation, server-side queue,
  cancellation context, and safe-point conflict delivery.
- `internal/attachcmd/attach.go` — the socket bridges for input, interleave, and
  cancellation.
- `internal/tui/composer.go` — the local Submit/Queue/Interleave decision and key
  semantics; attached mode forwards the resulting action instead of owning execution.
- `internal/tui/app.go` — rendering, local-only staging preferences, and `Ctrl+Up`
  retrieval.
