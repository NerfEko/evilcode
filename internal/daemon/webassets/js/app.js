// evilcode web — the app entry (plan-web.md P4.2/P4.4, §8, §9). Owns the
// routing table, the roster/status poll loop, and the sidebar. The window is
// never the owner of live work: if this tab dies, the daemon keeps going, and
// whatever reopens restores its place from the hash and last-open storage.
//
// Routing: "#/" is home (the roster on a phone, the empty state on a desk),
// "#/s/<name>" drills into a session. Last-open rides localStorage so a
// standalone relaunch lands back in the chat (§8).
//
// The roster polls — it never streams (§5: one stream per tab belongs to the
// transcript, and iOS caps connections per origin). The loop runs every 2s
// while the page is visible, fires immediately on focus/visibilitychange, and
// pauses entirely when hidden.

import { getJSON, postJSON } from "./api.js";
import { openSessionStream, readSessionCursor, writeSessionCursor } from "./sse.js";
import { createMirror, reduceMirror } from "./mirror.js";
import { renderRoster } from "./views/roster.js";
import { chipsFor, renderChatHead, renderRail, renderTranscript, storedBanner } from "./views/chat.js";
import { mountComposer } from "./views/composer.js";
import { answerAsk, renderAsks } from "./views/asks.js";
import { openConfirmSheet, openModelsSheet, openNewSessionSheet, openSpawnSheet } from "./sheets.js";

const POLL_INTERVAL_MS = 2000;
const LAST_OPEN_KEY = "evilcode:last-open";

const rosterEl = document.getElementById("roster");
const statusEl = document.getElementById("statusline");

let pollTimer = null;
let route = { view: "home" };
let renderGeneration = 0;
let rosterRows = []; // the poll's cache; chat headers borrow worker/task state from it
let sessionStream = null;
let mirrorState = null;
let liveSnapshot = null;
let liveRow = null;
let streamGeneration = 0;
let renderFrame = null;
let renderFrameCancel = null;
let pendingMirrorRender = null;
let historyLoading = false;

const TRANSCRIPT_BOTTOM_GAP = 48;
const HISTORY_TOP_GAP = 72;
const HISTORY_PAGE_LIMIT = 50;

// The composer is mounted once and rebound per route; the ask dock only
// repaints when the pending signature changes so an open ask card is never
// destroyed under the cursor by a coalesced mirror render.
const composer = mountComposer({
  onUrgent: ({ text, clear }) => {
    openConfirmSheet({
      title: "Interrupt urgently",
      body: text
        ? `Inject this text as an URGENT interrupt at the next safe point:\n\n“${text}”`
        : "Cancel the running turn urgently? The agent unwinds at the next safe point.",
      confirmLabel: "Interrupt urgently",
      danger: true,
      onConfirm: async () => {
        await postJSON(`/api/sessions/${encodeURIComponent(route.name)}/interrupt`, {
          text, urgent: true,
        });
        clear();
      },
    });
  },
});
let answeredAsks = {}; // per open session: ask id → labels this tab chose
let lastAskSignature = "";

// ---- the sidebar: status line + roster ------------------------------------

async function refreshSidebar() {
  const [status, rows] = await Promise.allSettled([getJSON("/api/status"), getJSON("/api/sessions")]);
  if (status.status === "fulfilled") {
    statusEl.classList.remove("is-unreachable");
    statusEl.replaceChildren(
      statusSpan("sessions", status.value.sessions),
      statusSpan("running", status.value.running),
      statusSpan("clients", status.value.clients),
    );
  } else {
    statusEl.classList.add("is-unreachable");
    statusEl.textContent = "daemon unreachable";
  }
  if (rows.status === "fulfilled") {
    rosterRows = rows.value;
    renderRoster(rosterEl, rosterRows, { active: route.view === "chat" ? route.name : undefined });
  }
}

function statusSpan(label, value) {
  const span = document.createElement("span");
  span.append(`${label}=`);
  const b = document.createElement("b");
  b.textContent = value;
  span.appendChild(b);
  span.append(" ");
  return span;
}

function tick() {
  refreshSidebar().catch(() => {});
}

function startPolling() {
  if (pollTimer !== null) return;
  tick();
  pollTimer = setInterval(tick, POLL_INTERVAL_MS);
}

function stopPolling() {
  if (pollTimer !== null) {
    clearInterval(pollTimer);
    pollTimer = null;
  }
}

function cancelMirrorRender() {
  pendingMirrorRender = null;
  if (renderFrame === null) return;
  if (renderFrameCancel) renderFrameCancel(renderFrame);
  renderFrame = null;
  renderFrameCancel = null;
}

function closeSessionStream() {
  streamGeneration++;
  cancelMirrorRender();
  if (sessionStream) sessionStream.close();
  sessionStream = null;
  mirrorState = null;
  liveSnapshot = null;
  liveRow = null;
  historyLoading = false;
  setActivityPill(false);
}

function transcriptScroll() {
  return document.getElementById("transcript-scroll");
}

function atTranscriptBottom(scroll) {
  if (!scroll) return true;
  return scroll.scrollHeight <= scroll.clientHeight ||
    scroll.scrollHeight - scroll.scrollTop - scroll.clientHeight <= TRANSCRIPT_BOTTOM_GAP;
}

function activityPill() {
  const scroll = transcriptScroll();
  if (!scroll) return null;
  let pill = document.getElementById("new-activity");
  if (pill) return pill;
  pill = document.createElement("button");
  pill.id = "new-activity";
  pill.className = "new-activity-pill";
  pill.type = "button";
  pill.textContent = "↓ new activity";
  pill.hidden = true;
  pill.setAttribute("aria-hidden", "true");
  pill.addEventListener("click", () => {
    scroll.scrollTop = scroll.scrollHeight;
    setActivityPill(false);
  });
  scroll.appendChild(pill);
  return pill;
}

function setActivityPill(visible) {
  let pill = document.getElementById("new-activity");
  if (!pill && !visible) return;
  pill = pill || activityPill();
  if (!pill) return;
  pill.hidden = !visible;
  pill.setAttribute("aria-hidden", visible ? "false" : "true");
}

function scheduleMirrorRender(name, snapshot, row, state, generation) {
  pendingMirrorRender = { name, snapshot, row, state, generation };
  if (renderFrame !== null) return;
  const flush = () => {
    renderFrame = null;
    renderFrameCancel = null;
    const pending = pendingMirrorRender;
    pendingMirrorRender = null;
    if (pending) renderMirrorState(pending.name, pending.snapshot, pending.row, pending.state, pending.generation);
  };
  if (typeof requestAnimationFrame === "function") {
    renderFrame = requestAnimationFrame(flush);
    renderFrameCancel = typeof cancelAnimationFrame === "function" ? cancelAnimationFrame : clearTimeout;
  } else {
    renderFrame = setTimeout(flush, 33);
    renderFrameCancel = clearTimeout;
  }
}

function entriesOf(value) {
  if (value instanceof Map) return [...value.entries()];
  if (Array.isArray(value)) return value.map((item, index) => [index, item]);
  return Object.entries(value ?? {});
}

function messagesFromMirror(state) {
  const messages = Array.isArray(state?.messages) ? state.messages.slice() : [];
  const seenCalls = new Set(messages.map((message) => message?.callId ?? message?.call?.id).filter(Boolean).map(String));
  for (const [key, value] of entriesOf(state?.toolCalls)) {
    if (!value || typeof value !== "object") continue;
    const call = value.call && typeof value.call === "object" ? { ...value.call, ...value } : value;
    const callId = String(call.callId ?? call.id ?? key ?? "");
    if (!callId || seenCalls.has(callId)) continue;
    seenCalls.add(callId);
    const output = call.output ?? call.content ?? "";
    messages.push({
      ...call,
      id: call.id ?? `tool-${callId}`,
      kind: "tool", role: "tool", callId,
      name: call.name ?? call.tool_name ?? "tool",
      args: call.args ?? "", target: call.target ?? "",
      output, content: output, diff: call.diff ?? "",
      error: call.error ?? call.err ?? "", failed: !!(call.failed || call.error || call.err),
      held: !!call.held, repairs: Array.isArray(call.repairs) ? call.repairs : [],
      images: Array.isArray(call.images) ? call.images : [],
      status: call.status ?? "done",
    });
  }
  return messages;
}

function mirrorSnapshotSequence(snapshot, state, cursor) {
  // A running snapshot's Seq can include an in-flight turn that is absent from
  // its history. Reuse a persisted cursor only inside the same conversation
  // epoch and only while it still belongs to this ring; otherwise start at the
  // fresh-attach seam and let the server replay the active turn.
  const value = snapshot?.mirror_end_seq ?? snapshot?.mirrorEndSeq ?? state?.mirrorEndSeq ?? state?.mirror_end_seq ?? state?._watermark;
  const safe = Number(value);
  const snapshotSeq = Number(snapshot?.seq ?? snapshot?.snapshot_seq ?? state?.seq);
  const epoch = Number(state?.epoch ?? snapshot?.epoch ?? snapshot?.snapshot_epoch);
  const cursorEpoch = Number(cursor?.epoch);
  const cursorSeq = Number(cursor?.seq);
  if (state?.running && Number.isFinite(epoch) && cursorEpoch === epoch &&
      Number.isFinite(cursorSeq) && cursorSeq > 0 &&
      Number.isFinite(snapshotSeq) && snapshotSeq >= cursorSeq) {
    return Math.trunc(cursorSeq);
  }
  return Number.isFinite(safe) && safe > 0 ? Math.trunc(safe) : 0;
}

function rememberMirrorCursor(name, state) {
  if (!state) return;
  // A running snapshot's transport Seq also covers deltas omitted from its
  // conversation copy. Persist only a proven mirror floor; EventSource records
  // each replayed event as it arrives.
  if (state.running) {
    const floor = Number(state._watermark);
    if (!Number.isFinite(floor) || floor < 0) return;
    writeSessionCursor(name, { epoch: state.epoch, seq: Math.trunc(floor) });
    return;
  }
  writeSessionCursor(name, { epoch: state.epoch, seq: state.seq });
}

function mirrorData(snapshot, state) {
  const background = entriesOf(state?.background).map(([, task]) => task).filter(Boolean);
  const pending = entriesOf(state?.pending).map(([, ask]) => ask).filter(Boolean);
  return {
    ...snapshot,
    session: state?.session || snapshot?.session,
    model: state?.model || snapshot?.model,
    provider: state?.provider || snapshot?.provider,
    cwd: state?.cwd || snapshot?.cwd,
    running: !!state?.running,
    pending,
    context_window: state?.contextWindow ?? snapshot?.context_window,
    background,
    mcp: state?.mcp ?? snapshot?.mcp,
    skills: state?.skills ?? snapshot?.skills,
    usage: state?.usage ?? snapshot?.usage,
  };
}

function askSignature(pending) {
  const asks = Array.isArray(pending) ? pending : [];
  return asks.map((a) => `${a?.id}:${a?.multi ? "m" : "s"}:${(a?.options ?? []).map((o) => o?.label ?? "").join("|")}`).join(";");
}

function renderMirrorState(name, snapshot, row, state, generation, options = {}) {
  if (generation !== renderGeneration || generation !== streamGeneration) return;
  const scroll = transcriptScroll();
  const wasAtBottom = options.forceBottom === true || atTranscriptBottom(scroll);
  const previousHeight = options.preserveScroll?.height;
  const previousTop = options.preserveScroll?.top;
  const data = mirrorData(snapshot, state);
  const info = data.session && typeof data.session === "object" ? data.session : {};
  renderChatHead({
    title: info.name ?? name,
    chips: chipsFor(data, row),
    sub: [data.model ?? info.model, data.cwd ?? info.cwd, row?.task ? `▸ ${row.task}` : ""].filter(Boolean).join(" · "),
  });
  const transcript = document.getElementById("transcript");
  renderTranscript(transcript, messagesFromMirror(state), { stateHistory: state.history, mirror: state, answered: answeredAsks });
  composer.setRunning(data.running);
  renderRail(document.getElementById("rail-body"), data, row, {
    onPickModel: () => openModelsSheet({
      session: route.name,
      currentModel: data.model,
      currentEffort: data.reasoning_effort,
      efforts: Array.isArray(data.reasoning_efforts) && data.reasoning_efforts.length
        ? data.reasoning_efforts
        : undefined,
      onPicked: () => composer.note("Model switched."),
    }),
    onPickEffort: (level) => {
      postJSON(`/api/sessions/${encodeURIComponent(route.name)}/effort`, { effort: level })
        .then(() => composer.note(`Reasoning effort: ${level}`))
        .catch((err) => composer.note(String(err.message ?? err)));
    },
    onSpawn: () => document.getElementById("chat-spawn").click(),
  });
  const dock = document.getElementById("ask-dock");
  if (dock) {
    const signature = askSignature(data.pending);
    if (signature !== lastAskSignature) {
      lastAskSignature = signature;
      renderAsks(dock, data.pending, answeredAsks, (ask, labels, card) => {
        answerAsk(route.name, ask, labels, card).then((result) => {
          if (result.ok) answeredAsks[ask.id] = labels;
        });
      });
    }
  }
  if (scroll) {
    if (previousHeight != null && previousTop != null) {
      scroll.scrollTop = previousTop + (scroll.scrollHeight - previousHeight);
    } else if (wasAtBottom) {
      scroll.scrollTop = scroll.scrollHeight;
    }
  }
  setActivityPill(!wasAtBottom && !options.preserveScroll);
}

function loadOlderHistory() {
  if (historyLoading || route.view !== "chat" || !mirrorState?.history?.hasMore) return;
  const before = Number(mirrorState.history.before);
  if (!Number.isFinite(before) || before <= 0) return;
  const epoch = mirrorState.epoch;
  const generation = renderGeneration;
  const scroll = transcriptScroll();
  const previous = scroll ? { height: scroll.scrollHeight, top: scroll.scrollTop } : null;
  historyLoading = true;
  const path = `/api/sessions/${encodeURIComponent(route.name)}/messages?before=${encodeURIComponent(Math.trunc(before))}&limit=${HISTORY_PAGE_LIMIT}`;
  getJSON(path).then((page) => {
    if (generation !== renderGeneration || route.view !== "chat" || !mirrorState || mirrorState.epoch !== epoch) return;
    const next = reduceMirror(mirrorState, {
      type: "HISTORY_PAGE", page, epoch, generation: mirrorState.history.generation,
    });
    if (next === mirrorState) return;
    mirrorState = next;
    cancelMirrorRender();
    renderMirrorState(route.name, liveSnapshot, liveRow, mirrorState, generation, {
      preserveScroll: previous,
    });
  }).catch(() => {
    // A transient history read failure should not tear down a live stream; a
    // later scroll-up/focus retries the same page.
  }).finally(() => {
    historyLoading = false;
  });
}

document.addEventListener("visibilitychange", () => {
  if (document.visibilityState === "visible") startPolling();
  else stopPolling(); // P2 verify, DOM half: the roster polls only while visible.
});
window.addEventListener("focus", () => {
  if (document.visibilityState === "visible") tick();
});

// ---- routing ----------------------------------------------------------------

// Roster rows are rebuilt on every poll. Delegate once from the stable nav so a
// freshly polled row is still a real route link (and so touch/keyboard clicks
// share exactly the same hash + localStorage path).
rosterEl.addEventListener("click", (event) => {
  const target = event.target;
  const row = target instanceof Element ? target.closest("button[data-name]") : null;
  if (!row || !rosterEl.contains(row)) return;
  location.hash = `#/s/${encodeURIComponent(row.dataset.name)}`;
});

function parseRoute() {
  const hash = location.hash || "#/";
  const m = /^#\/s\/([^/?]+)$/.exec(hash);
  if (!m) return { view: "home" };
  try {
    return { view: "chat", name: decodeURIComponent(m[1]) };
  } catch {
    return { view: "home" };
  }
}

function setView(view) {
  route = view;
  document.getElementById("app").dataset.view = view.view;
}

async function render() {
  const generation = ++renderGeneration;
  setView(parseRoute());
  closeSessionStream();
  const chatEl = document.getElementById("chat");

  if (route.view === "home") {
    localStorage.removeItem(LAST_OPEN_KEY);
    composer.bind("");
    return;
  }

  localStorage.setItem(LAST_OPEN_KEY, route.name);
  chatEl.setAttribute("aria-busy", "true");
  try {
    const data = await getJSON(`/api/sessions/${encodeURIComponent(route.name)}`);
    if (generation !== renderGeneration) return;
    const info = data.session ?? {};
    const row = rosterRows.find((r) => r.name === route.name);
    renderChatHead({
      title: info.name ?? route.name,
      chips: chipsFor(data, row),
      sub: [
        data.model ?? info.model,
        data.cwd ?? info.cwd,
        row?.task ? `▸ ${row.task}` : "",
      ].filter(Boolean).join(" · "),
    });

    const transcript = document.getElementById("transcript");
    if (info.live === false) {
      composer.bind(""); // stored views stay read-only; Reopen is the door back
      lastAskSignature = "";
      renderTranscript(transcript, data.messages, { before: storedBanner(info.name), history: true });
      renderRail(document.getElementById("rail-body"), data, row);
    } else {
      const cursor = readSessionCursor(route.name);
      mirrorState = createMirror(data);
      rememberMirrorCursor(route.name, mirrorState);
      answeredAsks = {};
      lastAskSignature = "";
      composer.bind(route.name);
      composer.setRunning(mirrorState.running || data.running);
      const streamEpoch = streamGeneration;
      liveSnapshot = data;
      liveRow = row;
      renderMirrorState(route.name, liveSnapshot, liveRow, mirrorState, generation, { forceBottom: true });
      sessionStream = openSessionStream(route.name, {
        epoch: mirrorState.epoch,
        since: mirrorSnapshotSequence(data, mirrorState, cursor),
        onFrame: (frame) => {
          const kind = frame?.event?.kind ?? frame?.kind;
          if (kind !== "text_delta" && kind !== "reasoning_delta") refreshSidebar().catch(() => {});
        },
        onSnapshot: (snapshot) => {
          if (generation !== renderGeneration || streamEpoch !== streamGeneration) return;
          const renamed = typeof snapshot?.session === "string" ? snapshot.session.trim() : "";
          if (renamed && renamed !== route.name) {
            localStorage.setItem(LAST_OPEN_KEY, renamed);
            location.hash = `#/s/${encodeURIComponent(renamed)}`;
            return;
          }
          liveSnapshot = snapshot && typeof snapshot === "object" ? snapshot : liveSnapshot;
          mirrorState = reduceMirror(mirrorState, { type: "SNAPSHOT", snapshot: liveSnapshot });
          rememberMirrorCursor(route.name, mirrorState);
          scheduleMirrorRender(route.name, liveSnapshot, liveRow, mirrorState, generation);
        },
        onEvent: (event) => {
          if (generation !== renderGeneration || streamEpoch !== streamGeneration || !event) return;
          mirrorState = reduceMirror(mirrorState, { type: "EVENT", event });
          rememberMirrorCursor(route.name, mirrorState);
          scheduleMirrorRender(route.name, liveSnapshot, liveRow, mirrorState, generation);
        },
      });
    }
    const scroll = transcriptScroll();
    if (scroll) scroll.scrollTop = scroll.scrollHeight;
    setActivityPill(false);
  } catch (err) {
    if (generation !== renderGeneration) return;
    renderChatHead({ title: route.name, chips: [], sub: "" });
    const transcript = document.getElementById("transcript");
    transcript.replaceChildren();
    const box = document.createElement("div");
    box.className = "callout callout--error";
    const t = document.createElement("div");
    t.className = "callout-title";
    t.textContent = "Cannot open this session";
    box.append(t, String(err.message ?? err));
    transcript.appendChild(box);
  } finally {
    if (generation === renderGeneration) chatEl.removeAttribute("aria-busy");
  }
  // Roster keeps its active-row marker in sync with the route.
  refreshSidebar().catch(() => {});
}

window.addEventListener("hashchange", render);

// Reopen-stored lands here: re-fetch the now-live session in place.
window.addEventListener("evilcode:session-opened", (e) => {
  if (e.detail?.name) {
    const nextHash = `#/s/${encodeURIComponent(e.detail.name)}`;
    // Reopening a stored view usually keeps the same hash, so assigning it
    // would not fire hashchange; render explicitly in that case.
    if (location.hash === nextHash) render();
    else location.hash = nextHash;
  }
  refreshSidebar().catch(() => {});
});

// ---- sidebar actions ---------------------------------------------------------

document.getElementById("new-session").addEventListener("click", () => {
  openNewSessionSheet({
    onCreated: (info) => {
      if (info?.name) location.hash = `#/s/${encodeURIComponent(info.name)}`;
      refreshSidebar().catch(() => {});
    },
  });
});

document.getElementById("chat-back").addEventListener("click", () => {
  location.hash = "#/";
});

function openSpawnSheetForRoute() {
  if (route.view !== "chat" || !route.name) return;
  openSpawnSheet({
    session: route.name,
    cwd: liveSnapshot?.cwd ?? liveRow?.cwd ?? "",
    onSpawned: (info) => {
      if (info?.name) composer.note(`Worker ${info.name} started.`);
      refreshSidebar().catch(() => {});
    },
  });
}

document.getElementById("chat-spawn").addEventListener("click", () => {
  openSpawnSheetForRoute();
});

document.getElementById("chat-spawn-wide").addEventListener("click", () => {
  openSpawnSheetForRoute();
});

document.getElementById("chat-model").addEventListener("click", () => {
  openModelSheet();
});

document.getElementById("chat-model-wide").addEventListener("click", () => {
  openModelSheet();
});

function openModelSheet() {
  const data = liveSnapshot ?? {};
  openModelsSheet({
    session: route.name,
    currentModel: data.model,
    currentEffort: data.reasoning_effort,
    efforts: Array.isArray(data.reasoning_efforts) && data.reasoning_efforts.length
      ? data.reasoning_efforts
      : undefined,
    onPicked: () => composer.note("Model switched."),
  });
}

transcriptScroll()?.addEventListener("scroll", () => {
  const scroll = transcriptScroll();
  if (!scroll) return;
  if (atTranscriptBottom(scroll)) setActivityPill(false);
  if (scroll.scrollTop <= HISTORY_TOP_GAP) loadOlderHistory();
}, { passive: true });

// ---- boot ----------------------------------------------------------------------

// A standalone relaunch (or a refresh) restores the last-open session; a
// first visit starts at home.
if (!location.hash && localStorage.getItem(LAST_OPEN_KEY)) {
  location.hash = `#/s/${encodeURIComponent(localStorage.getItem(LAST_OPEN_KEY))}`;
} else {
  render();
}
startPolling();
if (document.visibilityState !== "visible") stopPolling();