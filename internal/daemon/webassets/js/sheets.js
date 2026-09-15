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

// openConfirmSheet is the one destructive-action stop (decision 10): a plain
// question sheet whose confirm button carries the caller's danger action.
// Used by the composer's ⚡ urgent interrupt (P6.2).
export function openConfirmSheet({ title, body, confirmLabel, danger, onConfirm }) {
  openSheet({
    title,
    build: (sheetBody, close) => {
      const text = el("p", "sheet-note", body);
      sheetBody.appendChild(text);
      const actions = el("div", "sheet-actions");
      const go = el("button", `btn ${danger ? "btn--danger" : "btn--primary"}`, confirmLabel ?? "Confirm");
      go.type = "button";
      go.addEventListener("click", async () => {
        go.disabled = true;
        try {
          await onConfirm?.();
          close();
        } catch (err) {
          go.disabled = false;
          const existing = sheetBody.querySelector(".callout");
          if (existing) existing.remove();
          sheetBody.insertBefore(calloutFor(err), text);
        }
      });
      actions.appendChild(go);
      sheetBody.appendChild(actions);
    },
  });
}

// openModelsSheet is the model picker (P6.4): GET /api/models grouped by
// provider, a filter box, and — for models that expose levels — a second
// step choosing the reasoning effort before POST /api/sessions/{name}/model.
// Snapshot.ReasoningEfforts marks the current session's valid levels.
export function openModelsSheet({ session, currentModel, currentEffort, efforts, onPicked }) {
  openSheet({
    title: "Switch model",
    build: (body, close) => {
      const filter = el("input");
      filter.type = "search";
      filter.placeholder = "Filter models…";
      filter.setAttribute("aria-label", "Filter models");
      body.appendChild(filter);

      const listBox = el("div", "model-list");
      body.appendChild(listBox);
      const note = el("p", "sheet-note", "Loading the catalogue…");
      body.appendChild(note);

      let entries = [];
      const renderList = () => {
        const q = filter.value.trim().toLowerCase();
        listBox.replaceChildren();
        const visible = entries.filter((m) =>
          !q || `${m.name} ${m.provider ?? ""} ${m.detail ?? ""}`.toLowerCase().includes(q));
        if (!visible.length) {
          listBox.appendChild(el("p", "rail-dim", q ? "no models match" : "the catalogue is empty"));
          return;
        }
        let lastProvider = null;
        for (const m of visible) {
          const provider = m.provider || "(unknown)";
          if (provider !== lastProvider) {
            listBox.appendChild(el("p", "eyebrow", provider));
            lastProvider = provider;
          }
          const row = el("button", "model-row");
          row.type = "button";
          const main = el("span", "model-main");
          main.appendChild(el("span", "model-name", m.name));
          if (m.detail) main.appendChild(el("span", "palette-hint", m.detail));
          if (m.context_window) {
            main.appendChild(el("span", "palette-hint", `${(m.context_window / 1000).toFixed(0)}k ctx`));
          }
          row.appendChild(main);
          if (m.favorite) row.appendChild(el("span", "chip chip--accent", "favorite"));
          if (`${m.name}@${m.provider ?? ""}` === currentModel || m.name === currentModel) {
            row.appendChild(el("span", "chip chip--running", "current"));
          }
          const levels = (m.reasoning_efforts ?? []).filter((l) => l && l !== "none");
          const levelsForThis = efforts ?? levels;
          // The wire ref must name the provider: a bare model name resolves
          // against the config's first provider, which may not be the one that
          // advertised this entry.
          const ref = m.provider ? `${m.name}@${m.provider}` : m.name;
          row.addEventListener("click", async () => {
            row.disabled = true;
            try {
              if (levelsForThis && levelsForThis.length) {
                const chosen = await pickEffort(levelsForThis, currentEffort);
                if (chosen === null) return;
                await postJSON(`/api/sessions/${encodeURIComponent(session)}/model`, {
                  model: ref, effort: chosen || undefined,
                });
              } else {
                await postJSON(`/api/sessions/${encodeURIComponent(session)}/model`, { model: ref });
              }
              close();
              onPicked?.(m);
            } catch (err) {
              const existing = body.querySelector(".callout");
              if (existing) existing.remove();
              body.insertBefore(calloutFor(err), listBox);
            } finally {
              row.disabled = false;
            }
          });
          listBox.appendChild(row);
        }
      };
      filter.addEventListener("input", renderList);

      const pickEffort = (levels, active) => new Promise((resolve) => {
        const step = el("div");
        step.appendChild(el("p", "sheet-note", `Reasoning effort for ${currentModel ? "this model" : "the model"}:`));
        const row = el("div", "ask-options");
        for (const level of levels) {
          const btn = el("button", "btn ask-option", level);
          btn.type = "button";
          if (level === active) btn.classList.add("is-active");
          btn.addEventListener("click", () => {
            step.replaceWith(el("div"));
            resolve(level);
          });
          row.appendChild(btn);
        }
        const skip = el("button", "btn", "keep current");
        skip.type = "button";
        skip.addEventListener("click", () => {
          step.replaceWith(el("div"));
          resolve("");
        });
        row.appendChild(skip);
        step.appendChild(row);
        body.appendChild(step);
        skip.focus();
      });

      getJSON("/api/models").then((models) => {
        entries = Array.isArray(models) ? models : [];
        note.remove();
        renderList();
      }).catch((err) => {
        note.textContent = String(err.message ?? err);
      });
    },
  });
}

// openSpawnSheet is the spawn dialog (P6.6). The worker inherits the spawner's
// workspace — SpawnFor takes no cwd — so the picker shows it read-only and
// collects the task, the referenced files, and the optional result schema.
export function openSpawnSheet({ session, cwd, onSpawned }) {
  openSheet({
    title: "Spawn a worker",
    build: (body, close) => {
      const form = el("form");
      const workspace = el("p", "sheet-note", `Workspace (inherited): ${cwd || "the spawner's"}`);
      form.appendChild(workspace);

      const task = el("textarea");
      task.rows = 4;
      task.required = true;
      task.placeholder = "What should the worker do?";
      task.setAttribute("aria-label", "Worker task");
      const taskLabel = el("label", undefined, "Task");
      taskLabel.appendChild(task);
      form.appendChild(taskLabel);

      const files = el("input");
      files.type = "text";
      files.placeholder = "files it should start from (comma-separated)";
      files.autocomplete = "off";
      files.setAttribute("aria-label", "Files (optional)");
      const filesLabel = el("label", undefined, "Files");
      filesLabel.appendChild(files);
      form.appendChild(filesLabel);

      const schema = el("textarea");
      schema.rows = 4;
      schema.placeholder = '{\n  "type": "object",\n  "properties": { "verdict": { "type": "string" } }\n}';
      schema.setAttribute("aria-label", "Result schema (optional JSON)");
      schema.spellcheck = false;
      const schemaLabel = el("label", undefined, "Result schema (optional JSON)");
      schemaLabel.appendChild(schema);
      form.appendChild(schemaLabel);

      const submit = el("button", "btn btn--primary", "Spawn worker");
      submit.type = "submit";
      const actions = el("div", "sheet-actions");
      actions.appendChild(submit);
      form.appendChild(actions);
      body.appendChild(form);

      form.addEventListener("submit", async (e) => {
        e.preventDefault();
        let parsed;
        const raw = schema.value.trim();
        if (raw) {
          try {
            parsed = JSON.parse(raw);
          } catch (err) {
            const existing = body.querySelector(".callout");
            if (existing) existing.remove();
            body.insertBefore(calloutFor(new Error(`schema is not valid JSON: ${err.message}`)), form);
            return;
          }
        }
        submit.disabled = true;
        submit.textContent = "Spawning…";
        try {
          const info = await postJSON("/api/spawn", {
            session,
            task: task.value,
            files: files.value.split(",").map((f) => f.trim()).filter(Boolean),
            schema: parsed ?? undefined,
          });
          close();
          onSpawned?.(info);
        } catch (err) {
          submit.disabled = false;
          submit.textContent = "Spawn worker";
          const existing = body.querySelector(".callout");
          if (existing) existing.remove();
          body.insertBefore(calloutFor(err), form);
        }
      });
    },
  });
}

// calloutFor renders the uniform error shape's words inside the sheet, so a
// rejected create is read where the form is, not in a console.
function calloutFor(err) {
  const box = el("div", "callout callout--error");
  box.appendChild(el("div", "callout-title", "The daemon refused"));
  box.appendChild(el("span", undefined, String(err.message ?? err)));
  return box;
}

// openNewSessionSheet is the ＋ New session action (§9): a blank name creates a
// session, while an existing name reopens one. The workspace menu comes from
// GET /api/workspaces (server-side allowlist, §4), and success opens its chat.
export function openNewSessionSheet({ onCreated }) {
  openSheet({
    title: "New session",
    build: (body, close) => {
      const form = el("form");
      const name = el("input");
      name.type = "text";
      name.name = "name";
      name.placeholder = "leave blank to create";
      name.autocomplete = "off";
      name.setAttribute("aria-label", "Existing session name (optional)");
      const nameLabel = el("label", undefined, "Existing session");
      nameLabel.appendChild(name);
      form.appendChild(nameLabel);

      const workspace = el("select");
      workspace.name = "cwd";
      workspace.setAttribute("aria-label", "Workspace");
      const wsLabel = el("label", undefined, "Workspace");
      wsLabel.appendChild(workspace);
      form.appendChild(wsLabel);

      const note = el("p", "sheet-note", "Leave blank to create a new session; enter an existing name to reopen it.");
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