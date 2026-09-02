// evilcode web — the sheet primitive (plan-web.md §9, P4.3). A sheet is the
// one dialog surface in the app: bottom-anchored on the phone, centered on the
// desktop. It traps focus, closes on Escape or scrim tap, and restores focus
// to whoever opened it. Sheets carry the flows that post to the command
// surface; the new-session sheet here is the sidebar button's real action.

import { getJSON, postJSON } from "./api.js";

let openSheetState = null;

function el(tag, className, text) {
  const node = document.createElement(tag);
  if (className) node.className = className;
  if (text !== undefined) node.textContent = text;
  return node;
}

// openSheet mounts a dialog into #sheet-root and returns close(). `build`
// receives the sheet body element and a close() it may hand to its own
// buttons.
export function openSheet({ title, build }) {
  const root = document.getElementById("sheet-root");
  const scrim = el("div", "sheet-scrim");
  const sheet = el("div", "sheet");
  sheet.setAttribute("role", "dialog");
  sheet.setAttribute("aria-modal", "true");
  sheet.setAttribute("aria-label", title);
  sheet.appendChild(el("h2", "sheet-title", title));

  const body = el("div", "sheet-body");
  sheet.appendChild(body);
  const actions = el("div", "sheet-actions");
  const closeBtn = el("button", "btn btn--small", "Close");
  closeBtn.type = "button";
  actions.appendChild(closeBtn);
  sheet.appendChild(actions);

  let closed = false;
  const close = () => {
    if (closed) return;
    closed = true;
    document.removeEventListener("keydown", onKey);
    root.classList.remove("is-open");
    root.replaceChildren();
    openSheetState = null;
    if (sheetState.returnFocus instanceof HTMLElement) sheetState.returnFocus.focus();
  };
  const sheetState = { returnFocus: document.activeElement };
  const onKey = (e) => {
    if (e.key === "Escape") close();
  };

  scrim.addEventListener("click", close);
  closeBtn.addEventListener("click", close);
  document.addEventListener("keydown", onKey);

  root.classList.add("is-open");
  root.append(scrim, sheet);
  build(body, close);
  const first = body.querySelector("input, select, textarea, button");
  if (first) first.focus();
  openSheetState = { close, returnFocus: sheetState.returnFocus };
  return close;
}

export function closeSheet() {
  openSheetState?.close?.();
}

// calloutFor renders the uniform error shape's words inside the sheet, so a
// rejected create is read where the form is, not in a console.
function calloutFor(err) {
  const box = el("div", "callout callout--error");
  box.appendChild(el("div", "callout-title", "The daemon refused"));
  box.appendChild(el("span", undefined, String(err.message ?? err)));
  return box;
}

// openNewSessionSheet is the ＋ New session action (§9): an optional name, the
// workspace menu from GET /api/workspaces (server-side allowlist, §4), and
// POST /api/sessions. Success navigates to the new session's chat.
export function openNewSessionSheet({ onCreated }) {
  openSheet({
    title: "New session",
    build: (body, close) => {
      const form = el("form");
      const name = el("input");
      name.type = "text";
      name.name = "name";
      name.placeholder = "chosen for you";
      name.autocomplete = "off";
      name.setAttribute("aria-label", "Session name (optional)");
      const nameLabel = el("label", undefined, "Name");
      nameLabel.appendChild(name);
      form.appendChild(nameLabel);

      const workspace = el("select");
      workspace.name = "cwd";
      workspace.setAttribute("aria-label", "Workspace");
      const wsLabel = el("label", undefined, "Workspace");
      wsLabel.appendChild(workspace);
      form.appendChild(wsLabel);

      const note = el("p", "sheet-note", "The daemon only creates sessions in the workspaces it is configured for.");
      form.appendChild(note);

      const submit = el("button", "btn btn--primary", "Create session");
      submit.type = "submit";
      const actions = el("div", "sheet-actions");
      actions.appendChild(submit);
      form.appendChild(actions);
      body.appendChild(form);

      getJSON("/api/workspaces").then((ws) => {
        const options = [ws.cwd, ...(ws.workspaces ?? [])].filter((v, i, a) => v && a.indexOf(v) === i);
        for (const dir of options) {
          const opt = el("option", undefined, dir);
          opt.value = dir;
          workspace.appendChild(opt);
        }
        if (!options.length) {
          const opt = el("option", undefined, "(the daemon lists no workspaces)");
          opt.value = "";
          workspace.appendChild(opt);
        }
      });

      form.addEventListener("submit", async (e) => {
        e.preventDefault();
        submit.disabled = true;
        submit.textContent = "Creating…";
        try {
          const info = await postJSON("/api/sessions", {
            name: name.value.trim() || undefined,
            cwd: workspace.value || undefined,
          });
          close();
          onCreated?.(info);
        } catch (err) {
          submit.disabled = false;
          submit.textContent = "Create session";
          const existing = body.querySelector(".callout");
          if (existing) existing.remove();
          body.insertBefore(calloutFor(err), form);
        }
      });
    },
  });
}