// evilcode web — the mobile shell's behaviors (plan-web.md §9, P7.1/P7.2/P7.5).
//
// Three small pieces the phone needs and the desk ignores:
//
//   keyboard shim    iOS does not resize the layout viewport when its keyboard
//                    opens; only visualViewport shrinks. The overlap is
//                    published as the --kb-overlap custom property so the chat
//                    column and bottom sheets can ride above the keys without
//                    any position: fixed fighting (§9).
//   wake lock        while the open session's turn is running, keep the screen
//                    awake — when the API exists. Plain HTTP is not a secure
//                    context, so navigator.wakeLock is often missing; every
//                    failure degrades silently (P7.5, decision 3).
//   install hint     on a touch device that is not yet running standalone
//                    (display-mode: standalone, or iOS navigator.standalone),
//                    show the Add-to-Home-Screen hint once until dismissed
//                    (P7.1).
//
// The pure helpers at the top are unit-tested; the mount functions wire the
// real DOM and are exercised by the browser verify pass.

const KEYBOARD_MIN_PX = 100;
const PINCH_SCALE_TOLERANCE = 1.05;

/**
 * keyboardOverlapPx returns how many CSS pixels of the layout viewport the
 * keyboard currently covers, or 0 when there is no keyboard. A pinch zoom also
 * shrinks visualViewport.height, so zoomed viewports are ignored — that is not
 * a keyboard and must not shove the composer around — UNLESS an editable
 * element is focused (editing=true), in which case the overlap is the keyboard
 * even on a zoomed page (iOS can zoom when a field takes focus).
 */
export function keyboardOverlapPx(innerHeight, vv, editing = false) {
  if (!vv || !Number.isFinite(innerHeight) || !Number.isFinite(vv.height)) return 0;
  const scale = Number.isFinite(vv.scale) ? vv.scale : 1;
  if (scale > PINCH_SCALE_TOLERANCE && !editing) return 0;
  const offsetTop = Number.isFinite(vv.offsetTop) ? vv.offsetTop : 0;
  const overlap = innerHeight - vv.height - offsetTop;
  if (overlap < KEYBOARD_MIN_PX) return 0;
  return Math.min(Math.round(overlap), Math.round(innerHeight));
}

const INSTALL_HINT_KEY = "evilcode:install-hint-dismissed";

/**
 * shouldShowInstallHint decides whether the Add-to-Home-Screen hint is due:
 * only on touch devices, only when not already standalone, and never after
 * the user dismissed it.
 */
export function shouldShowInstallHint({ standalone, dismissed, touch }) {
  return !standalone && !dismissed && !!touch;
}

export function installHintDismissed(storage) {
  try {
    return storage?.getItem?.(INSTALL_HINT_KEY) === "1";
  } catch {
    return false;
  }
}

export function dismissInstallHint(storage) {
  try {
    storage?.setItem?.(INSTALL_HINT_KEY, "1");
    return true;
  } catch {
    return false;
  }
}

function noop() {}

/**
 * createWakeLock owns the screen wake lock for the app. The desired state is
 * "the open session is running a turn"; the actual lock exists only while the
 * API is available, the page is visible, and the browser lets us hold one.
 * Every rejection is silent: over plain HTTP there is no secure context, and
 * the badge-only notification model does not depend on the lock.
 */
export function createWakeLock({ navigatorRef, documentRef, onStateChange } = {}) {
  let sentinel = null;
  let desired = false;
  let releaseListener = noop;
  // In-flight request generation: desired can flip while a request() promise
  // is still pending (true to false to true). A late resolution from an older
  // generation must be released at once instead of being adopted — otherwise
  // one orphaned sentinel keeps the screen awake for the whole page lifetime.
  let requestGen = 0;
  const notify = () => {
    if (typeof onStateChange === "function") onStateChange(!!sentinel);
  };

  const visible = () => !documentRef || documentRef.visibilityState === "visible";

  const release = () => {
    requestGen++; // anything still in flight belongs to a superseded request
    releaseListener();
    releaseListener = noop;
    sentinel = null;
    notify();
  };

  const acquire = () => {
    if (sentinel || !desired || !visible()) return;
    const lockManager = navigatorRef?.wakeLock;
    if (!lockManager || typeof lockManager.request !== "function") return; // insecure context: silent
    const gen = ++requestGen;
    try {
      lockManager.request("screen").then((sentinelGranted) => {
        if (!desired || gen !== requestGen) {
          // The turn ended (or flapped) while the request was in flight; let
          // the granted lock go at once instead of adopting it.
          try { sentinelGranted.release(); } catch { /* already gone */ }
          return;
        }
        sentinel = sentinelGranted;
        releaseListener = () => {
          try { sentinelGranted.removeEventListener?.("release", onRelease); } catch { /* not supported */ }
        };
        const onRelease = () => {
          // Browsers drop the lock when the screen locks or the tab hides.
          // Re-arm on the next sync/visible if the turn is still running.
          sentinel = null;
          notify();
          if (desired && visible()) acquire();
        };
        try { sentinelGranted.addEventListener?.("release", onRelease); } catch { /* not supported */ }
        notify();
      }).catch(() => { /* denied or unsupported: silent */ });
    } catch {
      /* same */
    }
  };

  return {
    setDesired(active) {
      const next = !!active;
      if (next === desired) return;
      desired = next;
      if (desired) acquire();
      else if (sentinel) {
        const held = sentinel;
        release();
        try { held.release(); } catch { /* already gone */ }
      }
    },
    // sync re-arms the lock after the tab becomes visible again — browsers
    // drop wake locks on hide and do not restore them.
    sync() {
      if (desired && !sentinel) acquire();
      else if (!desired && sentinel) this.setDesired(false);
    },
    get held() { return !!sentinel; },
  };
}

function storageValue(storage) {
  if (storage !== undefined) return storage;
  try { return globalThis.localStorage; } catch { return null; }
}

/**
 * mountMobile wires the behaviors onto the real page and returns the small
 * control surface app.js needs: setRunning(running) drives the wake lock.
 */
export function mountMobile({ storage } = {}) {
  const store = storageValue(storage);

  // ---- P7.1: install hint ----------------------------------------------------

  const hint = document.getElementById("install-hint");
  if (hint) {
    const standalone =
      typeof matchMedia === "function" &&
      (matchMedia("(display-mode: standalone)").matches ||
        matchMedia("(display-mode: minimal-ui)").matches) ||
      globalThis.navigator?.standalone === true;
    const touch =
      (typeof matchMedia === "function" && matchMedia("(hover: none)").matches) ||
      (globalThis.navigator?.maxTouchPoints ?? 0) > 0 ||
      "ontouchstart" in globalThis;
    if (shouldShowInstallHint({
      standalone,
      dismissed: installHintDismissed(store),
      touch,
    })) {
      hint.hidden = false;
      const dismiss = document.getElementById("install-hint-dismiss");
      dismiss?.addEventListener("click", () => {
        dismissInstallHint(store);
        hint.hidden = true;
      });
    }
  }

  // ---- P7.2: the visualViewport keyboard shim --------------------------------

  const root = document.documentElement;
  let overlap = 0;
  let frame = null;

  const apply = () => {
    frame = null;
    const active = document.activeElement;
    const editing = !!active && (active.tagName === "INPUT" || active.tagName === "TEXTAREA" || active.isContentEditable === true);
    const next = keyboardOverlapPx(globalThis.innerHeight, globalThis.visualViewport, editing);
    if (next === overlap) return;
    overlap = next;
    root.style.setProperty("--kb-overlap", `${overlap}px`);
    if (overlap > 0) {
      // The keyboard just covered the bottom of the layout viewport. If the
      // user is typing, keep the transcript pinned to its (now raised) floor.
      const scroll = document.getElementById("transcript-scroll");
      const text = document.getElementById("composer-text");
      if (scroll && document.activeElement === text) scroll.scrollTop = scroll.scrollHeight;
    }
  };

  const schedule = () => {
    if (frame !== null) return;
    if (typeof requestAnimationFrame === "function") {
      frame = requestAnimationFrame(apply);
    } else {
      frame = setTimeout(apply, 33);
    }
  };

  if (globalThis.visualViewport) {
    globalThis.visualViewport.addEventListener("resize", schedule);
    globalThis.visualViewport.addEventListener("scroll", schedule);
  } else {
    globalThis.addEventListener?.("resize", schedule);
  }
  apply();

  // ---- P7.5: wake lock ---------------------------------------------------------

  const wakeLock = createWakeLock({
    navigatorRef: globalThis.navigator,
    documentRef: document,
  });
  document.addEventListener("visibilitychange", () => {
    if (document.visibilityState === "visible") wakeLock.sync();
  });

  return {
    setRunning(running) {
      wakeLock.setDesired(!!running);
    },
    get wakeLockHeld() {
      return wakeLock.held;
    },
  };
}