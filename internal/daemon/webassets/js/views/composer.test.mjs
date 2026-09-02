// evilcode web — composer unit tests (plan-web.md P6.1/P6.5). The palette
// filter, the command table lookup, and the 6 MiB attachment pre-check are
// pure functions; the DOM wiring is exercised by the live click-through.
// composer.js touches the DOM only inside mountComposer(), so importing it
// under node needs no DOM at all.

import { test } from "node:test";
import assert from "node:assert/strict";
import {
  filterCommands,
  checkImage,
  commandByKind,
  IMAGE_LIMIT_BYTES,
} from "./composer.js";

test("IMAGE_LIMIT_BYTES is the plan's 6 MiB", () => {
  assert.equal(IMAGE_LIMIT_BYTES, 6 * 1024 * 1024);
});

test("filterCommands lists everything for the bare slash", () => {
  const all = filterCommands("");
  assert.ok(all.length >= 10);
  assert.ok(all.some((c) => c.kind === "connect" && c.secret));
});

test("filterCommands matches by prefix first, then falls back to substring", () => {
  const byPrefix = filterCommands("co");
  assert.equal(byPrefix[0].kind, "connect");
  assert.ok(filterCommands("checkpoint").some((c) => c.kind === "checkpoint"));
  assert.deepEqual(filterCommands("zzz-nothing"), []);
});

test("commandByKind tolerates the raw composer spelling and case", () => {
  assert.equal(commandByKind("/Connect").kind, "connect");
  assert.equal(commandByKind("  skills ").kind, "skills");
  assert.equal(commandByKind("nope"), null);
});

test("checkImage refuses non-images and oversize files, and counts the running total", () => {
  const ok = { name: "a.png", type: "image/png", size: 1024 };
  assert.equal(checkImage(ok), null);
  assert.match(checkImage({ name: "x.txt", type: "text/plain", size: 1 }), /only images/);
  assert.match(
    checkImage({ name: "big.png", type: "image/png", size: 7 * 1024 * 1024 }),
    /limit is 6 MiB/,
  );
  const near = { name: "near.png", type: "image/png", size: 5 * 1024 * 1024 };
  assert.equal(checkImage(near, []), null);
  assert.match(
    checkImage(near, [{ size: 5 * 1024 * 1024 }]),
    /attachments would total/,
  );
  assert.match(checkImage(undefined), /readable size/);
});