// evilcode web — the chat pane and the right rail (plan-web.md §9, P4.2).
// Phase 4 renders the snapshot or the stored view as plain message cards; the
// live mirror, renderers, and streaming polish are Phase 5's engine. This view
// never boots an agent: a stored session stays read-only until Reopen posts
// POST /api/sessions (§4), which is the only hydration path.
//
// Two shapes arrive here. A live session answers GET /api/sessions/{name} with
// a Snapshot (running, pending[], background, skills, context_window at the
// top). A stored session answers with {session: SessionInfo, messages}. The
// fields the Snapshot does not carry (worker, stale, crashed, task) come from
// the roster row the poll loop already holds, so nothing needs a second API.

import { postJSON } from "../api.js";
import { renderTranscript as renderTranscriptCards } from "./transcript.js";

function el(tag, className, text) {
  const node = document.createElement(tag);
  if (className) node.className = className;
  if (text !== undefined) node.textContent = text;
  return node;
}

function chip(label, variant) {
  return el("span", `chip${variant ? ` chip--${variant}` : ""}`, label);
}

// chipsFor merges the wire shapes into one header vocabulary. `row` may be
// undefined between polls; every use tolerates that.
export function chipsFor(data, row) {
  const info = data.session ?? {};
  const chips = [];
  const pending = Array.isArray(data.pending) ? data.pending.length : info.pending ?? 0;
  if (row?.crashed || info.crashed) chips.push({ label: "crashed", variant: "crashed" });
  if (pending > 0) chips.push({ label: `${pending} pending`, variant: "accent" });
  if (data.running || row?.running) chips.push({ label: "running", variant: "running" });
  if (row?.stale || info.stale) chips.push({ label: "stale", variant: "stale" });
  if (row?.worker || info.worker) chips.push({ label: "worker", variant: "worker" });
  if (info.live === false || row?.live === false) chips.push({ label: "stored · read-only", variant: "stored" });
  return chips;
}

// renderChatHead fills the pane header.
export function renderChatHead({ title, chips, sub }) {
  document.getElementById("chat-title").textContent = title;
  const chipBox = document.getElementById("chat-chips");
  chipBox.replaceChildren();
  for (const c of chips) chipBox.appendChild(chip(c.label, c.variant));
  document.getElementById("chat-sub").textContent = sub;
}

// Keep the Phase 4 chat API stable while the renderer owns message semantics.
// The app already calls this seam for both live snapshots and stored history.
export function renderTranscript(container, messages, extra) {
  return renderTranscriptCards(container, messages, extra);
}

export function storedBanner(name) {
  const callout = el("div", "callout callout--warning");
  const title = el("div", "callout-title", "Stored session");
  const text = el("span", undefined, "This is a durable, read-only view. ");
  const btn = el("button", "btn btn--small", "Reopen to continue");
  btn.type = "button";
  btn.addEventListener("click", async () => {
    btn.disabled = true;
    btn.textContent = "Reopening…";
    try {
      const info = await postJSON("/api/sessions", { name });
      window.dispatchEvent(new CustomEvent("evilcode:session-opened", { detail: info }));
    } catch (err) {
      btn.disabled = false;
      btn.textContent = "Reopen to continue";
      const fail = el("div", "callout callout--error", String(err.message ?? err));
      callout.after(fail);
    }
  });
  callout.append(title, text, btn);
  return callout;
}

// renderRail draws the ≥1280px right rail. A stored view has none of this
// state; it says so instead of inventing numbers.
export function renderRail(container, data, row) {
  container.replaceChildren();
  const stored = data.session ? data.session.live === false : row?.live === false;
  if (stored) {
    const callout = el("div", "callout callout--info");
    callout.append(el("div", "callout-title", "Read-only"), el("span", undefined, "Reopen the session to see live state."));
    container.appendChild(callout);
    return;
  }

  // Context window (§9). ctx_used arrives with usage events in Phase 5; until
  // then the meter is empty and says so rather than inventing a number.
  const ctx = el("section", "card");
  ctx.appendChild(el("div", "card-head", "Context"));
  const ctxBody = el("div", "card-body");
  const meter = el("div", "meter");
  meter.setAttribute("role", "progressbar");
  meter.appendChild(el("div", "meter-fill"));
  ctxBody.appendChild(meter);
  ctxBody.appendChild(el("div", "meter-readout", `— / ${data.context_window ?? "?"} tokens · no usage yet`));
  ctx.appendChild(ctxBody);
  container.appendChild(ctx);

  const list = (heading, rows, emptyText) => {
    const card = el("section", "card");
    card.appendChild(el("div", "card-head", heading));
    const body = el("div", "card-body");
    if (!rows.length) {
      body.appendChild(el("div", "rail-dim", emptyText));
    } else {
      const ul = el("ul", "rail-list");
      for (const r of rows) ul.appendChild(r);
      body.appendChild(ul);
    }
    card.appendChild(body);
    return card;
  };

  const mcpRows = (data.mcp ?? []).map((m) => {
    const li = el("li");
    li.appendChild(el("span", `dot ${m.connected ? "dot--idle" : "dot--crashed"}`));
    li.appendChild(el("span", undefined, `${m.name} · ${m.tools} tool${m.tools === 1 ? "" : "s"}`));
    return li;
  });
  container.appendChild(list("MCP", mcpRows, "no MCP servers"));

  const bgRows = (data.background ?? []).map((b) => {
    const li = el("li");
    li.appendChild(el("span", `dot ${b.done ? "dot--idle" : "dot--running"}`));
    li.appendChild(el("span", undefined, `#${b.id} ${b.label}`));
    return li;
  });
  container.appendChild(list("Background", bgRows, "no background tasks"));

  const skillRows = (data.skills ?? []).map((s) => {
    const li = el("li");
    li.appendChild(chip(s, "accent"));
    return li;
  });
  container.appendChild(list("Skills", skillRows, "no skills loaded"));
}