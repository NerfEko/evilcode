// evilcode web — the browser-side transcript mirror (plan-web.md §8, Phase 5).
//
// The daemon owns the conversation. This reducer only owns a client's display
// copy, so it is deliberately pure: every action returns a new state and a
// reconnecting snapshot is always safe to apply. No DOM APIs live in this file;
// that makes the event contract testable with Node's built-in test runner.

export const EVENT_KINDS = Object.freeze([
  "turn_start", "text_delta", "reasoning_delta", "tool_start", "tool_result",
  "token_usage", "reasoning_effort", "notice", "memory_recall", "ask",
  "ask_resolved", "model", "background", "snapshot", "turn_end", "error",
]);

const STREAM_KINDS = new Set(["text_delta", "reasoning_delta"]);
const DEDUPE_LIMIT = 256;

const int = (value, fallback = 0) => {
  const n = Number(value);
  return Number.isFinite(n) ? Math.trunc(n) : fallback;
};

const text = (value) => value == null ? "" : String(value);
const has = (object, key) => !!object && Object.prototype.hasOwnProperty.call(object, key);

// Event payloads contain JSON-ish values (tool args, display cards and opaque
// provider items). Copy recursively rather than retaining a caller-owned
// object; reducers are much easier to reason about when dispatch is pure.
function cloneJSON(value, seen = new WeakMap()) {
  if (value == null || typeof value !== "object") return value;
  if (seen.has(value)) return seen.get(value);
  if (value instanceof Date) return new Date(value.getTime());
  if (value instanceof Set) {
    const out = new Set();
    seen.set(value, out);
    for (const entry of value) out.add(cloneJSON(entry, seen));
    return out;
  }
  if (value instanceof Map) {
    const out = new Map();
    seen.set(value, out);
    for (const [key, entry] of value) out.set(cloneJSON(key, seen), cloneJSON(entry, seen));
    return out;
  }
  if (Array.isArray(value)) {
    const out = [];
    seen.set(value, out);
    for (const entry of value) out.push(cloneJSON(entry, seen));
    return out;
  }
  const out = {};
  seen.set(value, out);
  for (const [key, entry] of Object.entries(value)) out[key] = cloneJSON(entry, seen);
  return out;
}

function copyUsage(usage) {
  if (!usage || typeof usage !== "object") return null;
  return {
    in: int(usage.in), out: int(usage.out),
    ctx_used: int(usage.ctx_used), ctx_max: int(usage.ctx_max),
    cache_hit: !!usage.cache_hit,
    cache_read: int(usage.cache_read), cache_write: int(usage.cache_write),
    gen_ms: int(usage.gen_ms),
  };
}

function emptyUsage() {
  return {
    in: 0, out: 0, ctx_used: 0, ctx_max: 0, cache_hit: false,
    cache_read: 0, cache_write: 0, gen_ms: 0,
  };
}

function copyCall(call) {
  if (!call || typeof call !== "object") return null;
  return {
    id: text(call.id), name: text(call.name),
    // Args is json.RawMessage on the Go side, but some providers send an
    // already-decoded object. Preserve either shape without sharing it.
    args: call.args == null ? "" : cloneJSON(call.args),
  };
}

function imageList(images) {
  return Array.isArray(images) ? images.map((image) => cloneJSON(image)) : [];
}

function imageInfo(message) {
  const images = imageList(message?.images ?? message?.image);
  let omitted = !!(
    message?.images_omitted || message?.image_omitted || message?.has_images ||
    message?.hasImages || message?.imagesOmitted
  );
  let count = 0;
  for (const key of ["image_count", "imageCount", "images_count", "imagesOmitted", "images_omitted"]) {
    const candidate = int(message?.[key]);
    if (candidate > 0) count = Math.max(count, candidate);
  }
  if (!count && images.length) count = images.length;
  if (!count && omitted) count = 1;
  // The daemon strips snapshot/history bytes but carries ImageCount. Treat that
  // metadata as omitted content, never as an empty live attachment list.
  if (!images.length && count > 0) omitted = true;
  return { images, omitted, count };
}

function item(kind, fields = {}, id = "") {
  return { id: id || `${kind}-${Math.random().toString(36).slice(2)}`, kind, ...fields };
}

// A deterministic id is useful for a history seam and keeps snapshots stable
// in tests. Live events use their call id/sequence instead.
function snapshotItem(kind, fields, index, sourceIndex = index) {
  return item(kind, {
    ...fields,
    // sourceKey is intentionally not used as the DOM id. It lets a durable
    // history page overlap a snapshot whose synthetic ids were made elsewhere.
    sourceKey: `${sourceIndex}:${kind}:${text(fields.callId)}`,
  }, `snapshot-${index}-${kind}`);
}

function callKey(call, index) {
  return text(call?.id) || `call-${index}`;
}

function snapshotMessages(raw, options = {}) {
  if (!Array.isArray(raw)) return [];
  const baseIndex = int(options.baseIndex, 0);
  const calls = new Map();
  for (const message of raw) {
    if (message?.role !== "assistant") continue;
    for (const [index, call] of (message.tool_calls ?? []).entries()) {
      const key = callKey(call, index);
      calls.set(key, copyCall(call));
    }
  }

  const out = [];
  const completedCalls = new Set();
  let turn = 0;
  raw.forEach((message, index) => {
    if (!message || message.hidden || message.role === "system") return;
    const role = text(message.role);
    const sourceIndex = baseIndex + index;
    const info = imageInfo(message);
    const imageFields = info.omitted ? { imagesOmitted: info.count || true } : {};

    if (role === "user") {
      turn += 1;
      if (message.content || info.images.length || info.omitted) {
        out.push(snapshotItem("user", {
          role, content: text(message.content), text: text(message.content),
          turn, images: info.images, ...imageFields, hidden: !!message.hidden,
          providerItems: Array.isArray(message.provider_items)
            ? cloneJSON(message.provider_items) : undefined,
        }, index, sourceIndex));
      }
      return;
    }
    if (role === "assistant") {
      const providerItems = Array.isArray(message.provider_items)
        ? cloneJSON(message.provider_items) : [];
      if (message.reasoning || providerItems.length) {
        out.push(snapshotItem("reasoning", {
          role: "assistant", content: text(message.reasoning),
          text: text(message.reasoning), turn, streaming: false,
          collapsed: !!message.content, providerItems,
        }, index, sourceIndex));
      }
      if (message.content) {
        out.push(snapshotItem("assistant", {
          role, content: text(message.content), text: text(message.content),
          turn, streaming: false, images: info.images, ...imageFields,
          providerItems,
        }, index, sourceIndex));
      }
      return;
    }
    if (role === "tool") {
      const id = text(message.tool_call_id);
      const call = calls.get(id) || null;
      if (id) completedCalls.add(id);
      const display = has(message, "display") ? { display: cloneJSON(message.display) } : {};
      const intent = text(message.intent ?? message.Intent);
      out.push(snapshotItem("tool", {
        role, callId: id, name: text(message.tool_name) || text(call?.name),
        args: call?.args ?? "", call: cloneJSON(call),
        target: toolTarget(call?.args), intent,
        output: text(message.content), content: text(message.content),
        diff: text(message.diff), error: message.is_error
          ? text(message.error || "tool failed") : "",
        failed: !!message.is_error, held: !!message.held,
        repairs: Array.isArray(message.repairs) ? cloneJSON(message.repairs) : [],
        images: info.images, ...imageFields, ...display, turn, status: "done",
      }, index, sourceIndex));
    }
  });

  // A snapshot can arrive in the middle of a tool round. Keep the requested
  // call visible even when its result has not landed yet.
  for (const [id, call] of calls) {
    if (completedCalls.has(id)) continue;
    out.push(snapshotItem("tool", {
      role: "tool", callId: id, name: call.name, args: cloneJSON(call.args),
      call: cloneJSON(call), target: toolTarget(call.args), intent: "",
      output: "", content: "", diff: "", error: "", failed: false, held: false,
      repairs: [], images: [], turn, status: "pending",
    }, raw.length + out.length, baseIndex + raw.length + out.length));
  }
  return out;
}

function toolTarget(args) {
  if (!args || typeof args !== "object") {
    if (typeof args !== "string") return "";
    try { return toolTarget(JSON.parse(args)); } catch { return ""; }
  }
  for (const key of ["path", "file", "target", "url", "query", "cmd", "command"]) {
    if (args[key] != null && String(args[key]) !== "") return String(args[key]);
  }
  return "";
}

function copyAsk(ask, id = "") {
  if (!ask || typeof ask !== "object") return null;
  const out = cloneJSON(ask);
  out.id = text(out.id) || text(id);
  // Invalid scalar options used to spread into characters. An ask with a
  // malformed options field is still renderable, but has no choices.
  out.options = Array.isArray(ask.options) ? cloneJSON(ask.options) : [];
  return out;
}

function copyPending(pending) {
  const out = {};
  if (Array.isArray(pending)) {
    for (const ask of pending) {
      const copy = copyAsk(ask);
      if (copy?.id) out[copy.id] = copy;
    }
  } else if (pending && typeof pending === "object") {
    for (const [id, ask] of Object.entries(pending)) {
      const copy = copyAsk(ask, id);
      if (copy?.id) out[copy.id] = copy;
    }
  }
  return out;
}

function copyBackground(background) {
  const out = {};
  if (Array.isArray(background)) {
    for (const task of background) {
      if (!task || task.id == null) continue;
      out[task.id] = cloneJSON(task);
    }
  } else if (background && typeof background === "object") {
    for (const [id, task] of Object.entries(background)) {
      if (!task || typeof task !== "object") continue;
      const copy = cloneJSON(task);
      copy.id = copy.id ?? (Number.isNaN(Number(id)) ? id : int(id));
      out[copy.id] = copy;
    }
  }
  return out;
}

function copyMCP(mcp) {
  return Array.isArray(mcp) ? cloneJSON(mcp) : [];
}

function turnsFromMessages(messages) {
  const turns = [];
  for (const message of messages) {
    const number = int(message?.turn);
    if (number <= 0) continue;
    while (turns.length < number) turns.push({
      id: `snapshot-turn-${turns.length + 1}`, usage: null,
    });
  }
  return turns;
}

function toolRecordFromMessage(message) {
  if (!message || message.kind !== "tool" || !message.callId) return null;
  const image = imageInfo(message);
  const record = {
    id: text(message.callId), name: text(message.name), args: message.args ?? "",
    call: message.call ?? null, target: text(message.target), intent: text(message.intent),
    output: text(message.output ?? message.content), content: text(message.content),
    diff: text(message.diff), error: text(message.error), failed: !!message.failed,
    held: !!message.held, repairs: Array.isArray(message.repairs) ? message.repairs : [],
    images: image.images, display: message.display, status: text(message.status) || "pending",
  };
  if (image.omitted) record.imagesOmitted = image.count || true;
  return cloneJSON(record);
}

function toolCallsFromMessages(messages) {
  const out = {};
  for (const message of messages ?? []) {
    if (message?.kind === "assistant") {
      for (const [index, call] of (message.toolCalls ?? message.tool_calls ?? []).entries()) {
        const copy = copyCall(call);
        if (!copy) continue;
        const id = copy.id || callKey(call, index);
        out[id] = {
          id, name: copy.name, args: cloneJSON(copy.args), call: copy,
          target: toolTarget(copy.args), intent: "", output: "", content: "",
          diff: "", error: "", failed: false, held: false, repairs: [], images: [],
          display: undefined, status: "pending",
        };
      }
    }
    const record = toolRecordFromMessage(message);
    if (record) out[record.id] = record;
  }
  return out;
}

function wireMessageCount(messages) {
  if (!Array.isArray(messages)) return 0;
  return messages.reduce((count, message) => {
    if (!message || text(message.role) === "system") return count;
    const imageCount = Math.max(
      int(message.image_count), int(message.imageCount),
      Array.isArray(message.images) ? message.images.length : 0,
      Array.isArray(message.Images) ? message.Images.length : 0,
    );
    const calls = message.tool_calls ?? message.toolCalls;
    if (!text(message.content) && !text(message.tool_name ?? message.toolName) &&
        !imageCount && !(Array.isArray(calls) && calls.length)) return count;
    return count + 1;
  }, 0);
}

function snapshotUsage(snapshot, previous, sameEpoch) {
  const provided = ["usage", "turn_usage", "turnUsage", "latest_usage", "latestUsage"]
    .find((key) => has(snapshot, key));
  if (provided) return copyUsage(snapshot[provided]);
  return sameEpoch ? copyUsage(previous?.usage) : null;
}

function snapshotSeq(snapshot, previous) {
  if (has(snapshot, "seq")) return int(snapshot.seq);
  if (has(snapshot, "snapshot_seq")) return int(snapshot.snapshot_seq);
  return int(previous?.seq);
}

function snapshotEpoch(snapshot, previous) {
  if (has(snapshot, "epoch")) return int(snapshot.epoch);
  if (has(snapshot, "snapshot_epoch")) return int(snapshot.snapshot_epoch);
  return int(previous?.epoch);
}

function snapshotRunning(snapshot, previous) {
  if (has(snapshot, "running")) return !!snapshot.running;
  if (has(snapshot, "snapshot_running")) return !!snapshot.snapshot_running;
  return !!previous?.running;
}

function snapshotField(snapshot, snake, camel, fallback) {
  if (has(snapshot, snake)) return snapshot[snake];
  if (camel && has(snapshot, camel)) return snapshot[camel];
  return fallback;
}

function snapshotMarker(snapshot) {
  for (const key of ["mirror_end_seq", "mirrorEndSeq", "snapshot_end_seq", "snapshotEndSeq"]) {
    if (has(snapshot, key)) return int(snapshot[key], -1);
  }
  return null;
}

function stateFromSnapshot(snapshot = {}, previous = null) {
  const rawMessages = Array.isArray(snapshot.messages)
    ? snapshot.messages
    : (Array.isArray(snapshot.snapshot_messages) ? snapshot.snapshot_messages : []);
  const old = snapshotField(snapshot, "oldest", "historyBefore", undefined);
  const historyBefore = old != null
    ? Math.max(0, int(old))
    : (has(snapshot, "history_before") ? Math.max(0, int(snapshot.history_before))
      : (snapshot.history && snapshot.history.before != null
        ? Math.max(0, int(snapshot.history.before)) : -1));
  const session = text(snapshotField(snapshot, "session", "snapshot_session", previous?.session));
  const model = text(snapshotField(snapshot, "model", "snapshot_model", previous?.model));
  const epoch = snapshotEpoch(snapshot, previous);
  const running = snapshotRunning(snapshot, previous);
  const sameEpoch = !!previous && epoch === int(previous.epoch);
  const rawBase = historyBefore >= 0 ? historyBefore : 0;
  const messages = snapshotMessages(rawMessages, { baseIndex: rawBase });
  const seq = snapshotSeq(snapshot, previous);
  const usage = snapshotUsage(snapshot, previous, sameEpoch);
  const turns = turnsFromMessages(messages);
  let activeTurn = null;
  let activePrompt = "";
  const latestUser = [...messages].reverse().find((message) => message.kind === "user");
  if (latestUser) activePrompt = text(latestUser.content);

  if (running) {
    const requestedID = snapshotField(snapshot, "active_turn_id", "activeTurnId", undefined)
      ?? snapshotField(snapshot, "request_id", "requestId", undefined);
    if (!turns.length) {
      activeTurn = {
        id: text(requestedID) || `turn-${seq || messages.length || 1}`,
        usage: usage || emptyUsage(), running: true,
      };
      turns.push(activeTurn);
    } else {
      const latest = turns[turns.length - 1];
      latest.usage = usage || copyUsage(latest.usage) || emptyUsage();
      latest.running = true;
      // Keep the snapshot turn id stable unless the wire explicitly gives an
      // active id and there is no message-derived turn to anchor it to.
      activeTurn = latest;
    }
  }

  const previousHistory = previous?.history;
  const truncated = !!(snapshot.truncated ?? snapshot.is_truncated);
  const hasMore = has(snapshot, "has_more") ? !!snapshot.has_more
    : (has(snapshot, "hasMore") ? !!snapshot.hasMore : truncated);
  // /messages indexes the shaped provider list. One assistant entry can become
  // two display cards (reasoning + answer), so using messages.length here skips
  // or repeats pages at the history seam.
  const before = historyBefore >= 0 ? historyBefore : wireMessageCount(rawMessages);
  const marker = snapshotMarker(snapshot);
  // A running snapshot does not necessarily include its partial turn. Without
  // the server's mirror_end_seq marker we cannot safely claim any sequence; the
  // replay path will fill it in. Idle snapshots are complete through seq.
  const epochChanged = !!previous && epoch !== int(previous.epoch);
  const floor = marker != null && !epochChanged ? marker
    : (epochChanged || running ? -1 : (seq > 0 ? seq : -1));
  const next = {
    session, model,
    provider: text(snapshotField(snapshot, "provider", "snapshot_provider", previous?.provider)),
    cwd: text(snapshotField(snapshot, "cwd", "snapshot_cwd", previous?.cwd)),
    running, epoch, seq,
    contextWindow: int(snapshotField(snapshot, "context_window", "contextWindow", previous?.contextWindow)),
    reasoningEffort: text(snapshotField(snapshot, "reasoning_effort", "reasoningEffort", previous?.reasoningEffort)),
    reasoningEfforts: Array.isArray(snapshotField(snapshot, "reasoning_efforts", "reasoningEfforts", undefined))
      ? cloneJSON(snapshotField(snapshot, "reasoning_efforts", "reasoningEfforts", []))
      : cloneJSON(previous?.reasoningEfforts ?? []),
    vision: !!snapshotField(snapshot, "vision", "vision", previous?.vision),
    skills: Array.isArray(snapshot.skills) ? cloneJSON(snapshot.skills) : cloneJSON(previous?.skills ?? []),
    mcp: copyMCP(snapshotField(snapshot, "mcp", "snapshot_mcp", previous?.mcp)),
    messages,
    pending: copyPending(snapshotField(snapshot, "pending", "snapshot_pending", [])),
    background: copyBackground(snapshotField(snapshot, "background", "snapshot_background", [])),
    usage,
    turns,
    activeTurn,
    activeTool: "",
    toolCalls: toolCallsFromMessages(messages),
    history: {
      before, hasMore, loading: false,
      generation: int(previousHistory?.generation) + 1,
    },
    // `_seen` is only a bounded replay cache. `_watermark` is the durable seam
    // represented by a snapshot, so a tab never retains every old sequence.
    _seen: new Set(),
    _watermark: floor,
    _highestSeq: floor,
    _activePrompt: activePrompt,
  };
  if (marker != null && marker > 0) next._seen.add(marker);
  // Keep the snapshot frame itself acknowledged for callers that inspect the
  // cache, but do not let a running frame hide replay events <= snapshot.seq.
  if (seq > 0 && (!running || marker === seq || epochChanged)) {
    next._seen.add(seq);
  }
  trimSeen(next._seen);
  return next;
}

export function createMirror(snapshot = {}) {
  // Accept either a Snapshot body or the server's {kind, snapshot} envelope.
  if (snapshot?.kind === "snapshot" && snapshot.snapshot) snapshot = snapshot.snapshot;
  return stateFromSnapshot(snapshot);
}

function cloneMessage(message) {
  return cloneJSON(message);
}

function cloneState(state) {
  const next = { ...state };
  next.messages = (state.messages ?? []).map(cloneMessage);
  next.pending = copyPending(state.pending);
  next.background = copyBackground(state.background);
  next.mcp = copyMCP(state.mcp);
  next.skills = cloneJSON(state.skills ?? []);
  next.turns = (state.turns ?? []).map((turn) => cloneJSON(turn));
  next.toolCalls = cloneJSON(state.toolCalls ?? {});
  next.activeTurn = null;
  if (state.activeTurn) {
    next.activeTurn = next.turns.find((turn) => turn.id === state.activeTurn.id)
      || cloneJSON(state.activeTurn);
    if (next.activeTurn.usage) next.activeTurn.usage = copyUsage(next.activeTurn.usage);
  }
  next.history = cloneJSON(state.history ?? {});
  next._seen = new Set(state._seen ?? []);
  next._watermark = int(state._watermark, -1);
  next._highestSeq = int(state._highestSeq, -1);
  next._activePrompt = text(state._activePrompt);
  if (state.usage) next.usage = copyUsage(state.usage);
  return next;
}

function trimSeen(seen) {
  while (seen.size > DEDUPE_LIMIT) {
    const first = seen.values().next().value;
    seen.delete(first);
  }
}

function finishStreaming(state) {
  for (const message of state.messages) {
    if (!message.streaming) continue;
    message.streaming = false;
    if (message.kind === "reasoning") message.collapsed = true;
  }
}

function activeTurn(state, event = {}) {
  const requestedID = text(event.request_id ?? event.requestId);
  if (state.activeTurn) {
    if (!requestedID || state.activeTurn.id === requestedID) return state.activeTurn;
    // Queued inputs can legitimately repeat the same text. A request id, when
    // present, is the only reliable distinction from a replayed turn_start.
    state.activeTurn.running = false;
    finishStreaming(state);
    state.activeTurn = null;
    state.activeTool = "";
  }
  const id = requestedID || `turn-${int(event.seq || state.seq || state.messages.length)}`;
  state.activeTurn = { id, usage: emptyUsage(), running: true };
  state.turns.push(state.activeTurn);
  return state.activeTurn;
}

function appendDelta(state, kind, delta, event) {
  const turn = activeTurn(state, event);
  const turnNumber = state.turns.length || 1;
  const last = state.messages[state.messages.length - 1];
  if (kind === "assistant") {
    for (const message of state.messages) {
      if (message.kind === "reasoning" && message.streaming) {
        message.streaming = false;
        message.collapsed = true;
      }
    }
  }
  if (last && last.kind === kind && last.streaming && last.turn === turnNumber) {
    last.content += text(delta);
    last.text = last.content;
    return last;
  }
  const message = item(kind, {
    role: "assistant", content: text(delta), text: text(delta),
    turn: turnNumber, streaming: true, collapsed: false,
  }, `${kind}-${turn.id}-${state.messages.length}`);
  state.messages.push(message);
  return message;
}

function findTool(state, event) {
  const call = event.call || {};
  const id = text(call.id) || text(event.tool_call_id ?? event.toolCallId)
    || state.activeTool;
  if (id) {
    const found = state.messages.find((message) => message.kind === "tool" && message.callId === id);
    if (found) return found;
  }
  return [...state.messages].reverse()
    .find((message) => message.kind === "tool" && message.status === "running") || null;
}

function syncToolCall(state, tool) {
  if (!tool?.callId) return;
  const record = toolRecordFromMessage(tool);
  if (record) state.toolCalls[record.id] = record;
}

function applyUsage(state, event) {
  const turn = activeTurn(state, event);
  const next = turn.usage ? copyUsage(turn.usage) : emptyUsage();
  const usage = event.usage || {};
  next.in += int(usage.in);
  next.out += int(usage.out);
  next.ctx_used = int(usage.ctx_used, next.ctx_used);
  next.ctx_max = int(usage.ctx_max, next.ctx_max);
  if (has(usage, "cache_hit")) next.cache_hit = !!usage.cache_hit;
  next.cache_read += int(usage.cache_read);
  next.cache_write += int(usage.cache_write);
  next.gen_ms += int(usage.gen_ms);
  turn.usage = next;
  state.usage = copyUsage(next);
}

function appendUsageItem(state, turn) {
  if (!turn?.usage || (!turn.usage.in && !turn.usage.out && !turn.usage.ctx_used)) return;
  const id = `usage-${turn.id}`;
  const existing = state.messages.find((message) => message.id === id);
  if (existing) {
    existing.usage = copyUsage(turn.usage);
    return;
  }
  state.messages.push(item("usage", {
    role: "system", usage: copyUsage(turn.usage), turn: state.turns.length,
  }, id));
}

function eventSnapshot(event, state) {
  const snapshot = {
    session: event.snapshot_session ?? event.snapshotSession ?? state.session,
    model: event.snapshot_model ?? event.snapshotModel ?? state.model,
    provider: event.snapshot_provider ?? event.snapshotProvider ?? state.provider,
    running: event.snapshot_running ?? event.snapshotRunning,
    epoch: event.snapshot_epoch ?? event.snapshotEpoch,
    seq: event.snapshot_seq ?? event.snapshotSeq ?? event.seq,
    messages: event.snapshot_messages ?? event.snapshotMessages,
    pending: event.snapshot_pending ?? event.snapshotPending,
    background: event.snapshot_background ?? event.snapshotBackground,
    mcp: event.snapshot_mcp ?? event.snapshotMCP,
    skills: event.snapshot_skills ?? event.snapshotSkills,
    usage: event.snapshot_usage ?? event.snapshotUsage,
    truncated: event.snapshot_truncated ?? event.snapshotTruncated,
    snapshot_incomplete: event.snapshot_incomplete ?? event.snapshotIncomplete,
    mirror_end_seq: event.mirror_end_seq ?? event.mirrorEndSeq,
    oldest: event.snapshot_oldest ?? event.snapshotOldest,
  };
  // Undefined properties should remain absent so explicit previous-state
  // fallback works (notably snapshot usage and running).
  for (const key of Object.keys(snapshot)) if (snapshot[key] === undefined) delete snapshot[key];
  return snapshot;
}

function applySnapshotMetadata(state, event) {
  const mappings = [
    ["snapshot_session", "snapshotSession", "session"],
    ["snapshot_model", "snapshotModel", "model"],
    ["snapshot_provider", "snapshotProvider", "provider"],
    ["snapshot_cwd", "snapshotCwd", "cwd"],
  ];
  for (const [snake, camel, target] of mappings) {
    if (has(event, snake) || has(event, camel)) state[target] = text(event[snake] ?? event[camel]);
  }
  if (has(event, "snapshot_epoch") || has(event, "snapshotEpoch")) {
    state.epoch = int(event.snapshot_epoch ?? event.snapshotEpoch);
  }
  if (has(event, "snapshot_pending") || has(event, "snapshotPending")) {
    state.pending = copyPending(event.snapshot_pending ?? event.snapshotPending);
  }
  if (has(event, "snapshot_background") || has(event, "snapshotBackground")) {
    state.background = copyBackground(event.snapshot_background ?? event.snapshotBackground);
  }
  if (has(event, "snapshot_mcp") || has(event, "snapshotMCP")) {
    state.mcp = copyMCP(event.snapshot_mcp ?? event.snapshotMCP);
  }
  if (has(event, "snapshot_skills") || has(event, "snapshotSkills")) {
    state.skills = cloneJSON(event.snapshot_skills ?? event.snapshotSkills ?? []);
  }
  if (has(event, "snapshot_running") || has(event, "snapshotRunning")) {
    state.running = !!(event.snapshot_running ?? event.snapshotRunning);
  }
}

function replaceTurnEndSnapshot(state, event) {
  if (!Array.isArray(event.snapshot_messages ?? event.snapshotMessages)) return null;
  if (event.snapshot_incomplete ?? event.snapshotIncomplete) return null;
  const raw = event.snapshot_messages ?? event.snapshotMessages;
  const oldest = event.snapshot_oldest ?? event.snapshotOldest;
  const messages = snapshotMessages(raw, { baseIndex: oldest == null ? 0 : int(oldest) });
  state.messages = messages;
  state.toolCalls = toolCallsFromMessages(messages);
  const turns = turnsFromMessages(messages);
  const usage = state.usage ? copyUsage(state.usage) : null;
  if (usage && turns.length) turns[turns.length - 1].usage = usage;
  state.turns = turns;
  state.history = {
    ...state.history,
    before: oldest == null ? wireMessageCount(raw) : Math.max(0, int(oldest)),
    hasMore: !!(event.snapshot_truncated ?? event.snapshotTruncated ?? event.truncated),
    loading: false,
  };
  return usage;
}

function reduceEvent(state, event) {
  if (!event || typeof event !== "object") return state;
  if (event.kind !== "snapshot" && event.session && state.session && event.session !== state.session) {
    return state;
  }
  if (event.kind === "snapshot") {
    const seq = int(event.seq);
    const eventEpoch = event.snapshot_epoch ?? event.snapshotEpoch;
    if (seq > 0 && state._seen?.has(seq)
      && (eventEpoch == null || int(eventEpoch) === int(state.epoch))) return state;
    return stateFromSnapshot(eventSnapshot(event, state), state);
  }

  const seq = int(event.seq);
  if (seq > 0) {
    const floor = Math.max(int(state._watermark, -1), int(state._highestSeq, -1));
    if (seq <= floor || state._seen?.has(seq)) return state;
  }
  const next = cloneState(state);
  if (seq > 0) {
    next._seen.add(seq);
    trimSeen(next._seen);
    next._highestSeq = Math.max(int(next._highestSeq, -1), seq);
    next.seq = Math.max(int(next.seq), seq);
  }

  switch (event.kind) {
    case "turn_start": {
      const requestedID = text(event.request_id ?? event.requestId);
      const samePrompt = text(next._activePrompt) === text(event.text);
      const wasActive = !!next.activeTurn &&
        (samePrompt || (requestedID && next.activeTurn.id === requestedID));
      next.running = true;
      const turn = activeTurn(next, event);
      turn.running = true;
      next._activePrompt = text(event.text);
      if (!wasActive && !event.hidden && (event.text || imageInfo(event).images.length || imageInfo(event).omitted)) {
        const info = imageInfo(event);
        const fields = {
          role: "user", content: text(event.text), text: text(event.text),
          turn: next.turns.length, images: info.images,
        };
        if (info.omitted) fields.imagesOmitted = info.count || true;
        next.messages.push(item("user", fields, `user-${turn.id}`));
      }
      break;
    }
    case "text_delta":
      next.running = true;
      appendDelta(next, "assistant", event.text, event);
      break;
    case "reasoning_delta":
      next.running = true;
      appendDelta(next, "reasoning", event.text, event);
      break;
    case "tool_start": {
      finishStreaming(next);
      next.running = true;
      const call = copyCall(event.call) || { id: "", name: "", args: "" };
      const id = call.id || `call-${seq || next.messages.length}`;
      let tool = next.messages.find((message) => message.kind === "tool" && message.callId === id);
      if (!tool) {
        tool = item("tool", {
          role: "tool", callId: id, name: call.name, args: cloneJSON(call.args),
          call: cloneJSON(call), target: toolTarget(call.args), intent: text(event.intent),
          output: "", content: "", diff: "", error: "", failed: false,
          held: false, repairs: Array.isArray(event.repairs) ? cloneJSON(event.repairs) : [],
          images: [], turn: next.turns.length || 1, status: "running",
        }, `tool-${id}`);
        next.messages.push(tool);
      } else {
        Object.assign(tool, {
          call: cloneJSON(call), name: call.name, args: cloneJSON(call.args), status: "running",
        });
        if (has(event, "intent")) tool.intent = text(event.intent);
        if (Array.isArray(event.repairs)) tool.repairs = cloneJSON(event.repairs);
      }
      next.activeTool = id;
      syncToolCall(next, tool);
      break;
    }
    case "tool_result": {
      finishStreaming(next);
      const tool = findTool(next, event);
      const call = copyCall(event.call);
      const id = text(call?.id) || text(event.tool_call_id ?? event.toolCallId)
        || text(tool?.callId) || `call-${seq || next.messages.length}`;
      const target = toolTarget(call?.args);
      const targetTool = tool || item("tool", {
        role: "tool", callId: id, name: call?.name || "tool",
        args: cloneJSON(call?.args || ""), call: cloneJSON(call), target,
        intent: text(event.intent), turn: next.turns.length || 1, status: "done",
      }, `tool-${id}`);
      if (!tool) next.messages.push(targetTool);
      if (call) Object.assign(targetTool, {
        call: cloneJSON(call), callId: call.id || targetTool.callId,
        name: call.name || targetTool.name, args: cloneJSON(call.args), target,
      });
      const error = has(event, "error") ? text(event.error) : text(event.err_text);
      const image = imageInfo(event);
      Object.assign(targetTool, {
        output: text(event.output), content: text(event.output), diff: text(event.diff),
        error, failed: !!error || !!event.is_error, held: !!event.held,
        repairs: Array.isArray(event.repairs) ? cloneJSON(event.repairs) : (targetTool.repairs ?? []),
        images: image.images, status: "done",
      });
      if (image.omitted) targetTool.imagesOmitted = image.count || true;
      else delete targetTool.imagesOmitted;
      if (has(event, "intent")) targetTool.intent = text(event.intent);
      if (has(event, "display")) targetTool.display = cloneJSON(event.display);
      next.activeTool = "";
      syncToolCall(next, targetTool);
      break;
    }
    case "token_usage":
      applyUsage(next, event);
      break;
    case "reasoning_effort":
      next.reasoningEffort = text(event.reasoning_effort ?? event.reasoningEffort);
      break;
    case "notice":
      finishStreaming(next);
      next.messages.push(item("notice", {
        role: "system", content: text(event.text), text: text(event.text),
        level: text(event.level) || "info", turn: next.turns.length,
      }, `notice-${seq || next.messages.length}`));
      break;
    case "memory_recall":
      next.messages.push(item("memory", {
        role: "system", content: "memory recall", display: cloneJSON(event.display),
        turn: next.turns.length,
      }, `memory-${seq || next.messages.length}`));
      break;
    case "ask": {
      const ask = copyAsk(event.ask);
      if (ask?.id) next.pending[ask.id] = ask;
      break;
    }
    case "ask_resolved": {
      const id = String(event.request_id ?? event.requestId ?? "");
      if (!id) break;
      const ask = next.pending[id];
      delete next.pending[id];
      // The resolution becomes a transcript card: the question rode in the
      // pending map, so the flow keeps the asked-and-answered shape even
      // though the wire event only names the id.
      if (ask && typeof ask === "object") {
        next.messages.push(item("ask", {
          role: "system", question: text(ask.question),
          options: Array.isArray(ask.options) ? cloneJSON(ask.options) : [],
          multi: !!ask.multi, askId: id, resolved: true,
          turn: next.turns.length,
        }, `ask-${id}`));
      }
      break;
    }
    case "model":
      if (has(event, "model")) next.model = text(event.model);
      if (has(event, "provider")) next.provider = text(event.provider);
      if (Array.isArray(event.reasoning_efforts)) next.reasoningEfforts = cloneJSON(event.reasoning_efforts);
      if (event.reasoning_effort_known || has(event, "reasoning_effort")) {
        next.reasoningEffort = text(event.reasoning_effort);
      }
      if (event.vision_known || has(event, "vision")) next.vision = !!event.vision;
      if (event.context_window_known || has(event, "context_window")) {
        next.contextWindow = int(event.context_window);
      }
      break;
    case "background": {
      const background = event.background ?? event.task;
      if (background?.id != null) next.background[background.id] = cloneJSON(background);
      break;
    }
    case "error":
      finishStreaming(next);
      next.messages.push(item("error", {
        role: "system", content: text(event.error ?? event.err_text),
        text: text(event.error ?? event.err_text), turn: next.turns.length,
      }, `error-${seq || next.messages.length}`));
      break;
    case "turn_end": {
      const turnBeforeSnapshot = next.activeTurn;
      const epochBeforeSnapshot = int(next.epoch);
      finishStreaming(next);
      const usageBeforeSnapshot = turnBeforeSnapshot?.usage
        ? copyUsage(turnBeforeSnapshot.usage) : copyUsage(next.usage);
      const replaced = replaceTurnEndSnapshot(next, event);
      applySnapshotMetadata(next, event);
      if (int(next.epoch) !== epochBeforeSnapshot) {
        next.history = {
          ...next.history,
          generation: int(next.history?.generation) + 1,
          loading: false,
        };
      }
      next.running = false;
      if (replaced && usageBeforeSnapshot && next.turns.length) {
        next.turns[next.turns.length - 1].usage = usageBeforeSnapshot;
      }
      const turn = turnBeforeSnapshot || (next.turns.length ? next.turns[next.turns.length - 1] : null);
      if (turn) {
        turn.running = false;
        if (usageBeforeSnapshot) {
          turn.usage = usageBeforeSnapshot;
          next.usage = copyUsage(usageBeforeSnapshot);
        }
        appendUsageItem(next, turn);
      }
      next.activeTurn = null;
      next.activeTool = "";
      next._activePrompt = "";
      break;
    }
    default:
      // Unknown future event kinds remain harmless to an older browser. The
      // sequence is still acknowledged so reconnect does not replay forever.
      break;
  }
  return next;
}

function messageFingerprint(message) {
  return JSON.stringify({
    kind: message.kind, role: message.role, content: message.content,
    text: message.text, turn: message.turn, callId: message.callId,
    name: message.name, target: message.target, args: message.args,
    output: message.output, diff: message.diff, error: message.error,
    sourceKey: undefined,
  });
}

function mergeHistory(state, action) {
  const page = action.page || action;
  if (!Array.isArray(page.messages)) return state;
  if (action.epoch != null && int(action.epoch) !== int(state.epoch)) return state;
  if (action.generation != null && int(action.generation) !== int(state.history?.generation)) return state;
  const start = Math.max(0, int(page.oldest));
  const older = snapshotMessages(page.messages, { baseIndex: start }).map((message, index) => ({
    ...message,
    id: `history-${start + index}-${message.kind}`,
    sourceKey: `${start + index}:${message.kind}:${text(message.callId)}`,
  }));
  const next = cloneState(state);
  const existingIDs = new Set(next.messages.map((message) => message.id));
  const existingKeys = new Set(next.messages.map((message) => message.sourceKey).filter(Boolean));
  const existingFingerprints = new Set(next.messages.map(messageFingerprint));
  const unique = [];
  for (const message of older) {
    const fingerprint = messageFingerprint(message);
    const sameSource = existingKeys.has(message.sourceKey) && existingFingerprints.has(fingerprint);
    if (existingIDs.has(message.id) || sameSource || existingFingerprints.has(fingerprint)) continue;
    existingIDs.add(message.id);
    existingKeys.add(message.sourceKey);
    existingFingerprints.add(fingerprint);
    unique.push(message);
  }
  next.messages = unique.concat(next.messages);
  const fromMessages = toolCallsFromMessages(next.messages);
  next.toolCalls = { ...next.toolCalls, ...fromMessages };
  next.history = {
    ...next.history,
    before: int(page.oldest), hasMore: !!page.hasMore, loading: false,
  };
  return next;
}

export function reduceMirror(state, action) {
  if (!state) return createMirror();
  // Raw ServerMsg/event input is accepted as a convenience for the SSE layer
  // and for small callers that do not need to spell out action types.
  if (action?.kind === "snapshot" && action.snapshot) {
    const snapshot = action.snapshot;
    const seq = int(snapshot.seq ?? snapshot.snapshot_seq);
    if (seq > 0 && state._seen?.has(seq)
      && (snapshot.epoch == null || int(snapshot.epoch) === int(state.epoch))) return state;
    return stateFromSnapshot(snapshot, state);
  }
  if (action?.kind === "event" && action.event) return reduceEvent(state, action.event);
  if (EVENT_KINDS.includes(action?.kind)) return reduceEvent(state, action);
  switch (action?.type) {
    case "SNAPSHOT": {
      const snapshot = action.snapshot || {};
      const seq = int(snapshot.seq ?? snapshot.snapshot_seq);
      if (seq > 0 && state._seen?.has(seq)
        && (snapshot.epoch == null || int(snapshot.epoch) === int(state.epoch))) return state;
      return stateFromSnapshot(snapshot, state);
    }
    case "EVENT":
      return reduceEvent(state, action.event);
    case "HISTORY_PAGE":
      return mergeHistory(state, action);
    case "EPOCH_BUMP": {
      if (action.snapshot) return stateFromSnapshot(action.snapshot, state);
      const next = stateFromSnapshot({
        ...state, epoch: int(state.epoch) + 1, messages: [], truncated: false,
      }, state);
      // An epoch bump clears display history but deliberately does not invent a
      // new turn. The agent may still be running; the next delta will rebuild it.
      next.turns = [];
      next.activeTurn = null;
      next.activeTool = "";
      next._activePrompt = "";
      next._seen = new Set();
      next._watermark = -1;
      next._highestSeq = -1;
      return next;
    }
    default:
      return state;
  }
}

export const mirrorReducer = reduceMirror;
export const applyEvent = (state, event) => reduceMirror(state, { type: "EVENT", event });
export const applySnapshot = (state, snapshot) => reduceMirror(state, { type: "SNAPSHOT", snapshot });
export const mergeHistoryPage = (state, page, epoch, generation) =>
  reduceMirror(state, { type: "HISTORY_PAGE", page, epoch, generation });

export function isStreamingKind(kind) {
  return STREAM_KINDS.has(kind);
}

export function snapshotMessagesForTest(messages) {
  return snapshotMessages(messages);
}
