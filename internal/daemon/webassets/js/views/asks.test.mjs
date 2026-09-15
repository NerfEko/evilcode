import { test } from "node:test";
import assert from "node:assert/strict";
import { answerAsk } from "./asks.js";

function fakeCard() {
  const button = { disabled: false };
  const body = {
    querySelector: () => null,
    appendChild: () => {},
  };
  const classes = new Set();
  return {
    button,
    body,
    dataset: {},
    classList: {
      add: (...names) => names.forEach((name) => classes.add(name)),
      contains: (name) => classes.has(name),
    },
    querySelectorAll: (selector) => {
      assert.equal(selector, "button");
      return [button];
    },
    querySelector: (selector) => selector === ".card-body" ? body : null,
  };
}

test("answerAsk keeps a successful answer disabled while SSE catches up", async () => {
  const oldFetch = globalThis.fetch;
  const oldDocument = globalThis.document;
  let resolveFetch;
  let calls = 0;
  globalThis.fetch = () => {
    calls++;
    return new Promise((resolve) => {
      resolveFetch = () => resolve({ ok: true, json: async () => ({}) });
    });
  };
  globalThis.document = {
    createElement: () => ({ className: "", textContent: "" }),
  };

  try {
    const card = fakeCard();
    const ask = { id: "ask-1" };
    const first = answerAsk("demo", ask, ["yes"], card);
    assert.equal(card.button.disabled, true);
    const duplicate = await answerAsk("demo", ask, ["yes"], card);
    assert.deepEqual(duplicate, { ok: false, duplicate: true });
    assert.equal(calls, 1);

    resolveFetch();
    assert.deepEqual(await first, { ok: true, labels: ["yes"] });
    assert.equal(card.button.disabled, true);
    assert.equal(card.dataset.answering, "true");
  } finally {
    globalThis.fetch = oldFetch;
    globalThis.document = oldDocument;
  }
});
