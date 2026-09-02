import test from "node:test";
import assert from "node:assert/strict";
import { readFile } from "node:fs/promises";

const source = await readFile(new URL("./sse.js", import.meta.url), "utf8");
const sse = await import(`data:text/javascript;charset=utf-8,${encodeURIComponent(source)}`);

function memoryStorage() {
  const values = new Map();
  return {
    getItem(key) { return values.has(key) ? values.get(key) : null; },
    setItem(key, value) { values.set(key, String(value)); },
    removeItem(key) { values.delete(key); },
  };
}

class FakeEventSource {
  static instances = [];

  constructor(url) {
    this.url = url;
    this.readyState = 0; // CONNECTING until the adapter hears "open"
    this.listeners = new Map();
    this.closed = false;
    FakeEventSource.instances.push(this);
  }

  addEventListener(kind, callback) {
    this.listeners.set(kind, callback);
  }

  close() { this.closed = true; }

  emit(kind, payload, id = "") {
    this.listeners.get(kind)?.({
      type: kind, lastEventId: String(id), data: JSON.stringify(payload),
    });
  }
}

function resetSources() { FakeEventSource.instances.length = 0; }


test("session cursors round-trip without throwing on malformed storage", () => {
  const storage = memoryStorage();
  assert.equal(sse.writeSessionCursor("a/b", { epoch: 3, seq: 8 }, storage), true);
  assert.deepEqual(sse.readSessionCursor("a/b", storage), { epoch: 3, seq: 8 });
  storage.setItem("evilcode:mirror:a%2Fb", "not json");
  assert.equal(sse.readSessionCursor("a/b", storage), null);
  assert.equal(sse.writeSessionCursor("a/b", { epoch: -1, seq: 8 }, storage), false);
  assert.equal(sse.clearSessionCursor("a/b", storage), true);
  assert.equal(sse.readSessionCursor("a/b", storage), null);
});

test("stream seeds from a same-epoch cursor and persists authoritative frames", () => {
  resetSources();
  const storage = memoryStorage();
  sse.writeSessionCursor("session-1", { epoch: 2, seq: 7 }, storage);
  const seen = [];
  const handle = sse.openSessionStream("session-1", {
    EventSource: FakeEventSource, storage, epoch: 2,
    onSnapshot(snapshot) { seen.push(["snapshot", snapshot.seq]); },
    onEvent(event) { seen.push(["event", event.kind]); },
  });
  const source = FakeEventSource.instances[0];
  assert.match(source.url, /[?&]since=7(?:&|$)/);

  source.emit("snapshot", {
    version: 2, kind: "snapshot",
    snapshot: { session: "session-1", epoch: 2, seq: 9, running: false },
  }, 9);
  source.emit("event", {
    version: 2, kind: "event", event: {
      session: "session-1", kind: "notice", seq: 10, text: "ok",
    },
  }, 10);

  assert.deepEqual(seen, [["snapshot", 9], ["event", "notice"]]);
  assert.deepEqual(sse.readSessionCursor("session-1", storage), { epoch: 2, seq: 10 });
  assert.equal(handle.lastSeen, 10);
  assert.deepEqual(handle.cursor, { epoch: 2, seq: 10 });
  handle.close();
});

test("epoch mismatch discards a persisted sequence before opening the stream", () => {
  resetSources();
  const storage = memoryStorage();
  sse.writeSessionCursor("session-2", { epoch: 1, seq: 99 }, storage);
  const handle = sse.openSessionStream("session-2", {
    EventSource: FakeEventSource, storage, epoch: 2,
  });
  assert.equal(FakeEventSource.instances[0].url, "/api/sessions/session-2/events");
  handle.close();
});

// ---- P7.4: reconnect resilience ---------------------------------------------

test("a CLOSED stream reconnects with backoff and replays from the last-seen seq", (t) => {
  resetSources();
  t.mock.timers.enable({ apis: ["setTimeout"] });
  const storage = memoryStorage();
  const handle = sse.openSessionStream("session-3", {
    EventSource: FakeEventSource, storage,
  });
  const first = FakeEventSource.instances[0];
  first.readyState = 1;
  first.emit("event", {
    version: 2, kind: "event",
    event: { session: "session-3", kind: "notice", seq: 5, text: "hi" },
  }, 5);
  assert.equal(handle.lastSeen, 5);

  // Safari killed the suspended socket outright: CLOSED, no retry scheduled.
  first.readyState = 2;
  first.onerror?.({ type: "error" });
  t.mock.timers.tick(500);
  const second = FakeEventSource.instances[1];
  assert.ok(second, "a CLOSED stream schedules exactly one reconnect");
  assert.match(second.url, /[?&]since=5(?:&|$)/);
  handle.close();
});

test("a CONNECTING stream is left to the browser's own retry", (t) => {
  resetSources();
  t.mock.timers.enable({ apis: ["setTimeout"] });
  const handle = sse.openSessionStream("session-4", { EventSource: FakeEventSource });
  const first = FakeEventSource.instances[0];
  first.readyState = 0;
  first.onerror?.({ type: "error" });
  t.mock.timers.tick(60000);
  assert.equal(FakeEventSource.instances.length, 1, "no self-scheduled reconnect while the browser retries");
  handle.close();
});

test("resume() drops the half-dead transport and reconnects at the last-seen seq", () => {
  resetSources();
  const handle = sse.openSessionStream("session-5", { EventSource: FakeEventSource });
  const first = FakeEventSource.instances[0];
  first.readyState = 1;
  first.emit("snapshot", {
    version: 2, kind: "snapshot",
    snapshot: { session: "session-5", epoch: 1, seq: 9, running: false },
  }, 9);
  assert.equal(handle.health, "open");
  handle.resume();
  const second = FakeEventSource.instances[1];
  assert.ok(first.closed, "resume closes the old transport");
  assert.match(second.url, /[?&]since=9(?:&|$)/);
  assert.equal(handle.health, "connecting");
  handle.close();
  assert.equal(handle.health, "closed");
});

test("reconnect attempts give up after a bounded run instead of hammering a 401", (t) => {
  resetSources();
  t.mock.timers.enable({ apis: ["setTimeout"] });
  const exhausted = [];
  const handle = sse.openSessionStream("session-6", {
    EventSource: FakeEventSource, onExhausted: (h) => exhausted.push(h),
  });
  for (let i = 0; i < 12; i++) {
    const source = FakeEventSource.instances.at(-1);
    source.readyState = 2;
    source.onerror?.({ type: "error" });
    t.mock.timers.tick(60000);
  }
  assert.equal(
    FakeEventSource.instances.length,
    sse.RECONNECT_MAX_ATTEMPTS + 1,
    "initial connection plus the bounded reconnect run, then silence",
  );
  assert.equal(exhausted.length, 1, "the app is told once when the run gives up");
  // Returning to the tab resets the run and connects immediately.
  handle.resume();
  assert.equal(FakeEventSource.instances.length, sse.RECONNECT_MAX_ATTEMPTS + 2);
  handle.close();
});

test("an idle snapshot with a lower sequence adopts the server's reset ring", () => {
  resetSources();
  const handle = sse.openSessionStream("session-7", { EventSource: FakeEventSource });
  const first = FakeEventSource.instances[0];
  first.readyState = 1;
  // Old ring: high sequence from a previous daemon lifetime.
  first.emit("event", {
    version: 2, kind: "event",
    event: { session: "session-7", kind: "notice", seq: 500, text: "old life" },
  }, 500);
  assert.equal(handle.lastSeen, 500);
  // Restart: a fresh idle snapshot resets the ring to a low sequence.
  first.emit("snapshot", {
    version: 2, kind: "snapshot",
    snapshot: { session: "session-7", epoch: 1, seq: 5, running: false },
  }, 5);
  assert.equal(handle.lastSeen, 5, "a dead ring's high cursor must not survive an authoritative snapshot");
  handle.resume();
  assert.match(FakeEventSource.instances[1].url, /[?&]since=5(?:&|$)/);
  handle.close();
});

test("frames queued from a replaced transport are ignored", () => {
  resetSources();
  const seen = [];
  const handle = sse.openSessionStream("session-8", {
    EventSource: FakeEventSource,
    onSnapshot: (s) => seen.push(["snapshot", s?.seq]),
    onEvent: (e) => seen.push(["event", e?.seq]),
  });
  const first = FakeEventSource.instances[0];
  first.readyState = 1;
  handle.resume(); // replaces the transport
  // A frame that was already queued in the old transport now fires — it must
  // not advance the cursor or touch the mirror.
  first.emit("event", {
    version: 2, kind: "event",
    event: { session: "session-8", kind: "notice", seq: 77, text: "stale" },
  }, 77);
  assert.deepEqual(seen, []);
  assert.equal(handle.lastSeen, 0);
  // The replacement still works normally.
  FakeEventSource.instances[1].readyState = 1;
  FakeEventSource.instances[1].emit("event", {
    version: 2, kind: "event",
    event: { session: "session-8", kind: "notice", seq: 3, text: "fresh" },
  }, 3);
  assert.deepEqual(seen, [["event", 3]]);
  assert.equal(handle.lastSeen, 3);
  handle.close();
});

test("reconnect() cancels a pending backoff timer instead of doubling streams", (t) => {
  resetSources();
  t.mock.timers.enable({ apis: ["setTimeout"] });
  const handle = sse.openSessionStream("session-9", { EventSource: FakeEventSource });
  const first = FakeEventSource.instances[0];
  first.readyState = 2;
  first.onerror?.({ type: "error" }); // schedules a retry
  handle.reconnect(); // explicit reconnect before the timer fires
  t.mock.timers.tick(60000);
  assert.equal(FakeEventSource.instances.length, 2, "no ghost third connection from the stale timer");
  handle.close();
});
