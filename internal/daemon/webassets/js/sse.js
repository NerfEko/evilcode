// evilcode web — one EventSource for the open transcript.
//
// The daemon names frames "snapshot" and "event" and puts the normal
// ServerMsg envelope in data. This small adapter keeps that wire detail out of
// app.js and persists the per-session epoch/sequence cursor used by reconnects.

function sequence(value, fallback = 0) {
  const number = Number(value);
  return Number.isFinite(number) && number > 0 ? Math.trunc(number) : fallback;
}

const CURSOR_PREFIX = "evilcode:mirror:";

function storageValue(storage) {
  if (storage !== undefined) return storage;
  try { return globalThis.localStorage; } catch { return null; }
}

function cursorKey(name) {
  return `${CURSOR_PREFIX}${encodeURIComponent(String(name))}`;
}

function integerOrNull(value) {
  const number = Number(value);
  return Number.isFinite(number) ? Math.trunc(number) : null;
}

/**
 * Read the small reconnect cursor kept per session. Storage is optional so the
 * stream remains usable in private browsing and in Node-based contract tests.
 */
export function readSessionCursor(name, storage) {
  const store = storageValue(storage);
  if (!store || typeof store.getItem !== "function") return null;
  try {
    const parsed = JSON.parse(store.getItem(cursorKey(name)) || "null");
    if (!parsed || typeof parsed !== "object") return null;
    const epoch = integerOrNull(parsed.epoch);
    const seq = integerOrNull(parsed.seq);
    if (epoch == null || epoch < 0 || seq == null || seq < 0) return null;
    return { epoch, seq };
  } catch {
    return null;
  }
}

/** Persist only protocol sequence state; never put transcript or image bytes in localStorage. */
export function writeSessionCursor(name, cursor, storage) {
  const store = storageValue(storage);
  if (!store || typeof store.setItem !== "function") return false;
  const epoch = integerOrNull(cursor?.epoch);
  const seq = integerOrNull(cursor?.seq);
  if (epoch == null || epoch < 0 || seq == null || seq < 0) return false;
  try {
    store.setItem(cursorKey(name), JSON.stringify({ epoch, seq }));
    return true;
  } catch {
    return false;
  }
}

export function clearSessionCursor(name, storage) {
  const store = storageValue(storage);
  if (!store || typeof store.removeItem !== "function") return false;
  try {
    store.removeItem(cursorKey(name));
    return true;
  } catch {
    return false;
  }
}

function jsonFrame(event) {
  if (!event || typeof event.data !== "string") return null;
  try { return JSON.parse(event.data); } catch { return null; }
}

function frameKind(payload, event) {
  if (event?.type === "snapshot" || event?.type === "event") return event.type;
  if (payload?.kind === "snapshot" || payload?.kind === "event") return payload.kind;
  if (payload?.snapshot) return "snapshot";
  if (payload?.event) return "event";
  return "";
}

function snapshotValue(payload) {
  if (payload?.kind === "snapshot" && payload.snapshot) return payload.snapshot;
  if (payload?.snapshot && typeof payload.snapshot === "object") return payload.snapshot;
  return payload;
}

function eventValue(payload) {
  if (payload?.kind === "event" && payload.event) return payload.event;
  if (payload?.event && typeof payload.event === "object") return payload.event;
  return payload;
}

function mirrorEnd(payload, snapshot) {
  // A snapshot's transport id is not a safe reducer/replay floor while a turn
  // is running. The daemon supplies mirror_end_seq when it can prove one.
  return sequence(snapshot?.mirror_end_seq ?? snapshot?.mirrorEndSeq ?? payload?.mirror_end_seq ?? payload?.mirrorEndSeq);
}

function noop() {}

/**
 * Open the event stream for one session.
 *
 * EventSource already reconnects with Last-Event-ID. `since` is included on the
 * first request so a caller can explicitly resume after a page reload. The
 * returned handle exposes the latest ring/mirror sequence and closes the
 * subscription deterministically when the route changes.
 */
export function openSessionStream(name, options = {}) {
  const EventSourceCtor = options.EventSource ?? globalThis.EventSource;
  const store = options.persist === false ? null : storageValue(options.storage);
  const persisted = readSessionCursor(name, store);
  const configuredEpoch = integerOrNull(options.epoch);
  const sameEpoch = configuredEpoch == null || !persisted || persisted.epoch === configuredEpoch;
  const initialSince = options.since !== undefined
    ? sequence(options.since)
    : (sameEpoch ? sequence(persisted?.seq) : 0);
  let lastSeen = initialSince;
  let currentEpoch = configuredEpoch ?? persisted?.epoch ?? null;
  let source = null;
  let closed = false;

  const callbacks = {
    onOpen: typeof options.onOpen === "function" ? options.onOpen : noop,
    onError: typeof options.onError === "function" ? options.onError : noop,
    onFrame: typeof options.onFrame === "function" ? options.onFrame : noop,
    onSnapshot: typeof options.onSnapshot === "function" ? options.onSnapshot : noop,
    onEvent: typeof options.onEvent === "function" ? options.onEvent : noop,
    onCursor: typeof options.onCursor === "function" ? options.onCursor : noop,
  };

  const persist = (seq) => {
    if (currentEpoch == null || seq < 0) return;
    const cursor = { epoch: currentEpoch, seq };
    writeSessionCursor(name, cursor, store);
    callbacks.onCursor(cursor, handle);
  };

  const handle = {
    get lastSeen() { return lastSeen; },
    get epoch() { return currentEpoch; },
    get cursor() { return currentEpoch == null ? null : { epoch: currentEpoch, seq: lastSeen }; },
    close() {
      closed = true;
      if (source && typeof source.close === "function") source.close();
      source = null;
    },
    reconnect() {
      if (closed) return;
      if (source && typeof source.close === "function") source.close();
      connect();
    },
  };

  function connect() {
    if (closed) return;
    if (typeof EventSourceCtor !== "function") {
      callbacks.onError(new Error("EventSource is unavailable"));
      return;
    }
    const suffix = lastSeen > 0 ? `?since=${encodeURIComponent(lastSeen)}` : "";
    const url = `/api/sessions/${encodeURIComponent(String(name))}/events${suffix}`;
    try {
      source = new EventSourceCtor(url, options.eventSourceOptions ?? { withCredentials: true });
    } catch (error) {
      callbacks.onError(error);
      return;
    }

    const receive = (event, explicitKind = "") => {
      if (closed) return;
      const payload = jsonFrame(event);
      if (!payload) {
        callbacks.onError(new Error("invalid SSE frame"), event);
        return;
      }
      const kind = explicitKind || frameKind(payload, event);
      const id = sequence(event?.lastEventId, sequence(payload?.seq ?? payload?.event?.seq));
      const snapshot = kind === "snapshot" ? snapshotValue(payload) : null;
      const eventValueForFrame = kind === "event" ? eventValue(payload) : null;
      const epochValue = kind === "snapshot"
        ? integerOrNull(snapshot?.epoch ?? snapshot?.snapshot_epoch)
        : integerOrNull(eventValueForFrame?.snapshot_epoch ?? eventValueForFrame?.snapshotEpoch);
      if (epochValue != null) currentEpoch = epochValue;
      const marker = kind === "snapshot" ? mirrorEnd(payload, snapshot) : 0;
      // An idle snapshot contains the complete conversation through its Seq;
      // a running snapshot may omit the in-flight turn, so only an explicit
      // mirror_end_seq is a safe replay floor in that case.
      const snapshotSeq = kind === "snapshot" ? sequence(snapshot?.seq ?? snapshot?.snapshot_seq) : 0;
      const end = kind === "snapshot"
        ? (marker || (!snapshot?.running ? snapshotSeq : 0))
        : sequence(id, sequence(eventValueForFrame?.seq));
      if (end > lastSeen) lastSeen = end;
      const meta = { id, kind, mirrorEndSeq: kind === "snapshot" ? marker : 0, raw: payload };
      callbacks.onFrame(payload, meta);
      if (kind === "snapshot") {
        callbacks.onSnapshot(snapshot, meta);
        // Do not erase a persisted running-turn cursor with the transport Seq;
        // the snapshot deliberately does not claim that partial turn.
        if (end > 0 || !persisted) persist(end);
      } else if (kind === "event") {
        callbacks.onEvent(eventValueForFrame, meta);
        if (end > 0) persist(end);
      }
    };

    source.onopen = (event) => callbacks.onOpen(event, handle);
    source.onerror = (event) => callbacks.onError(event, handle);
    if (typeof source.addEventListener === "function") {
      source.addEventListener("snapshot", (event) => receive(event, "snapshot"));
      source.addEventListener("event", (event) => receive(event, "event"));
    } else {
      source.onmessage = (event) => receive(event);
    }
  }

  connect();
  return handle;
}

// Names used by the app and by small embedders; all point at the same adapter.
export const connectSessionEvents = openSessionStream;
export const connectSSE = openSessionStream;
export const createEventStream = openSessionStream;
