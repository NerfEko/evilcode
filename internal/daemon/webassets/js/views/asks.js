// evilcode web — the ask dock (plan-web.md §9, P6.3). Pending asks are not
// transcript messages: they are blocking questions, so they live in their own
// dock pinned above the composer where scrolling cannot lose them. Options are
// buttons (single) or checkboxes with a submit button (multi). A stale or
// already-resolved answer comes back as 409 — the card flips to a resync note
// and the next snapshot (which carries the authoritative pending list)
// replaces the dock wholesale.

import { postJSON } from "../api.js";

function el(tag, className, text) {
  const node = document.createElement(tag);
  if (className) node.className = className;
  if (text !== undefined) node.textContent = text;
  return node;
}

function askCard(ask, onAnswer) {
  const card = el("article", "card ask-card");
  card.dataset.askId = ask.id ?? "";

  const head = el("div", "card-head", "The agent is asking");
  card.appendChild(head);
  const body = el("div", "card-body");
  body.appendChild(el("p", "ask-question", String(ask.question ?? "(no question text)")));

  const options = Array.isArray(ask.options) ? ask.options : [];
  const multi = !!ask.multi;

  if (multi) {
    const boxes = [];
    for (const opt of options) {
      const label = el("label", "ask-option");
      const box = document.createElement("input");
      box.type = "checkbox";
      box.value = opt.label ?? "";
      box.name = `ask-${ask.id}`;
      label.appendChild(box);
      label.appendChild(el("span", "ask-label", opt.label ?? ""));
      if (opt.description) label.appendChild(el("span", "ask-description", opt.description));
      body.appendChild(label);
      boxes.push(box);
    }
    const submit = el("button", "btn btn--primary", "Answer");
    submit.type = "button";
    submit.disabled = true;
    const sync = () => {
      submit.disabled = !boxes.some((b) => b.checked);
    };
    for (const box of boxes) box.addEventListener("change", sync);
    submit.addEventListener("click", () => {
      onAnswer(ask, boxes.filter((b) => b.checked).map((b) => b.value), card);
    });
    body.appendChild(submit);
  } else {
    const row = el("div", "ask-options");
    for (const opt of options) {
      const btn = el("button", "btn ask-option", opt.label ?? "(option)");
      btn.type = "button";
      if (opt.description) btn.title = opt.description;
      btn.addEventListener("click", () => {
        onAnswer(ask, [opt.label ?? ""], card);
      });
      row.appendChild(btn);
    }
    body.appendChild(row);
  }

  card.appendChild(body);
  return card;
}

// renderAsks replaces the dock with one card per pending ask. `answered` maps
// ask ids this tab already answered (so the card can show its own choice
// while the daemon still owes the transcript the resolved turn).
export function renderAsks(container, pending, answered, onAnswer) {
  container.replaceChildren();
  const asks = Array.isArray(pending) ? pending : [];
  if (!asks.length) {
    container.hidden = true;
    return;
  }
  container.hidden = false;
  for (const ask of asks) {
    if (!ask || !ask.id) continue;
    if (answered && answered[ask.id]) {
      const done = el("article", "card ask-card ask-card--answered");
      done.dataset.askId = ask.id;
      done.appendChild(el("div", "card-head", "Answered"));
      const body = el("div", "card-body");
      body.appendChild(el("p", "ask-question", String(ask.question ?? "")));
      body.appendChild(el("p", "ask-answered-labels", `→ ${answered[ask.id].join(", ")}`));
      done.appendChild(body);
      container.appendChild(done);
      continue;
    }
    container.appendChild(askCard(ask, onAnswer));
  }
}

// answerAsk posts the answer and resolves the card's local state. A 409 means
// another client answered first: the card says so and the pending list
// reconciles from the next snapshot/event.
export async function answerAsk(name, ask, labels, card) {
  const buttons = card.querySelectorAll("button");
  for (const b of buttons) b.disabled = true;
  try {
    await postJSON(`/api/sessions/${encodeURIComponent(name)}/answer`, {
      request_id: ask.id, answers: labels,
    });
    card.classList.add("ask-card--answered");
    const body = card.querySelector(".card-body");
    if (body) {
      const readout = el("p", "ask-answered-labels", `→ ${labels.join(", ")}`);
      const old = body.querySelector(".ask-answered-labels");
      if (old) old.replaceWith(readout);
      else body.appendChild(readout);
    }
    return { ok: true, labels };
  } catch (err) {
    card.classList.add("ask-card--stale");
    const body = card.querySelector(".card-body");
    if (body) {
      body.appendChild(el("p", "ask-answered-labels ask-stale", String(err.message ?? err)));
    }
    return { ok: false, error: err };
  } finally {
    for (const b of buttons) b.disabled = false;
  }
}