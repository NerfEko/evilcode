// evilcode web — the roster (plan-web.md §9, P4.2/P4.4). The roster renders
// whatever GET /api/sessions returns and encodes state the way the TUI does:
// one dot in the state's role color, a worker chip plus task line, and a
// pending-ask badge for anything waiting on the user. It is a poll client,
// never a stream client (§5: one stream per tab belongs to the transcript).

// stateOf collapses a SessionInfo row into the one state its dot shows. The
// order is the user's reading order: something that needs me beats something
// that is merely busy; a crash beats everything.
export function stateOf(info) {
  if (info.crashed) return { cls: "dot--crashed", label: "crashed" };
  if (info.pending > 0) return { cls: "dot--pending", label: `${info.pending} pending ask${info.pending === 1 ? "" : "s"}` };
  if (info.running) return { cls: "dot--running", label: "running" };
  if (info.stale) return { cls: "dot--stale", label: "stale worker" };
  if (info.live) return { cls: "dot--idle", label: "idle" };
  return { cls: "dot--stored", label: "stored" };
}

function el(tag, className, text) {
  const node = document.createElement(tag);
  if (className) node.className = className;
  if (text !== undefined) node.textContent = text;
  return node;
}

function chipRow(info) {
  const row = el("div", "session-sub");
  const parts = [];
  if (info.model) parts.push(el("span", "chip chip--model", info.model));
  if (info.worker) parts.push(el("span", "chip chip--worker", "worker"));
  if (info.stale) parts.push(el("span", "chip chip--stale", "stale"));
  if (info.crashed) parts.push(el("span", "chip chip--crashed", "crashed"));
  if (!info.live) parts.push(el("span", "chip chip--stored", "stored"));
  if (info.messages) parts.push(document.createTextNode(`${info.messages} msg`));
  parts.forEach((p) => row.appendChild(p));
  return row;
}

// renderRoster redraws the whole list; a roster is small (a personal fleet),
// and keyed patching would buy nothing at this size.
export function renderRoster(container, rows, { active } = {}) {
  container.replaceChildren();
  if (!rows.length) {
    const empty = el("div", "empty");
    empty.appendChild(el("p", "empty-title", "No sessions"));
    empty.appendChild(el("p", undefined, "Start one with ＋ New session."));
    container.appendChild(empty);
    return;
  }
  for (const info of rows) {
    const state = stateOf(info);
    const row = el("button", "session-row");
    row.type = "button";
    row.dataset.name = info.name;
    if (info.name === active) {
      row.classList.add("is-active");
      row.setAttribute("aria-current", "page");
    }
    row.title = state.label;

    const dot = el("span", `dot ${state.cls}`);
    dot.setAttribute("role", "img");
    dot.setAttribute("aria-label", state.label);
    row.appendChild(dot);

    const main = el("span", "session-main");
    main.appendChild(el("span", "session-name", info.name));
    main.appendChild(chipRow(info));
    row.appendChild(main);

    if (info.pending > 0) {
      const badge = el("span", "badge", String(info.pending));
      badge.title = state.label;
      row.appendChild(badge);
    } else {
      row.appendChild(el("span"));
    }

    if (info.task) {
      row.appendChild(el("span", "session-task", `▸ ${info.task}`));
    }
    container.appendChild(row);
  }
}