import test from "node:test";
import assert from "node:assert/strict";
import { readFile } from "node:fs/promises";

const source = await readFile(new URL("./mobile.js", import.meta.url), "utf8");
const mobile = await import(`data:text/javascript;charset=utf-8,${encodeURIComponent(source)}`);

function fakeSentinel() {
  const sentinel = {
    released: false,
    listeners: new Map(),
    release: async () => { sentinel.released = true; },
    addEventListener(kind, cb) { sentinel.listeners.set(kind, cb); },
    removeEventListener(kind, cb) { if (sentinel.listeners.get(kind) === cb) sentinel.listeners.delete(kind); },
    emitRelease() { sentinel.listeners.get("release")?.(); },
  };
  return sentinel;
}

function fakeNavigator({ supported = true, fail = false } = {}) {
  return {
    wakeLock: supported ? {
      request: async () => {
        if (fail) throw new Error("denied");
        return fakeSentinel();
      },
    } : undefined,
  };
}

function fakeDocument(state = "visible") {
  return { visibilityState: state, addEventListener() {}, removeEventListener() {} };
}

test("keyboardOverlapPx measures the keyboard and ignores pinch zoom", () => {
  // A keyboard: the layout viewport is 800 tall, the visual viewport lost 240px.
  assert.equal(mobile.keyboardOverlapPx(800, { height: 560, offsetTop: 0, scale: 1 }), 240);
  // A panned viewport subtracts offsetTop too.
  assert.equal(mobile.keyboardOverlapPx(800, { height: 620, offsetTop: 40, scale: 1 }), 140);
  // Small overlaps (rubber-banding, toolbar shifts) are not a keyboard.
  assert.equal(mobile.keyboardOverlapPx(800, { height: 770, offsetTop: 0, scale: 1 }), 0);
  // A pinch zoom shrinks the visual viewport as well; that is not a keyboard.
  assert.equal(mobile.keyboardOverlapPx(800, { height: 300, offsetTop: 0, scale: 2 }), 0);
  // …unless an editable element is focused: then the overlap IS the keyboard
  // (iOS zooms onto focused fields), and the composer must still ride up.
  assert.equal(mobile.keyboardOverlapPx(800, { height: 300, offsetTop: 0, scale: 2 }, true), 500);
  // Degenerate inputs degrade to "no keyboard".
  assert.equal(mobile.keyboardOverlapPx(800, null), 0);
  assert.equal(mobile.keyboardOverlapPx(undefined, { height: 100, scale: 1 }), 0);
});

test("shouldShowInstallHint only fires on an undismissed, non-standalone touch device", () => {
  assert.equal(mobile.shouldShowInstallHint({ standalone: false, dismissed: false, touch: true }), true);
  assert.equal(mobile.shouldShowInstallHint({ standalone: true, dismissed: false, touch: true }), false);
  assert.equal(mobile.shouldShowInstallHint({ standalone: false, dismissed: true, touch: true }), false);
  assert.equal(mobile.shouldShowInstallHint({ standalone: false, dismissed: false, touch: false }), false);
});

test("install hint dismissal persists and survives malformed storage", () => {
  const store = {
    data: new Map(),
    getItem(k) { return store.data.has(k) ? store.data.get(k) : null; },
    setItem(k, v) { store.data.set(k, String(v)); },
  };
  assert.equal(mobile.installHintDismissed(store), false);
  assert.equal(mobile.dismissInstallHint(store), true);
  assert.equal(mobile.installHintDismissed(store), true);
  const broken = { getItem() { throw new Error("no"); }, setItem() { throw new Error("no"); } };
  assert.equal(mobile.installHintDismissed(broken), false);
  assert.equal(mobile.dismissInstallHint(broken), false);
});

test("wake lock is acquired while running and released when the turn ends", async () => {
  const nav = fakeNavigator();
  const lock = mobile.createWakeLock({ navigatorRef: nav, documentRef: fakeDocument() });
  const states = [];
  const wake = mobile.createWakeLock({
    navigatorRef: nav,
    documentRef: fakeDocument(),
    onStateChange: (held) => states.push(held),
  });
  void lock;
  wake.setDesired(true);
  await new Promise((r) => setImmediate(r));
  assert.equal(wake.held, true);
  assert.deepEqual(states, [true]);
  wake.setDesired(false);
  await new Promise((r) => setImmediate(r));
  assert.equal(wake.held, false);
  assert.deepEqual(states, [true, false]);
});

test("wake lock degrades silently when unsupported or denied", async () => {
  const unsupported = mobile.createWakeLock({
    navigatorRef: fakeNavigator({ supported: false }),
    documentRef: fakeDocument(),
  });
  unsupported.setDesired(true);
  await new Promise((r) => setImmediate(r));
  assert.equal(unsupported.held, false);

  const denied = mobile.createWakeLock({
    navigatorRef: fakeNavigator({ fail: true }),
    documentRef: fakeDocument(),
  });
  denied.setDesired(true);
  await new Promise((r) => setImmediate(r));
  assert.equal(denied.held, false);
});

test("wake lock re-arms after a browser-initiated release while still running", async () => {
  let granted = null;
  const nav = {
    wakeLock: {
      request: async () => {
        granted = fakeSentinel();
        return granted;
      },
    },
  };
  const doc = fakeDocument();
  const lock = mobile.createWakeLock({ navigatorRef: nav, documentRef: doc });
  lock.setDesired(true);
  await new Promise((r) => setImmediate(r));
  assert.equal(lock.held, true);
  // The phone locks its screen: the browser drops the sentinel.
  granted.emitRelease = () => granted.listeners.get("release")?.();
  granted.emitRelease();
  assert.equal(lock.held, false);
  await new Promise((r) => setImmediate(r));
  // Still running and visible → the module re-arms by itself.
  assert.equal(lock.held, true);
  assert.equal(granted.released, false);
});

test("wake lock waits for visibility and syncs on return", async () => {
  let granted = null;
  const nav = {
    wakeLock: { request: async () => { granted = fakeSentinel(); return granted; } },
  };
  const doc = fakeDocument("hidden");
  const lock = mobile.createWakeLock({ navigatorRef: nav, documentRef: doc });
  lock.setDesired(true);
  await new Promise((r) => setImmediate(r));
  assert.equal(lock.held, false); // hidden: nothing requested yet
  doc.visibilityState = "visible";
  lock.sync();
  await new Promise((r) => setImmediate(r));
  assert.equal(lock.held, true);
});

test("a wake-lock request flapping true→false→true in flight never orphans a sentinel", async () => {
  const pending = [];
  const nav = {
    wakeLock: {
      request: () => new Promise((resolve) => pending.push(resolve)),
    },
  };
  const lock = mobile.createWakeLock({ navigatorRef: nav, documentRef: fakeDocument() });
  lock.setDesired(true);  // request #1 in flight
  lock.setDesired(false); // turn ended before it resolved
  lock.setDesired(true);  // request #2 in flight
  assert.equal(pending.length, 2, "the re-armed desire issues a fresh request");
  const sents = [];
  for (const resolve of pending) {
    const sentinel = {
      released: false,
      release: async () => { sentinel.released = true; },
      addEventListener() {},
      removeEventListener() {},
    };
    sents.push(sentinel);
    resolve(sentinel);
  }
  await new Promise((r) => setImmediate(r));
  await new Promise((r) => setImmediate(r));
  assert.equal(sents[0].released, true, "the superseded request's sentinel was released, not adopted");
  assert.equal(sents[1].released, false, "the current generation's sentinel is held");
  assert.equal(lock.held, true);
  // Releasing: setDesired(false) lets the held one go.
  lock.setDesired(false);
  await new Promise((r) => setImmediate(r));
  assert.equal(sents[1].released, true);
  assert.equal(lock.held, false);
});

test("mount wiring: a visualViewport resize publishes --kb-overlap and re-pins the transcript", async () => {
  // Drive the real mountMobile() against stubbed browser globals: this is the
  // exact wiring (event listeners → rAF-coalesced apply → CSS var + re-pin),
  // observed directly rather than through a browser.
  const listeners = new Map();
  const fire = (kind) => { for (const cb of listeners.get(kind) ?? []) cb({}); };
  let rafCb = null;
  const vv = {
    height: 844, offsetTop: 0, scale: 1,
    addEventListener(kind, cb) { listeners.set(kind, [...(listeners.get(kind) ?? []), cb]); },
    removeEventListener() {},
  };
  const inline = new Map();
  const docEl = {
    style: {
      setProperty(k, v) { inline.set(k, v); },
      getPropertyValue(k) { return inline.get(k) ?? ""; },
    },
  };
  const composerText = {};
  const scroll = { scrollTop: 0, scrollHeight: 1200 };
  const stubs = {
    visualViewport: vv, innerHeight: 844,
    matchMedia: () => ({ matches: false }),
    requestAnimationFrame: (cb) => { rafCb = cb; return 1; },
    document: {
      visibilityState: "visible",
      activeElement: null,
      documentElement: docEl,
      getElementById: (id) => (id === "transcript-scroll" ? scroll : id === "composer-text" ? composerText : null),
      addEventListener() {},
    },
    navigator: { maxTouchPoints: 0 },
  };
  for (const [k, v] of Object.entries(stubs)) {
    Object.defineProperty(globalThis, k, { value: v, configurable: true });
  }
  try {
    const m = mobile.mountMobile();
    // Resting: nothing to publish — the stylesheet's 0px default stands.
    assert.equal(inline.get("--kb-overlap"), undefined);

    // A keyboard opens (iOS: layout viewport stays, visual viewport shrinks).
    vv.height = 544;
    fire("resize");
    assert.equal(typeof rafCb, "function", "resize schedules the rAF-coalesced apply");
    rafCb();
    assert.equal(inline.get("--kb-overlap"), "300px");

    // The user is typing: the transcript re-pins to its raised floor.
    stubs.document.activeElement = composerText;
    vv.height = 300;
    fire("resize");
    rafCb();
    assert.equal(inline.get("--kb-overlap"), "544px");
    assert.equal(scroll.scrollTop, 1200);

    // Keyboard closed: back to 0.
    stubs.document.activeElement = null;
    vv.scale = 1;
    vv.height = 844;
    fire("resize");
    rafCb();
    assert.equal(inline.get("--kb-overlap"), "0px");

    // A pinch zoom alone is not a keyboard: no var, no pin.
    vv.scale = 2;
    vv.height = 300;
    fire("resize");
    rafCb();
    assert.equal(inline.get("--kb-overlap"), "0px");
    assert.equal(scroll.scrollTop, 1200, "no re-pin for a zoom");
  } finally {
    for (const k of Object.keys(stubs)) delete globalThis[k];
  }
});