---
description: Fan out independent work to worker agents — decomposition, briefing, sizing, merging, and failure handling.
---

# Orchestrate

Fan out only when the work genuinely splits. One worker for a survey is
overhead; one conversation for four independent investigations is a queue.

## Decomposition test

Task B may run beside task A only if B needs nothing A produces. If B needs
A's output, sequence them — do not batch them. "Can task B run without A's
output? If no, sequence" is the whole test.

## Brief format

A worker sees only its brief. Every brief carries:

1. **Objective** — the one outcome, in one sentence.
2. **Files** — where to start (`files_hint`). A hint, not a boundary: work
   found elsewhere still gets done.
3. **Done-condition** — how the worker knows it is finished, verifiable.
4. **Result schema** — when the shape matters, so the answer parses instead
   of needing prose surgery.
5. **Stopping condition** — when to stop and report blocked instead of
   guessing (missing access, a decision nobody can make, three failed
   approaches).

Vague delegation causes duplicated work: an assignment without an output
format, file scope, boundaries, and a stopping condition comes back as a
second draft of what you already have.

## Sizing

- 2–4 workers for comparisons (each side surveyed independently).
- More for wide surveys over disjoint areas (one worker per area).
- 1 for anything sequential — batching dependent briefs spends tokens to
  learn the dependency twice.

Parallel workers cost roughly an order of magnitude more tokens than doing
the work inline. Fan out for latency and independence, never for ceremony.

## Merge rules

- The parent validates and integrates every result. Workers do not review or
  coordinate each other.
- Mutation safety is structural: consecutive spawns run concurrently, but a
  mutating call barriers the batch, and overlapping `files_hint` sets
  serialize. Say which files each worker owns in its brief so the scheduler
  can see it too.
- Workers write artifacts and return lightweight references, not full
  transcripts. Context compression is the value.

## Failure path

A failed worker is data, not a verdict. Read its last message, re-spawn the
gap with the accumulated knowledge attached, and keep going. Do not absorb a
worker's failure by redoing its work silently inside your own turn — the next
identical brief fails identically.

## Follow-ups

Message an existing idle worker instead of spawning fresh when a follow-up
needs its context. A new worker re-reads everything the old one already
knows.
