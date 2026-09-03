// evilcode web — the composer (plan-web.md §9, P6.1/P6.2/P6.5/P6.7). One form
// owns the whole interaction state machine:
//
//   idle, empty    → Send posts POST /api/sessions/{name}/input
//   running        → Send posts POST /api/sessions/{name}/message (the poke:
//                    the text queues for the agent's next safe point),
//                    Stop posts an empty interrupt (cancel the turn), and
//                    ⚡ urgent offers the typed text as an URGENT interrupt
//                    behind an explicit confirm.
//   "/command"     → the slash palette resolves server commands; arg/secret
//                    fields appear for the commands that take them and submit
//                    posts POST /api/sessions/{name}/command.
//
// Images attach by file picker or paste, each pre-checked against the 6 MiB
// limit before any encoding work, and sent as base64 on the input call.

import { postJSON } from "../api.js";

export const IMAGE_LIMIT_BYTES = 6 * 1024 * 1024;

// The server command vocabulary (internal/daemon/command.go Session.Command).
// The daemon stays the authority — this list only powers the palette and the
// arg/secret prompts; the daemon re-validates everything.
export const COMMANDS = [
  { kind: "poke", arg: "on | off | status", hint: "auto-poke control" },
  { kind: "advisor", arg: "on | off", hint: "ambient reviewer control" },
  { kind: "memory", arg: "on | off", hint: "memory retrieval control" },
  { kind: "connect", arg: "brave", secret: true, hint: "save a Brave Search API key" },
  { kind: "credential", arg: "service", secret: true, hint: "store a service credential" },
  { kind: "skills", arg: "reload", hint: "reload skills from disk" },
  { kind: "lsp", hint: "language-server status" },
  { kind: "overnight", arg: "…", hint: "overnight run control" },
  { kind: "save", hint: "pin this session" },
  { kind: "unsave", hint: "unpin this session" },
  { kind: "rename", arg: "<new-name>", hint: "rename the session" },
  { kind: "fork", arg: "<new-name>", hint: "fork the durable log" },
  { kind: "checkpoint", arg: "[label]", hint: "mark a rewind point" },
  { kind: "rewind", arg: "[label]", hint: "rewind to a checkpoint" },
  { kind: "compact", hint: "compact the conversation now" },
];

// filterCommands returns the palette rows for a query. "" (just "/") lists
// everything; the match is prefix-first, then substring.
export function filterCommands(query) {
  const q = String(query || "").trim().toLowerCase();
  if (!q) return COMMANDS.slice();
  const starts = COMMANDS.filter((c) => c.kind.startsWith(q));
  if (starts.length) return starts;
  return COMMANDS.filter((c) => c.kind.includes(q) || c.hint.toLowerCase().includes(q));
}

export function commandByKind(kind) {
  const k = String(kind || "").replace(/^\//, "").trim().toLowerCase();
  return COMMANDS.find((c) => c.kind === k) ?? null;
}

// checkImage returns null when the file may attach, else the refusal.
export function checkImage(file, alreadyAttached = []) {
  if (!file || typeof file.size !== "number") return "that file has no readable size";
  if (!String(file.type || "").startsWith("image/")) return "only images can be attached";
  if (file.size > IMAGE_LIMIT_BYTES) {
    return `${file.name || "image"} is ${(file.size / (1024 * 1024)).toFixed(1)} MiB — the limit is 6 MiB`;
  }
  const total = alreadyAttached.reduce((sum, a) => sum + (a.size || 0), 0) + file.size;
  if (total > IMAGE_LIMIT_BYTES) {
    return `attachments would total ${(total / (1024 * 1024)).toFixed(1)} MiB — the limit is 6 MiB`;
  }
  return null;
}

// fileToBase64 reads an image and returns the bare base64 payload (no data:
// prefix — the daemon decodes StdEncoding/RawStdEncoding directly).
export function fileToBase64(file) {
  return new Promise((resolve, reject) => {
    const reader = new FileReader();
    reader.onerror = () => reject(new Error(`could not read ${file.name || "file"}`));
    reader.onload = () => {
      const result = String(reader.result ?? "");
      const comma = result.indexOf(",");
      resolve(comma >= 0 ? result.slice(comma + 1) : result);
    };
    reader.readAsDataURL(file);
  });
}

function el(tag, className, text) {
  const node = document.createElement(tag);
  if (className) node.className = className;
  if (text !== undefined) node.textContent = text;
  return node;
}

// mountComposer wires the static markup into a working composer. It owns no
// state across sessions: bind(name) retargets it, and setRunning(refreshes
// the send/stop/poke affordances. `handlers`:
//   onBusyChange(running)   — called whenever the mode flips
export function mountComposer(handlers = {}) {
  const form = document.getElementById("composer");
  const text = document.getElementById("composer-text");
  const send = document.getElementById("composer-send");
  const stop = document.getElementById("composer-stop");
  const urgent = document.getElementById("composer-urgent");
  const attach = document.getElementById("composer-attach");
  const fileInput = document.getElementById("composer-file");
  const attachStrip = document.getElementById("composer-attachments");
  const palette = document.getElementById("composer-palette");
  const secretField = document.getElementById("composer-secret");

  let name = "";
  let running = false;
  let sending = false;
  let attachments = [];
  let commandMode = null; // {kind, spec}

  const autogrow = () => {
    text.style.height = "auto";
    text.style.height = `${Math.min(text.scrollHeight, 160)}px`;
  };

  const renderAttachments = () => {
    attachStrip.replaceChildren();
    if (!attachments.length) {
      attachStrip.hidden = true;
      return;
    }
    attachStrip.hidden = false;
    attachments.forEach((a, index) => {
      const pill = el("span", "composer-attachment");
      pill.appendChild(el("span", undefined, `${a.name} · ${(a.size / 1024).toFixed(0)} KiB`));
      const remove = el("button", "btn btn--small", "×");
      remove.type = "button";
      remove.setAttribute("aria-label", `Remove ${a.name}`);
      remove.addEventListener("click", () => {
        attachments.splice(index, 1);
        renderAttachments();
      });
      pill.appendChild(remove);
      attachStrip.appendChild(pill);
    });
  };

  const addFiles = async (files) => {
    for (const file of Array.from(files ?? [])) {
      const refusal = checkImage(file, attachments);
      if (refusal) {
        showNote(refusal);
        continue;
      }
      const data = await fileToBase64(file);
      attachments.push({ name: file.name || "image", size: file.size, data });
    }
    renderAttachments();
  };

  let noteTimer = null;
  function showNote(message) {
    let note = document.getElementById("composer-note");
    if (!note) {
      note = el("div", "composer-note");
      note.id = "composer-note";
      note.setAttribute("role", "status");
      form.insertBefore(note, attachStrip);
    }
    note.textContent = message;
    clearTimeout(noteTimer);
    noteTimer = setTimeout(() => note.remove(), 4000);
  }

  // ---- slash palette --------------------------------------------------------

  const closePalette = () => {
    palette.hidden = true;
    palette.replaceChildren();
  };

  const renderPalette = () => {
    const value = text.value;
    if (commandMode || running || !value.startsWith("/") || !name) {
      closePalette();
      return;
    }
    const query = value.slice(1);
    const rows = filterCommands(query);
    if (!rows.length || (query && !rows.some((r) => r.kind.startsWith(query.toLowerCase())))) {
      closePalette();
      return;
    }
    palette.replaceChildren();
    palette.hidden = false;
    for (const c of rows.slice(0, 8)) {
      const row = el("button", "palette-row");
      row.type = "button";
      row.dataset.kind = c.kind;
      row.appendChild(el("span", "palette-kind", `/${c.kind}`));
      row.appendChild(el("span", "palette-hint", c.arg ? `${c.hint} · ${c.arg}` : c.hint));
      row.addEventListener("click", () => chooseCommand(c));
      palette.appendChild(row);
    }
  };

  const chooseCommand = (c) => {
    closePalette();
    commandMode = { kind: c.kind, spec: c };
    text.setAttribute("data-command", c.kind);
    secretField.hidden = !c.secret;
    if (c.arg && c.arg !== "…") {
      text.value = `/${c.kind} `;
      text.placeholder = c.secret ? `${c.arg} + secret (sent as the password field)` : c.arg;
    } else {
      text.value = "";
      text.placeholder = c.arg === "…"
        ? `${c.hint} — type the argument, Enter to run`
        : `${c.hint} — Enter to run`;
    }
    autogrow();
    text.focus();
  };

  const exitCommandMode = () => {
    commandMode = null;
    secretField.hidden = true;
    text.removeAttribute("data-command");
    text.placeholder = running ? "Queued — read at the next safe point…" : "Message the agent…";
  };

  // ---- submission -----------------------------------------------------------

  const submit = async () => {
    if (sending || !name) return;
    const value = text.value.trim();
    const images = attachments.map((a) => a.data);

    const finish = () => {
      sending = false;
      send.disabled = false;
    };

    try {
      sending = true;
      send.disabled = true;
      if (commandMode) {
        const spec = commandMode.spec ?? {};
        const arg = value.replace(/^\/\S+\s*/, "").trim();
        const secret = secretField.hidden ? "" : secretField.value;
        await postJSON(`/api/sessions/${encodeURIComponent(name)}/command`, {
          text: commandMode.kind, arg, secret,
        });
        text.value = "";
        secretField.value = "";
        exitCommandMode();
      } else if (running) {
        if (!value) return finish();
        await postJSON(`/api/sessions/${encodeURIComponent(name)}/message`, { text: value });
        text.value = "";
      } else {
        if (!value && !images.length) return finish();
        await postJSON(`/api/sessions/${encodeURIComponent(name)}/input`, {
          text: value, images,
        });
        text.value = "";
        attachments = [];
        renderAttachments();
      }
      autogrow();
    } catch (err) {
      showNote(String(err.message ?? err));
    } finally {
      finish();
      text.focus();
    }
  };

  form.addEventListener("submit", (e) => {
    e.preventDefault();
    submit();
  });

  stop.addEventListener("click", async () => {
    if (!name || sending) return;
    stop.disabled = true;
    try {
      await postJSON(`/api/sessions/${encodeURIComponent(name)}/interrupt`, { text: "" });
      showNote("Interrupt sent — the turn unwinds at the next safe point.");
    } catch (err) {
      showNote(String(err.message ?? err));
    } finally {
      stop.disabled = false;
    }
  });

  urgent.addEventListener("click", () => {
    if (!name || !handlers.onUrgent) return;
    handlers.onUrgent({
      text: text.value.trim(),
      clear: () => {
        text.value = "";
        exitCommandMode();
        autogrow();
      },
    });
  });

  attach.addEventListener("click", () => fileInput.click());
  fileInput.addEventListener("change", async () => {
    await addFiles(fileInput.files);
    fileInput.value = "";
  });
  text.addEventListener("paste", (e) => {
    const files = [];
    for (const item of Array.from(e.clipboardData?.items ?? [])) {
      const file = item.getAsFile?.();
      if (file && String(file.type || "").startsWith("image/")) files.push(file);
    }
    if (files.length) {
      e.preventDefault();
      addFiles(files);
    }
  });
  text.addEventListener("input", () => {
    if (!commandMode && text.value.startsWith("/")) renderPalette();
    else closePalette();
    autogrow();
  });
  text.addEventListener("keydown", (e) => {
    if (palette.hidden && e.key !== "Enter") return;
    if (!palette.hidden && (e.key === "ArrowDown" || e.key === "ArrowUp")) {
      e.preventDefault();
      const rows = [...palette.querySelectorAll(".palette-row")];
      const index = rows.indexOf(document.activeElement);
      const next = e.key === "ArrowDown" ? (index + 1) % rows.length : (index - 1 + rows.length) % rows.length;
      rows[next]?.focus();
      return;
    }
    if (!palette.hidden && (e.key === "Tab" || e.key === "Enter")) {
      const first = palette.querySelector(".palette-row");
      if (first instanceof HTMLElement) {
        e.preventDefault();
        const c = commandByKind(first.dataset.kind);
        if (c) chooseCommand(c);
        return;
      }
    }
    if (e.key === "Escape" && commandMode) {
      e.preventDefault();
      exitCommandMode();
      autogrow();
    }
  });

  // ---- external controls ------------------------------------------------------

  return {
    bind(nextName) {
      name = nextName || "";
      attachments = [];
      commandMode = null;
      text.value = "";
      secretField.value = "";
      secretField.hidden = true;
      sending = false;
      send.disabled = false;
      renderAttachments();
      closePalette();
      form.hidden = !name;
      exitCommandMode();
      autogrow();
    },
    focus() {
      text.focus();
    },
    setRunning(next) {
      const changed = running !== !!next;
      running = !!next;
      stop.hidden = !running;
      urgent.hidden = !running;
      send.textContent = running ? "Poke" : "Send";
      text.placeholder = running
        ? "Queued — the agent reads it at the next safe point…"
        : "Message the agent…";
      if (changed) {
        exitCommandMode();
        closePalette();
        handlers.onBusyChange?.(running);
      }
    },
    note: showNote,
    get running() {
      return running;
    },
  };
}