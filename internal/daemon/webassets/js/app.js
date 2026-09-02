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

import { getJSON } from "./api.js";
import { renderRoster } from "./views/roster.js";
import { chipsFor, renderChatHead, renderRail, renderTranscript, storedBanner } from "./views/chat.js";
import { openNewSessionSheet } from "./sheets.js";

const POLL_INTERVAL_MS = 2000;
const LAST_OPEN_KEY = "evilcode:last-open";

const rosterEl = document.getElementById("roster");
const statusEl = document.getElementById("statusline");

let pollTimer = null;
let route = { view: "home" };
let renderGeneration = 0;
let rosterRows = []; // the poll's cache; chat headers borrow worker/task state from it

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
  const chatEl = document.getElementById("chat");

  if (route.view === "home") {
    localStorage.removeItem(LAST_OPEN_KEY);
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
      renderTranscript(transcript, data.messages, { before: storedBanner(info.name) });
    } else {
      renderTranscript(transcript, data.messages);
    }
    renderRail(document.getElementById("rail-body"), data, row);
    document.getElementById("transcript-scroll").scrollTop = 0;
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