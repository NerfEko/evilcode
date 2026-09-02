import test from "node:test";
import assert from "node:assert/strict";
import { readFile } from "node:fs/promises";

// This repository intentionally has no package.json. Load the browser ES module
// as a data URL so `node --test mirror.test.mjs` works without changing source
// module metadata or requiring a loader/dependency.
const source = await readFile(new URL("./mirror.js", import.meta.url), "utf8");
const mirror = await import(`data:text/javascript;charset=utf-8,${encodeURIComponent(source)}`);

const {
  EVENT_KINDS,
  createMirror,
  reduceMirror,
  mirrorReducer,
  applyEvent,
  applySnapshot,
  mergeHistoryPage,
  isStreamingKind,
  snapshotMessagesForTest,
} = mirror;

const KINDS = [
  "turn_start", "text_delta", "reasoning_delta", "tool_start", "tool_result",
  "token_usage", "reasoning_effort", "notice", "memory_recall", "ask",
  "ask_resolved", "model", "background", "snapshot", "turn_end", "error",
];

function baseSnapshot(extra = {}) {
  return { session: "session-1", model: "model-1", provider: "provider-1", epoch: 1, ...extra };
}

function pureReduce(state, action) {
  const before = structuredClone(state);
  const next = reduceMirror(state, action);
  assert.deepStrictEqual(state, before, "the reducer must not mutate its input state");
  return next;
}

function messagesOf(state, kind) {
  return state.messages.filter((message) => message.kind === kind);
}

function onlyMessage(state, kind) {
  const messages = messagesOf(state, kind);
  assert.equal(messages.length, 1);
  return messages[0];
}

test("exports every event kind and only stream deltas are streaming kinds", () => {
  assert.deepStrictEqual(EVENT_KINDS, KINDS);
  assert.ok(Object.isFrozen(EVENT_KINDS));
  assert.equal(isStreamingKind("text_delta"), true);
  assert.equal(isStreamingKind("reasoning_delta"), true);
  for (const kind of KINDS.filter((kind) => !["text_delta", "reasoning_delta"].includes(kind))) {
    assert.equal(isStreamingKind(kind), false, kind);
  }
  assert.equal(isStreamingKind("future_kind"), false);
});

test("snapshot creation normalizes messages, calls, turns, pending asks, and background tasks", () => {
  const rawMessages = [
    { role: "system", content: "hidden system" },
    { role: "user", content: "question", images: ["image-1"] },
    {
      role: "assistant",
      reasoning: "thinking",
      content: "answer",
      images: ["image-2"],
      tool_calls: [{ id: "call-done", name: "read", args: { path: "src/main.go" } }],
    },
    {
      role: "tool",
      tool_call_id: "call-done",
      tool_name: "read",
      content: "file contents",
      diff: "diff",
    },
    {
      role: "assistant",
      tool_calls: [{ id: "call-pending", name: "write", args: '{"path":"new.txt"}' }],
    },
    { role: "user", hidden: true, content: "not displayed" },
  ];
  const state = createMirror(baseSnapshot({
    running: true,
    seq: 8,
    context_window: 8192,
    reasoning_effort: "high",
    reasoning_efforts: ["low", "high"],
    vision: true,
    truncated: true,
    messages: rawMessages,
    pending: [{ id: "ask-1", question: "Continue?", options: ["yes", "no"] }],
    background: [{ id: "bg-1", status: "running" }],
  }));

  assert.equal(state.session, "session-1");
  assert.equal(state.model, "model-1");
  assert.equal(state.provider, "provider-1");
  assert.equal(state.running, true);
  assert.equal(state.epoch, 1);
  assert.equal(state.seq, 8);
  assert.equal(state.contextWindow, 8192);
  assert.equal(state.reasoningEffort, "high");
  assert.deepStrictEqual(state.reasoningEfforts, ["low", "high"]);
  assert.equal(state.vision, true);
  assert.equal(state.usage, null);
  assert.deepStrictEqual(state.pending["ask-1"], {
    id: "ask-1", question: "Continue?", options: ["yes", "no"],
  });
  assert.deepStrictEqual(state.background["bg-1"], { id: "bg-1", status: "running" });

  const snapshotMessages = snapshotMessagesForTest(rawMessages);
  assert.deepStrictEqual(state.messages, snapshotMessages);
  assert.deepStrictEqual(state.messages.map(({ kind, content, turn }) => ({ kind, content, turn })), [
    { kind: "user", content: "question", turn: 1 },
    { kind: "reasoning", content: "thinking", turn: 1 },
    { kind: "assistant", content: "answer", turn: 1 },
    { kind: "tool", content: "file contents", turn: 1 },
    { kind: "tool", content: "", turn: 1 },
  ]);
  assert.equal(state.messages[3].status, "done");
  assert.equal(state.messages[3].target, "src/main.go");
  assert.equal(state.messages[4].status, "pending");
  assert.equal(state.messages[4].target, "new.txt");
  assert.equal(state.messages[4].call.id, "call-pending");
  assert.equal(state.messages[0].images !== rawMessages[1].images, true);
  assert.equal(state.turns.length, 1);
  assert.equal(state.turns[0].id, "snapshot-turn-1");
  assert.equal(state.history.before, state.messages.length);
  assert.equal(state.history.hasMore, true);
  assert.equal(state.history.generation, 1);
});

test("snapshot image counts become placeholders without retaining bytes", () => {
  const state = createMirror(baseSnapshot({
    messages: [
      { role: "user", content: "look", image_count: 2 },
      { role: "assistant", content: "I can inspect the attachments." },
    ],
  }));
  const user = onlyMessage(state, "user");
  assert.deepEqual(user.images, []);
  assert.equal(user.imagesOmitted, 2);
  assert.equal(user.image_count, undefined);
});

test("turn_start begins a turn and records a visible user input", () => {
  const state = createMirror(baseSnapshot());
  const next = pureReduce(state, {
    kind: "turn_start", seq: 1, session: "session-1", request_id: "request-1",
    text: "hello", images: ["image"],
  });

  assert.notStrictEqual(next, state);
  assert.equal(next.running, true);
  assert.deepStrictEqual(next.activeTurn, {
    id: "request-1",
    usage: {
      in: 0, out: 0, ctx_used: 0, ctx_max: 0, cache_hit: false,
      cache_read: 0, cache_write: 0, gen_ms: 0,
    },
    running: true,
  });
  assert.equal(next.turns.length, 1);
  const user = onlyMessage(next, "user");
  assert.deepStrictEqual(user, {
    id: "user-request-1", kind: "user", role: "user", content: "hello", text: "hello",
    turn: 1, images: ["image"],
  });
});

test("reasoning_delta and text_delta coalesce and finish reasoning before assistant text", () => {
  let state = createMirror(baseSnapshot());
  state = pureReduce(state, { kind: "turn_start", seq: 1, session: "session-1", request_id: "turn-1" });
  state = pureReduce(state, { kind: "reasoning_delta", seq: 2, session: "session-1", text: "think " });
  state = pureReduce(state, { kind: "reasoning_delta", seq: 3, session: "session-1", text: "first" });
  assert.equal(onlyMessage(state, "reasoning").content, "think first");
  assert.equal(onlyMessage(state, "reasoning").streaming, true);

  state = pureReduce(state, { kind: "text_delta", seq: 4, session: "session-1", text: "answer" });
  const reasoning = onlyMessage(state, "reasoning");
  const assistant = onlyMessage(state, "assistant");
  assert.equal(reasoning.streaming, false);
  assert.equal(reasoning.collapsed, true);
  assert.equal(assistant.content, "answer");
  assert.equal(assistant.text, "answer");
  assert.equal(assistant.streaming, true);
  assert.equal(state.running, true);
});

test("tool_start creates a call keyed by call id and stops streaming messages", () => {
  let state = createMirror(baseSnapshot());
  state = applyEvent(state, { kind: "turn_start", seq: 1, session: "session-1", request_id: "turn-1" });
  state = applyEvent(state, { kind: "text_delta", seq: 2, session: "session-1", text: "before tool" });
  const next = pureReduce(state, {
    kind: "tool_start", seq: 3, session: "session-1",
    call: { id: "call-1", name: "edit", args: { path: "file.go" } }, intent: "update file",
    repairs: ["retry once"],
  });

  assert.equal(onlyMessage(next, "assistant").streaming, false);
  const tool = onlyMessage(next, "tool");
  assert.equal(tool.callId, "call-1");
  assert.equal(tool.name, "edit");
  assert.deepStrictEqual(tool.args, { path: "file.go" });
  assert.equal(tool.target, "file.go");
  assert.equal(tool.intent, "update file");
  assert.deepStrictEqual(tool.repairs, ["retry once"]);
  assert.equal(tool.status, "running");
  assert.equal(next.activeTool, "call-1");
});

test("tool_result completes the matching call with output, diff, error, and images", () => {
  let state = createMirror(baseSnapshot());
  state = applyEvent(state, { kind: "turn_start", seq: 1, session: "session-1", request_id: "turn-1" });
  state = applyEvent(state, {
    kind: "tool_start", seq: 2, session: "session-1",
    call: { id: "call-1", name: "edit", args: { path: "file.go" } },
  });
  const next = pureReduce(state, {
    kind: "tool_result", seq: 3, session: "session-1",
    call: { id: "call-1", name: "edit", args: { path: "file.go" } },
    output: "changed", diff: "+line", error: "permission denied", held: true,
    repairs: ["fallback"], display: "formatted output", images: ["image-1"],
  });

  const tool = onlyMessage(next, "tool");
  assert.equal(tool.status, "done");
  assert.equal(tool.output, "changed");
  assert.equal(tool.content, "changed");
  assert.equal(tool.diff, "+line");
  assert.equal(tool.error, "permission denied");
  assert.equal(tool.failed, true);
  assert.equal(tool.held, true);
  assert.deepStrictEqual(tool.repairs, ["fallback"]);
  assert.equal(tool.display, "formatted output");
  assert.deepStrictEqual(tool.images, ["image-1"]);
  assert.equal(next.activeTool, "");
});

test("token_usage accumulates on the active turn and mirrors latest usage", () => {
  let state = createMirror(baseSnapshot());
  state = applyEvent(state, { kind: "turn_start", seq: 1, session: "session-1", request_id: "turn-1" });
  state = pureReduce(state, {
    kind: "token_usage", seq: 2, session: "session-1",
    usage: {
      in: 10, out: 5, ctx_used: 100, ctx_max: 200, cache_hit: true,
      cache_read: 2, cache_write: 3, gen_ms: 20,
    },
  });
  state = pureReduce(state, {
    kind: "token_usage", seq: 3, session: "session-1",
    usage: {
      in: 1, out: 2, ctx_used: 120, ctx_max: 200, cache_hit: true,
      cache_read: 4, cache_write: 5, gen_ms: 7,
    },
  });

  const expected = {
    in: 11, out: 7, ctx_used: 120, ctx_max: 200, cache_hit: true,
    cache_read: 6, cache_write: 8, gen_ms: 27,
  };
  assert.deepStrictEqual(state.activeTurn.usage, expected);
  assert.deepStrictEqual(state.usage, expected);
  assert.deepStrictEqual(state.turns[0].usage, expected);
});

test("reasoning_effort changes the selected effort", () => {
  const state = createMirror(baseSnapshot({ reasoning_effort: "low" }));
  const next = pureReduce(state, {
    kind: "reasoning_effort", seq: 1, session: "session-1", reasoning_effort: "high",
  });
  assert.equal(next.reasoningEffort, "high");
  assert.equal(state.reasoningEffort, "low");
});

test("notice finishes streaming and appends a leveled system message", () => {
  let state = createMirror(baseSnapshot());
  state = applyEvent(state, { kind: "turn_start", seq: 1, session: "session-1", request_id: "turn-1" });
  state = applyEvent(state, { kind: "text_delta", seq: 2, session: "session-1", text: "partial" });
  const next = pureReduce(state, {
    kind: "notice", seq: 3, session: "session-1", text: "rate limited", level: "warn",
  });

  assert.equal(onlyMessage(next, "assistant").streaming, false);
  assert.deepStrictEqual(onlyMessage(next, "notice"), {
    id: "notice-3", kind: "notice", role: "system", content: "rate limited", text: "rate limited",
    level: "warn", turn: 1,
  });
});

test("memory_recall appends a system memory item with display data", () => {
  const state = createMirror(baseSnapshot());
  const display = { matches: 2, snippets: ["one", "two"] };
  const next = pureReduce(state, {
    kind: "memory_recall", seq: 1, session: "session-1", display,
  });
  assert.deepStrictEqual(onlyMessage(next, "memory"), {
    id: "memory-1", kind: "memory", role: "system", content: "memory recall", display, turn: 0,
  });
});

test("ask adds a pending request and copies its options", () => {
  const state = createMirror(baseSnapshot());
  const ask = { id: "ask-1", question: "Continue?", options: ["yes", "no"] };
  const next = pureReduce(state, {
    kind: "ask", seq: 1, session: "session-1", ask,
  });
  assert.deepStrictEqual(next.pending, { "ask-1": ask });
  assert.notStrictEqual(next.pending["ask-1"], ask);
  assert.notStrictEqual(next.pending["ask-1"].options, ask.options);
  assert.deepStrictEqual(state.pending, {});
});

test("ask_resolved removes the request by request id", () => {
  const state = createMirror(baseSnapshot({
    pending: [{ id: "ask-1", question: "Continue?", options: ["yes"] }],
  }));
  const next = pureReduce(state, {
    kind: "ask_resolved", seq: 1, session: "session-1", request_id: "ask-1",
  });
  assert.deepStrictEqual(next.pending, {});
  assert.deepStrictEqual(state.pending, { "ask-1": { id: "ask-1", question: "Continue?", options: ["yes"] } });
});

test("model updates model/provider and known capabilities", () => {
  const state = createMirror(baseSnapshot());
  const next = pureReduce(state, {
    kind: "model", seq: 1, session: "session-1", model: "model-2", provider: "provider-2",
    reasoning_efforts: ["medium", "high"], reasoning_effort_known: true,
    reasoning_effort: "high", vision_known: true, vision: true,
    context_window_known: true, context_window: "16384",
  });
  assert.equal(next.model, "model-2");
  assert.equal(next.provider, "provider-2");
  assert.deepStrictEqual(next.reasoningEfforts, ["medium", "high"]);
  assert.equal(next.reasoningEffort, "high");
  assert.equal(next.vision, true);
  assert.equal(next.contextWindow, 16384);
});

test("background stores task state keyed by task id", () => {
  const state = createMirror(baseSnapshot());
  const background = { id: "bg-1", description: "index files", status: "running" };
  const next = pureReduce(state, {
    kind: "background", seq: 1, session: "session-1", background,
  });
  assert.deepStrictEqual(next.background, { "bg-1": background });
  assert.notStrictEqual(next.background["bg-1"], background);
  assert.deepStrictEqual(state.background, {});
});

test("snapshot event replaces transcript state and resets the seen sequence seam", () => {
  let state = createMirror(baseSnapshot());
  state = applyEvent(state, {
    kind: "turn_start", seq: 1, session: "session-1", request_id: "old-turn", text: "old input",
  });
  const next = pureReduce(state, {
    kind: "snapshot", seq: 10,
    snapshot_session: "session-1", snapshot_model: "model-2", snapshot_provider: "provider-2",
    snapshot_running: false, snapshot_epoch: 2,
    snapshot_messages: [{ role: "user", content: "fresh input" }],
    snapshot_pending: [{ id: "ask-2", question: "Fresh?", options: ["yes"] }],
    snapshot_background: [{ id: "bg-2", status: "done" }],
  });

  assert.equal(next.model, "model-2");
  assert.equal(next.provider, "provider-2");
  assert.equal(next.running, false);
  assert.equal(next.epoch, 2);
  assert.equal(next.seq, 10);
  assert.deepStrictEqual(next.messages.map((message) => message.content), ["fresh input"]);
  assert.deepStrictEqual(next.pending["ask-2"].options, ["yes"]);
  assert.equal(next.background["bg-2"].status, "done");
  assert.equal(next.activeTurn, null);
  assert.equal(next.activeTool, "");
  assert.equal(next.usage, null);
  assert.equal(next.history.generation, 2);
  assert.equal(next._seen.has(10), true);
  assert.equal(next._seen.has(1), false);

  // Snapshot reset starts a fresh dedupe seam: an old sequence can be replayed.
  const replayed = applyEvent(next, {
    kind: "notice", seq: 1, session: "session-1", text: "after reset",
  });
  assert.equal(messagesOf(replayed, "notice").length, 1);
});

test("turn_end stops the turn and emits a usage item when usage exists", () => {
  let state = createMirror(baseSnapshot());
  state = applyEvent(state, { kind: "turn_start", seq: 1, session: "session-1", request_id: "turn-1" });
  state = applyEvent(state, {
    kind: "text_delta", seq: 2, session: "session-1", text: "complete me",
  });
  state = applyEvent(state, {
    kind: "token_usage", seq: 3, session: "session-1", usage: { in: 4, out: 6, ctx_used: 20 },
  });
  const next = pureReduce(state, { kind: "turn_end", seq: 4, session: "session-1" });

  assert.equal(next.running, false);
  assert.equal(next.activeTurn, null);
  assert.equal(next.activeTool, "");
  assert.equal(next.turns[0].running, false);
  assert.equal(onlyMessage(next, "assistant").streaming, false);
  assert.deepStrictEqual(onlyMessage(next, "usage").usage, {
    in: 4, out: 6, ctx_used: 20, ctx_max: 0, cache_hit: false,
    cache_read: 0, cache_write: 0, gen_ms: 0,
  });
});

test("error finishes streaming and appends a system error message", () => {
  let state = createMirror(baseSnapshot());
  state = applyEvent(state, { kind: "turn_start", seq: 1, session: "session-1", request_id: "turn-1" });
  state = applyEvent(state, { kind: "text_delta", seq: 2, session: "session-1", text: "partial" });
  const next = pureReduce(state, {
    kind: "error", seq: 3, session: "session-1", error: "failed to continue",
  });

  assert.equal(onlyMessage(next, "assistant").streaming, false);
  assert.deepStrictEqual(onlyMessage(next, "error"), {
    id: "error-3", kind: "error", role: "system", content: "failed to continue",
    text: "failed to continue", turn: 1,
  });
});

test("positive sequence numbers are deduplicated without producing a new state", () => {
  const state = createMirror(baseSnapshot());
  const event = { kind: "notice", seq: 7, session: "session-1", text: "once" };
  const first = pureReduce(state, event);
  const duplicate = applyEvent(first, event);
  assert.strictEqual(duplicate, first);
  assert.equal(first.seq, 7);
  assert.equal(messagesOf(first, "notice").length, 1);

  const changedPayload = applyEvent(first, { ...event, text: "still once" });
  assert.strictEqual(changedPayload, first);
});

test("EPOCH_BUMP resets messages and advances the epoch", () => {
  let state = createMirror(baseSnapshot());
  state = applyEvent(state, {
    kind: "turn_start", seq: 1, session: "session-1", request_id: "turn-1", text: "old",
  });
  const next = pureReduce(state, { type: "EPOCH_BUMP" });
  assert.equal(next.epoch, 2);
  assert.deepStrictEqual(next.messages, []);
  assert.equal(next.activeTurn, null);
  assert.equal(next.activeTool, "");
  // An epoch bump clears the display seam while preserving an in-flight run.
  assert.equal(next.running, true);
  assert.equal(next.history.before, 0);
  assert.equal(next.history.hasMore, false);
  assert.equal(next.history.generation, 2);
  assert.equal(next._seen.size, 0);

  const explicit = pureReduce(next, {
    type: "EPOCH_BUMP",
    snapshot: { session: "session-1", epoch: 9, messages: [], pending: [], background: [] },
  });
  assert.equal(explicit.epoch, 9);
  assert.deepStrictEqual(explicit.messages, []);
  assert.deepStrictEqual(explicit.pending, {});
  assert.deepStrictEqual(explicit.background, {});
});

test("HISTORY_PAGE prepends older messages, merges duplicate ids, and rejects stale seams", () => {
  const state = createMirror(baseSnapshot({
    messages: [
      { role: "user", content: "current question" },
      { role: "assistant", content: "current answer" },
    ],
    truncated: true,
  }));
  const page = {
    oldest: 0,
    hasMore: true,
    messages: [
      { role: "user", content: "older question" },
      { role: "assistant", content: "older answer" },
    ],
  };
  const next = pureReduce(state, {
    type: "HISTORY_PAGE", page, epoch: 1, generation: state.history.generation,
  });

  assert.deepStrictEqual(next.messages.map((message) => message.content), [
    "older question", "older answer", "current question", "current answer",
  ]);
  assert.deepStrictEqual(next.messages.slice(0, 2).map((message) => message.id), [
    "history-0-user", "history-1-assistant",
  ]);
  assert.equal(next.history.before, 0);
  assert.equal(next.history.hasMore, true);
  assert.equal(next.history.loading, false);

  const duplicatePage = mergeHistoryPage(next, page, 1, next.history.generation);
  assert.equal(duplicatePage.messages.length, next.messages.length);
  assert.deepStrictEqual(duplicatePage.messages.map((message) => message.id), next.messages.map((message) => message.id));
  assert.strictEqual(mergeHistoryPage(next, page, 2, next.history.generation), next);
  assert.strictEqual(mergeHistoryPage(next, page, 1, next.history.generation + 1), next);
});

test("public reducer aliases accept event, snapshot, and history actions", () => {
  assert.strictEqual(mirrorReducer, reduceMirror);
  let state = createMirror();
  state = applyEvent(state, { kind: "notice", seq: 1, text: "event" });
  assert.equal(onlyMessage(state, "notice").content, "event");
  state = applySnapshot(state, { session: "reset", epoch: 4, messages: [] });
  assert.equal(state.session, "reset");
  assert.equal(state.epoch, 4);
  assert.deepStrictEqual(state.messages, []);
});
