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
